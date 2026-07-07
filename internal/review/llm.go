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
	"github.com/gnanam1990/sieve/internal/local"
	"github.com/gnanam1990/sieve/internal/prompt"
	"github.com/gnanam1990/sieve/internal/provider"
	"github.com/gnanam1990/sieve/internal/provider/anthropic"
	"github.com/gnanam1990/sieve/internal/provider/fake"
	"github.com/gnanam1990/sieve/internal/provider/openai"
)

// correctiveNote is appended to a batch's user prompt for the single
// retry after a response that failed the JSON contract.
const correctiveNote = "\n\nYour previous output was not valid JSON per the contract. Resend the full review as pure JSON — a single object {\"findings\": [...]} with no prose and no code fences."

// newProviderFrom builds the provider for one named provider config, wrapped
// in the shared retry decorator. Keys come only from the env var named by
// api_key_env.
func newProviderFrom(rp config.Provider, opts Options, onRetry func()) (provider.Provider, error) {
	var p provider.Provider
	switch rp.Type {
	case "anthropic", "openai-compat":
		key := os.Getenv(rp.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("environment variable %s (api_key_env) is unset or empty", rp.APIKeyEnv)
		}
		if rp.Type == "anthropic" {
			p = anthropic.New(key, rp.Model, rp.BaseURL)
		} else {
			p = openai.New(key, rp.Model, rp.BaseURL)
		}
	case "fake":
		p = fake.New(rp.Fixture)
	default:
		return nil, fmt.Errorf("unknown provider type %q", rp.Type)
	}
	return provider.WithRetry(p, opts.Log, onRetry), nil
}

// generatorRole resolves the provider + system prompt for the generation pass:
// the single reviewer, or the liberal generator when the judge pipeline is on.
func generatorRole(cfg config.Config) (rp config.Provider, system, role string, err error) {
	role = cfg.Review.Roles.Reviewer
	system, err = prompt.System()
	if cfg.Review.Pipeline == "judge" {
		role = cfg.Review.Roles.Generator
		system, err = prompt.Generator()
	}
	if err != nil {
		return config.Provider{}, "", "", err
	}
	rp, ok := cfg.Providers[role]
	if !ok {
		return config.Provider{}, "", "", fmt.Errorf("role provider %q is not defined", role)
	}
	return rp, system, role, nil
}

// primaryProvider resolves the provider used for auxiliary LLM tasks such as
// learnings rule drafting — the first provider the active pipeline calls.
// ValidateForReview guarantees this role is fully configured.
func primaryProvider(cfg config.Config) (config.Provider, error) {
	roles := cfg.ActiveRoles()
	if len(roles) == 0 {
		return config.Provider{}, fmt.Errorf("no active provider role for pipeline %q", cfg.Review.Pipeline)
	}
	rp, ok := cfg.Providers[roles[0]]
	if !ok {
		return config.Provider{}, fmt.Errorf("role provider %q is not defined", roles[0])
	}
	return rp, nil
}

// generation holds the prompt material shared by every generation pass in a
// run: the same batches, anchors, and learnings injection are reused across the
// single reviewer, the judge's generator, and every ensemble member.
type generation struct {
	batches   []prompt.Batch
	anchors   *findings.Anchors
	learnings string // "\n\n"-prefixed injection, appended to each member's system prompt
}

// buildGeneration assembles the shared prompt material once: learnings
// injection, the batched prompt input (recorded on rc for the judge), delta
// stats, and truncation marks. No provider calls happen here.
func buildGeneration(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, plan incremental.Plan) generation {
	var g generation
	if inj, n := fetchLearnings(ctx, client, rc, opts); inj != "" {
		g.learnings = "\n\n" + inj
		rc.learningsCount = n
	}
	input, sent := buildPromptInput(ctx, rc, client, cfg, opts, plan)
	rc.promptInput = input // reused by the judge pass (same context the generator saw)
	if !plan.Full {
		rc.Stats.FilesDeltaReviewed = len(sent)
		rc.Stats.TokensSaved = estimateTokensSaved(rc, plan)
	}
	pp, err := primaryProvider(cfg)
	if err != nil {
		// Should not happen because ValidateForReview already ran; keep the
		// historical behavior of using default batch size if it somehow did.
		pp = config.Provider{}
	}
	g.batches = prompt.BuildBatchesWithCap(input, pp.MaxInputTokens)
	markTruncated(rc, g.batches)
	g.anchors = findings.NewAnchors(sent)
	return g
}

// memberResult is one provider's generation outcome over the shared batches.
type memberResult struct {
	findings      []findings.Finding
	requests      int
	inputTokens   int
	outputTokens  int
	batchesFailed int
	dropped       int
	retries       int
}

