// Package memory is sieve's local, append-only outcome store: a JSONL event log
// per repository under the user's data dir. It records what sieve found, what
// was resolved, and how humans reacted, so `sieve learnings` and `sieve stats`
// can improve future reviews.
//
// Writes are best-effort — an unwritable store warns and is skipped, never
// failing a review. GitHub remains the source of truth: `sieve sync` rebuilds
// the store from the PR's walkthrough metadata, sieve's inline comments, their
// reactions, and thread resolution.
package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Schema is the event schema version.
const Schema = 1

// Event types.
const (
	TypeRun       = "run"
	TypeFinding   = "finding"
	TypeResolved  = "resolved"
	TypeReaction  = "reaction"
	TypeDismissed = "dismissed"
)

// Resolution mechanisms (Event.How on a resolved event).
const (
	ResolvedReReviewAbsent = "re-review-absent"
	ResolvedAnchorGone     = "anchor-gone"
)

// Event is one append-only record. A flat shape with omitempty keeps the JSONL
// compact and forward-compatible.
type Event struct {
	Schema  int    `json:"schema"`
	Ts      string `json:"ts"`
	Type    string `json:"type"`
	PR      int    `json:"pr,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`

	// run
	Model   string `json:"model,omitempty"`
	InTok   int    `json:"in,omitempty"`
	OutTok  int    `json:"out,omitempty"`
	Inline  int    `json:"inline,omitempty"`
	Notes   int    `json:"notes,omitempty"`
	Dropped int    `json:"dropped,omitempty"`

	// finding / resolved / reaction / dismissed
	Fp    string  `json:"fp,omitempty"`
	Path  string  `json:"path,omitempty"`
	Sev   string  `json:"sev,omitempty"`
	Conf  float64 `json:"conf,omitempty"`
	Cat   string  `json:"cat,omitempty"`
	Tier  string  `json:"tier,omitempty"`
	Title string  `json:"title,omitempty"`
	Cid   int64   `json:"cid,omitempty"`
	How   string  `json:"how,omitempty"`   // resolved: re-review-absent | anchor-gone
	React int     `json:"react,omitempty"` // reaction: +1 | -1
}

// Store is a per-repo event log. A zero Path is a no-op store (unresolvable
// data dir) — Append silently does nothing, Read returns empty.
type Store struct {
	Path string
	log  *slog.Logger
}

// Dir resolves the store directory: ${XDG_DATA_HOME:-~/.local/share}/sieve/
// <host>/<owner>/<repo>.
func Dir(host, owner, repo string) (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sieve", host, owner, repo), nil
}

// Open returns the store for host/owner/repo. A data-dir resolution failure
// yields a no-op store with a warning rather than an error.
func Open(host, owner, repo string, log *slog.Logger) *Store {
	dir, err := Dir(host, owner, repo)
	if err != nil {
		log.Warn("memory: cannot resolve data dir; outcome store disabled", "err", err)
		return &Store{log: log}
	}
	return &Store{Path: filepath.Join(dir, "events.jsonl"), log: log}
}

// Append writes events (best-effort). Each event's Schema is stamped here; the
// caller sets Ts. A write failure warns and returns without erroring.
func (s *Store) Append(events ...Event) {
	if s.Path == "" || len(events) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil { //nolint:gosec // user data dir
		s.log.Warn("memory: cannot create store dir; skipping write", "err", err)
		return
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // user data file
	if err != nil {
		s.log.Warn("memory: cannot open store; skipping write", "err", err)
		return
	}
	defer f.Close() //nolint:errcheck // best-effort
	enc := json.NewEncoder(f)
	for _, e := range events {
		e.Schema = Schema
		if err := enc.Encode(e); err != nil {
			s.log.Warn("memory: encode failed; skipping remaining", "err", err)
			return
		}
	}
}

// Read replays the log, skipping and counting corrupt lines (a partial write is
// not fatal). A missing store is empty, not an error.
func (s *Store) Read() (events []Event, corrupt int, err error) {
	if s.Path == "" {
		return nil, 0, nil
	}
	data, err := os.ReadFile(s.Path) //nolint:gosec // user data file
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read store: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) != nil {
			corrupt++
			continue
		}
		events = append(events, e)
	}
	return events, corrupt, nil
}

// Wipe removes the store file (used by the sync/delete-and-rebuild flow).
func (s *Store) Wipe() error {
	if s.Path == "" {
		return nil
	}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Rewrite replaces the store's contents with exactly these events (used by sync
// so re-running is idempotent). Best-effort like Append.
func (s *Store) Rewrite(events []Event) {
	if s.Path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil { //nolint:gosec // user data dir
		s.log.Warn("memory: cannot create store dir; skipping rewrite", "err", err)
		return
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, e := range events {
		e.Schema = Schema
		_ = enc.Encode(e) //nolint:errcheck // building an in-memory buffer
	}
	if err := os.WriteFile(s.Path, []byte(b.String()), 0o644); err != nil { //nolint:gosec // user data file
		s.log.Warn("memory: cannot write store; skipping rewrite", "err", err)
	}
}
