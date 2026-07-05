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

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/filter"
	"github.com/gnanam1990/sieve/internal/gh"
)

// FileEntry is one file of the PR with its filter decision.
type FileEntry struct {
	diff.FileDiff
	Skipped    bool
	SkipReason string `json:",omitempty"`
}

// Stats summarizes the review context.
type Stats struct {
	FilesTotal    int // from the files listing (authoritative even when the diff is truncated)
	FilesReviewed int
	FilesSkipped  int
	LinesAdded    int
	LinesRemoved  int
}

// ReviewContext is the full input a future review pass will consume, and
// the dry-run output of stage 1.
//
//nolint:revive // spec-mandated name; review.Context would shadow context.Context in readers' minds
type ReviewContext struct {
	Repo      string
	PRNumber  int
	Title     string
	Author    string
	BaseSHA   string
	HeadSHA   string
	Draft     bool
	Truncated bool
	Files     []FileEntry
	Stats     Stats
}

// Options configures a dry run.
type Options struct {
	Repo       string // "owner/name"
	PRNumber   int
	Token      string
	ConfigPath string
	APIBaseURL string // override for tests; empty means api.github.com
	Log        *slog.Logger
}

// Build fetches everything and assembles the ReviewContext.
func Build(ctx context.Context, opts Options) (*ReviewContext, error) {
	owner, name, ok := strings.Cut(opts.Repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("invalid --repo %q, want owner/name", opts.Repo)
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	client, err := gh.New(opts.Token, opts.Log)
	if err != nil {
		return nil, err
	}
	if opts.APIBaseURL != "" {
		client.BaseURL = opts.APIBaseURL
	}

	pr, err := client.GetPR(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, err
	}
	diffData, diffTruncated, err := client.GetDiff(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, err
	}
	listing, listTruncated, err := client.ListFiles(ctx, owner, name, opts.PRNumber)
	if err != nil {
		return nil, err
	}

	files, err := diff.Parse(diffData)
	if err != nil {
		return nil, fmt.Errorf("parse diff: %w", err)
	}
	results, err := filter.Apply(files, cfg.Paths.Exclude)
	if err != nil {
		return nil, err
	}

	rc := &ReviewContext{
		Repo:      opts.Repo,
		PRNumber:  opts.PRNumber,
		Title:     pr.Title,
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
	return rc, nil
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
}
