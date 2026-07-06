package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/fingerprint"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/incremental"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/post"
	"github.com/gnanam1990/sieve/internal/render"
	"github.com/gnanam1990/sieve/internal/version"
)

// planReview locates the prior walkthrough (post runs only) and decides the
// delta plan: a full re-review, or one scoped to files changed since the last
// review with the rest of the findings carried forward. Non-post runs always
// do a full review (there is no prior walkthrough to delta from).
func planReview(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, kept []diff.FileDiff) (incremental.Plan, *post.Poster, post.Locator, []gate.CompactFinding, error) {
	if !opts.Post {
		return incremental.Plan{Full: true, FullReason: "read-only run (no prior walkthrough)"}, nil, post.Locator{}, nil, nil
	}
	owner, name, _ := strings.Cut(rc.Repo, "/")
	poster := &post.Poster{Client: client, Owner: owner, Repo: name, PR: rc.PRNumber, Log: opts.Log}
	loc, err := poster.LocateWalkthrough(ctx)
	if err != nil {
		return incremental.Plan{}, nil, post.Locator{}, nil, fmt.Errorf("locate existing walkthrough: %w", err)
	}

	in := incremental.Inputs{
		Enabled:    cfg.Review.Incremental,
		ForceFull:  opts.Full,
		HasPrior:   loc.HasMeta,
		PriorMeta:  loc.Meta,
		HeadSHA:    rc.HeadSHA,
		PRPaths:    keptPaths(rc),
		CurrentIdx: fingerprint.NewContentIndex(kept),
	}
	var prior []gate.CompactFinding
	if loc.HasMeta {
		prior = loc.Meta.PriorForRoute()
		if !loc.Meta.IsV1() && loc.Meta.HeadSHA != "" && loc.Meta.HeadSHA != rc.HeadSHA {
			cmp, found, cErr := client.Compare(ctx, owner, name, loc.Meta.HeadSHA, rc.HeadSHA)
			if cErr != nil {
				return incremental.Plan{}, nil, post.Locator{}, nil, fmt.Errorf("compare for delta review: %w", cErr)
			}
			in.Compare, in.CompareOK = cmp, found
		}
	}
	return incremental.Decide(in), poster, loc, prior, nil
}

// gateAndPost routes the fresh findings through the noise gate, merges any
// carried-forward findings and anchor-gone resolutions (delta), records the
// gate result in the JSON output, and — only when --post is set — writes the
// PR.
//
// Posting order (stage 5): the inline review is submitted first so its comment
// IDs can be recovered and stamped into the walkthrough metadata (cids power
// carry-forward and reactions). A partial inline failure is exit 2 but still
// writes the walkthrough; a walkthrough failure is exit 1.
func gateAndPost(ctx context.Context, rc *ReviewContext, cfg config.Config, opts Options, kept []diff.FileDiff, plan incremental.Plan, prior []gate.CompactFinding, poster *post.Poster, loc post.Locator) error {
	if cfg.Review.Calibration {
		applyCalibration(rc, opts)
	}
	idx := fingerprint.NewContentIndex(kept)
	priorReviewed := plan.PriorForReviewedPaths(prior)
	res := gate.Route(rc.Findings, idx, priorReviewed, cfg.Review)
	if !plan.Full {
		res.AddCarried(plan.Carried)
		res.AddResolved(plan.AnchorGone)
		rc.Stats.FindingsCarriedForward = len(plan.Carried)
	}
	rc.Gate = &res

	if !opts.Post {
		return nil
	}

	anchors := findings.NewAnchors(kept)
	comments := post.BuildInlineComments(res.Inline, anchors)
	failed, subErr := poster.SubmitInlineReview(ctx, rc.HeadSHA, comments)
	if subErr != nil {
		opts.Log.Error("inline review submission problem", "err", subErr)
	}
	rc.Stats.InlinePostFailed = failed

	// Fetch existing comments (comment IDs + reaction snapshots) and resolved
	// threads (dismissals). Best-effort — a failure leaves cids at 0 and skips
	// the outcome record; the next run reconciles.
	ghComments, cErr := poster.Comments(ctx)
	if cErr != nil {
		opts.Log.Warn("could not list review comments; metadata cids are 0 this run", "err", cErr)
	}
	cids := post.CidsOf(ghComments)
	threads, tErr := poster.ResolvedThreads(ctx)
	if tErr != nil {
		opts.Log.Warn("could not fetch review threads; dismissals not recorded this run", "err", tErr)
	}

	meta := gate.BuildMeta(rc.HeadSHA, opts.now(), res.ActiveCompact(cids), loc.Meta.Resolved, fpsOf(res.Resolved))
	walkthrough := render.Walkthrough(render.WalkthroughInput{
		Result:        res,
		Meta:          meta,
		Skipped:       skippedFiles(rc),
		FilesReviewed: rc.Stats.FilesReviewed,
		FilesSkipped:  rc.Stats.FilesSkipped,
		Model:         modelLabel(cfg),
		Learnings:     rc.learningsCount,
		Calibrated:    cfg.Review.Calibration,
		InputTokens:   rc.Stats.InputTokens,
		OutputTokens:  rc.Stats.OutputTokens,
		Pipeline:      cfg.Review.Pipeline,
		RoleTokens:    roleTokensForRender(cfg, rc.Stats),
		Version:       version.Version,
	})
	if err := poster.UpsertWalkthrough(ctx, loc, walkthrough); err != nil {
		return err
	}

	// Record outcomes to the local store (best-effort, never fails a review).
	owner, name, _ := strings.Cut(rc.Repo, "/")
	store := memory.Open(memoryHost, owner, name, opts.Log)
	recordOutcomes(store, rc, res, plan, ghComments, threads, modelLabel(cfg), opts.now())
	return nil
}

