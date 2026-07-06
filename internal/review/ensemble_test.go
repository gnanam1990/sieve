package review

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
)

// mkFinding is a compact finding constructor for cluster tests.
func mkFinding(path string, line, end int, side findings.Side, cat string, conf float64) findings.Finding {
	return findings.Finding{Path: path, Line: line, EndLine: end, Side: side, Category: cat, Confidence: conf, Severity: findings.SeverityMajor, Title: "t", Body: "b"}
}

// member wraps a list of findings as one member's generation result.
func member(fs ...findings.Finding) memberResult { return memberResult{findings: fs} }

func TestEnsembleAgree(t *testing.T) {
	base := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.8)
	tests := []struct {
		name string
		b    findings.Finding
		want bool
	}{
		{"identical line", mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.5), true},
		{"within +3", mkFinding("a.go", 13, 0, findings.SideRight, "bug", 0.5), true},
		{"at -3 boundary", mkFinding("a.go", 7, 0, findings.SideRight, "bug", 0.5), true},
		{"just past +3", mkFinding("a.go", 14, 0, findings.SideRight, "bug", 0.5), false},
		{"range overlap far apart", mkFinding("a.go", 20, 8, findings.SideRight, "bug", 0.5), false}, // 8<20 collapses to single line 20
		{"true range overlap", mkFinding("a.go", 20, 40, findings.SideRight, "bug", 0.5), false},
		{"different path", mkFinding("b.go", 10, 0, findings.SideRight, "bug", 0.5), false},
		{"different category", mkFinding("a.go", 10, 0, findings.SideRight, "security", 0.5), false},
		{"different side", mkFinding("a.go", 10, 0, findings.SideLeft, "bug", 0.5), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensembleAgree(base, tt.b); got != tt.want {
				t.Fatalf("ensembleAgree=%v want %v", got, tt.want)
			}
		})
	}
}

// TestEnsembleAgreeRangeOverlap: overlapping multi-line ranges agree even when
// their start lines are >3 apart.
func TestEnsembleAgreeRangeOverlap(t *testing.T) {
	a := mkFinding("a.go", 10, 25, findings.SideRight, "bug", 0.8) // [10,25]
	b := mkFinding("a.go", 20, 30, findings.SideRight, "bug", 0.7) // [20,30]
	if !ensembleAgree(a, b) {
		t.Fatal("overlapping ranges must agree")
	}
	c := mkFinding("a.go", 26, 30, findings.SideRight, "bug", 0.7) // [26,30] just past [10,25]
	if ensembleAgree(a, c) {
		t.Fatal("non-overlapping ranges >3 apart must not agree")
	}
}

func TestClusterEnsembleKeepsAgreement(t *testing.T) {
	// A and B agree on line 10; C is alone at line 100.
	a := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.7)
	b := mkFinding("a.go", 12, 0, findings.SideRight, "bug", 0.9)
	c := mkFinding("a.go", 100, 0, findings.SideRight, "bug", 0.6)
	survivors, dropped := clusterEnsemble([]memberResult{member(a), member(b), member(c)})
	if len(survivors) != 1 {
		t.Fatalf("want 1 surviving cluster, got %d: %+v", len(survivors), survivors)
	}
	if survivors[0].Confidence != 0.9 {
		t.Errorf("survivor must be the highest-confidence member finding, got %v", survivors[0].Confidence)
	}
	if got := survivors[0].EnsembleMean; got < 0.79 || got > 0.81 {
		t.Errorf("ensemble mean want ~0.80, got %v", got)
	}
	if dropped != 1 {
		t.Errorf("the lonely finding must be dropped, got dropped=%d", dropped)
	}
}

// TestEnsembleMeanIsPerMember: a member that contributes several near-duplicate
// findings to one cluster is weighted once (by its best), so it can't skew the
// agreement metric upward.
func TestEnsembleMeanIsPerMember(t *testing.T) {
	// Member 0 emits the same issue twice (lines 10, 11 — within ±3), member 1
	// once at line 10. All cluster together; 2 distinct members survive.
	a1 := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.9)
	a2 := mkFinding("a.go", 11, 0, findings.SideRight, "bug", 0.9)
	b := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.3)
	survivors, dropped := clusterEnsemble([]memberResult{member(a1, a2), member(b)})
	if len(survivors) != 1 || dropped != 0 {
		t.Fatalf("want 1 cluster / 0 dropped, got %d/%d", len(survivors), dropped)
	}
	// Per-member mean is (0.9 + 0.3)/2 = 0.60, NOT the per-finding (0.9+0.9+0.3)/3 = 0.70.
	if got := survivors[0].EnsembleMean; got < 0.59 || got > 0.61 {
		t.Errorf("EnsembleMean should be the per-member mean ~0.60, got %v", got)
	}
}

