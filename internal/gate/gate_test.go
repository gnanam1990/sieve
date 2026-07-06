package gate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/fingerprint"
)

func testCfg() config.Review { return config.Default().Review } // floor .6, inline .8, sev major, cap 10

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func emptyIndex() *fingerprint.ContentIndex { return fingerprint.NewContentIndex(nil) }

func mk(path string, line int, sev findings.Severity, conf float64) findings.Finding {
	return findings.Finding{
		Path: path, Line: line, Side: findings.SideRight,
		Severity: sev, Confidence: conf, Category: "bug",
		Title: "issue in " + path, Body: "why",
	}
}

// TestRoutingMatrix exhaustively checks severity x confidence -> tier/drop
// with the default gate (floor 0.6, inline conf 0.8, inline severity major).
func TestRoutingMatrix(t *testing.T) {
	const (
		drop   = "drop"
		notes  = "notes"
		inline = "inline"
	)
	type want string
	sevs := []findings.Severity{findings.SeverityCritical, findings.SeverityMajor, findings.SeverityMinor, findings.SeverityNit}
	// confidence bands: below floor, floor..inline, >=inline
	confBands := []float64{0.5, 0.7, 0.9}
	expect := map[string]map[float64]want{
		"critical": {0.5: drop, 0.7: notes, 0.9: inline},
		"major":    {0.5: drop, 0.7: notes, 0.9: inline},
		"minor":    {0.5: drop, 0.7: notes, 0.9: notes},
		"nit":      {0.5: drop, 0.7: notes, 0.9: notes},
	}

	var fs []findings.Finding
	idOf := map[string]string{} // path -> "sev@conf"
	i := 0
	for _, s := range sevs {
		for _, c := range confBands {
			i++
			p := fmt.Sprintf("f%d.go", i)
			fs = append(fs, mk(p, 10, s, c))
			idOf[p] = fmt.Sprintf("%s@%.1f", s, c)
		}
	}

	res := Route(fs, emptyIndex(), nil, testCfg())

	got := map[string]string{} // path -> tier landed
	for _, f := range res.Inline {
		got[f.Path] = inline
	}
	for _, f := range res.Notes {
		got[f.Path] = notes
	}
	dropped := 0
	for _, f := range fs {
		w := string(expect[string(f.Severity)][f.Confidence])
		g := got[f.Path]
		if g == "" {
			g = drop
			dropped++
		}
		if g != w {
			t.Errorf("%s (%s): got %s, want %s", f.Path, idOf[f.Path], g, w)
		}
	}
	if res.Stats.FindingsBelowFloor != 4 { // one per severity at conf 0.5
		t.Errorf("FindingsBelowFloor = %d, want 4", res.Stats.FindingsBelowFloor)
	}
	if dropped != 4 {
		t.Errorf("dropped = %d, want 4", dropped)
	}
	if res.Stats.InputFindings != 12 {
		t.Errorf("InputFindings = %d, want 12", res.Stats.InputFindings)
	}
}

// TestInlineSeverityCriticalOnly checks the inline_min_severity=critical gate.
func TestInlineSeverityCriticalOnly(t *testing.T) {
	cfg := testCfg()
	cfg.InlineMinSeverity = "critical"
	fs := []findings.Finding{
		mk("a.go", 1, findings.SeverityCritical, 0.95),
		mk("b.go", 1, findings.SeverityMajor, 0.95), // major no longer inline
	}
	res := Route(fs, emptyIndex(), nil, cfg)
	if len(res.Inline) != 1 || res.Inline[0].Path != "a.go" {
		t.Fatalf("only critical should be inline, got %+v", res.Inline)
	}
	if len(res.Notes) != 1 || res.Notes[0].Path != "b.go" {
		t.Fatalf("major should drop to notes, got %+v", res.Notes)
	}
}

