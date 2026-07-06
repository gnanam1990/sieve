package review

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/incremental"
	"github.com/gnanam1990/sieve/internal/learnings"
	"github.com/gnanam1990/sieve/internal/prompt"
	"github.com/gnanam1990/sieve/internal/provider"
	"github.com/gnanam1990/sieve/internal/provider/anthropic"
	"github.com/gnanam1990/sieve/internal/provider/fake"
	"github.com/gnanam1990/sieve/internal/provider/openai"
)

// correctiveNote is appended to a batch's user prompt for the single
// retry after a response that failed the JSON contract.
const correctiveNote = "\n\nYour previous output was not valid JSON per the contract. Resend the full review as pure JSON — a single object {\"findings\": [...]} with no prose and no code fences."

// newProvider builds the configured provider, wrapped in the shared retry
// decorator. API keys come exclusively from the env var named by
// provider.api_key_env.
func newProvider(cfg config.Config, opts Options, onRetry func()) (provider.Provider, error) {
	var p provider.Provider
	switch cfg.Provider.Type {
	case "anthropic", "openai-compat":
		key := os.Getenv(cfg.Provider.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("environment variable %s (provider.api_key_env) is unset or empty", cfg.Provider.APIKeyEnv)
		}
		if cfg.Provider.Type == "anthropic" {
			p = anthropic.New(key, cfg.Provider.Model, cfg.Provider.BaseURL)
		} else {
			p = openai.New(key, cfg.Provider.Model, cfg.Provider.BaseURL)
		}
	case "fake":
		p = fake.New(cfg.Provider.Fixture)
	default:
		return nil, fmt.Errorf("unknown provider.type %q", cfg.Provider.Type)
	}
	return provider.WithRetry(p, opts.Log, onRetry), nil
}

// reviewPass runs prompts through the provider over bounded-parallel batches,
// parses and anchor-validates findings, and merges them into rc. On a delta
// plan it reviews only the plan's paths and records the delta stats.
func reviewPass(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, plan incremental.Plan) error {
	var retries atomic.Int64
	p, err := newProvider(cfg, opts, func() { retries.Add(1) })
	if err != nil {
		return err
	}
	system, err := prompt.System()
	if err != nil {
		return err
	}
	if inj, n := fetchLearnings(ctx, client, rc, opts); inj != "" {
		system += "\n\n" + inj
		rc.learningsCount = n
	}

	input, sent := buildPromptInput(ctx, rc, client, cfg, opts, plan)
	if !plan.Full {
		rc.Stats.FilesDeltaReviewed = len(sent)
		rc.Stats.TokensSaved = estimateTokensSaved(rc, plan)
	}
	batches := prompt.BuildBatches(input)
	markTruncated(rc, batches)
	anchors := findings.NewAnchors(sent)

	timeout := time.Duration(cfg.Provider.TimeoutSeconds) * time.Second
	results := make([]batchResult, len(batches))
	sem := make(chan struct{}, cfg.Review.Concurrency)
	var wg sync.WaitGroup
	for i, b := range batches {
		wg.Add(1)
		go func(i int, b prompt.Batch) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runBatch(ctx, p, system, b, cfg, timeout)
		}(i, b)
	}
	wg.Wait()

	// Merge in batch order for deterministic output.
	for i, r := range results {
		rc.Stats.Requests += r.requests
		rc.Stats.InputTokens += r.usage.InputTokens
		rc.Stats.OutputTokens += r.usage.OutputTokens
		if r.err != nil {
			rc.Stats.BatchesFailed++
			opts.Log.Error("batch failed", "batch", i, "files", strings.Join(batches[i].Files, ","), "err", r.err)
			continue
		}
		for _, f := range r.findings {
			if err := anchors.Validate(f); err != nil {
				rc.Stats.FindingsDropped++
				opts.Log.Debug("finding dropped by anchor gate", "path", f.Path, "line", f.Line, "side", f.Side, "reason", err)
				continue
			}
			rc.Findings = append(rc.Findings, f)
		}
	}
	findings.Sort(rc.Findings)
	rc.Stats.FindingsTotal = len(rc.Findings)
	rc.Stats.Retries = int(retries.Load())
	return nil
}

