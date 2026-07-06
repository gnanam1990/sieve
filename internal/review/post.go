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

	// Recover comment IDs for the metadata (best-effort; a failure leaves cids
	// at 0, which the next run's sync/backfill reconciles).
	cids, cErr := poster.CollectCids(ctx)
	if cErr != nil {
		opts.Log.Warn("could not collect comment IDs; metadata cids are 0 this run", "err", cErr)
	}

	meta := gate.BuildMeta(rc.HeadSHA, opts.now(), res.ActiveCompact(cids), loc.Meta.Resolved, fpsOf(res.Resolved))
	walkthrough := render.Walkthrough(render.WalkthroughInput{
		Result:        res,
		Meta:          meta,
		Skipped:       skippedFiles(rc),
		FilesReviewed: rc.Stats.FilesReviewed,
		FilesSkipped:  rc.Stats.FilesSkipped,
		Model:         modelLabel(cfg),
		InputTokens:   rc.Stats.InputTokens,
		OutputTokens:  rc.Stats.OutputTokens,
		Version:       version.Version,
	})
	if err := poster.UpsertWalkthrough(ctx, loc, walkthrough); err != nil {
		return err
	}
	return nil
}

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
func modelLabel(cfg config.Config) string {
	if cfg.Provider.Model != "" {
		return cfg.Provider.Model
	}
	return cfg.Provider.Type
}
