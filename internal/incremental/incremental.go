// Package incremental decides how much of a re-review a run needs: a full
// re-review, or a delta that re-reviews only the files changed since the last
// posted walkthrough and carries the rest of the active findings forward.
//
// The decision is pure — given the prior metadata, the compare range, the
// current PR file set, and a content index over the current diff, it returns a
// Plan. It never calls the network or an LLM; the "anchor gone" resolution
// check recomputes fingerprints locally at zero model cost.
package incremental

import (
	"github.com/gnanam1990/sieve/internal/fingerprint"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
)

// Plan is the delta-review decision.
type Plan struct {
	Full        bool                  // do a full re-review of every kept file
	FullReason  string                // why (logged + Stats.FullReviewReason); empty when delta
	ReviewPaths map[string]bool       // delta: PR paths to re-review
	Carried     []gate.CompactFinding // prior findings on untouched files, anchor still present
	AnchorGone  []gate.CompactFinding // prior findings resolved because their anchor content vanished
}

// Inputs are everything Decide needs.
type Inputs struct {
	Enabled    bool                      // review.incremental
	ForceFull  bool                      // --full
	HasPrior   bool                      // a prior walkthrough existed
	PriorMeta  gate.Meta                 // decoded prior metadata
	HeadSHA    string                    // current head SHA
	Compare    gh.CompareResult          // compare(prior head ... current head)
	CompareOK  bool                      // compare succeeded (base SHA found)
	PRPaths    map[string]bool           // current kept PR file set
	CurrentIdx *fingerprint.ContentIndex // content index over the current full diff
}

// Decide returns the review plan.
func Decide(in Inputs) Plan {
	switch {
	case !in.Enabled:
		return full("incremental disabled (review.incremental: false)")
	case in.ForceFull:
		return full("--full requested")
	case !in.HasPrior:
		return full("no prior walkthrough to delta from")
	case in.PriorMeta.IsV1():
		return full("prior metadata is v1 (no compact records); upgrading with a full review")
	case in.PriorMeta.HeadSHA == "":
		return full("prior metadata has no head SHA")
	case in.PriorMeta.HeadSHA == in.HeadSHA:
		return full("head SHA unchanged")
	case !in.CompareOK || !in.Compare.AheadOnly():
		return full("base SHA is not an ancestor (force-push/rebase) or compare unavailable")
	}

	reviewPaths := make(map[string]bool)
	for _, f := range in.Compare.Files {
		if in.PRPaths[f] {
			reviewPaths[f] = true
		}
	}

	var carried, anchorGone []gate.CompactFinding
	for _, cf := range in.PriorMeta.Findings {
		if reviewPaths[cf.Path] {
			continue // re-reviewed: gate.Route decides repeat vs resolve
		}
		if anchorPresent(cf, in.CurrentIdx) {
			carried = append(carried, cf)
		} else {
			anchorGone = append(anchorGone, cf) // source (b): content-gone resolution
		}
	}
	return Plan{ReviewPaths: reviewPaths, Carried: carried, AnchorGone: anchorGone}
}

func full(reason string) Plan { return Plan{Full: true, FullReason: reason} }

// anchorPresent reports whether cf's fingerprint is still reproducible from the
// current diff — i.e. a line at (path, side) still has the anchored content.
// A nil index means no current diff to check, so the finding is conservatively
// kept (carried).
func anchorPresent(cf gate.CompactFinding, idx *fingerprint.ContentIndex) bool {
	if idx == nil {
		return true
	}
	side := string(gate.ExpandSide(cf.Side))
	for _, content := range idx.ContentsFor(cf.Path, side) {
		if fingerprint.For(cf.Path, side, cf.Category, cf.Title, content) == cf.Fp {
			return true
		}
	}
	return false
}

// PriorForReviewedPaths scopes the prior compact findings to the re-reviewed
// paths, so gate.Route's repeat/resolve logic only fires for files actually
// re-reviewed this run (untouched files carry forward instead).
func (p Plan) PriorForReviewedPaths(prior []gate.CompactFinding) []gate.CompactFinding {
	if p.Full {
		return prior
	}
	out := make([]gate.CompactFinding, 0, len(prior))
	for _, cf := range prior {
		if p.ReviewPaths[cf.Path] {
			out = append(out, cf)
		}
	}
	return out
}
