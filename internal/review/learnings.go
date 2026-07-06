package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/learnings"
	"github.com/gnanam1990/sieve/internal/memory"
)

// LearningsFile is the worktree path sieve manages.
const LearningsFile = ".sieve/learnings.md"

// Learnings clusters the repo's negative outcomes, drafts one suppressive rule
// per cluster via the configured model, and merges them into .sieve/learnings.md
// in the worktree (creating it if absent). It returns a human-readable diff of
// the change (or "" when nothing changed) and never commits — the maintainer
// reviews and commits.
func Learnings(ctx context.Context, opts Options) (string, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return "", err
	}
	owner, name, ok := strings.Cut(opts.Repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("invalid --repo %q, want owner/name", opts.Repo)
	}

	store := memory.Open(memoryHost, owner, name, opts.Log)
	events, _, err := store.Read()
	if err != nil {
		return "", fmt.Errorf("read outcome store: %w", err)
	}
	clusters := learnings.ClusterNegatives(learnings.NegativesFromEvents(events))
	if len(clusters) == 0 {
		opts.Log.Info("no negative-outcome clusters (>=2 signals); nothing to learn")
		return "", nil
	}

	if err := cfg.ValidateForReview(); err != nil {
		return "", err
	}
	rp, err := primaryProvider(cfg)
	if err != nil {
		return "", err
	}
	p, err := newProviderFrom(rp, opts, func() {})
	if err != nil {
		return "", err
	}

	ym := yearMonth(opts.now())
	var rules []learnings.Rule
	for _, c := range clusters {
		r, err := learnings.DraftRule(ctx, p, rp.MaxTokens, rp.Temperature, c)
		if err != nil {
			opts.Log.Warn("skipping cluster: rule draft failed validation", "category", c.Category, "area", c.PathPrefix, "err", err)
			continue
		}
		rules = append(rules, r)
	}
	if len(rules) == 0 {
		return "", nil
	}

	existing, _ := os.ReadFile(LearningsFile) //nolint:gosec // worktree path; absent is fine
	newBody, changed := learnings.Merge(string(existing), rules, ym)
	if !changed {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(LearningsFile), 0o755); err != nil { //nolint:gosec // worktree dir
		return "", fmt.Errorf("create %s dir: %w", LearningsFile, err)
	}
	if err := os.WriteFile(LearningsFile, []byte(newBody), 0o644); err != nil { //nolint:gosec // worktree file
		return "", fmt.Errorf("write %s: %w", LearningsFile, err)
	}
	return diffLines(string(existing), newBody), nil
}

// yearMonth extracts YYYY-MM from an RFC3339-ish timestamp.
func yearMonth(ts string) string {
	if len(ts) >= 7 {
		return ts[:7]
	}
	return ts
}

// diffLines is a minimal, dependency-free line diff: lines only in the new text
// are prefixed "+", lines only in the old "-", shared lines " ". Good enough to
// review a learnings.md change before committing it.
func diffLines(oldText, newText string) string {
	oldSet := map[string]bool{}
	for _, l := range strings.Split(oldText, "\n") {
		oldSet[l] = true
	}
	newSet := map[string]bool{}
	for _, l := range strings.Split(newText, "\n") {
		newSet[l] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (current)\n+++ %s (proposed)\n", LearningsFile, LearningsFile)
	for _, l := range strings.Split(oldText, "\n") {
		if !newSet[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(newText, "\n") {
		if !oldSet[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
