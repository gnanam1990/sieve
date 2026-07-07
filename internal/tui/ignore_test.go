package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/ignore"
)

func TestApplyIgnoreRuleCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".sieve", "ignore.yml")

	rule := ignore.Rule{Fingerprint: "abc123", Reason: "test"}
	if err := applyIgnoreRule(path, rule); err != nil {
		t.Fatalf("applyIgnoreRule: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ignore file: %v", err)
	}
	if !strings.Contains(string(data), "abc123") {
		t.Fatalf("ignore file missing fingerprint: %s", string(data))
	}
	// A brand-new file with no hand-written preamble intentionally omits the
	// marker so it stays consistent with `sieve ignore` from the CLI.
	if strings.Contains(string(data), ignore.Marker) {
		t.Fatalf("new ignore file should not contain marker: %s", string(data))
	}
}

func TestApplyIgnoreRulePreservesPreamble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".sieve", "ignore.yml")
	preamble := "# hand-written preamble\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(preamble+ignore.Marker+"\nignore:\n"), 0o644); err != nil { //nolint:gosec // test file
		t.Fatalf("write initial file: %v", err)
	}

	rule := ignore.Rule{Path: "vendor/**", Reason: "vendored"}
	if err := applyIgnoreRule(path, rule); err != nil {
		t.Fatalf("applyIgnoreRule: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), "# hand-written preamble") {
		t.Fatalf("preamble lost: %s", string(data))
	}
	if !strings.Contains(string(data), "vendor/**") {
		t.Fatalf("new rule missing: %s", string(data))
	}
}

func TestApplyIgnoreRuleReplacesDuplicateFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".sieve", "ignore.yml")

	rule1 := ignore.Rule{Fingerprint: "abc123", Reason: "first"}
	rule2 := ignore.Rule{Fingerprint: "abc123", Reason: "second"}
	if err := applyIgnoreRule(path, rule1); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := applyIgnoreRule(path, rule2); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Count(string(data), "abc123") != 1 {
		t.Fatalf("expected one abc123 entry, got: %s", string(data))
	}
	if !strings.Contains(string(data), "second") {
		t.Fatalf("expected updated reason, got: %s", string(data))
	}
}
