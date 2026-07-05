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
	if diff := cmp.Diff(Default(), cfg); diff != "" {
		t.Errorf("defaults mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadFullFile(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
paths:
  exclude:
    - "docs/**"
    - "**/*.gen.go"
review:
  max_comments: 5
  min_confidence: 0.9
provider:
  model: "some-model"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Paths:    Paths{Exclude: []string{"docs/**", "**/*.gen.go"}},
		Review:   Review{MaxComments: 5, MinConfidence: 0.9},
		Provider: Provider{Model: "some-model"},
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "paths:\n  exclude: [\"docs/**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxComments != 10 || cfg.Review.MinConfidence != 0.7 {
		t.Fatalf("partial file clobbered defaults: %+v", cfg.Review)
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

func TestLoadValidation(t *testing.T) {
	cases := map[string]string{
		"max_comments too low":   "review:\n  max_comments: 0\n",
		"max_comments too high":  "review:\n  max_comments: 51\n",
		"min_confidence too low": "review:\n  min_confidence: -0.1\n",
		"min_confidence too big": "review:\n  min_confidence: 1.5\n",
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
	t.Setenv("SIEVE_MAX_COMMENTS", "20")
	t.Setenv("SIEVE_MIN_CONFIDENCE", "0.5")
	t.Setenv("SIEVE_MODEL", "env-model")
	t.Setenv("SIEVE_EXCLUDE", "a/**, b/**")
	cfg, err := Load(writeConfig(t, "review:\n  max_comments: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxComments != 20 {
		t.Errorf("env should override file: got %d", cfg.Review.MaxComments)
	}
	if cfg.Review.MinConfidence != 0.5 || cfg.Provider.Model != "env-model" {
		t.Errorf("env overrides not applied: %+v", cfg)
	}
	if diff := cmp.Diff([]string{"a/**", "b/**"}, cfg.Paths.Exclude); diff != "" {
		t.Errorf("SIEVE_EXCLUDE mismatch (-want +got):\n%s", diff)
	}
}

func TestEnvInvalid(t *testing.T) {
	t.Setenv("SIEVE_MAX_COMMENTS", "lots")
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("want error for non-integer env, got nil")
	}
}

func TestEnvOutOfRangeStillValidated(t *testing.T) {
	t.Setenv("SIEVE_MAX_COMMENTS", "99")
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("want validation error for out-of-range env value, got nil")
	}
}
