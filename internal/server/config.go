// Package server is sieve's daemon: it validates a server.yml, wires GitHub App
// auth, the webhook receiver, and the persistent review queue, and runs the
// review pipeline per job against an installation token. TLS is expected to
// terminate at a reverse proxy.
package server

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gnanam1990/sieve/internal/config"
)

// AppConfig is the GitHub App identity.
type AppConfig struct {
	ID             int64  `yaml:"id"`
	PrivateKeyPath string `yaml:"private_key_path"`
}

// Config is the parsed server.yml. The review/providers blocks share the
// .sieve.yml schema; providers are server-owned (untrusted repos never choose
// the model or keys).
type Config struct {
	Listen           string                     `yaml:"listen"`
	App              AppConfig                  `yaml:"app"`
	WebhookSecretEnv string                     `yaml:"webhook_secret_env"`
	DataDir          string                     `yaml:"data_dir"`
	ReposAllow       []string                   `yaml:"repos_allow"`
	Workers          int                        `yaml:"workers"`
	Review           config.Review              `yaml:"review"`
	Providers        map[string]config.Provider `yaml:"providers"`
}

// LoadConfig reads and strictly decodes server.yml, seeding review defaults so
// omitted knobs are valid.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return Config{}, fmt.Errorf("read server config: %w", err)
	}
	return LoadConfigFromBytes(data)
}

// LoadConfigFromBytes strictly decodes server.yml content, seeding review
// defaults so omitted knobs stay valid.
func LoadConfigFromBytes(data []byte) (Config, error) {
	sc := Config{
		Listen:  "127.0.0.1:8787",
		Workers: 2,
		Review:  config.Default().Review, // seed defaults; server.yml overlays
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return sc, fmt.Errorf("parse server config: %w", err)
	}
	return sc, nil
}

// reviewConfig assembles the validated base config (server review + providers)
// the daemon runs reviews with.
func (sc Config) reviewConfig() (config.Config, error) {
	cfg := config.Config{Review: sc.Review, Providers: sc.Providers}
	if err := config.PrepareServerConfig(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Check is one startup validation with a human-readable label.
type Check struct {
	label string
	err   error
}

// Label is the human-readable name of the check.
func (c Check) Label() string { return c.label }

// Err is the check's failure, or nil when it passed.
func (c Check) Err() error { return c.err }

// Validate runs the startup checks and returns them in order plus the first
// fatal error. The caller prints one line per check, then "ready".
func (sc Config) Validate() ([]Check, error) {
	var checks []Check
	add := func(label string, err error) error {
		checks = append(checks, Check{label: label, err: err})
		return err
	}

	if err := add("app.id set", requireAppID(sc.App.ID)); err != nil {
		return checks, err
	}
	if err := add("app private key present and mode 0600/0400", checkKeyPerms(sc.App.PrivateKeyPath)); err != nil {
		return checks, err
	}
	if err := add("webhook secret present in "+sc.WebhookSecretEnv, checkSecretEnv(sc.WebhookSecretEnv)); err != nil {
		return checks, err
	}
	if err := add("data dir writable ("+sc.DataDir+")", checkDataDir(sc.DataDir)); err != nil {
		return checks, err
	}
	if err := add("workers positive", func() error {
		if sc.Workers <= 0 {
			return fmt.Errorf("workers must be at least 1 (got %d)", sc.Workers)
		}
		return nil
	}()); err != nil {
		return checks, err
	}
	if err := add("review config valid", checkReview(sc)); err != nil {
		return checks, err
	}
	return checks, nil
}

func requireAppID(id int64) error {
	if id <= 0 {
		return fmt.Errorf("app.id must be a positive GitHub App id, got %d", id)
	}
	return nil
}

// checkKeyPerms refuses a private key that isn't a regular file with mode
// exactly 0600 or 0400 (the spec's hard policy). Symlinks are rejected outright
// (os.Lstat, not os.Stat) so an attacker who can place a link at the configured
// path cannot point the daemon at an arbitrary readable target whose mode the
// check would otherwise pass.
func checkKeyPerms(path string) error {
	if path == "" {
		return fmt.Errorf("app.private_key_path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("app private key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("app private key %s is a symlink; copy the PEM to a regular file (no follow)", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("app private key %s is not a regular file", path)
	}
	if mode := info.Mode().Perm(); mode != 0o600 && mode != 0o400 {
		return fmt.Errorf("app private key %s has mode %#o; must be 0600 or 0400 (chmod 600)", path, mode)
	}
	return nil
}

func checkSecretEnv(name string) error {
	if name == "" {
		return fmt.Errorf("webhook_secret_env is required")
	}
	if os.Getenv(name) == "" {
		return fmt.Errorf("webhook secret env var %s is unset or empty", name)
	}
	return nil
}

// checkDataDir ensures data_dir exists (creating it) and is writable.
func checkDataDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // operator data dir
		return fmt.Errorf("data_dir: %w", err)
	}
	probe := dir + "/.sieve-write-probe"
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("data_dir %s is not writable: %w", dir, err)
	}
	_ = os.Remove(probe)
	return nil
}

func checkReview(sc Config) error {
	_, err := sc.reviewConfig()
	return err
}
