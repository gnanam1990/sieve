// Package config loads and validates .sieve.yml.
//
// Precedence: built-in defaults -> config file -> SIEVE_* env -> flags
// (flags are applied by the caller, on top of what Load returns).
package config

import (
	"errors"
	"fmt"
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
	Paths    Paths    `yaml:"paths"`
	Review   Review   `yaml:"review"`
	Provider Provider `yaml:"provider"`
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

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// no file: defaults + env
	case err != nil:
		return cfg, fmt.Errorf("read config: %w", err)
	default:
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, humanizeUnknownField(err))
		}
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
		cfg.Provider.Model = v
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
	switch c.Provider.Type {
	case "anthropic", "openai-compat", "fake":
	default:
		return fmt.Errorf("provider.type must be anthropic, openai-compat, or fake; got %q", c.Provider.Type)
	}
	if c.Provider.MaxTokens < 256 || c.Provider.MaxTokens > 32768 {
		return fmt.Errorf("provider.max_tokens must be between 256 and 32768, got %d", c.Provider.MaxTokens)
	}
	if c.Provider.Temperature < 0 || c.Provider.Temperature > 1 {
		return fmt.Errorf("provider.temperature must be between 0 and 1, got %g", c.Provider.Temperature)
	}
	if c.Provider.TimeoutSeconds < 10 || c.Provider.TimeoutSeconds > 600 {
		return fmt.Errorf("provider.timeout_seconds must be between 10 and 600, got %d", c.Provider.TimeoutSeconds)
	}
	return nil
}

// ValidateForReview checks the requirements that only matter when an LLM
// call is about to happen (a dry run never needs a model or key name).
func (c Config) ValidateForReview() error {
	switch c.Provider.Type {
	case "anthropic", "openai-compat":
		if c.Provider.Model == "" {
			return fmt.Errorf("provider.model is required for provider.type %q", c.Provider.Type)
		}
		if c.Provider.APIKeyEnv == "" {
			return fmt.Errorf("provider.api_key_env is required for provider.type %q", c.Provider.Type)
		}
		if c.Provider.Type == "openai-compat" && c.Provider.BaseURL == "" {
			return fmt.Errorf("provider.base_url is required for provider.type openai-compat (e.g. https://api.openai.com/v1, http://localhost:11434/v1)")
		}
	case "fake":
		if c.Provider.Fixture == "" {
			return fmt.Errorf("provider.fixture is required for provider.type fake")
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
