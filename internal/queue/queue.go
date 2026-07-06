// Package queue is the daemon's persistent, coalescing review queue. Jobs are
// appended to an on-disk log so a crash replays unfinished work; at most one
// review runs per (repo, PR) at a time, and a newer push for a PR already
// queued replaces the older one (only the newest head matters). Job execution
// is injected, so the queue has no dependency on the review pipeline.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Job identifies one review to run.
type Job struct {
	InstallationID int64  `json:"installation_id"`
	Repo           string `json:"repo"` // owner/name
	PR             int    `json:"pr"`
	HeadSHA        string `json:"head_sha"`
	Draft          bool   `json:"draft,omitempty"`
	DeliveryID     string `json:"delivery_id,omitempty"` // X-GitHub-Delivery, for tracing
}

// Key is the coalescing identity: one review per (repo, PR).
func (j Job) Key() string { return j.Repo + "#" + strconv.Itoa(j.PR) }

// op is a log record kind.
type op string

const (
	opEnqueue  op = "enqueue"
	opDone     op = "done"
	opDead     op = "dead"
	opAttempt  op = "attempt" // crash-resilient retry counter; see runWithRetry
)

type record struct {
	Op      op     `json:"op"`
	Job     Job    `json:"job"`
	Err     string `json:"err,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
}

// ErrClosed is returned by Enqueue after shutdown has begun.
var ErrClosed = errors.New("queue is shutting down")

// RunFunc executes one job (the review pipeline with --post). A non-nil error
// after all retries dead-letters the job.
type RunFunc func(context.Context, Job) error

// Options configures a Queue.
type Options struct {
	Dir        string // data_dir; queue.jsonl lives here
	Workers    int
	Run        RunFunc
	Log        *slog.Logger
	MaxRetries int           // attempts beyond the first; default 2 (3 total)
	Backoff    time.Duration // base retry backoff; tests shrink it
}

// Queue is a bounded worker pool over a persistent, coalescing job log.
type Queue struct {
	dir        string
	workers    int
	run        RunFunc
	log        *slog.Logger
	maxRetries int
	backoff    time.Duration

	mu      sync.Mutex
	cond    *sync.Cond
	pending []Job
	running map[string]bool
	closed  bool
	dead    int
	f       *os.File
	// attempts is the persisted retry counter for jobs being executed — seeded
	// from opAttempt records on replay so a crash mid-backoff resumes from the
	// last attempt rather than 0 (otherwise a flaky upstream loops forever).
	attempts map[string]int

	// runCtx is the context workers run jobs under. It is deliberately NOT the
	// context passed to Start/Serve (which SIGTERM cancels) — that path would
	// abort an in-flight review mid-POST and produce partial GitHub writes
	// (exactly the anti-pattern the spec calls out). Shutdown cancels runCtx
	// only after the drain timeout, so an idle-in-flight job is force-aborted
	// rather than the whole pool being torn down on SIGTERM.
	runCtx    context.Context
	runCancel context.CancelFunc
	loopWg    sync.WaitGroup
}

// Open creates (or reopens) the queue at Dir, replaying any unfinished jobs
// from the log into the pending set.
func Open(opts Options) (*Queue, error) {
	if opts.Run == nil {
		return nil, errors.New("queue: Run is required")
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2
	}
	backoff := opts.Backoff
	if backoff == 0 {
		backoff = time.Second
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("queue dir: %w", err)
	}
	path := filepath.Join(opts.Dir, "queue.jsonl")
	pending, attempts, err := replay(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // daemon data; owner-only
	if err != nil {
		return nil, fmt.Errorf("open queue log: %w", err)
	}
	q := &Queue{
		dir:        opts.Dir,
		workers:    opts.Workers,
		run:        opts.Run,
		log:        opts.Log,
		maxRetries: maxRetries,
		backoff:    backoff,
		pending:    pending,
		attempts:   attempts,
		running:    map[string]bool{},
		f:          f,
	}
	q.cond = sync.NewCond(&q.mu)
	return q, nil
}

// replay reconstructs the pending set: the latest enqueue per (repo, PR) that
// was not later settled (done/dead), in original enqueue order, plus the most
// recent opAttempt count per key — so a crash mid-retry resumes from the last
// attempt rather than 0 (the infinite-retry-loop-across-crashes anti-pattern).
func replay(path string) ([]Job, map[string]int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // daemon log path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read queue log: %w", err)
	}
	type state struct {
		job         Job
		enqueueIdx  int
		settledIdx  int
		everSettled bool
		lastAttempt int
	}
	byKey := map[string]*state{}
	idx := 0
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // tolerate a torn final line from a crash mid-write
		}
		// Integrity guard: each record's first segment must be a valid op we
		// recognize. A torn line whose JSON happens to parse but yields an
		// unknown op is a corruption artefact, not a job — ignore it. (The
		// alternative — a full CRC-framed format — would change the on-disk
		// format and break existing logs; this op-allowlist guard is the
		// smallest-reasonable version that still rejects the simplest ghost-job
		// case the spec names, e.g. a torn enqueue that parses with empty fields.)
		switch rec.Op {
		case opEnqueue, opDone, opDead, opAttempt:
		default:
			continue
		}
		idx++
		key := rec.Job.Key()
		st := byKey[key]
		if st == nil {
			st = &state{settledIdx: -1, enqueueIdx: -1}
			byKey[key] = st
		}
		switch rec.Op {
		case opEnqueue:
			st.job = rec.Job
			st.enqueueIdx = idx
		case opDone, opDead:
			// Only a settle record for the *current* latest head actually settles the
			// pending job. If an older review finishes (done/dead for a previous head)
			// after a newer head was already enqueued, that record must not drop the
			// newer job — the spec requires coalescing to the newest head.
			if rec.Job.HeadSHA == st.job.HeadSHA {
				st.settledIdx = idx
				st.everSettled = true
			}
		case opAttempt:
			st.lastAttempt = rec.Attempt
		}
	}
	var pending []Job
	var order []int
	tmp := map[int]Job{}
	attempts := map[string]int{}
	for k, st := range byKey {
		if st.enqueueIdx < 0 {
			continue // only settled records, no live enqueue
		}
		if st.everSettled && st.settledIdx > st.enqueueIdx {
			continue // settled after the last enqueue → done
		}
		tmp[st.enqueueIdx] = st.job
		order = append(order, st.enqueueIdx)
		if st.lastAttempt > 0 {
			attempts[k] = st.lastAttempt
		}
	}
	sort.Ints(order)
	for _, i := range order {
		pending = append(pending, tmp[i])
	}
	return pending, attempts, nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// mrandUint64 is a process-wide pseudo-random source for full-jitter backoff.
// math/rand/v2's top-level functions are safe for concurrent use and auto-seeded.
func mrandUint64() uint64 { return rand.Uint64() }

// sanitizeLog replaces CR/LF/TAB/NUL in untrusted strings (repo names, error
// messages that may quote user-controlled payloads) so a slog text-handler
// cannot be log-forged into splitting one record across many apparent lines.
// Truncates very long error chains so an attacker cannot blow up log volume.
func sanitizeLog(s string) string {
	if !strings.ContainsAny(s, "\r\n\t\x00") && len(s) <= 4096 {
		return s
	}
	b := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if len(b) == 4096 {
			break
		}
		switch c {
		case '\r', '\n', '\t', 0:
			b = append(b, '?')
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// Enqueue durably records a job and adds it to the pending set, coalescing with
// any job already queued for the same (repo, PR) — the newer head wins and
// keeps the older one's queue position.
func (q *Queue) Enqueue(job Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	if err := q.writeLocked(record{Op: opEnqueue, Job: job}); err != nil {
		return err // not enqueued; caller (webhook) returns 5xx → GitHub redelivers
	}
	key := job.Key()
	for i := range q.pending {
		if q.pending[i].Key() == key {
			q.pending[i] = job // coalesce in place: newest head only
			q.cond.Broadcast()
			return nil
		}
	}
	q.pending = append(q.pending, job)
	q.cond.Broadcast()
	return nil
}

// Start launches the worker pool. The ctx argument is ignored for run-time
// purposes: workers run on an internal context that Shutdown cancels only after
// the drain timeout, so a SIGTERM delivered to the outer Serve ctx does NOT
// abort an in-flight review mid-POST (the partial-GitHub-write anti-pattern).
// To force-abort in-flight jobs before the drain timeout, call Shutdown with a
// short context.
func (q *Queue) Start(ctx context.Context) {
	q.runCtx, q.runCancel = context.WithCancel(context.Background())
	for i := 0; i < q.workers; i++ {
		q.loopWg.Add(1)
		go q.workerLoop()
	}
}

func (q *Queue) workerLoop() {
	defer q.loopWg.Done()
	for {
		job, ok := q.take()
		if !ok {
			return
		}
		err := q.runWithRetry(q.runCtx, job)
		q.complete(job, err)
	}
}

// take blocks until a job whose key is not already running is available, or the
// queue is closed (stop starting new work — in-flight jobs already left take()).
func (q *Queue) take() (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.closed {
			return Job{}, false
		}
		if idx := q.pickRunnableLocked(); idx >= 0 {
			job := q.pending[idx]
			q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
			q.running[job.Key()] = true
			return job, true
		}
		q.cond.Wait()
	}
}

// pickRunnableLocked returns the index of the first pending job whose key is not
// currently running, or -1.
func (q *Queue) pickRunnableLocked() int {
	for i, j := range q.pending {
		if !q.running[j.Key()] {
			return i
		}
	}
	return -1
}

func (q *Queue) runWithRetry(ctx context.Context, job Job) error {
	var err error
	// Seed from any persisted opAttempt counter so a crash mid-backoff resumes
	// from the last attempt rather than 0 (otherwise a flaky upstream that
	// succeeds on attempt 1 of each run loops forever across crashes).
	q.mu.Lock()
	startAttempt := q.attempts[job.Key()]
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.attempts, job.Key()) // a finished job's retry state is moot
		q.mu.Unlock()
	}()
	for attempt := startAttempt; attempt <= q.maxRetries; attempt++ {
		if attempt > 0 {
			base := q.backoff * time.Duration(1<<(attempt-1))
			if base <= 0 {
				base = time.Millisecond
			}
			// Compute jitter in unsigned space; casting a full uint64 directly to
			// time.Duration can wrap to a negative duration, which would make
			// time.After fire immediately and collapse the backoff to zero.
			jitter := time.Duration(mrandUint64() % uint64(base))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(jitter):
			}
		}
		if err = q.run(ctx, job); err == nil {
			return nil
		}
		q.persistAttempt(job, attempt+1)
		q.log.Warn("review job failed", "repo", job.Repo, "pr", job.PR, "attempt", attempt+1, "err", sanitizeLog(err.Error()))
	}
	return err
}

// persistAttempt records the current attempt count for a job so the next replay
// can seed runWithRetry from it rather than starting at attempt 0. Written as a
// separate opAttempt record so the existing opDone/opDead replay semantics are
// unchanged; replay ignores opAttempt unless an enqueue-with-no-settle replays.
func (q *Queue) persistAttempt(job Job, attempt int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	_ = q.writeLocked(record{Op: opAttempt, Job: job, Attempt: attempt})
}

func (q *Queue) complete(job Job, err error) {
	q.mu.Lock()
	if q.f == nil {
		// Shutdown already closed the log; an in-flight worker finishing after
		// the drain timeout must not write to a closed fd. The job's marker is
		// NOT recorded, so the next replay re-runs it (correct: the review may
		// have been interrupted and the GitHub state is uncertain).
		delete(q.running, job.Key())
		q.mu.Unlock()
		q.cond.Broadcast()
		return
	}
	if err != nil {
		_ = q.writeLocked(record{Op: opDead, Job: job, Err: sanitizeLog(err.Error())})
		q.dead++
		q.log.Error("job dead-lettered", "repo", sanitizeLog(job.Repo), "pr", job.PR, "err", sanitizeLog(err.Error()))
	} else {
		_ = q.writeLocked(record{Op: opDone, Job: job})
		// Durability: a crash between Write returning and the OS flushing the
		// page cache can lose this done record, which would re-run the review
		// and produce a duplicate walkthrough post on the next boot. Sync the
		// settle markers (done/dead) so a clean OS-flushed close is the witness.
		// Dead-letter markers above don't strictly need a separate Sync — the
		// dead path's marker is forensics-only — but we fsync both for symmetry.
		_ = q.f.Sync() //nolint:errcheck // best-effort fsync; a failure logs the write-err path
	}
	delete(q.running, job.Key())
	q.mu.Unlock()
	q.cond.Broadcast() // a job queued behind this key may now be runnable
}

// writeLocked appends one record to the log. Caller holds q.mu.
func (q *Queue) writeLocked(rec record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := q.f.Write(append(line, '\n')); err != nil {
		q.log.Error("queue log write failed", "op", rec.Op, "err", err)
		return fmt.Errorf("queue log write: %w", err)
	}
	return nil
}

// Depth returns the number of jobs waiting (not counting in-flight).
func (q *Queue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// DeadLetters returns the count of jobs that exhausted their retries this run.
func (q *Queue) DeadLetters() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dead
}

// Shutdown stops intake, lets in-flight jobs finish (bounded by ctx), and closes
// the log. Un-started pending jobs remain in the log and replay on next Open.
// In-flight jobs run on an internal context (not the one passed to Start), so a
// SIGTERM cancelling the outer Serve ctx does NOT abort them mid-POST; only the
// ctx passed here bounds the drain. On timeout, in-flight jobs are force- aborted.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.loopWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// Drain timeout: force-abort any remaining in-flight runs by cancelling
		// the worker context. Workers then exit (their run sees ctx.Err()), and
		// complete() sees q.f still open to record the dead-letter marker. We do
		// NOT close the log here; complete() runs to settle.
		if q.runCancel != nil {
			q.runCancel()
		}
		// Give force-aborted runs a brief grace to record their markers.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}

	// Now that no worker can be writing, hold q.mu and close the log. The nil
	// check in complete() is the belt-and-suspenders guard against any worker
	// that slips in between the close and the unlock.
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.f == nil {
		return ctx.Err()
	}
	f := q.f
	q.f = nil
	if err := f.Close(); err != nil {
		return fmt.Errorf("close queue log: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
