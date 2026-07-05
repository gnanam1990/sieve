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
	// MaxComments and MinConfidence are reserved for stage 3; they are
	// validated now so a bad config fails fast.
	MaxComments   int     `yaml:"max_comments"`
	MinConfidence float64 `yaml:"min_confidence"`
}

// Provider holds LLM provider selection.
type Provider struct {
	// Model is reserved for stage 2.
	Model string `yaml:"model"`
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
			MaxComments:   10,
			MinConfidence: 0.7,
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
	if v := os.Getenv("SIEVE_MAX_COMMENTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SIEVE_MAX_COMMENTS: %q is not an integer", v)
		}
		cfg.Review.MaxComments = n
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
	if c.Review.MaxComments < 1 || c.Review.MaxComments > 50 {
		return fmt.Errorf("review.max_comments must be between 1 and 50, got %d", c.Review.MaxComments)
	}
	if c.Review.MinConfidence < 0.0 || c.Review.MinConfidence > 1.0 {
		return fmt.Errorf("review.min_confidence must be between 0.0 and 1.0, got %g", c.Review.MinConfidence)
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
