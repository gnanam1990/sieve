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
// fingerprint seen last run is Repeated (inheriting its cid); a prior
// fingerprint absent this run is Resolved.
func TestCrossRunRepeatedAndResolved(t *testing.T) {
	cfg := testCfg()
	fInline := mk("a.go", 1, findings.SeverityCritical, 0.95) // inline tier
	fNote := mk("b.go", 2, findings.SeverityMinor, 0.70)      // notes tier
	fs := []findings.Finding{fInline, fNote}

	// Prior compact records from a first run, with cids stamped, plus a
	// fingerprint that won't reappear (a resolved finding).
	first := Route(fs, emptyIndex(), nil, cfg)
	prior := first.ActiveCompact(map[string]int64{first.Inline[0].Fingerprint: 4242})
	prior = append(prior, CompactFinding{Fp: "deadbeefdeadbeef", Path: "gone.go", Severity: "major"})

	second := Route(fs, emptyIndex(), prior, cfg)
	if second.Stats.RepeatedInline != 1 || second.Stats.RepeatedNotes != 1 {
		t.Fatalf("want 1 repeated inline + 1 repeated note, got %+v", second.Stats)
	}
	if !second.Inline[0].Repeated || !second.Notes[0].Repeated {
		t.Fatal("reappearing findings must be marked Repeated")
	}
	if second.Inline[0].Cid != 4242 {
		t.Fatalf("repeated inline must inherit its prior cid, got %d", second.Inline[0].Cid)
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

// TestMetaRoundTrip: v2 findings + resolved survive encode/decode.
func TestMetaRoundTrip(t *testing.T) {
	res := Route([]findings.Finding{
		mk("a.go", 1, findings.SeverityCritical, 0.95),
		mk("b.go", 2, findings.SeverityMinor, 0.70),
	}, emptyIndex(), nil, testCfg())
	m := BuildMeta("head123", "2026-07-06T00:00:00Z", res.ActiveCompact(nil), nil, []string{"aaaa"})
	if m.Version != 2 || m.HeadSHA != "head123" || len(m.Findings) != 2 || len(m.Resolved) != 1 {
		t.Fatalf("bad meta: %+v", m)
	}
	got, err := DecodeMeta(EncodeMeta(m))
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "head123" || len(got.Findings) != 2 || got.Findings[0].Path != "a.go" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Findings[0].Side != "R" || got.Findings[0].Category != "bug" {
		t.Fatalf("compact record fields lost: %+v", got.Findings[0])
	}
	if len(got.Resolved) != 1 || got.Resolved[0] != "aaaa" {
		t.Fatalf("resolved lost: %+v", got.Resolved)
	}
}

// TestMetaV1Migration: a stage-3 v1 block decodes, reports IsV1, and
// PriorForRoute synthesizes minimal records so cross-run dedupe still works.
func TestMetaV1Migration(t *testing.T) {
	v1 := `{"v":1,"head_sha":"h","fps":[{"f":"abc","p":"x.go","s":"major","extra":"ignored"}],"ts":"t","future_key":42}`
	got, err := DecodeMeta(base64Std(v1))
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsV1() {
		t.Fatal("v1 block must report IsV1")
	}
	pr := got.PriorForRoute()
	if len(pr) != 1 || pr[0].Fp != "abc" || pr[0].Path != "x.go" {
		t.Fatalf("v1 -> compact synthesis wrong: %+v", pr)
	}
}

// TestV2DecodeToleratesUnknownFields is the forward-compat guarantee for v2.
func TestV2DecodeToleratesUnknownFields(t *testing.T) {
	raw := `{"v":2,"head_sha":"h","ts":"t","findings":[{"f":"abc","p":"x.go","l":5,"sd":"R","s":"major","c":0.9,"t":"T","cid":7,"cat":"bug","surprise":1}],"resolved":["z"],"newfield":true}`
	got, err := DecodeMeta(base64Std(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.IsV1() || len(got.Findings) != 1 || got.Findings[0].Cid != 7 {
		t.Fatalf("v2 decode broke: %+v", got)
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

// TestMetaFindingsCap: >100 findings => compact records capped at 100.
func TestMetaFindingsCap(t *testing.T) {
	var fs []findings.Finding
	for i := 0; i < 150; i++ {
		fs = append(fs, mk(fmt.Sprintf("f%d.go", i), 1, findings.SeverityMinor, 0.70))
	}
	res := Route(fs, emptyIndex(), nil, testCfg())
	m := BuildMeta("h", "t", res.ActiveCompact(nil), nil, nil)
	if len(m.Findings) != maxMetaFindings {
		t.Fatalf("findings not capped: %d, want %d", len(m.Findings), maxMetaFindings)
	}
}

// TestCapFindingsEvictsNotesFirst: inline records survive the cap; notes are
// dropped first.
func TestCapFindingsEvictsNotesFirst(t *testing.T) {
	var active []CompactFinding
	for i := 0; i < 90; i++ {
		active = append(active, CompactFinding{Fp: fmt.Sprintf("i%d", i), Tier: "inline"})
	}
	for i := 0; i < 30; i++ {
		active = append(active, CompactFinding{Fp: fmt.Sprintf("n%d", i), Tier: "notes"})
	}
	m := BuildMeta("h", "t", active, nil, nil)
	if len(m.Findings) != maxMetaFindings {
		t.Fatalf("cap = %d, want %d", len(m.Findings), maxMetaFindings)
	}
	inlineKept := 0
	for _, f := range m.Findings {
		if f.Tier == "inline" {
			inlineKept++
		}
	}
	if inlineKept != 90 {
		t.Fatalf("all 90 inline records must survive, got %d", inlineKept)
	}
}

// TestRollResolved: dedup keeps newest position and caps oldest-first.
func TestRollResolved(t *testing.T) {
	got := rollResolved([]string{"a", "b", "c"}, []string{"b", "d"})
	// "b" reappears -> moved to newest; order: a, c, b, d
	want := []string{"a", "c", "b", "d"}
	if len(got) != 4 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rollResolved = %v, want %v", got, want)
		}
	}
	// cap oldest-first
	var many []string
	for i := 0; i < 150; i++ {
		many = append(many, fmt.Sprintf("r%d", i))
	}
	capped := rollResolved(many, nil)
	if len(capped) != maxMetaResolved || capped[0] != "r50" {
		t.Fatalf("resolved cap wrong: len %d first %s", len(capped), capped[0])
	}
}

func TestShortExpandSide(t *testing.T) {
	if ShortSide(findings.SideRight) != "R" || ShortSide(findings.SideLeft) != "L" {
		t.Fatal("ShortSide")
	}
	if ExpandSide("R") != findings.SideRight || ExpandSide("L") != findings.SideLeft {
		t.Fatal("ExpandSide")
	}
}

// TestAddCarriedAndResolved covers the delta-assembly helpers.
func TestAddCarriedAndResolved(t *testing.T) {
	res := Route([]findings.Finding{mk("fresh.go", 1, findings.SeverityCritical, 0.95)}, emptyIndex(), nil, testCfg())
	res.AddCarried([]CompactFinding{
		{Fp: "carried1", Path: "old.go", Line: 9, Side: "R", Severity: "major", Conf: 0.9, Title: "carried inline", Cid: 11, Tier: "inline"},
		{Fp: "carried2", Path: "old.go", Line: 3, Side: "R", Severity: "minor", Conf: 0.7, Title: "carried note", Tier: "notes"},
	})
	if len(res.Inline) != 2 || res.Stats.InlineCount != 2 {
		t.Fatalf("carried inline not merged: %d", len(res.Inline))
	}
	var carried *Finding
	for i := range res.Inline {
		if res.Inline[i].Fingerprint == "carried1" {
			carried = &res.Inline[i]
		}
	}
	if carried == nil || !carried.Repeated || !carried.Carried || carried.Cid != 11 {
		t.Fatalf("carried inline flags wrong: %+v", carried)
	}
	res.AddResolved([]CompactFinding{{Fp: "gone1", Path: "z.go", Severity: "major"}, {Fp: "gone1"}})
	if res.Stats.ResolvedCount != 1 {
		t.Fatalf("AddResolved dedupe failed: %d", res.Stats.ResolvedCount)
	}
}
