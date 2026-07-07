package render

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
)

func gf(path string, line int, sev findings.Severity, conf float64, cat, title, body string) gate.Finding {
	return gate.Finding{
		Finding: findings.Finding{
			Path: path, Line: line, Side: findings.SideRight,
			Severity: sev, Confidence: conf, Category: cat, Title: title, Body: body,
		},
		Fingerprint: "fp" + path,
	}
}

func sampleResult() gate.GateResult {
	inline := []gate.Finding{
		gf("internal/db/query.go", 88, findings.SeverityCritical, 0.95, "security", "SQL built by string concatenation", "User input is concatenated into the query. Use parameterized queries."),
		gf("internal/gh/client.go", 141, findings.SeverityMajor, 0.86, "bug", "Unchecked error from Close", "The deferred Close error is ignored; wrap it."),
	}
	for i := range inline {
		inline[i].Tier = gate.TierInline
	}
	notes := []gate.Finding{
		gf("internal/util/x.go", 5, findings.SeverityMinor, 0.72, "style", "Prefer errors.Is over ==", "Comparing errors with == is fragile."),
		gf("internal/util/x.go", 30, findings.SeverityNit, 0.65, "style", "Stutter in name UtilUtil", "Rename to avoid stutter."),
		gf("internal/api/h.go", 12, findings.SeverityMinor, 0.68, "perf", "Allocation in hot loop", "Hoist the buffer out of the loop."),
	}
	for i := range notes {
		notes[i].Tier = gate.TierNotes
	}
	return gate.GateResult{
		Inline:   inline,
		Notes:    notes,
		Resolved: []gate.CompactFinding{{Fp: "deadbeefdeadbeef", Path: "internal/old/gone.go", Severity: "major"}},
		Stats:    gate.Stats{InlineCount: 2, NotesCount: 3, ResolvedCount: 1},
	}
}

func sampleInput() WalkthroughInput {
	res := sampleResult()
	return WalkthroughInput{
		Result:        res,
		Meta:          gate.BuildMeta("headsha0011223344", "2026-07-06T12:00:00Z", res.ActiveCompact(nil), nil, []string{"deadbeefdeadbeef"}),
		Skipped:       []SkippedFile{{Path: "go.sum", Reason: "default exclude"}, {Path: "docs/x.md", Reason: "config exclude: docs/**"}},
		FilesReviewed: 6,
		FilesSkipped:  2,
		Model:         "claude-sonnet-5",
		InputTokens:   18234,
		OutputTokens:  1420,
		Version:       "v0.3.0",
	}
}

func goldenCompare(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden (run `make golden`): %v", err)
	}
	if d := cmp.Diff(string(want), got); d != "" {
		t.Errorf("golden mismatch %s (-want +got):\n%s", path, d)
	}
}

func TestWalkthroughGolden(t *testing.T) {
	body := Walkthrough(sampleInput())
	goldenCompare(t, "testdata/walkthrough_full.golden.md", body)

	// Structural invariants beyond the golden.
	if !strings.HasPrefix(body, WalkthroughMarker) {
		t.Error("walkthrough must start with the locator marker")
	}
	if !strings.Contains(body, metaLocate) {
		t.Error("walkthrough must carry the metadata block")
	}
	for _, want := range []string{"🔴 critical", "🟠 major", "📝 Notes (3)", "✅ Resolved since last review (1)", "⏭️ Skipped files (2)", "18.2k", "1.4k", "sieve v0.3.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("walkthrough missing %q", want)
		}
	}
}

// TestMetaRoundTripThroughWalkthrough: the meta encoded into the walkthrough
// can be extracted and decoded — the cross-run persistence path.
func TestMetaRoundTripThroughWalkthrough(t *testing.T) {
	body := Walkthrough(sampleInput())
	m, ok, err := ExtractMeta(body)
	if err != nil || !ok {
		t.Fatalf("extract meta: ok=%v err=%v", ok, err)
	}
	if m.HeadSHA != "headsha0011223344" || len(m.Findings) != 5 {
		t.Fatalf("bad extracted meta: %+v", m)
	}
}