// buildPromptInput converts the reviewed files into prompt input, optionally
// attaching full file contents fetched at the head SHA. On a delta plan only
// the plan's paths are included; the returned slice is the files actually sent.
func buildPromptInput(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, plan incremental.Plan) (prompt.Input, []diff.FileDiff) {
	owner, name, _ := strings.Cut(rc.Repo, "/")
	maxContent := cfg.Review.MaxFileContentKB * 1024
	in := prompt.Input{Title: rc.Title, Body: rc.Body}
	var sent []diff.FileDiff
	for _, fe := range rc.Files {
		if fe.Skipped {
			continue
		}
		path := fe.NewPath
		if path == "" {
			path = fe.OldPath
		}
		if !plan.Full && !plan.ReviewPaths[path] {
			continue // delta: this file was not changed since the last review
		}
		sent = append(sent, fe.FileDiff)
		f := prompt.File{Path: path, Status: fe.Status.String(), Diff: fe.FileDiff}
		if cfg.Review.IncludeFileContent && fe.Status != diff.Deleted {
			content, err := client.GetContents(ctx, owner, name, path, rc.HeadSHA)
			switch {
			case err != nil:
				opts.Log.Debug("skipping content attachment", "path", path, "err", err)
			case len(content) > maxContent:
				opts.Log.Debug("skipping content attachment: over size cap", "path", path, "bytes", len(content), "cap", maxContent)
			default:
				f.Content = content
			}
		}
		in.Files = append(in.Files, f)
	}
	return in, sent
}

// fetchLearnings loads .sieve/learnings.md at the PR head and returns the
// injection text (capped at 8 KB) and the active-rule count. Absent or
// unreadable learnings are simply skipped.
func fetchLearnings(ctx context.Context, client *gh.Client, rc *ReviewContext, opts Options) (string, int) {
	owner, name, _ := strings.Cut(rc.Repo, "/")
	content, err := client.GetContents(ctx, owner, name, ".sieve/learnings.md", rc.HeadSHA)
	if err != nil {
		return "", 0 // no learnings file (common); not an error
	}
	text, count := learnings.InjectionText(string(content))
	if count > 0 {
		opts.Log.Debug("applying repository learnings", "rules", count)
	}
	return text, count
}

// estimateTokensSaved approximates the input tokens a delta run avoided: the
// diff bytes of kept files that were NOT re-reviewed, over the ~4 bytes/token
// heuristic used by the batcher. An estimate, not a measurement.
func estimateTokensSaved(rc *ReviewContext, plan incremental.Plan) int {
	bytes := 0
	for _, fe := range rc.Files {
		if fe.Skipped {
			continue
		}
		path := fe.NewPath
		if path == "" {
			path = fe.OldPath
		}
		if plan.ReviewPaths[path] {
			continue // reviewed: not saved
		}
		for _, h := range fe.Hunks {
			for _, l := range h.Lines {
				bytes += len(l.Content) + 1
			}
		}
	}
	return bytes / 4
}

func markTruncated(rc *ReviewContext, batches []prompt.Batch) {
	cut := map[string]bool{}
	for _, b := range batches {
		for _, p := range b.Truncated {
			cut[p] = true
		}
	}
	if len(cut) == 0 {
		return
	}
	for i := range rc.Files {
		path := rc.Files[i].NewPath
		if path == "" {
			path = rc.Files[i].OldPath
		}
		if cut[path] {
			rc.Files[i].TruncatedForReview = true
		}
	}
}

type batchResult struct {
	findings []findings.Finding
	usage    provider.Usage
	requests int
	err      error
}

// runBatch performs one provider call, with a single corrective retry if
// the response violates the JSON contract.
func runBatch(ctx context.Context, p provider.Provider, system string, b prompt.Batch, cfg config.Config, timeout time.Duration) batchResult {
	var r batchResult
	req := provider.Request{
		System:      system,
		User:        b.User,
		MaxTokens:   cfg.Provider.MaxTokens,
		Temperature: cfg.Provider.Temperature,
	}
	resp, err := completeWithTimeout(ctx, p, req, timeout)
	r.requests++
	if err != nil {
		r.err = err
		return r
	}
	r.usage.InputTokens += resp.Usage.InputTokens
	r.usage.OutputTokens += resp.Usage.OutputTokens

	fs, perr := findings.ParseResponse(resp.Text)
	if perr != nil {
		req.User = b.User + correctiveNote
		resp, err = completeWithTimeout(ctx, p, req, timeout)
		r.requests++
		if err != nil {
			r.err = err
			return r
		}
		r.usage.InputTokens += resp.Usage.InputTokens
		r.usage.OutputTokens += resp.Usage.OutputTokens
		fs, perr = findings.ParseResponse(resp.Text)
		if perr != nil {
			r.err = fmt.Errorf("response invalid after corrective retry: %w", perr)
			return r
		}
	}
	r.findings = fs
	return r
}

func completeWithTimeout(ctx context.Context, p provider.Provider, req provider.Request, timeout time.Duration) (provider.Response, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return p.Complete(tctx, req)
}
