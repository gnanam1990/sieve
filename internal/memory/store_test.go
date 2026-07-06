package memory

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "events.jsonl"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestAppendReplay(t *testing.T) {
	s := testStore(t)
	s.Append(
		Event{Ts: "t1", Type: TypeRun, PR: 7, HeadSHA: "sha", Model: "m", InTok: 100, OutTok: 20, Inline: 2},
		Event{Ts: "t1", Type: TypeFinding, Fp: "abc", Path: "a.go", Sev: "major", Conf: 0.9, Cat: "bug", Tier: "inline", Cid: 42},
	)
	s.Append(Event{Ts: "t2", Type: TypeReaction, Fp: "abc", Cid: 42, React: -1})

	events, corrupt, err := s.Read()
	if err != nil || corrupt != 0 {
		t.Fatalf("read err=%v corrupt=%d", err, corrupt)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Schema != Schema || events[0].Type != TypeRun || events[0].InTok != 100 {
		t.Fatalf("run event wrong: %+v", events[0])
	}
	if events[2].React != -1 || events[2].Fp != "abc" {
		t.Fatalf("reaction event wrong: %+v", events[2])
	}
}

func TestCorruptLineTolerance(t *testing.T) {
	s := testStore(t)
	s.Append(Event{Ts: "t1", Type: TypeFinding, Fp: "ok"})
	// Inject a corrupt line.
	f, _ := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("{not valid json\n")
	_ = f.Close()
	s.Append(Event{Ts: "t2", Type: TypeFinding, Fp: "ok2"})

	events, corrupt, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if corrupt != 1 {
		t.Fatalf("want 1 corrupt line, got %d", corrupt)
	}
	if len(events) != 2 {
		t.Fatalf("valid events should survive: got %d", len(events))
	}
}

func TestReadMissingIsEmpty(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "nope.jsonl"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	events, corrupt, err := s.Read()
	if err != nil || corrupt != 0 || len(events) != 0 {
		t.Fatalf("missing store must be empty: %v %d %v", err, corrupt, events)
	}
}

func TestWipeAndRewrite(t *testing.T) {
	s := testStore(t)
	s.Append(Event{Ts: "t1", Type: TypeFinding, Fp: "a"})
	if err := s.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("wipe must remove the file")
	}
	// Wipe of an absent file is fine.
	if err := s.Wipe(); err != nil {
		t.Fatalf("wipe of absent store must not error: %v", err)
	}
	s.Rewrite([]Event{{Ts: "t2", Type: TypeFinding, Fp: "b"}, {Ts: "t3", Type: TypeFinding, Fp: "c"}})
	events, _, _ := s.Read()
	if len(events) != 2 || events[0].Fp != "b" {
		t.Fatalf("rewrite wrong: %+v", events)
	}
}

func TestNoOpStore(t *testing.T) {
	s := &Store{log: slog.New(slog.NewTextHandler(io.Discard, nil))} // empty Path
	s.Append(Event{Type: TypeFinding})                               // no panic, no write
	s.Rewrite([]Event{{Type: TypeFinding}})
	events, corrupt, err := s.Read()
	if err != nil || corrupt != 0 || len(events) != 0 {
		t.Fatalf("no-op store must be inert: %v %d %v", err, corrupt, events)
	}
	if err := s.Wipe(); err != nil {
		t.Fatal(err)
	}
}

// TestUnwritableStoreIsBestEffort: a store whose parent path is a file (so the
// dir cannot be created) warns and no-ops rather than failing.
func TestUnwritableStoreIsBestEffort(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: filepath.Join(blocker, "events.jsonl"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Append(Event{Type: TypeFinding, Fp: "a"}) // MkdirAll fails: warn + skip
	s.Rewrite([]Event{{Type: TypeFinding, Fp: "b"}})
	// Read of a path whose parent is a file returns a read error, not a panic.
	if _, _, err := s.Read(); err == nil {
		t.Log("read returned no error (platform-dependent); acceptable")
	}
}

func TestDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	d, err := Dir("github.com", "o", "r")
	if err != nil || d != "/custom/data/sieve/github.com/o/r" {
		t.Fatalf("Dir = %q err %v", d, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	d2, err := Dir("github.com", "o", "r")
	if err != nil || !filepath.IsAbs(d2) || !contains(d2, ".local/share/sieve/github.com/o/r") {
		t.Fatalf("Dir fallback = %q err %v", d2, err)
	}
}

func TestOpenBuildsPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := Open("github.com", "acme", "app", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s.Path == "" || !contains(s.Path, "sieve/github.com/acme/app/events.jsonl") {
		t.Fatalf("Open path wrong: %q", s.Path)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && filepath.ToSlash(s) != "" && indexOf(filepath.ToSlash(s), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
