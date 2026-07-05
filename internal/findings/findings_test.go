package findings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/diff"
)

func valid() Finding {
	return Finding{
		Path: "main.go", Line: 2, Side: SideRight,
		Severity: SeverityMajor, Confidence: 0.8, Category: "bug",
		Title: "Fix nil deref", Body: "why + fix",
	}
}

// anchorsFor builds Anchors over a small synthetic diff:
//
//	@@ -1,3 +1,4 @@   ctx(1,1) del(2) add(2) add(3) ctx(3,4)
//	@@ -10,2 +11,2 @@  ctx(10,11) del(11) add(12)
func testAnchors() *Anchors {
	fd := diff.FileDiff{
		NewPath: "main.go", OldPath: "main.go", Status: diff.Modified,
		Hunks: []diff.Hunk{
			{OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 4, Lines: []diff.Line{
				{Kind: diff.Context, OldNum: 1, NewNum: 1},
				{Kind: diff.RemovedLine, OldNum: 2},
				{Kind: diff.AddedLine, NewNum: 2},
				{Kind: diff.AddedLine, NewNum: 3},
				{Kind: diff.Context, OldNum: 3, NewNum: 4},
			}},
			{OldStart: 10, OldLines: 2, NewStart: 11, NewLines: 2, Lines: []diff.Line{
				{Kind: diff.Context, OldNum: 10, NewNum: 11},
				{Kind: diff.RemovedLine, OldNum: 11},
				{Kind: diff.AddedLine, NewNum: 12},
			}},
		},
	}
	return NewAnchors([]diff.FileDiff{fd})
}

