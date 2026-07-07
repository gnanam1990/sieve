package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Load normalizes the legacy provider into providers.default.
	want := Default()
	_ = normalizeProviders(&want, false, false)
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("defaults mismatch (-want +got):\n%s", diff)
	}
	if _, ok := cfg.Providers["default"]; !ok || cfg.Review.Roles.Reviewer != "default" {
		t.Fatalf("legacy default not mapped: %+v", cfg)
	}
}

func TestLoadFullFile(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
paths:
  exclude:
    - "docs/**"
    - "**/*.gen.go"
review:
  max_inline_comments: 5
  min_confidence: 0.5
  inline_min_confidence: 0.9
  inline_min_severity: critical
provider:
  model: "some-model"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Paths = Paths{Exclude: []string{"docs/**", "**/*.gen.go"}}
	want.Review.MaxInlineComments = 5
	want.Review.MinConfidence = 0.5
	want.Review.InlineMinConfidence = 0.9
	want.Review.InlineMinSeverity = "critical"
	want.Provider.Model = "some-model"
	_ = normalizeProviders(&want, true, false) // legacy provider block present
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

// TestProviderFormsMatrix: the legacy singular block, the new providers map,
// and the both-present hard error (R1 back-compat).
func TestProviderFormsMatrix(t *testing.T) {
	// Legacy singular -> providers.default + roles.reviewer=default.
	old, err := Load(writeConfig(t, "provider:\n  type: anthropic\n  model: m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p := old.Providers["default"]; p.Model != "m" || old.Review.Roles.Reviewer != "default" {
		t.Fatalf("legacy mapping wrong: %+v roles=%+v", old.Providers, old.Review.Roles)
	}

	// New providers map + judge pipeline.
	neu, err := Load(writeConfig(t, `
providers:
  fast:
    type: openai-compat
    base_url: https://openrouter.ai/api/v1
    model: cheap
    api_key_env: OPENROUTER_API_KEY
  strong:
    type: anthropic
    model: claude-sonnet-4-6
    api_key_env: ANTHROPIC_API_KEY
review:
  pipeline: judge
  roles:
    generator: fast
    judge: strong
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(neu.Providers) != 2 || neu.Review.Pipeline != "judge" {
		t.Fatalf("new form wrong: %+v", neu.Review)
	}
	if p := neu.Providers["fast"]; p.MaxTokens != 4096 || p.TimeoutSeconds != 120 {
		t.Fatalf("named provider defaults not filled: %+v", p)
	}
	if err := neu.ValidateForReview(); err != nil {
		t.Fatalf("judge config should validate for review: %v", err)
	}

	// Both present -> hard error with a migration hint.
	_, err = Load(writeConfig(t, "provider:\n  model: m\nproviders:\n  default:\n    type: anthropic\n    model: m\n"))
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("both forms must hard-error: %v", err)
	}
}

// TestPipelineRoleValidation covers the role/pipeline validation rules.
func TestPipelineRoleValidation(t *testing.T) {
	base := "providers:\n  a:\n    type: anthropic\n    model: m\n  b:\n    type: anthropic\n    model: m\n"
	cases := map[string]string{
		"bad pipeline":         base + "review:\n  pipeline: quad\n  roles:\n    reviewer: a\n",
		"single unknown role":  base + "review:\n  pipeline: single\n  roles:\n    reviewer: nope\n",
		"judge missing judge":  base + "review:\n  pipeline: judge\n  roles:\n    generator: a\n",
		"ensemble too few":     base + "review:\n  pipeline: ensemble\n  roles:\n    ensemble: [a]\n",
		"ensemble undefined":   base + "review:\n  pipeline: ensemble\n  roles:\n    ensemble: [a, nope]\n",
		"negative run budget":  base + "review:\n  pipeline: single\n  roles:\n    reviewer: a\n  max_run_tokens: -1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatal("want validation error, got nil")
			}
		})
	}
	// A valid ensemble of two.
	if _, err := Load(writeConfig(t, base+"review:\n  pipeline: ensemble\n  roles:\n    ensemble: [a, b]\n")); err != nil {
		t.Fatalf("valid ensemble rejected: %v", err)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "paths:\n  exclude: [\"docs/**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxInlineComments != 10 || cfg.Review.MinConfidence != 0.6 ||
		cfg.Review.InlineMinConfidence != 0.8 || cfg.Review.InlineMinSeverity != "major" {
		t.Fatalf("partial file clobbered defaults: %+v", cfg.Review)
	}
}

func TestContextDepthDefaultsAndOverride(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.ContextDepth != "symbols" {
		t.Errorf("default context_depth = %q, want symbols", cfg.Review.ContextDepth)
	}
	if cfg.Review.ContextMaxFiles != 20 {
		t.Errorf("default context_max_files = %d, want 20", cfg.Review.ContextMaxFiles)
	}
	if cfg.Review.ContextMaxTokens != 8000 {
		t.Errorf("default context_max_tokens = %d, want 8000", cfg.Review.ContextMaxTokens)
	}

	cfg, err = Load(writeConfig(t, "review:\n  context_depth: blast\n  context_max_files: 50\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.ContextDepth != "blast" {
		t.Errorf("context_depth override = %q, want blast", cfg.Review.ContextDepth)
	}
	if cfg.Review.ContextMaxFiles != 50 {
		t.Errorf("context_max_files override = %d, want 50", cfg.Review.ContextMaxFiles)
	}
}

func TestLoadUnknownKeyIsHardError(t *testing.T) {
	_, err := Load(writeConfig(t, "review:\n  max_comment: 5\n"))
	if err == nil {
		t.Fatal("want error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "max_comment") {
		t.Fatalf("error should name the offending key, got: %v", err)
	}
}

// TestPostKeyIsRejected is the R1.1 safety invariant: no config key can ever
// enable posting. A `post:` block anywhere is an unknown-key hard error.
func TestPostKeyIsRejected(t *testing.T) {
	for _, content := range []string{
		"post: true\n",
		"review:\n  post: true\n",
		"provider:\n  post: aggressive\n",
	} {
		_, err := Load(writeConfig(t, content))
		if err == nil {
			t.Fatalf("post config must be an unknown-key error, got nil for %q", content)
		}
		if !strings.Contains(err.Error(), "post") {
			t.Fatalf("error should name the post key, got: %v", err)
		}
	}
}

func TestLoadValidation(t *testing.T) {
	cases := map[string]string{
		"max_inline_comments too low":  "review:\n  max_inline_comments: 0\n",
		"max_inline_comments too high": "review:\n  max_inline_comments: 31\n",
		"min_confidence too low":       "review:\n  min_confidence: -0.1\n",
		"min_confidence too big":       "review:\n  min_confidence: 1.5\n",
		"inline_conf below floor":      "review:\n  min_confidence: 0.7\n  inline_min_confidence: 0.6\n",
		"inline_conf out of range":     "review:\n  inline_min_confidence: 1.5\n",
		"bad inline severity":          "review:\n  inline_min_severity: nit\n",
		"minor inline severity":        "review:\n  inline_min_severity: minor\n",
		"max_tokens too low":             "provider:\n  max_tokens: 255\n",
		"max_tokens too high":            "provider:\n  max_tokens: 32769\n",
		"max_input_tokens negative":      "provider:\n  max_input_tokens: -1\n",
		"max_input_tokens too high":      "provider:\n  max_input_tokens: 200001\n",
		"temperature too high":           "provider:\n  temperature: 1.5\n",
		"temperature negative":           "provider:\n  temperature: -0.1\n",
		"concurrency too low":          "review:\n  concurrency: 0\n",
		"concurrency too high":         "review:\n  concurrency: 9\n",
		"timeout too low":              "provider:\n  timeout_seconds: 9\n",
		"timeout too high":             "provider:\n  timeout_seconds: 601\n",
		"bad provider type":            "provider:\n  type: bedrock\n",
		"bad context depth":            "review:\n  context_depth: full\n",
		"negative context max files":   "review:\n  context_max_files: -1\n",
		"negative context max tokens":  "review:\n  context_max_tokens: -1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatal("want validation error, got nil")
			}
		})
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("SIEVE_MAX_INLINE_COMMENTS", "20")
	t.Setenv("SIEVE_MIN_CONFIDENCE", "0.5")
	t.Setenv("SIEVE_MODEL", "env-model")
	t.Setenv("SIEVE_EXCLUDE", "a/**, b/**")
	cfg, err := Load(writeConfig(t, "review:\n  max_inline_comments: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxInlineComments != 20 {
		t.Errorf("env should override file: got %d", cfg.Review.MaxInlineComments)
	}
	if cfg.Review.MinConfidence != 0.5 {
		t.Errorf("SIEVE_MIN_CONFIDENCE not applied: %v", cfg.Review.MinConfidence)
	}
	// SIEVE_MODEL must reach the provider the review actually calls — the map
	// entry for the active reviewer role — not just the legacy singular struct.
	if got := cfg.Providers[cfg.Review.Roles.Reviewer].Model; got != "env-model" {
		t.Errorf("SIEVE_MODEL did not reach the active provider: got %q via role %q", got, cfg.Review.Roles.Reviewer)
	}
	if cfg.Provider.Model != "env-model" {
		t.Errorf("legacy Provider.Model should stay coherent: %q", cfg.Provider.Model)
	}
	if diff := cmp.Diff([]string{"a/**", "b/**"}, cfg.Paths.Exclude); diff != "" {
		t.Errorf("SIEVE_EXCLUDE mismatch (-want +got):\n%s", diff)
	}
}

// TestEnvModelOverridesJudgeGenerator: SIEVE_MODEL overrides the primary active
// role (the generator) in a multi-model judge config, reaching the review path.
func TestEnvModelOverridesJudgeGenerator(t *testing.T) {
	t.Setenv("SIEVE_MODEL", "env-model")
	yaml := "review:\n  pipeline: judge\n  roles:\n    generator: gen\n    judge: jdg\n" +
		"providers:\n  gen:\n    type: anthropic\n    model: file-gen\n    api_key_env: A\n" +
		"  jdg:\n    type: anthropic\n    model: file-judge\n    api_key_env: B\n"
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["gen"].Model != "env-model" {
		t.Errorf("SIEVE_MODEL should override the generator model, got %q", cfg.Providers["gen"].Model)
	}
	if cfg.Providers["jdg"].Model != "file-judge" {
		t.Errorf("SIEVE_MODEL must not touch the judge model, got %q", cfg.Providers["jdg"].Model)
	}
}

func TestEnvInvalid(t *testing.T) {
	t.Setenv("SIEVE_MAX_INLINE_COMMENTS", "lots")
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("want error for non-integer env, got nil")
	}
}

func TestEnvOutOfRangeStillValidated(t *testing.T) {
	t.Setenv("SIEVE_MAX_INLINE_COMMENTS", "99")
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("want validation error for out-of-range env value, got nil")
	}
}

// TestAPIKeyFieldIsRejected asserts the security invariant: there is no
// api_key config field, so a key pasted into .sieve.yml is a hard error.
func TestAPIKeyFieldIsRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "provider:\n  api_key: sk-secret\n"))
	if err == nil {
		t.Fatal("api_key in config must be an unknown-key error")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error should name api_key: %v", err)
	}
}

func TestValidateForReview(t *testing.T) {
	// Build a single-pipeline config whose reviewer provider is `p`.
	with := func(p Provider) Config {
		c := Default()
		c.Providers = map[string]Provider{"r": p}
		c.Review.Roles.Reviewer = "r"
		return c
	}
	cases := map[string]struct {
		p       Provider
		wantErr string
	}{
		"anthropic needs model":    {Provider{Type: "anthropic", APIKeyEnv: "K"}, "model"},
		"anthropic needs key env":  {Provider{Type: "anthropic", Model: "m"}, "api_key_env"},
		"openai-compat needs base": {Provider{Type: "openai-compat", Model: "m", APIKeyEnv: "K"}, "base_url"},
		"fake needs fixture":       {Provider{Type: "fake"}, "fixture"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := with(c.p).ValidateForReview()
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
	if err := with(Provider{Type: "anthropic", Model: "m", APIKeyEnv: "K"}).ValidateForReview(); err != nil {
		t.Fatalf("valid anthropic rejected: %v", err)
	}
	if err := with(Provider{Type: "fake", Fixture: "x.json"}).ValidateForReview(); err != nil {
		t.Fatalf("valid fake rejected: %v", err)
	}
}

func TestIncludeFileContentExplicitFalse(t *testing.T) {
	cfg, err := Load(writeConfig(t, "review:\n  include_file_content: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.IncludeFileContent {
		t.Fatal("explicit false must override the true default")
	}
}

// TestMergeRepoReviewHonorsNoiseButNotSpend: a repo may tune noise/scope knobs
// but the server keeps ownership of every spend-governing field.
func TestMergeRepoReviewHonorsNoiseButNotSpend(t *testing.T) {
	base, err := Load(writeConfig(t, "review:\n  pipeline: judge\n  roles:\n    generator: g\n    judge: j\n  max_run_tokens: 5000\n  min_confidence: 0.5\n"+
		"providers:\n  g:\n    type: anthropic\n    model: gm\n    api_key_env: A\n  j:\n    type: anthropic\n    model: jm\n    api_key_env: B\n"))
	if err != nil {
		t.Fatal(err)
	}
	// The untrusted repo tries to raise its own budget, switch to ensemble,
	// pick providers, AND lower the noise floor.
	repo := []byte("provider:\n  type: anthropic\n  model: EVIL\n  api_key_env: STEAL\n" +
		"providers:\n  evil:\n    type: anthropic\n    model: EVIL\n" +
		"review:\n  pipeline: ensemble\n  max_run_tokens: 99999999\n  concurrency: 8\n  min_confidence: 0.6\n  max_inline_comments: 3\n")
	merged, err := MergeRepoReview(base, repo)
	if err != nil {
		t.Fatal(err)
	}
	// Noise/scope knobs from the repo take effect.
	if merged.Review.MinConfidence != 0.6 {
		t.Errorf("repo min_confidence should apply, got %v", merged.Review.MinConfidence)
	}
	if merged.Review.MaxInlineComments != 3 {
		t.Errorf("repo max_inline_comments should apply, got %v", merged.Review.MaxInlineComments)
	}
	// Spend-governing fields stay at the server's values.
	if merged.Review.Pipeline != "judge" {
		t.Errorf("repo must NOT change the pipeline, got %q", merged.Review.Pipeline)
	}
	if merged.Review.MaxRunTokens != 5000 {
		t.Errorf("repo must NOT raise the token budget, got %d", merged.Review.MaxRunTokens)
	}
	if merged.Review.Concurrency != base.Review.Concurrency {
		t.Errorf("repo must NOT change concurrency, got %d", merged.Review.Concurrency)
	}
	// Providers/keys are never sourced from the repo.
	if _, ok := merged.Providers["evil"]; ok {
		t.Error("repo providers must be ignored entirely")
	}
	if merged.Providers["g"].Model != "gm" {
		t.Errorf("server providers must be untouched, got %q", merged.Providers["g"].Model)
	}
}

func TestMergeRepoReviewNoBlockIsNoop(t *testing.T) {
	base, err := Load(writeConfig(t, "review:\n  min_confidence: 0.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeRepoReview(base, []byte("# just a comment, no review block\n"))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Review.MinConfidence != 0.4 {
		t.Fatalf("absent repo review must leave server settings, got %v", merged.Review.MinConfidence)
	}
}

func TestMergeRepoReviewInvalidIsRejected(t *testing.T) {
	base, err := Load(writeConfig(t, "review:\n  min_confidence: 0.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	// An out-of-range noise floor from the repo must be rejected (base returned).
	if _, err := MergeRepoReview(base, []byte("review:\n  min_confidence: 5.0\n")); err == nil {
		t.Fatal("invalid repo review must error")
	}
	if _, err := MergeRepoReview(base, []byte("review: [not, a, map]\n")); err == nil {
		t.Fatal("malformed review block must error")
	}
}
