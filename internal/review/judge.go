package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/prompt"
	"github.com/gnanam1990/sieve/internal/provider"
)

// judgeCorrectiveNote is appended to a judge prompt for the single retry after
// a response that failed the verdict contract.
const judgeCorrectiveNote = "\n\nYour previous output was not valid JSON per the verdict contract. Resend as pure JSON — a single object {\"verdicts\": [...]} with exactly one verdict per finding index, no prose and no code fences."

// judgePass runs the generator's surviving findings past the judge model, one
// request per file, and rewrites rc.Findings to the survivors with the judge's
// (never-raised) severities and recalibrated confidences. A judge that fails
// the contract twice, or errors, falls open for that file: the generator's
// findings pass through untouched and Stats.JudgeFailedOpen is counted. The
// noise gate downstream still applies.
func judgePass(ctx context.Context, rc *ReviewContext, cfg config.Config, opts Options) error {
	if len(rc.Findings) == 0 {
		return nil
	}
	role := cfg.Review.Roles.Judge
	rp, ok := cfg.Providers[role]
	if !ok {
		return fmt.Errorf("role provider %q is not defined", role)
	}
	var retries atomic.Int64
	p, err := newProviderFrom(rp, opts, func() { retries.Add(1) })
	if err != nil {
		return err
	}
	system := prompt.JudgeSystem()
	timeout := time.Duration(rp.TimeoutSeconds) * time.Second

	// The context the generator saw, indexed by path, so the judge verifies
	// against the same diff/content rather than re-fetching.
	fileByPath := make(map[string]prompt.File, len(rc.promptInput.Files))
	for _, pf := range rc.promptInput.Files {
		fileByPath[pf.Path] = pf
	}

	// Group findings by path, preserving the current (sorted) order.
	byPath := map[string][]findings.Finding{}
	var order []string
	for _, f := range rc.Findings {
		if _, seen := byPath[f.Path]; !seen {
			order = append(order, f.Path)
		}
		byPath[f.Path] = append(byPath[f.Path], f)
	}

	results := make([]judgeFileResult, len(order))
	sem := make(chan struct{}, cfg.Review.Concurrency)
	var wg sync.WaitGroup
	for i, path := range order {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runJudge(ctx, p, system, fileByPath[path], byPath[path], rp, timeout)
		}(i, path)
	}
	wg.Wait()

	var kept []findings.Finding
	for i, path := range order {
		r := results[i]
		rc.Stats.Requests += r.requests
		rc.Stats.InputTokens += r.usage.InputTokens
		rc.Stats.OutputTokens += r.usage.OutputTokens
		rc.Stats.addRole(role, r.usage.InputTokens, r.usage.OutputTokens)
		group := byPath[path]
		if r.failOpen {
			rc.Stats.JudgeFailedOpen++
			opts.Log.Warn("judge failed open; keeping generator findings for file", "path", path, "findings", len(group), "err", r.err)
			kept = append(kept, group...)
			continue
		}
		for idx := range group {
			f := group[idx]
			v := r.verdicts[idx]
			if !v.Keep {
				rc.Stats.JudgeKilled++
				rc.JudgeDrops = append(rc.JudgeDrops, JudgeDrop{Path: f.Path, Line: f.Line, Side: f.Side, Title: f.Title, Reason: v.Reason})
				opts.Log.Debug("judge dropped finding", "path", f.Path, "line", f.Line, "reason", v.Reason)
				continue
			}
			applyVerdict(&f, v)
			kept = append(kept, f)
		}
	}
	rc.Findings = kept
	findings.Sort(rc.Findings)
	rc.Stats.FindingsTotal = len(rc.Findings)
	rc.Stats.Retries += int(retries.Load())
	return nil
}

// judgeFileResult is one file's judgment outcome.
type judgeFileResult struct {
	verdicts map[int]verdict // index -> verdict; unused when failOpen
	failOpen bool            // keep the generator's findings for this file untouched
	usage    provider.Usage
	requests int
	err      error // why it failed open (for the log), if any
}