func TestValidateAccepts(t *testing.T) {
	a := testAnchors()
	cases := []func(*Finding){
		func(_ *Finding) {},                                             // added line RIGHT
		func(f *Finding) { f.Line = 4 },                                 // context line RIGHT
		func(f *Finding) { f.Line = 2; f.Side = SideLeft },              // removed line LEFT
		func(f *Finding) { f.Line = 1; f.Side = SideLeft },              // context line LEFT
		func(f *Finding) { f.Line = 1; f.EndLine = 4 },                  // full-hunk range RIGHT
		func(f *Finding) { f.Line = 11; f.EndLine = 12 },                // second hunk range RIGHT
		func(f *Finding) { f.Severity = SeverityNit; f.Confidence = 0 }, // enum bounds
		func(f *Finding) { f.Severity = SeverityCritical; f.Confidence = 1 },
	}
	for i, mutate := range cases {
		f := valid()
		mutate(&f)
		if err := a.Validate(f); err != nil {
			t.Errorf("case %d (%+v): unexpected reject: %v", i, f, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	a := testAnchors()
	cases := map[string]func(*Finding){
		"unknown path":             func(f *Finding) { f.Path = "other.go" },
		"line not in diff":         func(f *Finding) { f.Line = 99 },
		"old-only number on RIGHT": func(f *Finding) { f.Line = 10; f.Side = SideRight },
		"added-only line on LEFT":  func(f *Finding) { f.Line = 12; f.Side = SideLeft },
		"gap between hunks":        func(f *Finding) { f.Line = 7 },
		"range spanning hunks":     func(f *Finding) { f.Line = 4; f.EndLine = 11 },
		"range past hunk end":      func(f *Finding) { f.Line = 3; f.EndLine = 5 },
		"end before start":         func(f *Finding) { f.Line = 4; f.EndLine = 2 },
		"zero line":                func(f *Finding) { f.Line = 0 },
		"bad severity":             func(f *Finding) { f.Severity = "blocker" },
		"bad category":             func(f *Finding) { f.Category = "vibes" },
		"bad side":                 func(f *Finding) { f.Side = "MIDDLE" },
		"confidence below range":   func(f *Finding) { f.Confidence = -0.1 },
		"confidence above range":   func(f *Finding) { f.Confidence = 1.1 },
		"empty title":              func(f *Finding) { f.Title = "  " },
		"title too long":           func(f *Finding) { f.Title = strings.Repeat("x", MaxTitleLen+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := valid()
			mutate(&f)
			if err := a.Validate(f); err == nil {
				t.Fatalf("want reject for %+v", f)
			}
		})
	}
}

// TestAnchorsAgainstFixtures is the property test: for every stage-1
// fixture, every commentable (side, line) accepts a synthetic finding and
// neighboring non-commentable lines reject one.
func TestAnchorsAgainstFixtures(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/diffs/*.diff")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			files, err := diff.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			a := NewAnchors(files)
			for _, fd := range files {
				path := fd.NewPath
				if path == "" {
					path = fd.OldPath
				}
				rights, lefts := map[int]bool{}, map[int]bool{}
				for _, h := range fd.Hunks {
					for _, l := range h.Lines {
						switch l.Kind {
						case diff.AddedLine:
							rights[l.NewNum] = true
						case diff.RemovedLine:
							lefts[l.OldNum] = true
						case diff.Context:
							rights[l.NewNum] = true
							lefts[l.OldNum] = true
						}
					}
				}
				probe := func(line int, side Side) error {
					f := valid()
					f.Path, f.Line, f.EndLine, f.Side = path, line, 0, side
					return a.Validate(f)
				}
				for n := range rights {
					if err := probe(n, SideRight); err != nil {
						t.Errorf("%s R:%d should be commentable: %v", path, n, err)
					}
					if !rights[n+1] {
						if probe(n+1, SideRight) == nil {
							t.Errorf("%s R:%d should NOT be commentable", path, n+1)
						}
					}
				}
				for n := range lefts {
					if err := probe(n, SideLeft); err != nil {
						t.Errorf("%s L:%d should be commentable: %v", path, n, err)
					}
					if !lefts[n+1] {
						if probe(n+1, SideLeft) == nil {
							t.Errorf("%s L:%d should NOT be commentable", path, n+1)
						}
					}
				}
			}
		})
	}
}

func TestSortDeterministic(t *testing.T) {
	in := []Finding{
		{Path: "b.go", Line: 5, Severity: SeverityNit},
		{Path: "a.go", Line: 9, Severity: SeverityMinor},
		{Path: "b.go", Line: 1, Severity: SeverityCritical},
		{Path: "a.go", Line: 3, Severity: SeverityMinor},
		{Path: "a.go", Line: 3, Severity: SeverityCritical},
		{Path: "z.go", Line: 2, Severity: SeverityMajor},
	}
	want := []struct {
		path string
		line int
	}{
		{"a.go", 3}, {"b.go", 1}, // criticals by path
		{"z.go", 2},              // major
		{"a.go", 3}, {"a.go", 9}, // minors by path,line
		{"b.go", 5}, // nit
	}
	for i := 0; i < 3; i++ { // stable across repeated sorts
		Sort(in)
		for j, w := range want {
			if in[j].Path != w.path || in[j].Line != w.line {
				t.Fatalf("pos %d: got %s:%d, want %s:%d", j, in[j].Path, in[j].Line, w.path, w.line)
			}
		}
	}
}

func TestParseResponse(t *testing.T) {
	body := `{"findings":[{"Path":"a.go","Line":3,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"T","Body":"B"}]}`
	cases := map[string]string{
		"bare json":        body,
		"code fenced":      "```json\n" + body + "\n```",
		"prose wrapped":    "Here is my review:\n" + body + "\nHope that helps!",
		"lowercase fields": `{"findings":[{"path":"a.go","line":3,"side":"RIGHT","severity":"major","confidence":0.9,"category":"bug","title":"T","body":"B"}]}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			fs, err := ParseResponse(in)
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) != 1 || fs[0].Path != "a.go" || fs[0].Line != 3 || fs[0].Severity != SeverityMajor {
				t.Fatalf("bad parse: %+v", fs)
			}
		})
	}
}

func TestParseResponseErrors(t *testing.T) {
	cases := map[string]string{
		"no json":        "I found no issues!",
		"unknown field":  `{"findings":[{"Path":"a.go","Line":1,"Side":"RIGHT","Severity":"nit","Category":"style","Title":"T","Body":"B","Vibe":"good"}]}`,
		"wrong shape":    `{"issues":[]}`,
		"truncated json": `{"findings":[{"Path":"a.go"`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponse(in); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestParseResponseEmptyFindings(t *testing.T) {
	fs, err := ParseResponse(`{"findings":[]}`)
	if err != nil || len(fs) != 0 {
		t.Fatalf("fs=%v err=%v", fs, err)
	}
}

func TestRankAndSeverityOrder(t *testing.T) {
	if Rank(SeverityCritical) >= Rank(SeverityMajor) ||
		Rank(SeverityMajor) >= Rank(SeverityMinor) ||
		Rank(SeverityMinor) >= Rank(SeverityNit) {
		t.Fatal("ranks must strictly increase from critical to nit")
	}
	if Rank(Severity("bogus")) <= Rank(SeverityNit) {
		t.Fatal("unknown severity must rank after all known ones")
	}
}

func TestAtLeastAsSevere(t *testing.T) {
	if !AtLeastAsSevere(SeverityCritical, SeverityMajor) {
		t.Error("critical >= major")
	}
	if !AtLeastAsSevere(SeverityMajor, SeverityMajor) {
		t.Error("major >= major (inclusive)")
	}
	if AtLeastAsSevere(SeverityMinor, SeverityMajor) {
		t.Error("minor is not >= major")
	}
}

func TestRightRangeCommentable(t *testing.T) {
	a := testAnchors()
	// RIGHT lines present: 1,2,3,4 (hunk 1) and 11,12 (hunk 2).
	if !a.RightRangeCommentable("main.go", 2, 0) {
		t.Error("single RIGHT line 2 should be commentable")
	}
	if !a.RightRangeCommentable("main.go", 2, 4) {
		t.Error("range 2-4 within one hunk should be commentable")
	}
	if a.RightRangeCommentable("main.go", 4, 11) {
		t.Error("range spanning two hunks must not be commentable")
	}
	if a.RightRangeCommentable("main.go", 99, 0) {
		t.Error("absent line must not be commentable")
	}
	if a.RightRangeCommentable("other.go", 1, 0) {
		t.Error("unknown path must not be commentable")
	}
}
