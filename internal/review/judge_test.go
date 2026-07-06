package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/prompt"
)

// judgeProviderOptions writes a two-provider judge-pipeline config backed by
// fake providers (generator fixture + judge fixture) and returns run Options.
func judgeProviderOptions(t *testing.T, srv *httptest.Server, genFixture, judgeFixture string) Options {
	t.Helper()
	genAbs, err := filepath.Abs(genFixture)
	if err != nil {
		t.Fatal(err)
	}
	judgeAbs, err := filepath.Abs(judgeFixture)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf(`review:
  pipeline: judge
  roles:
    generator: gen
    judge: judge
providers:
  gen:
    type: fake
    fixture: %q
  judge:
    type: fake
    fixture: %q
`, genAbs, judgeAbs)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		Repo:       "octo/hello",
		PRNumber:   7,
		Token:      "test-token",
		ConfigPath: cfgPath,
		APIBaseURL: srv.URL,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestJudgePipelineKeeps: the judge verifies the one surviving generator
// finding, lowers its severity/confidence, and it passes through.
func TestJudgePipelineKeeps(t *testing.T) {
	opts := judgeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json", "testdata/fake_verdicts_keep.json")
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("want 1 surviving finding, got %d: %+v", len(rc.Findings), rc.Findings)
	}
	f := rc.Findings[0]
	if f.Severity != findings.SeverityMinor {
		t.Errorf("judge should have lowered severity to minor, got %q", f.Severity)
	}
	if f.Confidence != 0.4 {
		t.Errorf("judge confidence not applied: %v", f.Confidence)
	}
	if rc.Stats.Pipeline != "judge" {
		t.Errorf("pipeline stat wrong: %q", rc.Stats.Pipeline)
	}
	if rc.Stats.JudgeKilled != 0 || rc.Stats.JudgeFailedOpen != 0 {
		t.Errorf("no kills/fail-opens expected: %+v", rc.Stats)
	}
	// One generator batch + one judge call for the one file with findings.
	if rc.Stats.Requests != 2 {
		t.Errorf("want 2 requests (gen+judge), got %d", rc.Stats.Requests)
	}
	if _, ok := rc.Stats.RoleTokens["gen"]; !ok {
		t.Errorf("generator role tokens missing: %+v", rc.Stats.RoleTokens)
	}
	if _, ok := rc.Stats.RoleTokens["judge"]; !ok {
		t.Errorf("judge role tokens missing: %+v", rc.Stats.RoleTokens)
	}
}

// TestJudgePipelineKills: keep:false drops the finding, counts JudgeKilled,
// and records a transparent JudgeDrop with the judge's reason.
func TestJudgePipelineKills(t *testing.T) {
	opts := judgeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json", "testdata/fake_verdicts_kill.json")
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 0 {
		t.Fatalf("killed finding must not survive: %+v", rc.Findings)
	}
	if rc.Stats.JudgeKilled != 1 {
		t.Errorf("want JudgeKilled=1, got %d", rc.Stats.JudgeKilled)
	}
	if len(rc.JudgeDrops) != 1 || rc.JudgeDrops[0].Path != "alpha.txt" || rc.JudgeDrops[0].Reason == "" {
		t.Fatalf("judge drop not recorded for transparency: %+v", rc.JudgeDrops)
	}
	if rc.Stats.FindingsTotal != 0 {
		t.Errorf("FindingsTotal must reflect post-judge survivors: %d", rc.Stats.FindingsTotal)
	}
}

// TestJudgePipelineEmptyGeneratorSkipsJudge: a generator that finds nothing
// short-circuits the judge pass entirely (no judge call, no counters).
func TestJudgePipelineEmptyGeneratorSkipsJudge(t *testing.T) {
	opts := judgeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_empty.json", "testdata/fake_verdicts_keep.json")
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 0 {
		t.Fatalf("no findings expected: %+v", rc.Findings)
	}
	// One generator batch call, zero judge calls.
	if rc.Stats.Requests != 1 {
		t.Errorf("judge must be skipped when there is nothing to judge, got %d requests", rc.Stats.Requests)
	}
	if _, ok := rc.Stats.RoleTokens["judge"]; ok {
		t.Errorf("judge should not have been called: %+v", rc.Stats.RoleTokens)
	}
}

// TestJudgePipelineFailsOpenE2E: a judge that never returns valid JSON falls
// open — the generator finding survives untouched and JudgeFailedOpen counts.
func TestJudgePipelineFailsOpenE2E(t *testing.T) {
	opts := judgeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json", "testdata/fake_verdicts_bad.json")
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("fail-open must keep the generator finding: %+v", rc.Findings)
	}
	if rc.Findings[0].Severity != findings.SeverityMajor || rc.Findings[0].Confidence != 0.9 {
		t.Errorf("fail-open finding must be untouched, got %+v", rc.Findings[0])
	}
	if rc.Stats.JudgeFailedOpen != 1 {
		t.Errorf("want JudgeFailedOpen=1, got %d", rc.Stats.JudgeFailedOpen)
	}
	// Generator batch (1) + judge attempt + corrective retry (2) = 3 requests.
	if rc.Stats.Requests != 3 {
		t.Errorf("want 3 requests (gen + judge + retry), got %d", rc.Stats.Requests)
	}
}

