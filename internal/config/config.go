// Package config loads and validates .sieve.yml.
//
// Precedence: built-in defaults -> config file -> SIEVE_* env -> flags
// (flags are applied by the caller, on top of what Load returns).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultFile is the config file name looked up at the repo root.
const DefaultFile = ".sieve.yml"

// Paths holds path-based review scoping.
type Paths struct {
	Exclude []string `yaml:"exclude"`
}

// Review holds review-behavior knobs.
type Review struct {
	// Noise-gate tiering (stage 3). MinConfidence is the drop floor;
	// InlineMinConfidence/InlineMinSeverity gate the inline tier;
	// MaxInlineComments caps inline comments per run (overflow is demoted to
	// notes, never dropped). There is deliberately no "post" key: posting is
	// enabled only by the --post flag, never by config (see the safety model
	// in the README).
	MinConfidence       float64 `yaml:"min_confidence"`        // drop floor: below this a finding is discarded
	InlineMinConfidence float64 `yaml:"inline_min_confidence"` // inline tier requires >= this
	InlineMinSeverity   string  `yaml:"inline_min_severity"`   // inline tier requires severity >= this (major|critical)
	MaxInlineComments   int     `yaml:"max_inline_comments"`   // hard cap on inline tier per run (1..30)

	IncludeFileContent bool `yaml:"include_file_content"` // attach full changed-file contents when small
	MaxFileContentKB   int  `yaml:"max_file_content_kb"`  // per-file cap for content attachment
	Concurrency        int  `yaml:"concurrency"`          // parallel provider calls
	ReviewDrafts       bool `yaml:"review_drafts"`        // review draft PRs too

	Incremental bool `yaml:"incremental"` // delta re-review of only changed files (stage 5)
	Calibration bool `yaml:"calibration"` // runtime confidence calibration from addressed-rate (stage 5, opt-in)

	Pipeline string `yaml:"pipeline"` // single | judge | ensemble (stage 6)
	Roles    Roles  `yaml:"roles"`    // which named provider fills each role
	MaxRunTokens int `yaml:"max_run_tokens"` // pre-flight token budget; 0 = unlimited

	// Stage 8 context-depth controls.
	ContextDepth     string   `yaml:"context_depth"`     // symbols | repomap | blast
	ContextMaxFiles  int      `yaml:"context_max_files"`  // cap files in repomap/blast
	ContextMaxTokens int      `yaml:"context_max_tokens"` // rough token budget for context
	ContextLangs     []string `yaml:"context_langs"`      // empty = all languages
}

// Roles binds pipeline roles to named providers (stage 6).
type Roles struct {
	Reviewer  string   `yaml:"reviewer"`  // single pipeline
	Generator string   `yaml:"generator"` // judge pipeline
	Judge     string   `yaml:"judge"`     // judge pipeline
	Ensemble  []string `yaml:"ensemble"`  // ensemble pipeline (2..3 members)
}

