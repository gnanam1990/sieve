// Package review assembles the ReviewContext for a PR: metadata + parsed
// diff + filter decisions. No LLM calls, no GitHub writes (stage 1).
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/filter"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
)

// FileEntry is one file of the PR with its filter decision.
type FileEntry struct {
	diff.FileDiff
	Skipped            bool
	SkipReason         string `json:",omitempty"`
	TruncatedForReview bool   `json:",omitempty"` // diff cut to fit the per-batch token budget
}

// Stats summarizes the review context.
type Stats struct {
	FilesTotal    int // from the files listing (authoritative even when the diff is truncated)
	FilesReviewed int
	FilesSkipped  int
	LinesAdded    int
	LinesRemoved  int

	FindingsTotal   int // surviving (anchor-valid) findings
	FindingsDropped int // rejected by the anchor/shape gate
	BatchesFailed   int
	Requests        int
	Retries         int
	InputTokens     int
	OutputTokens    int

	InlinePostFailed int // inline comments that failed to post (--post); >0 => exit 2
}

// ReviewContext is the full input a future review pass will consume, and
// the dry-run output of stage 1.
//
//nolint:revive // spec-mandated name; review.Context would shadow context.Context in readers' minds
type ReviewContext struct {
	Repo      string
	PRNumber  int
	Title     string
	Body      string `json:",omitempty"`
	Author    string
	BaseSHA   string
	HeadSHA   string
	Draft     bool
	Truncated bool
	Files     []FileEntry
	Findings  []findings.Finding
	Gate      *gate.GateResult `json:",omitempty"` // tier routing + drop/demote counters + fingerprints
	Stats     Stats
}

// Options configures a run.
type Options struct {
	Repo       string // "owner/name"
	PRNumber   int
	Token      string
	ConfigPath string
	DryRun     bool          // stop after context assembly; no LLM calls
	Post       bool          // write results to the PR (walkthrough + inline review)
	APIBaseURL string        // override for tests; empty means api.github.com
	Now        func() string // metadata timestamp source; defaults to time.Now (UTC RFC3339)
	Log        *slog.Logger
}

// now returns the metadata timestamp, using the injected clock when set.
func (o Options) now() string {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// Run performs the full pipeline: stage-1 context assembly, then (unless
// DryRun or a skipped draft) the LLM review pass. Still zero GitHub writes.
func Run(ctx context.Context, opts Options) (*ReviewContext, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	rc, client, err := build(ctx, opts, cfg)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		return rc, nil
	}
	if err := cfg.ValidateForReview(); err != nil {
		return nil, err
	}
	if rc.Draft && !cfg.Review.ReviewDrafts {
		// R1.4: --post on a draft still respects review_drafts:false — skip,
		// notice, exit 0. No gate, no writes.
		opts.Log.Info("PR is a draft; skipping review and any posting (set review.review_drafts: true to review drafts)")
		return rc, nil
	}
	kept, err := reviewPass(ctx, rc, client, cfg, opts)
	if err != nil {
		return nil, err
	}
	if err := gateAndPost(ctx, rc, client, cfg, opts, kept); err != nil {
		return nil, err
	}
	return rc, nil
}

// Build assembles the stage-1 ReviewContext only (no LLM).
func Build(ctx context.Context, opts Options) (*ReviewContext, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	rc, _, err := build(ctx, opts, cfg)
	return rc, err
}