func TestExtractMetaAbsent(t *testing.T) {
	_, ok, err := ExtractMeta("just a normal comment, no sieve here")
	if ok || err != nil {
		t.Fatalf("want absent, got ok=%v err=%v", ok, err)
	}
}

func TestExtractMetaCorrupt(t *testing.T) {
	if _, _, err := ExtractMeta(metaLocate + "!!!notbase64!!!" + metaSuffix); err == nil {
		t.Error("corrupt meta must error")
	}
	if _, _, err := ExtractMeta(metaLocate + "unterminated"); err == nil {
		t.Error("unterminated meta must error")
	}
}

// TestWalkthroughEmpty: no findings at all still renders a valid comment.
func TestWalkthroughEmpty(t *testing.T) {
	in := WalkthroughInput{
		Result:  gate.GateResult{Stats: gate.Stats{}},
		Meta:    gate.BuildMeta("h", "t", nil, nil, nil),
		Version: "v0.3.0",
	}
	body := Walkthrough(in)
	if !strings.Contains(body, "**0 findings** · 0 notes · 0 resolved") {
		t.Errorf("empty walkthrough header wrong:\n%s", body)
	}
	if strings.Contains(body, "Notes (") || strings.Contains(body, "Resolved") || strings.Contains(body, "Skipped") {
		t.Error("empty result must omit optional sections")
	}
}

// TestWalkthroughTruncationOrder: an oversized notes body forces truncation of
// notes first (then resolved, then skipped); the metadata block always
// survives, and the body fits under the cap.
func TestWalkthroughTruncationOrder(t *testing.T) {
	res := sampleResult()
	// Blow up the notes bodies past the cap.
	huge := strings.Repeat("x", MaxCommentBytes)
	res.Notes[0].Body = huge
	in := sampleInput()
	in.Result = res

	body := Walkthrough(in)
	if len(body) > MaxCommentBytes {
		t.Fatalf("body %d exceeds cap %d", len(body), MaxCommentBytes)
	}
	if !strings.Contains(body, "…and 3 more notes — see JSON output") {
		t.Error("notes should be truncated to the placeholder")
	}
	// Metadata is never truncated.
	if _, ok, err := ExtractMeta(body); !ok || err != nil {
		t.Fatalf("metadata must survive truncation: ok=%v err=%v", ok, err)
	}
	// Resolved/skipped stay intact because truncating notes alone sufficed.
	if !strings.Contains(body, "✅ Resolved since last review (1)") {
		t.Error("resolved should remain when notes truncation is enough")
	}
}

// TestTruncationCascade: when even truncated notes + resolved don't fit,
// skipped is truncated too.
func TestTruncationCascade(t *testing.T) {
	res := sampleResult()
	// Many skipped files, each huge, so notes+resolved truncation is not enough.
	var many []SkippedFile
	for i := 0; i < 5; i++ {
		many = append(many, SkippedFile{Path: strings.Repeat("p", MaxCommentBytes/3), Reason: "big"})
	}
	in := sampleInput()
	in.Result = res
	in.Skipped = many
	body := Walkthrough(in)
	if len(body) > MaxCommentBytes {
		t.Fatalf("cascade failed: %d > %d", len(body), MaxCommentBytes)
	}
	if !strings.Contains(body, "…and 5 more — see JSON output") {
		t.Errorf("skipped should be truncated:\n%.300s", body)
	}
}

// TestFooterPerRoleTokens: a multi-model pipeline surfaces the pipeline name
// and each role's token split in the footer.
func TestFooterPerRoleTokens(t *testing.T) {
	in := sampleInput()
	in.Pipeline = "judge"
	in.RoleTokens = []RoleToken{{Role: "gen", In: 12000, Out: 900}, {Role: "judge", In: 3400, Out: 120}}
	body := Walkthrough(in)
	for _, must := range []string{"pipeline: judge", "gen 12.0k/900", "judge 3.4k/120"} {
		if !strings.Contains(body, must) {
			t.Errorf("footer missing %q\n%s", must, body)
		}
	}
}