// Provider holds LLM provider selection. There is deliberately no api_key
// field: keys come only from the env var named by api_key_env, so a key
// can never end up committed in .sieve.yml.
type Provider struct {
	Type           string  `yaml:"type"`     // anthropic | openai-compat | fake
	Model          string  `yaml:"model"`    // required for anthropic/openai-compat
	BaseURL        string  `yaml:"base_url"` // required iff type == openai-compat
	APIKeyEnv      string  `yaml:"api_key_env"`
	MaxTokens      int     `yaml:"max_tokens"`
	Temperature    float64 `yaml:"temperature"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
	Fixture        string  `yaml:"fixture"` // fake type only: canned response file
}

// Config is the full .sieve.yml schema.
type Config struct {
	Paths     Paths               `yaml:"paths"`
	Review    Review              `yaml:"review"`
	Provider  Provider            `yaml:"provider"`  // legacy singular; mapped to providers.default
	Providers map[string]Provider `yaml:"providers"` // named provider map (stage 6)
}

// ProviderFor returns the provider bound to a role name.
func (c Config) ProviderFor(name string) (Provider, bool) {
	p, ok := c.Providers[name]
	return p, ok
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Review: Review{
			// TODO(calibration): finalize min_confidence / inline_min_confidence
			// from the stage-03 gate-4 calibration report during the live batch.
			MinConfidence:       0.6,
			InlineMinConfidence: 0.8,
			InlineMinSeverity:   "major",
			MaxInlineComments:   10,
			IncludeFileContent:  true,
			MaxFileContentKB:    64,
			Concurrency:         3,
			ReviewDrafts:        false,
			Incremental:         true,
			Calibration:         false,
			Pipeline:            "single",
			ContextDepth:        "symbols",
			ContextMaxFiles:     20,
			ContextMaxTokens:    8000,
		},
		Provider: Provider{ //nolint:gosec // G101: APIKeyEnv holds the NAME of an env var, never a credential
			Type:           "anthropic",
			APIKeyEnv:      "ANTHROPIC_API_KEY",
			MaxTokens:      4096,
			Temperature:    0.1,
			TimeoutSeconds: 120,
		},
	}
}

// Load reads the config file at path (missing file is fine: defaults),
// applies SIEVE_* env overrides, and validates. Unknown YAML keys are a
// hard error so typos never silently disable a setting.
func Load(path string) (Config, error) {
	cfg := Default()

	hasSingular, hasMap := false, false
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// no file: defaults + env
	case err != nil:
		return cfg, fmt.Errorf("read config: %w", err)
	default:
		var probe map[string]yaml.Node
		if err := yaml.Unmarshal(data, &probe); err == nil {
			_, hasSingular = probe["provider"]
			_, hasMap = probe["providers"]
		}
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, humanizeUnknownField(err))
		}
	}

	if err := normalizeProviders(&cfg, hasSingular, hasMap); err != nil {
		return cfg, err
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("SIEVE_MAX_INLINE_COMMENTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SIEVE_MAX_INLINE_COMMENTS: %q is not an integer", v)
		}
		cfg.Review.MaxInlineComments = n
	}
	if v := os.Getenv("SIEVE_MIN_CONFIDENCE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("SIEVE_MIN_CONFIDENCE: %q is not a number", v)
		}
		cfg.Review.MinConfidence = f
	}
	if v := os.Getenv("SIEVE_MODEL"); v != "" {
		// Override the model of the provider the review actually calls. applyEnv
		// runs AFTER normalizeProviders, so writing only the legacy singular
		// cfg.Provider would be a dead write — the review path reads the map.
		cfg.Provider.Model = v // keep the legacy field coherent for anyone reading it
		if roles := cfg.ActiveRoles(); len(roles) > 0 {
			if p, ok := cfg.Providers[roles[0]]; ok {
				p.Model = v
				cfg.Providers[roles[0]] = p
			}
		}
	}
	if v := os.Getenv("SIEVE_EXCLUDE"); v != "" {
		for _, g := range strings.Split(v, ",") {
			if g = strings.TrimSpace(g); g != "" {
				cfg.Paths.Exclude = append(cfg.Paths.Exclude, g)
			}
		}
	}
	return nil
}

// Validate checks value ranges.
func (c Config) Validate() error {
	if c.Review.MaxInlineComments < 1 || c.Review.MaxInlineComments > 30 {
		return fmt.Errorf("review.max_inline_comments must be between 1 and 30, got %d", c.Review.MaxInlineComments)
	}
	if c.Review.MinConfidence < 0.0 || c.Review.MinConfidence > 1.0 {
		return fmt.Errorf("review.min_confidence must be between 0.0 and 1.0, got %g", c.Review.MinConfidence)
	}
	if c.Review.InlineMinConfidence < 0.0 || c.Review.InlineMinConfidence > 1.0 {
		return fmt.Errorf("review.inline_min_confidence must be between 0.0 and 1.0, got %g", c.Review.InlineMinConfidence)
	}
	if c.Review.InlineMinConfidence < c.Review.MinConfidence {
		return fmt.Errorf("review.inline_min_confidence (%g) must be >= review.min_confidence (%g)", c.Review.InlineMinConfidence, c.Review.MinConfidence)
	}
	switch c.Review.InlineMinSeverity {
	case "major", "critical":
	default:
		return fmt.Errorf("review.inline_min_severity must be major or critical, got %q", c.Review.InlineMinSeverity)
	}
	if c.Review.MaxFileContentKB < 1 {
		return fmt.Errorf("review.max_file_content_kb must be positive, got %d", c.Review.MaxFileContentKB)
	}
	if c.Review.Concurrency < 1 || c.Review.Concurrency > 8 {
		return fmt.Errorf("review.concurrency must be between 1 and 8, got %d", c.Review.Concurrency)
	}
	if c.Review.MaxRunTokens < 0 {
		return fmt.Errorf("review.max_run_tokens must be >= 0, got %d", c.Review.MaxRunTokens)
	}
	switch c.Review.ContextDepth {
	case "symbols", "repomap", "blast":
	default:
		return fmt.Errorf("review.context_depth must be symbols, repomap, or blast; got %q", c.Review.ContextDepth)
	}
	if c.Review.ContextMaxFiles < 0 {
		return fmt.Errorf("review.context_max_files must be >= 0, got %d", c.Review.ContextMaxFiles)
	}
	if c.Review.ContextMaxTokens < 0 {
		return fmt.Errorf("review.context_max_tokens must be >= 0, got %d", c.Review.ContextMaxTokens)
	}
	for name, p := range c.Providers {
		if err := validateProviderRanges(name, p); err != nil {
			return err
		}
	}
	return c.validatePipeline()
}

// validateProviderRanges checks one named provider's value ranges.
func validateProviderRanges(name string, p Provider) error {
	switch p.Type {
	case "anthropic", "openai-compat", "fake":
	default:
		return fmt.Errorf("providers.%s.type must be anthropic, openai-compat, or fake; got %q", name, p.Type)
	}
	if p.MaxTokens < 256 || p.MaxTokens > 32768 {
		return fmt.Errorf("providers.%s.max_tokens must be between 256 and 32768, got %d", name, p.MaxTokens)
	}
	if p.Temperature < 0 || p.Temperature > 1 {
		return fmt.Errorf("providers.%s.temperature must be between 0 and 1, got %g", name, p.Temperature)
	}
	if p.TimeoutSeconds < 10 || p.TimeoutSeconds > 600 {
		return fmt.Errorf("providers.%s.timeout_seconds must be between 10 and 600, got %d", name, p.TimeoutSeconds)
	}
	return nil
}

// validatePipeline checks the pipeline selection and that its roles reference
// defined providers.
func (c Config) validatePipeline() error {
	switch c.Review.Pipeline {
	case "single":
		return c.requireRole(c.Review.Roles.Reviewer, "reviewer")
	case "judge":
		if err := c.requireRole(c.Review.Roles.Generator, "generator"); err != nil {
			return err
		}
		return c.requireRole(c.Review.Roles.Judge, "judge")
	case "ensemble":
		if n := len(c.Review.Roles.Ensemble); n < 2 || n > 3 {
			return fmt.Errorf("pipeline ensemble requires 2..3 members in review.roles.ensemble, got %d", n)
		}
		for _, m := range c.Review.Roles.Ensemble {
			if _, ok := c.Providers[m]; !ok {
				return fmt.Errorf("review.roles.ensemble references undefined provider %q", m)
			}
		}
		return nil
	default:
		return fmt.Errorf("review.pipeline must be single, judge, or ensemble; got %q", c.Review.Pipeline)
	}
}

func (c Config) requireRole(name, role string) error {
	if name == "" {
		return fmt.Errorf("review.roles.%s is required for pipeline %q", role, c.Review.Pipeline)
	}
	if _, ok := c.Providers[name]; !ok {
		return fmt.Errorf("review.roles.%s references undefined provider %q", role, name)
	}
	return nil
}

// normalizeProviders reconciles the legacy singular `provider:` with the new
// `providers:` map: both present is a hard error; only the singular (or the
// built-in default) maps to providers.default with roles.reviewer=default; and
// every named provider gets the built-in defaults filled in for unset fields.
func normalizeProviders(cfg *Config, hasSingular, hasMap bool) error {
	if hasSingular && hasMap {
		return fmt.Errorf("config sets both 'provider' and 'providers' — use one. " +
			"Migration: move the 'provider:' block under 'providers:' as a named entry " +
			"(e.g. providers.default) and set review.roles.reviewer to that name")
	}
	if !hasMap {
		if cfg.Providers == nil {
			cfg.Providers = map[string]Provider{}
		}
		cfg.Providers["default"] = cfg.Provider
		if cfg.Review.Roles.Reviewer == "" {
			cfg.Review.Roles.Reviewer = "default"
		}
	}
	for name, p := range cfg.Providers {
		applyProviderDefaults(&p)
		cfg.Providers[name] = p
	}
	return nil
}

// applyProviderDefaults fills the built-in defaults for a named provider's
// unset fields (temperature 0 is left as-is — a valid deterministic setting).
func applyProviderDefaults(p *Provider) {
	if p.Type == "" {
		p.Type = "anthropic"
	}
	if p.MaxTokens == 0 {
		p.MaxTokens = 4096
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 120
	}
	if p.APIKeyEnv == "" && p.Type == "anthropic" {
		p.APIKeyEnv = "ANTHROPIC_API_KEY"
	}
}

// PrepareServerConfig normalizes and validates a config assembled from a daemon
// server.yml (a review block seeded from Default plus a providers map). Provider
// defaults are filled; per-provider key indirection is still resolved from the
// process env at review time via ValidateForReview.
func PrepareServerConfig(cfg *Config) error {
	if err := normalizeProviders(cfg, false, cfg.Providers != nil); err != nil {
		return err
	}
	return cfg.Validate()
}

// MergeRepoReview overlays a repository's .sieve.yml review settings onto a
// base (server) config for daemon mode. Only the `review:` block is consulted —
// any `provider:`/`providers:` in the untrusted repo file is ignored entirely,
// and the spend-governing review fields (pipeline, roles, run-token budget,
// concurrency, content attachment) are restored from the server so a repo can
// tune WHAT gets flagged but never how much its reviews cost. The result is
// re-validated; on any error the base config is returned unchanged.
//
// Decoding is strict (yaml.Decoder.KnownFields true): a repo file may carry
// `provider`/`providers`/`paths` (kept here as discarded yaml.Node fields so the
// strict decoder treats them as permitted-but-not-acted-on per spec) AND
// `review`, but ANY other top-level key (a typo, or a future spend knob added to
// the repo file) hard-errors so the spend-lockout cannot be silently bypassed.
// The `review:` sub-block is strict-decoded the same way. An empty or
// comment-only repo file (no mapping) is a no-op, not an error. See
// TestMergeRepoReviewStrictRejectsUnknownKey and TestMergeRepoReviewNoBlockIsNoop.
func MergeRepoReview(base Config, repoYAML []byte) (Config, error) {
	// An empty or comment-only repo file (no mappings at all) is a no-op: the
	// strict decoder would otherwise error with EOF, and a repo with no .sieve.yml
	// content is the common case.
	if len(bytes.TrimSpace(repoYAML)) == 0 {
		return base, nil
	}
	// First strict decode: permit the spec-allowed top-level keys (provider,
	// providers, paths are kept as yaml.Node so they parse without acting on;
	// review is the overlay target). Any other top-level key is unknown and
	// → hard error.
	probe := struct {
		Review    yaml.Node            `yaml:"review"`
		Provider  yaml.Node            `yaml:"provider"`
		Providers yaml.Node            `yaml:"providers"`
		Paths     yaml.Node            `yaml:"paths"`
	}{}
	dec := yaml.NewDecoder(bytes.NewReader(repoYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&probe); err != nil {
		// A truly empty document (just newlines, no mapping) is not an error.
		if errors.Is(err, io.EOF) {
			return base, nil
		}
		return base, fmt.Errorf("parse repo .sieve.yml (strict): %w", err)
	}
	if probe.Review.Kind == 0 {
		return base, nil // no review block; server settings stand
	}
	// Decode the repo's review over a copy of the server's, so unset repo fields
	// keep the server value. Strict-decode the review node too: a typo'd
	// review sub-key is rejected so the spend-lockout contract for review
	// knobs cannot drift silently.
	review := base.Review
	reviewDec := yaml.NewDecoder(bytes.NewReader(repoYAML))
	reviewDec.KnownFields(true)
	probeFull := struct {
		Review yaml.Node `yaml:"review"`
		Provider yaml.Node `yaml:"provider"`
		Providers yaml.Node `yaml:"providers"`
		Paths yaml.Node `yaml:"paths"`
	}{}
	if err := reviewDec.Decode(&probeFull); err != nil {
		return base, fmt.Errorf("parse repo .sieve.yml: %w", err)
	}
	if err := probeFull.Review.Decode(&review); err != nil {
		return base, fmt.Errorf("parse repo review settings: %w", err)
	}
	// Server-owned, spend-governing fields: an untrusted repo does not get to
	// choose the pipeline, its roles, the token budget, concurrency, or how much
	// file content is attached.
	review.Pipeline = base.Review.Pipeline
	review.Roles = base.Review.Roles
	review.MaxRunTokens = base.Review.MaxRunTokens
	review.Concurrency = base.Review.Concurrency
	review.IncludeFileContent = base.Review.IncludeFileContent
	review.MaxFileContentKB = base.Review.MaxFileContentKB

	merged := base
	merged.Review = review
	if err := merged.Validate(); err != nil {
		return base, fmt.Errorf("repo review settings invalid: %w", err)
	}
	return merged, nil
}

// ActiveRoles returns the provider names the current pipeline will actually
// call (a dry run needs none of them fully configured).
func (c Config) ActiveRoles() []string {
	switch c.Review.Pipeline {
	case "judge":
		return []string{c.Review.Roles.Generator, c.Review.Roles.Judge}
	case "ensemble":
		return c.Review.Roles.Ensemble
	default:
		return []string{c.Review.Roles.Reviewer}
	}
}

// ValidateForReview checks the requirements that only matter when an LLM call
// is about to happen (a dry run never needs a model or key name), for every
// provider the active pipeline will call.
func (c Config) ValidateForReview() error {
	for _, name := range c.ActiveRoles() {
		p, ok := c.Providers[name]
		if !ok {
			return fmt.Errorf("role references undefined provider %q", name)
		}
		if err := validateProviderForReview(name, p); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderForReview(name string, p Provider) error {
	switch p.Type {
	case "anthropic", "openai-compat":
		if p.Model == "" {
			return fmt.Errorf("providers.%s.model is required for type %q", name, p.Type)
		}
		if p.APIKeyEnv == "" {
			return fmt.Errorf("providers.%s.api_key_env is required for type %q", name, p.Type)
		}
		if p.Type == "openai-compat" && p.BaseURL == "" {
			return fmt.Errorf("providers.%s.base_url is required for type openai-compat (e.g. https://api.openai.com/v1)", name)
		}
	case "fake":
		if p.Fixture == "" {
			return fmt.Errorf("providers.%s.fixture is required for type fake", name)
		}
	}
	return nil
}

// humanizeUnknownField rewrites yaml.v3's strict-mode error ("field X not
// found in type ...") into a message that names the unknown key plainly.
func humanizeUnknownField(err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "not found in type") {
		return err
	}
	var keys []string
	for _, line := range strings.Split(msg, "\n") {
		if i := strings.Index(line, "field "); i >= 0 {
			rest := line[i+len("field "):]
			if j := strings.Index(rest, " not found"); j > 0 {
				keys = append(keys, rest[:j])
			}
		}
	}
	if len(keys) == 0 {
		return err
	}
	return fmt.Errorf("unknown config key(s): %s (check for typos; see README for valid keys)", strings.Join(keys, ", "))
}
