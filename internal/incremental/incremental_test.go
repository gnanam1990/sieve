package incremental

import (
	"testing"

	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/fingerprint"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
)

func v2Meta(head string, fs ...gate.CompactFinding) gate.Meta {
	return gate.Meta{Version: 2, HeadSHA: head, Findings: fs}
}

func baseInputs() Inputs {
	return Inputs{
		Enabled:   true,
		HasPrior:  true,
		PriorMeta: v2Meta("oldsha"),
		HeadSHA:   "newsha",
		Compare:   gh.CompareResult{Status: "ahead"},
		CompareOK: true,
		PRPaths:   map[string]bool{},
	}
}

// TestDecideFullFallbacks exhaustively checks the reasons a run falls back to a
// full re-review.
func TestDecideFullFallbacks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
		reason string
	}{
		{"disabled", func(i *Inputs) { i.Enabled = false }, "disabled"},
		{"force full", func(i *Inputs) { i.ForceFull = true }, "--full"},
		{"no prior", func(i *Inputs) { i.HasPrior = false }, "no prior"},
		{"v1 meta", func(i *Inputs) { i.PriorMeta = gate.Meta{Version: 1, Fps: []gate.PriorFinding{{Fingerprint: "a"}}} }, "v1"},
		{"no prior head", func(i *Inputs) { i.PriorMeta = v2Meta("") }, "no head SHA"},
		{"same sha", func(i *Inputs) { i.PriorMeta = v2Meta("newsha") }, "unchanged"},
		{"compare not found", func(i *Inputs) { i.CompareOK = false }, "not an ancestor"},
		{"diverged", func(i *Inputs) { i.Compare = gh.CompareResult{Status: "diverged"} }, "not an ancestor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInputs()
			c.mutate(&in)
			p := Decide(in)
			if !p.Full {
				t.Fatalf("expected full re-review, got delta")
			}
			if !contains(p.FullReason, c.reason) {
				t.Fatalf("reason %q does not mention %q", p.FullReason, c.reason)
			}
		})
	}
}

// TestDecideDelta: a normal push reviews only changed+PR files, carries
// untouched findings whose anchor survives, and resolves anchor-gone ones.
func TestDecideDelta(t *testing.T) {
	// A carried finding on an untouched file whose anchor content still exists.
	keepContent := "return doThing()"
	keepFp := fingerprint.For("keep.go", "RIGHT", "bug", "problem", keepContent)
	// A finding on an untouched file whose anchor content is gone.
	goneFp := fingerprint.For("gone.go", "RIGHT", "bug", "was here", "deleted content")
	// A finding on a changed file — handled by gate.Route, not carried here.
	changedFp := fingerprint.For("changed.go", "RIGHT", "bug", "in flux", "x")

	idx := fingerprint.NewContentIndex([]diff.FileDiff{
		{NewPath: "keep.go", Hunks: []diff.Hunk{{Lines: []diff.Line{{Kind: diff.AddedLine, NewNum: 10, Content: keepContent}}}}},
		{NewPath: "gone.go", Hunks: []diff.Hunk{{Lines: []diff.Line{{Kind: diff.AddedLine, NewNum: 3, Content: "something else now"}}}}},
	})

	in := baseInputs()
	in.Compare = gh.CompareResult{Status: "ahead", Files: []string{"changed.go", "excluded.bin"}}
	in.PRPaths = map[string]bool{"changed.go": true, "keep.go": true, "gone.go": true}
	in.CurrentIdx = idx
	in.PriorMeta = v2Meta("oldsha",
		gate.CompactFinding{Fp: keepFp, Path: "keep.go", Line: 10, Side: "R", Category: "bug", Title: "problem", Cid: 5, Tier: "inline"},
		gate.CompactFinding{Fp: goneFp, Path: "gone.go", Line: 3, Side: "R", Category: "bug", Title: "was here"},
		gate.CompactFinding{Fp: changedFp, Path: "changed.go", Line: 1, Side: "R", Category: "bug", Title: "in flux"},
	)

	p := Decide(in)
	if p.Full {
		t.Fatalf("expected delta, got full: %s", p.FullReason)
	}
	// changed.go is the only compare file that's also a PR path.
	if len(p.ReviewPaths) != 1 || !p.ReviewPaths["changed.go"] {
		t.Fatalf("review paths wrong: %v", p.ReviewPaths)
	}
	if len(p.Carried) != 1 || p.Carried[0].Path != "keep.go" || p.Carried[0].Cid != 5 {
		t.Fatalf("carried wrong: %+v", p.Carried)
	}
	if len(p.AnchorGone) != 1 || p.AnchorGone[0].Path != "gone.go" {
		t.Fatalf("anchor-gone wrong: %+v", p.AnchorGone)
	}
	// The changed-file finding is neither carried nor resolved here (gate.Route
	// owns it), so PriorForReviewedPaths surfaces exactly it.
	pr := p.PriorForReviewedPaths(in.PriorMeta.Findings)
	if len(pr) != 1 || pr[0].Path != "changed.go" {
		t.Fatalf("PriorForReviewedPaths wrong: %+v", pr)
	}
}

// TestPriorForReviewedPathsFull returns everything on a full plan.
func TestPriorForReviewedPathsFull(t *testing.T) {
	prior := []gate.CompactFinding{{Path: "a"}, {Path: "b"}}
	got := Plan{Full: true}.PriorForReviewedPaths(prior)
	if len(got) != 2 {
		t.Fatalf("full plan must pass all prior through, got %d", len(got))
	}
}

// TestAnchorPresentNilIndex keeps a finding when there is no diff to check.
func TestAnchorPresentNilIndex(t *testing.T) {
	if !anchorPresent(gate.CompactFinding{Fp: "x"}, nil) {
		t.Fatal("nil index must conservatively keep the finding")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