// TestFooterSinglePipelineOmitsRoleBreakdown: the single reviewer footer stays
// clean — no pipeline label, no per-role line.
func TestFooterSinglePipelineOmitsRoleBreakdown(t *testing.T) {
	in := sampleInput()
	in.Pipeline = "single"
	body := Walkthrough(in)
	if strings.Contains(body, "pipeline:") {
		t.Errorf("single pipeline must not show a pipeline label:\n%s", body)
	}
}

// TestSuggestionsSection: the walkthrough renders a collapsible suggestion
// footer with count, reason, matcher summary, and the apply command. No
// suggestion section appears when the list is empty.
func TestSuggestionsSection(t *testing.T) {
	rule := ignore.Rule{
		Fingerprint: "abc123def4567890",
		Reason:      "1 negative reaction",
		Expires:     time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
	}
	sugs := []suggest.Suggestion{{Rule: rule}}

	in := sampleInput()
	in.Suggestions = sugs
	body := Walkthrough(in)
	for _, want := range []string{
		"💡 Suggested ignore rules (1)",
		"1 negative reaction",
		"`0` fingerprint: `abc123def4567890`",
		"Apply with: `sieve ignore --suggest --repo owner/name`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("suggestion footer missing %q:\n%s", want, body)
		}
	}

	// Empty suggestions omit the section entirely.
	in.Suggestions = nil
	body = Walkthrough(in)
	if strings.Contains(body, "Suggested ignore rules") {
		t.Error("empty suggestions must not render the section")
	}
}

// TestSuggestionsSectionPathCategoryTitle: rules without a fingerprint render
// path/category/title summary in the footer.
func TestSuggestionsSectionPathCategoryTitle(t *testing.T) {
	rule := ignore.Rule{
		Path:     "**/*.go",
		Category: "style",
		Title:    "naming",
		Reason:   "2 negative reactions in pkg/foo",
	}
	sugs := []suggest.Suggestion{{Rule: rule}}
	in := sampleInput()
	in.Suggestions = sugs
	body := Walkthrough(in)
	for _, want := range []string{
		"💡 Suggested ignore rules (1)",
		"`0` path: `**/*.go`, category: `style`, title: `naming`",
		"2 negative reactions in pkg/foo",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("suggestion footer missing %q:\n%s", want, body)
		}
	}
}