// runGeneration runs one provider over the shared batches, anchor-validates its
// findings, and returns the aggregate. Provider construction failure is fatal;
// per-batch failures are counted, not fatal.
func runGeneration(ctx context.Context, rp config.Provider, system string, g generation, concurrency int, opts Options) (memberResult, error) {
	var res memberResult
	var retries atomic.Int64
	p, err := newProviderFrom(rp, opts, func() { retries.Add(1) })
	if err != nil {
		return res, err
	}
	system += g.learnings
	timeout := time.Duration(rp.TimeoutSeconds) * time.Second
	results := make([]batchResult, len(g.batches))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, b := range g.batches {
		wg.Add(1)
		go func(i int, b prompt.Batch) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runBatch(ctx, p, system, b, rp, timeout)
		}(i, b)
	}
	wg.Wait()

	// Merge in batch order for deterministic output.
	for i, r := range results {
		res.requests += r.requests
		res.inputTokens += r.usage.InputTokens
		res.outputTokens += r.usage.OutputTokens
		if r.err != nil {
			res.batchesFailed++
			opts.Log.Error("batch failed", "batch", i, "files", strings.Join(g.batches[i].Files, ","), "err", r.err)
			continue
		}
		for _, f := range r.findings {
			if err := g.anchors.Validate(f); err != nil {
				res.dropped++
				opts.Log.Debug("finding dropped by anchor gate", "path", f.Path, "line", f.Line, "side", f.Side, "reason", err)
				continue
			}
			res.findings = append(res.findings, f)
		}
	}
	res.retries = int(retries.Load())
	return res, nil
}

// reviewPass runs the generation pass(es) for the configured pipeline, applies
// the cost guardrail first, and leaves the surviving generator findings on rc
// for the judge/gate downstream. On a delta plan it reviews only the plan's
// paths and records the delta stats.
func reviewPass(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, plan incremental.Plan) error {
	g := buildGeneration(ctx, rc, client, cfg, opts, plan)
	if err := enforceRunBudget(cfg, g, opts); err != nil {
		return err
	}
	if cfg.Review.Pipeline == "ensemble" {
		return ensembleGenerate(ctx, rc, cfg, opts, g)
	}

	rp, system, role, err := generatorRole(cfg)
	if err != nil {
		return err
	}
	res, err := runGeneration(ctx, rp, system, g, cfg.Review.Concurrency, opts)
	if err != nil {
		return err
	}
	rc.Findings = append(rc.Findings, res.findings...)
	rc.Stats.Requests += res.requests
	rc.Stats.InputTokens += res.inputTokens
	rc.Stats.OutputTokens += res.outputTokens
	rc.Stats.BatchesFailed += res.batchesFailed
	rc.Stats.FindingsDropped += res.dropped
	rc.Stats.addRole(role, res.inputTokens, res.outputTokens)
	findings.Sort(rc.Findings)
	rc.Stats.FindingsTotal = len(rc.Findings)
	rc.Stats.Retries = res.retries
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
			content, err := readContent(ctx, opts, client, owner, name, path, rc.HeadSHA)
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
	if extra, err := buildExtraContext(ctx, cfg, opts.RepoPath, in.Files); err == nil && extra != "" {
		in.ExtraContext = extra
	} else if err != nil {
		opts.Log.Debug("context assembly failed", "depth", cfg.Review.ContextDepth, "err", err)
	}
	return in, sent
}

// readContent fetches a file's current content from GitHub or from disk in
// local mode. It is the single place buildPromptInput resolves content.
func readContent(ctx context.Context, opts Options, client *gh.Client, owner, name, path, ref string) ([]byte, error) {
	if opts.Local {
		return local.ReadFile(opts.RepoPath, path)
	}
	return client.GetContents(ctx, owner, name, path, ref)
}

// fetchLearnings loads .sieve/learnings.md at the PR head and returns the
// injection text (capped at 8 KB) and the active-rule count. Absent or
// unreadable learnings are simply skipped.
func fetchLearnings(ctx context.Context, client *gh.Client, rc *ReviewContext, opts Options) (string, int) {
	owner, name, _ := strings.Cut(rc.Repo, "/")
	var content []byte
	var err error
	if opts.Local {
		content, err = local.ReadFile(opts.RepoPath, ".sieve/learnings.md")
	} else {
		content, err = client.GetContents(ctx, owner, name, ".sieve/learnings.md", rc.HeadSHA)
	}
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
func runBatch(ctx context.Context, p provider.Provider, system string, b prompt.Batch, rp config.Provider, timeout time.Duration) batchResult {
	var r batchResult
	req := provider.Request{
		System:      system,
		User:        b.User,
		MaxTokens:   rp.MaxTokens,
		Temperature: rp.Temperature,
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