// verdict is one finding's judgment, keyed by the generator index i.
// Confidence is a pointer so an omitted field (nil) is distinguishable from an
// explicit 0.0 — the former keeps the generator's confidence, the latter is the
// judge's deliberate call.
type verdict struct {
	I          int      `json:"i"`
	Keep       bool     `json:"keep"`
	Confidence *float64 `json:"confidence"`
	Severity   string   `json:"severity"`
	Reason     string   `json:"reason"`
}

// runJudge issues one judge request for a file's findings, with a single
// corrective retry on a contract violation. Any hard failure (transport error,
// or a second malformed response) sets failOpen.
func runJudge(ctx context.Context, p provider.Provider, system string, f prompt.File, fs []findings.Finding, rp config.Provider, timeout time.Duration) judgeFileResult {
	var r judgeFileResult
	user := prompt.JudgeUser(f, fs)
	req := provider.Request{
		System:      system,
		User:        user,
		MaxTokens:   rp.MaxTokens,
		Temperature: rp.Temperature,
	}
	resp, err := completeWithTimeout(ctx, p, req, timeout)
	r.requests++
	if err != nil {
		r.failOpen, r.err = true, err
		return r
	}
	r.usage.InputTokens += resp.Usage.InputTokens
	r.usage.OutputTokens += resp.Usage.OutputTokens

	vs, perr := parseVerdicts(resp.Text, len(fs))
	if perr != nil {
		req.User = user + judgeCorrectiveNote
		resp, err = completeWithTimeout(ctx, p, req, timeout)
		r.requests++
		if err != nil {
			r.failOpen, r.err = true, err
			return r
		}
		r.usage.InputTokens += resp.Usage.InputTokens
		r.usage.OutputTokens += resp.Usage.OutputTokens
		vs, perr = parseVerdicts(resp.Text, len(fs))
		if perr != nil {
			r.failOpen, r.err = true, perr
			return r
		}
	}
	r.verdicts = vs
	return r
}

// parseVerdicts strict-decodes the judge's verdict object and requires exactly
// one verdict per index in [0,n). Any deviation is an error the caller retries.
func parseVerdicts(text string, n int) (map[int]verdict, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in judge response")
	}
	dec := json.NewDecoder(strings.NewReader(text[start : end+1]))
	dec.DisallowUnknownFields()
	var out struct {
		Verdicts []verdict `json:"verdicts"`
	}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("judge response does not match verdict contract: %w", err)
	}
	m := make(map[int]verdict, len(out.Verdicts))
	for _, v := range out.Verdicts {
		if v.I < 0 || v.I >= n {
			return nil, fmt.Errorf("verdict index %d out of range [0,%d)", v.I, n)
		}
		if _, dup := m[v.I]; dup {
			return nil, fmt.Errorf("duplicate verdict for index %d", v.I)
		}
		m[v.I] = v
	}
	if len(m) != n {
		return nil, fmt.Errorf("judge returned %d verdicts, want %d", len(m), n)
	}
	return m, nil
}

// applyVerdict recalibrates a kept finding: a present confidence is trusted
// (clamped to [0,1]) while an omitted one leaves the generator's confidence
// intact; severity is applied only when valid and no more severe than the
// generator's — the judge may lower severity but never raise it.
func applyVerdict(f *findings.Finding, v verdict) {
	if v.Confidence != nil {
		c := *v.Confidence
		switch {
		case c < 0:
			c = 0
		case c > 1:
			c = 1
		}
		f.Confidence = c
	}
	// else: judge omitted confidence — keep the generator's, don't zero it.

	js := findings.Severity(v.Severity)
	if findings.IsValidSeverity(js) && findings.Rank(js) >= findings.Rank(f.Severity) {
		f.Severity = js // equal or less severe; a raise is ignored
	}
}