func TestHumanK(t *testing.T) {
	cases := map[int]string{0: "0", 840: "840", 999: "999", 1000: "1.0k", 18234: "18.2k", 1420: "1.4k"}
	for n, want := range cases {
		if got := humanK(n); got != want {
			t.Errorf("humanK(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestWhereRange(t *testing.T) {
	single := where(findings.Finding{Path: "a.go", Line: 5})
	rng := where(findings.Finding{Path: "a.go", Line: 5, EndLine: 9})
	if single != "a.go:5" || rng != "a.go:5-9" {
		t.Fatalf("where: single=%q range=%q", single, rng)
	}
}

func TestParseFpMarkerDefensive(t *testing.T) {
	good := "0123456789abcdef"
	body := "some comment\n" + FpMarkerPrefix + "v1 " + good + FpMarkerSuffix + "\n"
	if got := ParseFpMarker(body); got != good {
		t.Fatalf("valid marker: got %q", got)
	}
	// Untrusted input: reject forged/malformed values, never panic.
	for _, bad := range []string{
		"no marker here",
		FpMarkerPrefix + "v1 not-hex-16-chars!!" + FpMarkerSuffix,   // non-hex
		FpMarkerPrefix + "v1 0123" + FpMarkerSuffix,                 // too short
		FpMarkerPrefix + "v1 0123456789ABCDEF" + FpMarkerSuffix,     // uppercase
		FpMarkerPrefix + "v1 " + good, // unterminated
	} {
		if got := ParseFpMarker(bad); got != "" {
			t.Errorf("malformed marker %q must parse to empty, got %q", bad, got)
		}
	}
}

func TestEscapeCell(t *testing.T) {
	if got := escapeCell("a | b\nc"); got != "a \\| b c" {
		t.Fatalf("escapeCell = %q", got)
	}
}

// --- inline comment rendering ---

func rightAnchors() *findings.Anchors {
	fd := diff.FileDiff{
		NewPath: "a.go",
		Hunks: []diff.Hunk{{Lines: []diff.Line{
			{Kind: diff.AddedLine, NewNum: 10, Content: "x := 1"},
			{Kind: diff.AddedLine, NewNum: 11, Content: "y := 2"},
			{Kind: diff.AddedLine, NewNum: 12, Content: "z := 3"},
			{Kind: diff.RemovedLine, OldNum: 40, Content: "old"},
		}}},
	}
	return findings.NewAnchors([]diff.FileDiff{fd})
}

// TestInlineCommittableSuggestion: a RIGHT-side finding whose range is fully
// commentable emits a ```suggestion block.
func TestInlineCommittableSuggestion(t *testing.T) {
	f := gf("a.go", 10, findings.SeverityMajor, 0.9, "bug", "Fix the init", "Init is wrong.")
	f.EndLine = 12
	f.Suggestion = "x := 10\ny := 20\nz := 30"
	body := Inline(f, rightAnchors())
	goldenCompare(t, "testdata/inline_suggestion.golden.md", body)
	if !strings.Contains(body, "```suggestion\n") {
		t.Error("committable suggestion must use a suggestion block")
	}
	if strings.Contains(body, "proposed fix") {
		t.Error("committable path must not fall back to proposed fix")
	}
}

// TestInlineProposedFixFallback: a LEFT-side finding with a suggestion cannot
// be committable, so the suggestion ships as a "proposed fix" fenced block.
func TestInlineProposedFixFallback(t *testing.T) {
	f := gf("a.go", 40, findings.SeverityMajor, 0.9, "bug", "Remove dead code", "This is dead.")
	f.Side = findings.SideLeft
	f.Suggestion = "// removed"
	body := Inline(f, rightAnchors())
	goldenCompare(t, "testdata/inline_proposed.golden.md", body)
	if strings.Contains(body, "```suggestion") {
		t.Error("LEFT side must not emit a committable suggestion")
	}
	if !strings.Contains(body, "_proposed fix_") {
		t.Error("fallback must label the block as proposed fix")
	}
}

// TestInlineNonCommentableRangeFallback: a RIGHT finding whose range escapes
// the commentable lines also falls back.
func TestInlineNonCommentableRangeFallback(t *testing.T) {
	f := gf("a.go", 10, findings.SeverityMajor, 0.9, "bug", "Spans past the hunk", "…")
	f.EndLine = 99 // 13..99 are not commentable
	f.Suggestion = "whatever"
	body := Inline(f, rightAnchors())
	if strings.Contains(body, "```suggestion") {
		t.Error("non-commentable range must not emit a committable suggestion")
	}
	if !strings.Contains(body, "_proposed fix_") {
		t.Error("expected proposed-fix fallback")
	}
}

// TestInlineNoSuggestion: a finding without a suggestion renders just the body.
func TestInlineNoSuggestion(t *testing.T) {
	f := gf("a.go", 10, findings.SeverityMajor, 0.9, "bug", "No fix offered", "Explain only.")
	body := Inline(f, rightAnchors())
	if strings.Contains(body, "suggestion") || strings.Contains(body, "proposed fix") {
		t.Error("no suggestion should render neither block")
	}
	if !strings.Contains(body, "category `bug` · confidence 0.90") {
		t.Errorf("footer missing/incorrect:\n%s", body)
	}
}
