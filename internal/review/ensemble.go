package review

import (
	"context"
	"fmt"
	"sort"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/prompt"
)

// ensembleGenerate runs each ensemble member independently over the single
// reviewer prompt, then keeps only findings two or more distinct members agree
// on. Survivors carry the highest-confidence member finding plus the cluster's
// mean confidence, and flow through the normal gate downstream.
func ensembleGenerate(ctx context.Context, rc *ReviewContext, cfg config.Config, opts Options, g generation) error {
	members := cfg.Review.Roles.Ensemble
	system, err := prompt.System()
	if err != nil {
		return err
	}
	rc.Stats.EnsembleMembers = len(members)

	results := make([]memberResult, len(members))
	for mi, name := range members {
		rp, ok := cfg.Providers[name]
		if !ok {
			return fmt.Errorf("ensemble member %q is not a defined provider", name)
		}
		res, err := runGeneration(ctx, rp, system, g, cfg.Review.Concurrency, opts)
		if err != nil {
			return err
		}
		results[mi] = res
		rc.Stats.Requests += res.requests
		rc.Stats.InputTokens += res.inputTokens
		rc.Stats.OutputTokens += res.outputTokens
		rc.Stats.BatchesFailed += res.batchesFailed
		rc.Stats.FindingsDropped += res.dropped
		rc.Stats.Retries += res.retries
		rc.Stats.addRole(name, res.inputTokens, res.outputTokens)
	}

	survivors, dropped := clusterEnsemble(results)
	rc.Findings = append(rc.Findings, survivors...)
	rc.Stats.EnsembleClusters = len(survivors)
	rc.Stats.EnsembleDropped = dropped
	findings.Sort(rc.Findings)
	rc.Stats.FindingsTotal = len(rc.Findings)
	return nil
}

// taggedFinding pairs a finding with the index of the member that produced it,
// so a cluster's distinct-member count can gate agreement.
type taggedFinding struct {
	f      findings.Finding
	member int
}

// clusterEnsemble union-find clusters all members' findings by agreement and
// returns one survivor per cluster backed by ≥2 distinct members (the
// highest-confidence member finding, annotated with the cluster mean), plus the
// count of findings dropped for lacking a second member.
func clusterEnsemble(results []memberResult) (survivors []findings.Finding, dropped int) {
	var all []taggedFinding
	for mi, r := range results {
		for _, f := range r.findings {
			all = append(all, taggedFinding{f: f, member: mi})
		}
	}
	n := len(all)
	if n == 0 {
		return nil, 0
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]] // path halving
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if ensembleAgree(all[i].f, all[j].f) {
				union(i, j)
			}
		}
	}

	groups := map[int][]int{}
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}
	roots := make([]int, 0, len(groups))
	for root := range groups {
		roots = append(roots, root)
	}
	sort.Ints(roots) // deterministic emission order (findings.Sort re-sorts later)

	for _, root := range roots {
		idxs := groups[root]
		distinct := map[int]bool{}
		for _, i := range idxs {
			distinct[all[i].member] = true
		}
		if len(distinct) < 2 {
			dropped += len(idxs)
			continue
		}
		// EnsembleMean is the mean confidence ACROSS DISTINCT MEMBERS (each
		// represented by its best finding in the cluster), so a single chatty
		// member with several near-duplicate findings doesn't skew the agreement
		// metric. `best` is still the single highest-confidence member finding.
		best := idxs[0]
		memberBest := map[int]float64{}
		for _, i := range idxs {
			conf := all[i].f.Confidence
			if conf > all[best].f.Confidence {
				best = i
			}
			m := all[i].member
			if b, ok := memberBest[m]; !ok || conf > b {
				memberBest[m] = conf
			}
		}
		sum := 0.0
		for _, c := range memberBest {
			sum += c
		}
		f := all[best].f
		f.EnsembleMean = sum / float64(len(memberBest))
		survivors = append(survivors, f)
	}
	return survivors, dropped
}

// ensembleAgree reports whether two findings are the "same" issue for ensemble
// clustering: same path, category, and diff side, with lines within ±3 or
// overlapping ranges. Side is required — a LEFT and RIGHT anchor at the same
// number are different lines.
func ensembleAgree(a, b findings.Finding) bool {
	if a.Path != b.Path || a.Category != b.Category || a.Side != b.Side {
		return false
	}
	if abs(a.Line-b.Line) <= 3 {
		return true
	}
	return rangesOverlap(a, b)
}

// lineRange returns a finding's [start,end] inclusive; a zero/short EndLine
// collapses to a single line.
func lineRange(f findings.Finding) (int, int) {
	end := f.EndLine
	if end < f.Line {
		end = f.Line
	}
	return f.Line, end
}

func rangesOverlap(a, b findings.Finding) bool {
	as, ae := lineRange(a)
	bs, be := lineRange(b)
	return as <= be && bs <= ae
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// enforceRunBudget refuses a run whose pre-flight token estimate exceeds
// review.max_run_tokens, before any provider is called. The estimate is
// bytes/4 over the batched prompt, scaled by the pipeline multiplier.
func enforceRunBudget(cfg config.Config, g generation, opts Options) error {
	limit := cfg.Review.MaxRunTokens
	if limit <= 0 {
		return nil
	}
	est := estimateRunTokens(g, pipelineMultiplier(cfg))
	if est > limit {
		return fmt.Errorf("estimated run cost ~%d tokens exceeds review.max_run_tokens %d; no provider was called (raise the cap or narrow the PR)", est, limit)
	}
	opts.Log.Debug("pre-flight token estimate within budget", "estimate", est, "max", limit, "pipeline", cfg.Review.Pipeline)
	return nil
}

// estimateRunTokens approximates a run's input tokens: the batched prompt bytes
// over the ~4 bytes/token heuristic, times the pipeline multiplier.
func estimateRunTokens(g generation, multiplier float64) int {
	bytes := 0
	for _, b := range g.batches {
		bytes += len(b.User)
	}
	return int(float64(bytes/4) * multiplier)
}

// pipelineMultiplier scales the single-pass estimate for the extra provider
// work each pipeline does: judge adds a verification pass (~1.6×), ensemble
// runs one pass per member (n×).
func pipelineMultiplier(cfg config.Config) float64 {
	switch cfg.Review.Pipeline {
	case "judge":
		return 1.6
	case "ensemble":
		if n := len(cfg.Review.Roles.Ensemble); n > 1 {
			return float64(n)
		}
		return 1.0
	default:
		return 1.0
	}
}