// TestCapOverflowOrdering: more inline-eligible findings than the cap ->
// the most severe/confident survive inline in order, the rest are demoted to
// notes (never dropped), and the demotion counter is exact.
func TestCapOverflowOrdering(t *testing.T) {
	cfg := testCfg()
	cfg.MaxInlineComments = 3
	fs := []findings.Finding{
		mk("z.go", 5, findings.SeverityMajor, 0.80),
		mk("a.go", 1, findings.SeverityCritical, 0.90),
		mk("a.go", 9, findings.SeverityCritical, 0.99),
		mk("m.go", 2, findings.SeverityMajor, 0.95),
		mk("q.go", 3, findings.SeverityCritical, 0.90), // ties a.go:1 on sev+conf, path breaks it
	}
	res := Route(fs, emptyIndex(), nil, cfg)

	// Expected inline order: critical desc-conf then path/line, then major.
	// critical: a.go:9(.99), a.go:1(.90)/q.go:3(.90) -> path order a.go then q.go.
	wantOrder := []string{"a.go:9", "a.go:1", "q.go:3"}
	if len(res.Inline) != 3 {
		t.Fatalf("want 3 inline, got %d", len(res.Inline))
	}
	for i, w := range wantOrder {
		got := fmt.Sprintf("%s:%d", res.Inline[i].Path, res.Inline[i].Line)
		if got != w {
			t.Errorf("inline[%d] = %s, want %s", i, got, w)
		}
	}
	if res.Stats.InlineDemotedByCap != 2 {
		t.Errorf("InlineDemotedByCap = %d, want 2", res.Stats.InlineDemotedByCap)
	}
	// The two demoted findings (m.go, z.go) must appear in notes, none lost.
	if len(res.Notes) != 2 {
		t.Fatalf("want 2 demoted notes, got %d", len(res.Notes))
	}
	total := len(res.Inline) + len(res.Notes)
	if total != 5 {
		t.Errorf("cap must never drop: kept %d of 5", total)
	}
	for _, n := range res.Notes {
		if n.Tier != TierNotes {
			t.Errorf("demoted finding %s must carry TierNotes", n.Path)
		}
	}
}

// TestWithinRunDedupe: overlapping same-category findings collapse to the
// highest-confidence one; different category or non-overlapping ranges stay.
func TestWithinRunDedupe(t *testing.T) {
	fs := []findings.Finding{
		mk("a.go", 10, findings.SeverityMajor, 0.70),
		mk("a.go", 10, findings.SeverityMajor, 0.95), // dup of above, higher conf -> winner
		func() findings.Finding { f := mk("a.go", 10, findings.SeverityMajor, 0.99); f.Category = "perf"; return f }(), // different category, kept
		mk("a.go", 40, findings.SeverityMajor, 0.90), // non-overlapping line, kept
	}
	res := Route(fs, emptyIndex(), nil, testCfg())
	if res.Stats.DuplicatesMerged != 1 {
		t.Fatalf("DuplicatesMerged = %d, want 1", res.Stats.DuplicatesMerged)
	}
	// Winner at a.go:10/bug must be the 0.95 one.
	var found bool
	for _, f := range append(append([]Finding{}, res.Inline...), res.Notes...) {
		if f.Path == "a.go" && f.Line == 10 && f.Category == "bug" {
			found = true
			if f.Confidence != 0.95 {
				t.Errorf("dedupe kept conf %.2f, want 0.95", f.Confidence)
			}
		}
	}
	if !found {
		t.Fatal("deduped bug finding missing")
	}
	kept := len(res.Inline) + len(res.Notes)
	if kept != 3 { // 0.95 bug, perf, a.go:40 bug
		t.Errorf("kept %d after dedupe, want 3", kept)
	}
}

// TestRangeOverlapDedupe: a single-line finding inside another's range dedupes.
func TestRangeOverlapDedupe(t *testing.T) {
	f1 := mk("a.go", 10, findings.SeverityMajor, 0.90)
	f1.EndLine = 20
	f2 := mk("a.go", 15, findings.SeverityMajor, 0.95) // inside [10,20]
	res := Route([]findings.Finding{f1, f2}, emptyIndex(), nil, testCfg())
	if res.Stats.DuplicatesMerged != 1 {
		t.Fatalf("overlapping range should merge, got %d", res.Stats.DuplicatesMerged)
	}
}

// TestCrossRunRepeatedAndResolved covers the metadata-driven dedupe: a
// fingerprint seen last run is Repeated; a prior fingerprint absent this run
// is Resolved.
func TestCrossRunRepeatedAndResolved(t *testing.T) {
	cfg := testCfg()
	fInline := mk("a.go", 1, findings.SeverityCritical, 0.95) // inline tier
	fNote := mk("b.go", 2, findings.SeverityMinor, 0.70)      // notes tier
	fs := []findings.Finding{fInline, fNote}

	// Build the prior metadata from a first run, then add a fingerprint that
	// won't reappear (a resolved finding).
	first := Route(fs, emptyIndex(), nil, cfg)
	prior := BuildMeta("headA", "t0", first).Fps
	prior = append(prior, PriorFinding{Fingerprint: "deadbeefdeadbeef", Path: "gone.go", Severity: "major"})

	second := Route(fs, emptyIndex(), prior, cfg)
	if second.Stats.RepeatedInline != 1 || second.Stats.RepeatedNotes != 1 {
		t.Fatalf("want 1 repeated inline + 1 repeated note, got %+v", second.Stats)
	}
	if !second.Inline[0].Repeated || !second.Notes[0].Repeated {
		t.Fatal("reappearing findings must be marked Repeated")
	}
	if len(second.Resolved) != 1 || second.Resolved[0].Path != "gone.go" {
		t.Fatalf("want gone.go resolved, got %+v", second.Resolved)
	}
	if second.Stats.ResolvedCount != 1 {
		t.Errorf("ResolvedCount = %d, want 1", second.Stats.ResolvedCount)
	}
}

