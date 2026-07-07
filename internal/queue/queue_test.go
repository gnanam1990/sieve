package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// harness is an injectable RunFunc that records invocations and can block or
// fail on demand.
type harness struct {
	mu      sync.Mutex
	runs    []Job
	started chan Job      // each run announces itself here (nil = don't)
	gate    chan struct{} // if non-nil, each run blocks for one token
	fail    bool
}

func (h *harness) run(_ context.Context, job Job) error {
	h.mu.Lock()
	h.runs = append(h.runs, job)
	h.mu.Unlock()
	if h.started != nil {
		h.started <- job
	}
	if h.gate != nil {
		<-h.gate
	}
	if h.fail {
		return errors.New("boom")
	}
	return nil
}

func (h *harness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs)
}

func (h *harness) shas() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.runs))
	for i, j := range h.runs {
		out[i] = j.HeadSHA
	}
	return out
}

func openQ(t *testing.T, h *harness, workers int) *Queue {
	t.Helper()
	q, err := Open(Options{
		Dir: t.TempDir(), Workers: workers, Run: h.run, Log: discardLog(),
		Backoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func job(repo string, pr int, sha string) Job {
	return Job{InstallationID: 1, Repo: repo, PR: pr, HeadSHA: sha}
}

// TestCoalesceQueuedReplace: two enqueues for one PR before it runs collapse to
// a single run at the newest head.
func TestCoalesceQueuedReplace(t *testing.T) {
	h := &harness{}
	q := openQ(t, h, 1)
	if err := q.Enqueue(job("o/r", 1, "sha1")); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(job("o/r", 1, "sha2")); err != nil {
		t.Fatal(err)
	}
	if d := q.Depth(); d != 1 {
		t.Fatalf("two enqueues for one PR must coalesce to depth 1, got %d", d)
	}
	q.Start(context.Background())
	waitFor(t, func() bool { return h.count() == 1 })
	// Give any erroneous second run a chance to appear.
	time.Sleep(20 * time.Millisecond)
	if got := h.shas(); len(got) != 1 || got[0] != "sha2" {
		t.Fatalf("want exactly one run at sha2, got %v", got)
	}
	_ = q.Shutdown(context.Background())
}

// TestRunningThenQueued: a push for a PR already running is queued behind and
// runs after the in-flight review finishes (never concurrently).
func TestRunningThenQueued(t *testing.T) {
	h := &harness{started: make(chan Job, 1), gate: make(chan struct{})}
	q := openQ(t, h, 2) // 2 workers, but same-key jobs must still serialize
	q.Start(context.Background())

	if err := q.Enqueue(job("o/r", 1, "sha1")); err != nil {
		t.Fatal(err)
	}
	<-h.started // sha1 is now running (blocked on gate)

	if err := q.Enqueue(job("o/r", 1, "sha2")); err != nil {
		t.Fatal(err)
	}
	// sha2 must NOT start while sha1 runs (same key).
	select {
	case j := <-h.started:
		t.Fatalf("second job started while first still running: %+v", j)
	case <-time.After(30 * time.Millisecond):
	}
	h.gate <- struct{}{} // release sha1
	select {
	case j := <-h.started:
		if j.HeadSHA != "sha2" {
			t.Fatalf("want sha2 to run next, got %s", j.HeadSHA)
		}
	case <-time.After(time.Second):
		t.Fatal("queued-behind job never ran")
	}
	h.gate <- struct{}{} // release sha2
	waitFor(t, func() bool { return h.count() == 2 })
	_ = q.Shutdown(context.Background())
	if got := h.shas(); len(got) != 2 || got[0] != "sha1" || got[1] != "sha2" {
		t.Fatalf("want [sha1 sha2] in order, got %v", got)
	}
}

// TestCrashReplay: a job enqueued but never completed (log has enqueue, no done)
// is replayed as pending on the next Open.
func TestCrashReplay(t *testing.T) {
	dir := t.TempDir()
	h := &harness{}
	q1, err := Open(Options{Dir: dir, Workers: 1, Run: h.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Enqueue without ever starting workers → the enqueue record persists,
	// no done record — exactly the "killed mid-review" state.
	if err := q1.Enqueue(job("o/r", 7, "shaX")); err != nil {
		t.Fatal(err)
	}
	if err := q1.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reopen: the unfinished job must replay into pending.
	h2 := &harness{}
	q2, err := Open(Options{Dir: dir, Workers: 1, Run: h2.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if d := q2.Depth(); d != 1 {
		t.Fatalf("crash-replay must restore the pending job, depth=%d", d)
	}
	q2.Start(context.Background())
	waitFor(t, func() bool { return h2.count() == 1 })
	if got := h2.shas(); got[0] != "shaX" {
		t.Fatalf("replayed job ran with wrong head: %v", got)
	}
	_ = q2.Shutdown(context.Background())

	// A third Open sees the job settled (done) → nothing pending.
	h3 := &harness{}
	q3, err := Open(Options{Dir: dir, Workers: 1, Run: h3.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if d := q3.Depth(); d != 0 {
		t.Fatalf("a completed job must not replay, depth=%d", d)
	}
	_ = q3.Shutdown(context.Background())
}

// TestDeadLetterAfterRetries: a job that always fails is retried maxRetries
// times then dead-lettered, not retried forever.
func TestDeadLetterAfterRetries(t *testing.T) {
	h := &harness{fail: true}
	q, err := Open(Options{Dir: t.TempDir(), Workers: 1, Run: h.run, Log: discardLog(), MaxRetries: 2, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	if err := q.Enqueue(job("o/r", 3, "sha")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return q.DeadLetters() == 1 })
	time.Sleep(20 * time.Millisecond)
	if c := h.count(); c != 3 { // 1 initial + 2 retries
		t.Errorf("want 3 attempts (1 + 2 retries), got %d", c)
	}
	if q.Depth() != 0 {
		t.Errorf("dead-lettered job must leave the queue, depth=%d", q.Depth())
	}
	_ = q.Shutdown(context.Background())
}

// TestGracefulShutdownFinishesInFlightKeepsPending: shutdown lets the running
// job finish (done record) while an un-started pending job stays for replay.
func TestGracefulShutdownFinishesInFlightKeepsPending(t *testing.T) {
	dir := t.TempDir()
	h := &harness{started: make(chan Job, 1), gate: make(chan struct{})}
	q, err := Open(Options{Dir: dir, Workers: 1, Run: h.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	if err := q.Enqueue(job("o/r", 1, "running")); err != nil {
		t.Fatal(err)
	}
	<-h.started // job 1 running
	if err := q.Enqueue(job("o/r", 2, "pending")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return q.Depth() == 1 }) // job 2 waiting

	// Shutdown: intake stops, in-flight finishes, pending PR#2 is not started.
	shutErr := make(chan error, 1)
	go func() { shutErr <- q.Shutdown(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	h.gate <- struct{}{} // let job 1 finish
	if err := <-shutErr; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := q.Enqueue(job("o/r", 9, "x")); !errors.Is(err, ErrClosed) {
		t.Errorf("enqueue after shutdown must be rejected, got %v", err)
	}
	// PR#2 never ran; it must replay on reopen.
	h2 := &harness{}
	q2, err := Open(Options{Dir: dir, Workers: 1, Run: h2.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if d := q2.Depth(); d != 1 {
		t.Fatalf("un-started pending job must survive shutdown for replay, depth=%d", d)
	}
	_ = q2.Shutdown(context.Background())
}

func TestReplayTornFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	good := `{"op":"enqueue","job":{"repo":"o/r","pr":1,"head_sha":"s"}}` + "\n"
	torn := `{"op":"enqueue","job":{"repo":"o/r","pr":2,"head_` // crash mid-write, no newline
	if err := os.WriteFile(path, []byte(good+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	pending, _, err := replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].PR != 1 {
		t.Fatalf("torn final line must be skipped, got %+v", pending)
	}
}

func TestEnqueueValidationAndOpenErrors(t *testing.T) {
	if _, err := Open(Options{Dir: t.TempDir()}); err == nil {
		t.Fatal("missing Run must error")
	}
}

// TestCrashReplayCoalesceOlderHeadSettle: a newer head is enqueued while an older
// head is running; the older review finishes (done for the old sha) after the new
// enqueue is already on disk. The new head must still replay as pending.
func TestCrashReplayCoalesceOlderHeadSettle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.jsonl")
	rec := func(op op, sha string) string {
		return fmt.Sprintf(`{"op":"%s","job":{"installation_id":1,"repo":"o/r","pr":1,"head_sha":"%s"}}%s`, op, sha, "\n")
	}
	log := rec(opEnqueue, "sha1") + rec(opEnqueue, "sha2") + rec(opDone, "sha1")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	pending, attempts, err := replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 {
		t.Fatalf("unexpected attempts: %v", attempts)
	}
	if len(pending) != 1 || pending[0].HeadSHA != "sha2" {
		t.Fatalf("newest head sha2 must replay when older-head done is stale, got %+v", pending)
	}
}

// TestOpenDefaults exercises the zero-value option fallbacks (workers, log,
// backoff, retries).
func TestOpenDefaults(t *testing.T) {
	h := &harness{}
	q, err := Open(Options{Dir: t.TempDir(), Run: h.run}) // everything else defaulted
	if err != nil {
		t.Fatal(err)
	}
	if q.workers != 1 || q.maxRetries != 2 || q.backoff != time.Second || q.log == nil {
		t.Fatalf("defaults not applied: workers=%d retries=%d backoff=%v log=%v", q.workers, q.maxRetries, q.backoff, q.log)
	}
	_ = q.Shutdown(context.Background())
}

// TestShutdownIdempotentAndTimeout: a second Shutdown is a no-op; a shutdown
// whose context expires before an in-flight job finishes returns the ctx error
// but still closes the log.
func TestShutdownIdempotentAndTimeout(t *testing.T) {
	h := &harness{started: make(chan Job, 1), gate: make(chan struct{})}
	q := openQ(t, h, 1)
	q.Start(context.Background())
	if err := q.Enqueue(job("o/r", 1, "stuck")); err != nil {
		t.Fatal(err)
	}
	<-h.started // job is running, blocked on gate forever

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := q.Shutdown(ctx); err == nil {
		t.Fatal("shutdown must report the context timeout while a job is stuck")
	}
	// Second shutdown is a clean no-op.
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown must be a no-op, got %v", err)
	}
	h.gate <- struct{}{} // unblock the leaked worker so the test exits cleanly
}

// TestShutdownTimeoutAbortsRetries: a shutdown whose context expires while a job
// is in retry backoff cancels the run context, dead-letters the job, and does not
// let it retry to completion.
func TestShutdownTimeoutAbortsRetries(t *testing.T) {
	h := &harness{fail: true}
	q, err := Open(Options{Dir: t.TempDir(), Workers: 1, Run: h.run, Log: discardLog(), MaxRetries: 5, Backoff: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	if err := q.Enqueue(job("o/r", 1, "s")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.count() >= 1 }) // first attempt done, now in long backoff

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := q.Shutdown(ctx); err == nil {
		t.Fatal("shutdown must report the context timeout while a job is in backoff")
	}
	if q.DeadLetters() != 1 {
		t.Fatalf("shutdown timeout must dead-letter the job, got %d dead", q.DeadLetters())
	}
	if c := h.count(); c > 2 {
		t.Errorf("shutdown timeout should stop retries early, got %d attempts", c)
	}
}

// TestEnqueueLogWriteError: a failed log write surfaces as an Enqueue error so
// the webhook can 5xx and GitHub redelivers.
func TestEnqueueLogWriteError(t *testing.T) {
	h := &harness{}
	q := openQ(t, h, 1)
	q.f.Close() //nolint:errcheck // force subsequent writes to fail
	if err := q.Enqueue(job("o/r", 1, "s")); err == nil {
		t.Fatal("enqueue must fail when the log write fails")
	}
	if q.Depth() != 0 {
		t.Errorf("a job whose log write failed must not be queued, depth=%d", q.Depth())
	}
}

func TestRunningAndRecentDead(t *testing.T) {
	// Gate blocks the first attempt long enough to observe Running(); after we
	// close the gate all attempts (fail=true) finish quickly and the job is
	// dead-lettered. We do NOT use a started channel here — its buffer would
	// block the harness on the third attempt once we stopped reading it.
	dir := t.TempDir()
	h := &harness{gate: make(chan struct{}), fail: true}
	q, err := Open(Options{Dir: dir, Workers: 1, Run: h.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	if err := q.Enqueue(job("o/r", 1, "s")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(q.Running()) == 1 })
	running := q.Running()
	if len(running) != 1 || running[0] != "o/r#1" {
		t.Fatalf("running = %v", running)
	}
	// Let it exhaust retries and die.
	close(h.gate)
	waitFor(t, func() bool { return q.DeadLetters() == 1 })
	if len(q.Running()) != 0 {
		t.Fatalf("no jobs should be running after completion, got %v", q.Running())
	}
	dead := q.RecentDead()
	if len(dead) != 1 || dead[0].Repo != "o/r" || dead[0].PR != 1 {
		t.Fatalf("recent dead = %v", dead)
	}
	_ = q.Shutdown(context.Background())

	// Reopen: the dead-letter journal should replay the same record.
	q2, err := Open(Options{Dir: dir, Workers: 1, Run: h.run, Log: discardLog(), Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	dead2 := q2.RecentDead()
	if len(dead2) != 1 || dead2[0].Repo != "o/r" || dead2[0].PR != 1 {
		t.Fatalf("dead letters did not survive reopen: %v", dead2)
	}
	_ = q2.Shutdown(context.Background())
}

// waitFor polls cond until true or fails after ~2s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