// TestClusterEnsembleSameMemberTwiceIsNotAgreement: two findings from ONE
// member that agree with each other still form a single-member cluster.
func TestClusterEnsembleSameMemberTwiceIsNotAgreement(t *testing.T) {
	a1 := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.9)
	a2 := mkFinding("a.go", 11, 0, findings.SideRight, "bug", 0.8)
	survivors, dropped := clusterEnsemble([]memberResult{member(a1, a2), member()})
	if len(survivors) != 0 {
		t.Fatalf("one member cannot agree with itself: %+v", survivors)
	}
	if dropped != 2 {
		t.Errorf("both same-member findings must be dropped, got %d", dropped)
	}
}

// TestClusterEnsembleAllDisagree: the degenerate case — nothing agrees.
func TestClusterEnsembleAllDisagree(t *testing.T) {
	a := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.9)
	b := mkFinding("b.go", 10, 0, findings.SideRight, "bug", 0.9)
	c := mkFinding("c.go", 10, 0, findings.SideRight, "bug", 0.9)
	survivors, dropped := clusterEnsemble([]memberResult{member(a), member(b), member(c)})
	if len(survivors) != 0 || dropped != 3 {
		t.Fatalf("all-disagree must yield no survivors and 3 dropped, got %d/%d", len(survivors), dropped)
	}
}

// TestClusterEnsembleThreeWayAgreement: all three members agree → one cluster
// of three, mean over all three.
func TestClusterEnsembleThreeWayAgreement(t *testing.T) {
	a := mkFinding("a.go", 10, 0, findings.SideRight, "bug", 0.6)
	b := mkFinding("a.go", 11, 0, findings.SideRight, "bug", 0.9)
	c := mkFinding("a.go", 12, 0, findings.SideRight, "bug", 0.6)
	survivors, dropped := clusterEnsemble([]memberResult{member(a), member(b), member(c)})
	if len(survivors) != 1 || dropped != 0 {
		t.Fatalf("want 1 cluster and 0 dropped, got %d/%d", len(survivors), dropped)
	}
	if survivors[0].Confidence != 0.9 {
		t.Errorf("survivor confidence want 0.9, got %v", survivors[0].Confidence)
	}
	if got := survivors[0].EnsembleMean; got < 0.69 || got > 0.71 {
		t.Errorf("mean want ~0.70, got %v", got)
	}
}