// TestFirstRunNoPrior: nil prior => nothing repeated, nothing resolved.
func TestFirstRunNoPrior(t *testing.T) {
	res := Route([]findings.Finding{mk("a.go", 1, findings.SeverityCritical, 0.95)}, emptyIndex(), nil, testCfg())
	if res.Stats.RepeatedInline != 0 || len(res.Resolved) != 0 {
		t.Fatalf("first run should have no repeats/resolved: %+v", res.Stats)
	}
}

func TestEmptyInput(t *testing.T) {
	res := Route(nil, emptyIndex(), nil, testCfg())
	if len(res.Inline) != 0 || len(res.Notes) != 0 || res.Stats.InputFindings != 0 {
		t.Fatalf("empty input must produce empty result: %+v", res)
	}
}

func TestTierJSON(t *testing.T) {
	b, err := json.Marshal(TierInline)
	if err != nil || string(b) != `"inline"` {
		t.Fatalf("TierInline JSON = %s err %v", b, err)
	}
	if TierNotes.String() != "notes" {
		t.Fatalf("TierNotes.String() = %q", TierNotes.String())
	}
}

func TestFingerprintAttached(t *testing.T) {
	res := Route([]findings.Finding{mk("a.go", 1, findings.SeverityCritical, 0.95)}, emptyIndex(), nil, testCfg())
	if len(res.Inline[0].Fingerprint) != fingerprint.Len {
		t.Fatalf("fingerprint not attached: %q", res.Inline[0].Fingerprint)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	res := Route([]findings.Finding{
		mk("a.go", 1, findings.SeverityCritical, 0.95),
		mk("b.go", 2, findings.SeverityMinor, 0.70),
	}, emptyIndex(), nil, testCfg())
	m := BuildMeta("head123", "2026-07-06T00:00:00Z", res)
	if m.Version != MetaVersion || m.HeadSHA != "head123" || len(m.Fps) != 2 {
		t.Fatalf("bad meta: %+v", m)
	}
	enc := EncodeMeta(m)
	got, err := DecodeMeta(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "head123" || len(got.Fps) != 2 || got.Fps[0].Path != "a.go" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestMetaDecodeToleratesUnknownFields is the forward-compat guarantee.
func TestMetaDecodeToleratesUnknownFields(t *testing.T) {
	raw := `{"v":1,"head_sha":"h","fps":[{"f":"abc","p":"x.go","s":"major","extra":"ignored"}],"ts":"t","future_key":42}`
	enc := EncodeMeta(Meta{}) // placeholder to reuse base64 helper below
	_ = enc
	got, err := DecodeMeta(base64Std(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "h" || len(got.Fps) != 1 || got.Fps[0].Fingerprint != "abc" {
		t.Fatalf("unknown fields broke decode: %+v", got)
	}
}

func TestMetaDecodeBadInput(t *testing.T) {
	if _, err := DecodeMeta("!!!not base64!!!"); err == nil {
		t.Error("bad base64 should error")
	}
	if _, err := DecodeMeta(base64Std("{not json")); err == nil {
		t.Error("bad json should error")
	}
}

// TestMetaFpsCap: more than 200 findings => fps truncated to 200.
func TestMetaFpsCap(t *testing.T) {
	var fs []findings.Finding
	for i := 0; i < 250; i++ {
		fs = append(fs, mk(fmt.Sprintf("f%d.go", i), 1, findings.SeverityMinor, 0.70))
	}
	res := Route(fs, emptyIndex(), nil, testCfg())
	m := BuildMeta("h", "t", res)
	if len(m.Fps) != maxMetaFps {
		t.Fatalf("fps not capped: %d, want %d", len(m.Fps), maxMetaFps)
	}
}
