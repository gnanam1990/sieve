package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
)

func TestRecordReactionAppendsEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	store := &memory.Store{Path: path}
	recordReaction(store, "fp1", "main.go", time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var ev memory.Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != memory.TypeReaction {
		t.Fatalf("expected reaction event, got %s", ev.Type)
	}
	if ev.Fp != "fp1" {
		t.Fatalf("expected fp1, got %s", ev.Fp)
	}
	if ev.Minus != 1 || ev.Plus != 0 {
		t.Fatalf("expected minus=1 plus=0, got %d/%d", ev.Minus, ev.Plus)
	}
}

func TestRecordReactionNoOpWhenStoreNil(_ *testing.T) {
	recordReaction(nil, "fp1", "main.go", time.Now()) // should not panic
}

func TestSaveReportWritesFile(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	rc := &review.ReviewContext{Repo: "owner/repo", PRNumber: 7}
	path, err := saveReport(rc)
	if err != nil {
		t.Fatalf("saveReport: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report file missing: %v", err)
	}
}