// memoryHost is the store host segment. sieve targets github.com only today.
const memoryHost = "github.com"

// fpsOf extracts the fingerprints of a resolved-finding slice.
func fpsOf(cfs []gate.CompactFinding) []string {
	out := make([]string, 0, len(cfs))
	for _, c := range cfs {
		out = append(out, c.Fp)
	}
	return out
}

// skippedFiles collects the skipped-file rows for the walkthrough.
func skippedFiles(rc *ReviewContext) []render.SkippedFile {
	var out []render.SkippedFile
	for _, f := range rc.Files {
		if !f.Skipped {
			continue
		}
		path := f.NewPath
		if path == "" {
			path = f.OldPath
		}
		out = append(out, render.SkippedFile{Path: path, Reason: f.SkipReason})
	}
	return out
}

// modelLabel names the model for the walkthrough footer, falling back to the
// provider type when no model is configured (e.g. the fake provider).
// modelLabel names the model(s) behind the active pipeline: the single
// reviewer's model, or a "+"-joined label across the roles a multi-model
// pipeline calls (generator+judge, or the ensemble members).
func modelLabel(cfg config.Config) string {
	var models []string
	seen := map[string]bool{}
	for _, name := range cfg.ActiveRoles() {
		p, ok := cfg.Providers[name]
		if !ok {
			continue
		}
		m := p.Model
		if m == "" {
			m = p.Type
		}
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	return strings.Join(models, "+")
}

// roleTokensForRender converts the per-role token map into a deterministic,
// role-ordered slice for the footer — only for multi-model pipelines, where the
// breakdown is informative (a single reviewer's row would just restate the
// aggregate).
func roleTokensForRender(cfg config.Config, st Stats) []render.RoleToken {
	if cfg.Review.Pipeline == "single" || len(st.RoleTokens) == 0 {
		return nil
	}
	out := make([]render.RoleToken, 0, len(st.RoleTokens))
	seen := map[string]bool{}
	for _, name := range cfg.ActiveRoles() {
		if seen[name] {
			continue // two roles can share one provider; count it once
		}
		if u, ok := st.RoleTokens[name]; ok {
			seen[name] = true
			out = append(out, render.RoleToken{Role: name, In: u.In, Out: u.Out})
		}
	}
	return out
}