// TestWriteSummaryJudgePipeline: the stderr summary surfaces the pipeline, its
// judge counters, and a deterministic per-role token breakdown.
func TestWriteSummaryJudgePipeline(t *testing.T) {
	rc := &ReviewContext{Repo: "o/r", PRNumber: 1, Title: "t", Author: "a"}
	rc.Stats.Requests = 4
	rc.Stats.Pipeline = "judge"
	rc.Stats.JudgeKilled = 2
	rc.Stats.JudgeFailedOpen = 1
	rc.Stats.RoleTokens = map[string]RoleUsage{"judge": {In: 300, Out: 20}, "gen": {In: 1200, Out: 90}}
	var b strings.Builder
	rc.WriteSummary(&b)
	out := b.String()
	for _, must := range []string{"pipeline: judge", "killed 2", "failed-open 1", "gen 1200/90", "judge 300/20"} {
		if !strings.Contains(out, must) {
			t.Errorf("summary missing %q\n%s", must, out)
		}
	}
	// Roles print alphabetically: gen before judge.
	if strings.Index(out, "gen 1200/90") > strings.Index(out, "judge 300/20") {
		t.Error("role token lines must be deterministic (alphabetical)")
	}
}

// TestRoleTokensForRender: the footer breakdown is role-ordered for multi-model
// pipelines and empty for the single reviewer.
func TestRoleTokensForRender(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{"g": {}, "j": {}}}
	cfg.Review.Pipeline = "judge"
	cfg.Review.Roles.Generator = "g"
	cfg.Review.Roles.Judge = "j"
	var st Stats
	st.addRole("j", 5, 1)
	st.addRole("g", 10, 2)
	got := roleTokensForRender(cfg, st)
	if len(got) != 2 || got[0].Role != "g" || got[1].Role != "j" {
		t.Fatalf("want [g,j] in active-role order, got %+v", got)
	}
	if got[0].In != 10 || got[1].Out != 1 {
		t.Errorf("token values mismatched: %+v", got)
	}

	single := config.Config{Providers: map[string]config.Provider{"default": {}}}
	single.Review.Pipeline = "single"
	single.Review.Roles.Reviewer = "default"
	var ss Stats
	ss.addRole("default", 9, 9)
	if r := roleTokensForRender(single, ss); r != nil {
		t.Errorf("single pipeline should not render a per-role breakdown, got %+v", r)
	}

	// Two roles pointing at ONE provider must be counted once, not twice.
	shared := config.Config{Providers: map[string]config.Provider{"default": {}}}
	shared.Review.Pipeline = "judge"
	shared.Review.Roles.Generator = "default"
	shared.Review.Roles.Judge = "default"
	var sh Stats
	sh.addRole("default", 140, 14)
	if r := roleTokensForRender(shared, sh); len(r) != 1 || r[0].Role != "default" || r[0].In != 140 {
		t.Errorf("shared provider must render one deduped row, got %+v", r)
	}
}

func TestParseVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		n       int
		wantErr bool
	}{
		{"valid", `{"verdicts":[{"i":0,"keep":true,"confidence":0.5,"severity":"minor","reason":"ok"}]}`, 1, false},
		{"prose wrapped", "Here you go:\n{\"verdicts\":[{\"i\":0,\"keep\":false,\"confidence\":0,\"severity\":\"nit\",\"reason\":\"x\"}]}\nDone.", 1, false},
		{"missing index", `{"verdicts":[{"i":0,"keep":true,"confidence":0.5,"severity":"minor","reason":"ok"}]}`, 2, true},
		{"duplicate index", `{"verdicts":[{"i":0,"keep":true,"confidence":0.5,"severity":"minor","reason":"a"},{"i":0,"keep":false,"confidence":0,"severity":"nit","reason":"b"}]}`, 2, true},
		{"index out of range", `{"verdicts":[{"i":5,"keep":true,"confidence":0.5,"severity":"minor","reason":"ok"}]}`, 1, true},
		{"unknown field", `{"verdicts":[{"i":0,"keep":true,"confidence":0.5,"severity":"minor","reason":"ok","bogus":1}]}`, 1, true},
		{"not json", `no verdicts here`, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseVerdicts(tt.text, tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func fptr(x float64) *float64 { return &x }

func TestApplyVerdict(t *testing.T) {
	base := func() findings.Finding {
		return findings.Finding{Severity: findings.SeverityMajor, Confidence: 0.9}
	}
	t.Run("lowers severity", func(t *testing.T) {
		f := base()
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(0.3), Severity: "minor"})
		if f.Severity != findings.SeverityMinor || f.Confidence != 0.3 {
			t.Fatalf("got %+v", f)
		}
	})
	t.Run("ignores a raise", func(t *testing.T) {
		f := base()
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(0.5), Severity: "critical"})
		if f.Severity != findings.SeverityMajor {
			t.Fatalf("judge must not raise severity, got %q", f.Severity)
		}
	})
	t.Run("ignores invalid severity", func(t *testing.T) {
		f := base()
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(0.5), Severity: "blocker"})
		if f.Severity != findings.SeverityMajor {
			t.Fatalf("invalid severity must be ignored, got %q", f.Severity)
		}
	})
	t.Run("clamps confidence", func(t *testing.T) {
		f := base()
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(1.7), Severity: "major"})
		if f.Confidence != 1 {
			t.Fatalf("confidence must clamp to 1, got %v", f.Confidence)
		}
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(-0.2), Severity: "major"})
		if f.Confidence != 0 {
			t.Fatalf("confidence must clamp to 0, got %v", f.Confidence)
		}
	})
	t.Run("omitted confidence keeps generator's", func(t *testing.T) {
		f := base() // Confidence 0.9
		applyVerdict(&f, verdict{Keep: true, Confidence: nil, Severity: "major"})
		if f.Confidence != 0.9 {
			t.Fatalf("omitted confidence must not zero the finding, got %v", f.Confidence)
		}
	})
	t.Run("explicit zero confidence is honored", func(t *testing.T) {
		f := base()
		applyVerdict(&f, verdict{Keep: true, Confidence: fptr(0), Severity: "major"})
		if f.Confidence != 0 {
			t.Fatalf("explicit 0 confidence must apply, got %v", f.Confidence)
		}
	})
}

// TestJudgeVerdictOmittedConfidenceE2E: a judge that keeps a finding but omits
// the confidence field must not silently zero it (which the noise gate would
// then drop).
func TestJudgeVerdictOmittedConfidenceE2E(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "verdicts_noconf.json")
	// keep:true, no "confidence" key.
	if err := os.WriteFile(fx, []byte(`{"verdicts":[{"i":0,"keep":true,"severity":"major","reason":"real bug"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := judgeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json", fx)
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("finding should survive, got %+v", rc.Findings)
	}
	// The generator finding had confidence 0.9; the omitted verdict field keeps it.
	if rc.Findings[0].Confidence != 0.9 {
		t.Errorf("omitted judge confidence must keep the generator's 0.9, got %v", rc.Findings[0].Confidence)
	}
}

func judgeTestFinding() []findings.Finding {
	return []findings.Finding{{Path: "a.go", Line: 1, Side: findings.SideRight, Severity: findings.SeverityMajor, Confidence: 0.9, Category: "bug", Title: "t", Body: "b"}}
}

func judgeTestFile() prompt.File {
	return prompt.File{Path: "a.go", Status: "modified"}
}

// TestRunJudgeCorrectiveRetry: a first non-JSON response then a valid one
// succeeds within the single retry.
func TestRunJudgeCorrectiveRetry(t *testing.T) {
	p := &scripted{texts: []string{
		"Sure, here are my verdicts.",
		`{"verdicts":[{"i":0,"keep":true,"confidence":0.6,"severity":"major","reason":"ok"}]}`,
	}}
	r := runJudge(context.Background(), p, "sys", judgeTestFile(), judgeTestFinding(), testProvider(), time.Minute)
	if r.failOpen {
		t.Fatalf("corrective retry should have recovered: %v", r.err)
	}
	if r.requests != 2 || p.calls != 2 {
		t.Fatalf("requests=%d calls=%d, want 2/2", r.requests, p.calls)
	}
	if !r.verdicts[0].Keep {
		t.Fatalf("verdict not parsed: %+v", r.verdicts)
	}
}

// TestRunJudgeFailsOpenAfterRetry: two malformed responses fail open.
func TestRunJudgeFailsOpenAfterRetry(t *testing.T) {
	p := &scripted{texts: []string{"nope", "still nope"}}
	r := runJudge(context.Background(), p, "sys", judgeTestFile(), judgeTestFinding(), testProvider(), time.Minute)
	if !r.failOpen || r.err == nil {
		t.Fatalf("second malformed response must fail open: %+v", r)
	}
	if r.requests != 2 {
		t.Fatalf("want 2 requests before failing open, got %d", r.requests)
	}
}

// TestRunJudgeFailsOpenOnProviderError: a transport error fails open without a
// corrective retry (the note is only for contract violations).
func TestRunJudgeFailsOpenOnProviderError(t *testing.T) {
	p := &scripted{texts: []string{""}, errs: []error{errors.New("boom")}}
	r := runJudge(context.Background(), p, "sys", judgeTestFile(), judgeTestFinding(), testProvider(), time.Minute)
	if !r.failOpen || r.requests != 1 {
		t.Fatalf("provider error must fail open with one request: %+v", r)
	}
}
