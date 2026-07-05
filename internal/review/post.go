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
	"github.com/gnanam1990/sieve/internal/post"
	"github.com/gnanam1990/sieve/internal/render"
	"github.com/gnanam1990/sieve/internal/version"
)

// gateAndPost routes the validated findings through the noise gate — always,
// so the gate/tier decisions land in the JSON output — and, only when --post
// is set, writes the walkthrough and inline review to the PR.
//
// Failure model (R7): a walkthrough failure is returned as an error (exit 1,
// nothing else attempted); a partial inline failure is recorded in
// Stats.InlinePostFailed (exit 2) without failing the run.
func gateAndPost(ctx context.Context, rc *ReviewContext, client *gh.Client, cfg config.Config, opts Options, kept []diff.FileDiff) error {
	idx := fingerprint.NewContentIndex(kept)

	var prior []gate.PriorFinding
	var poster *post.Poster
	var loc post.Locator

	if opts.Post {
		owner, name, _ := strings.Cut(rc.Repo, "/")
		poster = &post.Poster{Client: client, Owner: owner, Repo: name, PR: rc.PRNumber, Log: opts.Log}
		var err error
		loc, err = poster.LocateWalkthrough(ctx)
		if err != nil {
			return fmt.Errorf("locate existing walkthrough: %w", err)
		}
		if loc.HasMeta {
			prior = loc.Meta.Fps
		}
	}

	res := gate.Route(rc.Findings, idx, prior, cfg.Review)
	rc.Gate = &res

	if !opts.Post {
		return nil
	}

	meta := gate.BuildMeta(rc.HeadSHA, opts.now(), res)
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

	// Walkthrough first. Its failure is terminal (exit 1) — we do not attempt
	// the inline review on top of a missing walkthrough.
	if err := poster.UpsertWalkthrough(ctx, loc, walkthrough); err != nil {
		return err
	}

	anchors := findings.NewAnchors(kept)
	comments := post.BuildInlineComments(res.Inline, anchors)
	failed, err := poster.SubmitInlineReview(ctx, rc.HeadSHA, comments)
	if err != nil {
		// A hard submission error still leaves the walkthrough posted, so it is
		// a partial (exit 2) outcome, surfaced via the failed count below.
		opts.Log.Error("inline review submission problem", "err", err)
	}
	rc.Stats.InlinePostFailed = failed
	return nil
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