// ensembleProviderOptions writes a three-member ensemble config over fake
// providers and returns run Options.
func ensembleProviderOptions(t *testing.T, srv *httptest.Server, fixtures ...string) Options {
	t.Helper()
	var providerBlock, memberList strings.Builder
	for i, fx := range fixtures {
		abs, err := filepath.Abs(fx)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("m%d", i)
		fmt.Fprintf(&providerBlock, "  %s:\n    type: fake\n    fixture: %q\n", name, abs)
		fmt.Fprintf(&memberList, "      - %s\n", name)
	}
	cfgYAML := "review:\n  pipeline: ensemble\n  roles:\n    ensemble:\n" + memberList.String() +
		"providers:\n" + providerBlock.String()
	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
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

// TestEnsemblePipelineE2E: three fake members, two agree on alpha.txt:3, the
// third is alone → one survivor, one dropped, per-member token accounting.
func TestEnsemblePipelineE2E(t *testing.T) {
	opts := ensembleProviderOptions(t, fixtureGitHub(t, false),
		"testdata/ens_member_a.json", "testdata/ens_member_b.json", "testdata/ens_member_c.json")
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 || rc.Findings[0].Line != 3 {
		t.Fatalf("want the agreed alpha.txt:3 finding to survive, got %+v", rc.Findings)
	}
	if rc.Findings[0].Confidence != 0.9 {
		t.Errorf("survivor must carry the highest member confidence, got %v", rc.Findings[0].Confidence)
	}
	if got := rc.Findings[0].EnsembleMean; got < 0.84 || got > 0.86 {
		t.Errorf("ensemble_mean want ~0.85, got %v", got)
	}
	if rc.Stats.Pipeline != "ensemble" || rc.Stats.EnsembleMembers != 3 {
		t.Errorf("ensemble stats wrong: %+v", rc.Stats)
	}
	if rc.Stats.EnsembleClusters != 1 || rc.Stats.EnsembleDropped != 1 {
		t.Errorf("want 1 cluster / 1 dropped, got %d/%d", rc.Stats.EnsembleClusters, rc.Stats.EnsembleDropped)
	}
	if rc.Stats.Requests != 3 {
		t.Errorf("want 3 requests (one per member), got %d", rc.Stats.Requests)
	}
	for _, m := range []string{"m0", "m1", "m2"} {
		if _, ok := rc.Stats.RoleTokens[m]; !ok {
			t.Errorf("missing per-member tokens for %q: %+v", m, rc.Stats.RoleTokens)
		}
	}
}

// TestMaxRunTokensRefuses: a tiny budget refuses the run before any provider
// call and surfaces the estimate.
func TestMaxRunTokensRefuses(t *testing.T) {
	opts := fakeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json")
	// Rewrite the config to add a 1-token budget.
	data, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgYAML := "review:\n  max_run_tokens: 1\n" + string(data)
	if err := os.WriteFile(opts.ConfigPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "max_run_tokens") {
		t.Fatalf("tiny budget must refuse the run, got err=%v", err)
	}
}

// TestMaxRunTokensAllowsAmpleBudget: a generous budget does not interfere.
func TestMaxRunTokensAllowsAmpleBudget(t *testing.T) {
	opts := fakeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json")
	data, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgYAML := "review:\n  max_run_tokens: 10000000\n" + string(data)
	if err := os.WriteFile(opts.ConfigPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("ample budget must not refuse: %v", err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("review should have proceeded normally, got %+v", rc.Findings)
	}
}

// TestEnsembleGenerateUndefinedMember: an ensemble member with no provider
// definition fails loudly before any generation.
func TestEnsembleGenerateUndefinedMember(t *testing.T) {
	rc := &ReviewContext{}
	cfg := config.Config{Providers: map[string]config.Provider{}}
	cfg.Review.Pipeline = "ensemble"
	cfg.Review.Concurrency = 1
	cfg.Review.Roles.Ensemble = []string{"ghost"}
	opts := Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := ensembleGenerate(context.Background(), rc, cfg, opts, generation{})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("undefined member must error, got %v", err)
	}
}

// TestGeneratorRoleUndefined: a reviewer role pointing at a missing provider is
// a hard error.
func TestGeneratorRoleUndefined(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{}}
	cfg.Review.Pipeline = "single"
	cfg.Review.Roles.Reviewer = "missing"
	if _, _, _, err := generatorRole(cfg); err == nil {
		t.Fatal("undefined reviewer role must error")
	}
}

func TestModelLabel(t *testing.T) {
	single := config.Config{Providers: map[string]config.Provider{"default": {Model: "sonnet"}}}
	single.Review.Pipeline = "single"
	single.Review.Roles.Reviewer = "default"
	if got := modelLabel(single); got != "sonnet" {
		t.Errorf("single label = %q, want sonnet", got)
	}

	judge := config.Config{Providers: map[string]config.Provider{"g": {Model: "haiku"}, "j": {Model: "opus"}}}
	judge.Review.Pipeline = "judge"
	judge.Review.Roles.Generator = "g"
	judge.Review.Roles.Judge = "j"
	if got := modelLabel(judge); got != "haiku+opus" {
		t.Errorf("judge label = %q, want haiku+opus", got)
	}

	// Identical models de-duplicate; a model-less fake falls back to its type.
	ens := config.Config{Providers: map[string]config.Provider{"a": {Model: "m"}, "b": {Model: "m"}, "c": {Type: "fake"}}}
	ens.Review.Pipeline = "ensemble"
	ens.Review.Roles.Ensemble = []string{"a", "b", "c"}
	if got := modelLabel(ens); got != "m+fake" {
		t.Errorf("ensemble label = %q, want m+fake", got)
	}
}

func TestPipelineMultiplier(t *testing.T) {
	cases := []struct {
		pipeline string
		ensemble []string
		want     float64
	}{
		{"single", nil, 1.0},
		{"judge", nil, 1.6},
		{"ensemble", []string{"a", "b"}, 2.0},
		{"ensemble", []string{"a", "b", "c"}, 3.0},
		{"ensemble", []string{"a"}, 1.0}, // degenerate: single-member ensemble
		{"ensemble", nil, 1.0},           // degenerate: no members
		{"", nil, 1.0},                   // unset defaults to single-pass
	}
	for _, c := range cases {
		cfg := config.Config{}
		cfg.Review.Pipeline = c.pipeline
		cfg.Review.Roles.Ensemble = c.ensemble
		if got := pipelineMultiplier(cfg); got != c.want {
			t.Errorf("pipeline %q ensemble %v: multiplier=%v want %v", c.pipeline, c.ensemble, got, c.want)
		}
	}
}
