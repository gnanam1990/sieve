package fingerprint

import (
	"testing"

	"github.com/gnanam1990/sieve/internal/diff"
)

func TestForDeterministic(t *testing.T) {
	a := For("p.go", "RIGHT", "bug", "Title here", "x := 1")
	b := For("p.go", "RIGHT", "bug", "Title here", "x := 1")
	if a != b {
		t.Fatalf("not deterministic: %s != %s", a, b)
	}
	if len(a) != Len {
		t.Fatalf("fp length %d, want %d", len(a), Len)
	}
}

// TestStableUnderLineDrift: same anchor content at a different line number
// yields the same fingerprint (line number is not hashed).
func TestStableUnderLineDrift(t *testing.T) {
	line := func(n int, content string) diff.Line {
		return diff.Line{Kind: diff.AddedLine, NewNum: n, Content: content}
	}
	early := []diff.FileDiff{{NewPath: "p.go", Hunks: []diff.Hunk{{Lines: []diff.Line{line(10, "return doThing()")}}}}}
	late := []diff.FileDiff{{NewPath: "p.go", Hunks: []diff.Hunk{{Lines: []diff.Line{line(42, "return doThing()")}}}}}

	fpEarly := For("p.go", "RIGHT", "bug", "T", NewContentIndex(early).Anchor("p.go", "RIGHT", 10))
	fpLate := For("p.go", "RIGHT", "bug", "T", NewContentIndex(late).Anchor("p.go", "RIGHT", 42))
	if fpEarly != fpLate {
		t.Fatalf("line drift changed fp: %s != %s", fpEarly, fpLate)
	}
}

func TestChangedUnderContentEdit(t *testing.T) {
	before := For("p.go", "RIGHT", "bug", "T", "x := computeOld()")
	after := For("p.go", "RIGHT", "bug", "T", "x := computeNew()")
	if before == after {
		t.Fatal("content edit must change the fingerprint")
	}
}

func TestChangedUnderTitleRewrite(t *testing.T) {
	before := For("p.go", "RIGHT", "bug", "Guard against nil", "x := 1")
	after := For("p.go", "RIGHT", "bug", "Check for a nil pointer", "x := 1")
	if before == after {
		t.Fatal("title rewrite must change the fingerprint")
	}
}

func TestChangedUnderPathRename(t *testing.T) {
	before := For("old/p.go", "RIGHT", "bug", "T", "x := 1")
	after := For("new/p.go", "RIGHT", "bug", "T", "x := 1")
	if before == after {
		t.Fatal("path rename must change the fingerprint (documented)")
	}
}

func TestChangedUnderSideOrCategory(t *testing.T) {
	base := For("p.go", "RIGHT", "bug", "T", "x")
	if base == For("p.go", "LEFT", "bug", "T", "x") {
		t.Fatal("side must affect the fingerprint")
	}
	if base == For("p.go", "RIGHT", "security", "T", "x") {
		t.Fatal("category must affect the fingerprint")
	}
}

// TestTitleNormalization: case, punctuation, and whitespace runs collapse to
// the same normalized title, so cosmetically different titles fingerprint
// identically.
func TestTitleNormalization(t *testing.T) {
	cases := [][2]string{
		{"Unchecked error from Close()", "unchecked   error from close"},
		{"SQL   by  string-concat!!!", "sql by string concat"},
		{"  leading and trailing  ", "leading and trailing"},
	}
	for _, c := range cases {
		if a, b := normTitle(c[0]), normTitle(c[1]); a != b {
			t.Errorf("normTitle mismatch: %q -> %q vs %q -> %q", c[0], a, c[1], b)
		}
	}
	if got := normTitle("Résumé build 42"); got != "résumé build 42" {
		t.Errorf("unicode letters/numbers must survive: got %q", got)
	}
	if got := normTitle("!!! @@@ ###"); got != "" {
		t.Errorf("all-punctuation must normalize to empty, got %q", got)
	}
}

func TestContentIndexSides(t *testing.T) {
	fd := diff.FileDiff{
		NewPath: "p.go",
		Hunks: []diff.Hunk{{Lines: []diff.Line{
			{Kind: diff.Context, OldNum: 1, NewNum: 1, Content: "ctx"},
			{Kind: diff.RemovedLine, OldNum: 2, Content: "old line"},
			{Kind: diff.AddedLine, NewNum: 2, Content: "new line"},
		}}},
	}
	ci := NewContentIndex([]diff.FileDiff{fd})
	if got := ci.Anchor("p.go", "RIGHT", 2); got != "new line" {
		t.Errorf("RIGHT anchor: got %q", got)
	}
	if got := ci.Anchor("p.go", "LEFT", 2); got != "old line" {
		t.Errorf("LEFT anchor: got %q", got)
	}
	if got := ci.Anchor("p.go", "RIGHT", 1); got != "ctx" {
		t.Errorf("context RIGHT anchor: got %q", got)
	}
	if got := ci.Anchor("p.go", "LEFT", 1); got != "ctx" {
		t.Errorf("context LEFT anchor: got %q", got)
	}
	if got := ci.Anchor("p.go", "RIGHT", 999); got != "" {
		t.Errorf("missing anchor must be empty, got %q", got)
	}
	if got := ci.Anchor("other.go", "RIGHT", 1); got != "" {
		t.Errorf("unknown path must be empty, got %q", got)
	}
}

func TestContentsFor(t *testing.T) {
	ci := NewContentIndex([]diff.FileDiff{{
		NewPath: "p.go",
		Hunks: []diff.Hunk{{Lines: []diff.Line{
			{Kind: diff.AddedLine, NewNum: 1, Content: "added a"},
			{Kind: diff.AddedLine, NewNum: 2, Content: "added b"},
			{Kind: diff.RemovedLine, OldNum: 5, Content: "removed x"},
		}}},
	}})
	right := ci.ContentsFor("p.go", "RIGHT")
	if len(right) != 2 {
		t.Fatalf("RIGHT contents = %v", right)
	}
	left := ci.ContentsFor("p.go", "LEFT")
	if len(left) != 1 || left[0] != "removed x" {
		t.Fatalf("LEFT contents = %v", left)
	}
	if len(ci.ContentsFor("other.go", "RIGHT")) != 0 {
		t.Fatal("unknown path must be empty")
	}
}

// TestContentIndexDeletePathFallback: a pure delete keys on OldPath.
func TestContentIndexDeletePathFallback(t *testing.T) {
	fd := diff.FileDiff{
		OldPath: "gone.go",
		Status:  diff.Deleted,
		Hunks: []diff.Hunk{{Lines: []diff.Line{
			{Kind: diff.RemovedLine, OldNum: 5, Content: "deleted content"},
		}}},
	}
	ci := NewContentIndex([]diff.FileDiff{fd})
	if got := ci.Anchor("gone.go", "LEFT", 5); got != "deleted content" {
		t.Errorf("delete fallback anchor: got %q", got)
	}
}
