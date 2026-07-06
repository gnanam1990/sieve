package review

import (
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/incremental"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/render"
)

// recordOutcomes appends this run's outcome events to the local store: a run
// event, one finding event per active finding, one resolved event per resolved
// finding (with the resolution mechanism), a reaction snapshot per sieve
// comment (inline cids only), and a dismissed event for each resolved thread
// whose finding is still active (a human closed the thread without a fix).
//
// All writes are best-effort; a store failure never affects the review.
func recordOutcomes(store *memory.Store, rc *ReviewContext, res gate.GateResult, plan incremental.Plan, comments []gh.ReviewComment, threads []gh.ReviewThread, model, ts string) {
	var events []memory.Event

	events = append(events, memory.Event{
		Ts: ts, Type: memory.TypeRun, PR: rc.PRNumber, HeadSHA: rc.HeadSHA,
		Model: model, InTok: rc.Stats.InputTokens, OutTok: rc.Stats.OutputTokens,
		Inline: res.Stats.InlineCount, Notes: res.Stats.NotesCount, Dropped: rc.Stats.FindingsDropped,
	})

	active := make(map[string]bool)
	for _, f := range append(append([]gate.Finding{}, res.Inline...), res.Notes...) {
		active[f.Fingerprint] = true
		events = append(events, memory.Event{
			Ts: ts, Type: memory.TypeFinding, Fp: f.Fingerprint, Path: f.Path,
			Sev: string(f.Severity), Conf: f.Confidence, Cat: f.Category,
			Tier: f.Tier.String(), Title: f.Title, Cid: f.Cid,
		})
	}

	anchorGone := make(map[string]bool, len(plan.AnchorGone))
	for _, cf := range plan.AnchorGone {
		anchorGone[cf.Fp] = true
	}
	for _, r := range res.Resolved {
		how := memory.ResolvedReReviewAbsent
		if anchorGone[r.Fp] {
			how = memory.ResolvedAnchorGone
		}
		events = append(events, memory.Event{Ts: ts, Type: memory.TypeResolved, Fp: r.Fp, Path: r.Path, How: how})
	}

	// Reaction snapshots — inline cids only (cid != 0, i.e. sieve's posted
	// comments). Emitted every run; aggregation keeps the latest.
	for _, c := range comments {
		fp := render.ParseFpMarker(c.Body)
		if fp == "" || c.ID == 0 {
			continue
		}
		if c.Reactions.PlusOne == 0 && c.Reactions.MinusOne == 0 {
			continue
		}
		events = append(events, memory.Event{
			Ts: ts, Type: memory.TypeReaction, Fp: fp, Cid: c.ID,
			Plus: c.Reactions.PlusOne, Minus: c.Reactions.MinusOne,
		})
	}

	// Dismissals — a resolved thread whose finding is still active means the
	// human closed it without a fix (the anchor content is unchanged).
	for _, th := range threads {
		if !th.IsResolved {
			continue
		}
		for _, cm := range th.Comments {
			fp := render.ParseFpMarker(cm.Body)
			if fp != "" && active[fp] {
				events = append(events, memory.Event{Ts: ts, Type: memory.TypeDismissed, Fp: fp, Cid: cm.DatabaseID})
			}
		}
	}

	store.Append(events...)
}