func build(ctx context.Context, opts Options, cfg config.Config) (*ReviewContext, *gh.Client, error) {
	owner, name, ok := strings.Cut(opts.Repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, nil, fmt.Errorf("invalid --repo %q, want owner/name", opts.Repo)
	}

	client, err := gh.New(gh.NewStaticTokenSource(opts.Token), opts.Log)
	if err != nil {
		return nil, nil, err
	}
	if opts.APIBaseURL != "" {
		client.BaseURL = opts.APIBaseURL
	}

	pr, err := client.GetPR(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, nil, err
	}
	diffData, diffTruncated, err := client.GetDiff(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, nil, err
	}
	listing, listTruncated, err := client.ListFiles(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, nil, err
	}

	files, err := diff.Parse(diffData)
	if err != nil {
		return nil, nil, fmt.Errorf("parse diff: %w", err)
	}
	results, err := filter.Apply(files, cfg.Paths.Exclude)
	if err != nil {
		return nil, nil, err
	}

	rc := &ReviewContext{
		Findings:  []findings.Finding{}, // marshals as [] rather than null
		Repo:      opts.Repo,
		PRNumber:  opts.PRNumber,
		Title:     pr.Title,
		Body:      pr.Body,
		Author:    pr.User.Login,
		BaseSHA:   pr.Base.SHA,
		HeadSHA:   pr.Head.SHA,
		Draft:     pr.Draft,
		Truncated: diffTruncated || listTruncated,
	}
	rc.Stats.FilesTotal = len(listing)
	for _, r := range results {
		rc.Files = append(rc.Files, FileEntry{FileDiff: r.File, Skipped: r.Skipped, SkipReason: r.SkipReason})
		if r.Skipped {
			rc.Stats.FilesSkipped++
		} else {
			rc.Stats.FilesReviewed++
		}
		for _, h := range r.File.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.AddedLine:
					rc.Stats.LinesAdded++
				case diff.RemovedLine:
					rc.Stats.LinesRemoved++
				}
			}
		}
	}
	return rc, client, nil
}

// WriteJSON emits the canonical JSON form: struct-ordered fields, 2-space
// indent, trailing newline. Byte-identical across runs for the same input.
func (rc *ReviewContext) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rc)
}

// WriteSummary emits the human-readable stderr table.
func (rc *ReviewContext) WriteSummary(w io.Writer) {
	draft := ""
	if rc.Draft {
		draft = " [draft]"
	}
	truncated := ""
	if rc.Truncated {
		truncated = " [TRUNCATED]"
	}
	fmt.Fprintf(w, "%s#%d %q by %s%s%s\n", rc.Repo, rc.PRNumber, rc.Title, rc.Author, draft, truncated)
	fmt.Fprintf(w, "base %.12s -> head %.12s\n\n", rc.BaseSHA, rc.HeadSHA)

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tSTATUS\t+\t-\tREVIEW")
	for _, f := range rc.Files {
		path := f.NewPath
		if path == "" {
			path = f.OldPath
		}
		var added, removed int
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.AddedLine:
					added++
				case diff.RemovedLine:
					removed++
				}
			}
		}
		decision := "keep"
		if f.Skipped {
			decision = "skip: " + f.SkipReason
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", path, f.Status, added, removed, decision)
	}
	tw.Flush() //nolint:errcheck // best-effort human output

	fmt.Fprintf(w, "\n%d files total, %d to review, %d skipped, +%d -%d lines\n",
		rc.Stats.FilesTotal, rc.Stats.FilesReviewed, rc.Stats.FilesSkipped, rc.Stats.LinesAdded, rc.Stats.LinesRemoved)

	if len(rc.Findings) > 0 {
		fmt.Fprintf(w, "\nFINDINGS (%d)\n", len(rc.Findings))
		ftw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(ftw, "SEVERITY\tLOCATION\tCONF\tTITLE")
		for _, f := range rc.Findings {
			loc := fmt.Sprintf("%s:%d", f.Path, f.Line)
			if f.EndLine > 0 {
				loc = fmt.Sprintf("%s:%d-%d", f.Path, f.Line, f.EndLine)
			}
			fmt.Fprintf(ftw, "%s\t%s\t%.2f\t%s\n", f.Severity, loc, f.Confidence, f.Title)
		}
		ftw.Flush() //nolint:errcheck // best-effort human output
	}
	if rc.Stats.Requests > 0 {
		fmt.Fprintf(w, "\n%d findings (%d dropped by anchor gate), %d requests (%d retries, %d batches failed), tokens in/out %d/%d\n",
			rc.Stats.FindingsTotal, rc.Stats.FindingsDropped, rc.Stats.Requests, rc.Stats.Retries,
			rc.Stats.BatchesFailed, rc.Stats.InputTokens, rc.Stats.OutputTokens)
	}
}
