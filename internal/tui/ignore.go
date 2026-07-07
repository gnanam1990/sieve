package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gnanam1990/sieve/internal/ignore"
)

// applyIgnoreRule appends rule to path, preserving any hand-written preamble
// above the sieve marker. It mirrors the logic in cmd/sieve/main.go so the TUI
// can suppress findings without invoking the CLI.
func applyIgnoreRule(path string, rule ignore.Rule) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // worktree dir
		return err
	}

	manual, managed, hasMarker, err := readIgnoreFile(path)
	if err != nil {
		return err
	}
	rules, err := ignore.Parse([]byte(managed))
	if err != nil {
		return err
	}
	rules, _ = rules.Add(rule)

	return writeIgnoreFile(path, manual, rules, hasMarker)
}

func readIgnoreFile(path string) (manual, managed string, hasMarker bool, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // worktree path
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	body := string(data)
	idx := strings.Index(body, ignore.Marker)
	if idx < 0 {
		return "", body, false, nil
	}
	manual = body[:idx]
	managed = strings.TrimPrefix(body[idx+len(ignore.Marker):], "\n")
	return manual, managed, true, nil
}

func writeIgnoreFile(path string, manual string, rules ignore.Rules, hasMarker bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // best-effort

	if strings.TrimSpace(manual) != "" {
		fmt.Fprint(f, strings.TrimRight(manual, "\n"))
		fmt.Fprint(f, "\n\n")
	}
	if hasMarker || strings.TrimSpace(manual) != "" {
		fmt.Fprintln(f, ignore.Marker)
	}
	return rules.WriteYAML(f)
}
