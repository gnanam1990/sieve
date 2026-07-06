package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// writeKey writes a throwaway RSA private key (PKCS#1 PEM) at the given mode and
// returns its path. No key material is committed to the repo.
func writeKey(t *testing.T, mode os.FileMode) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "app.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := os.WriteFile(p, pemBytes, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile mode is pre-umask; force it
		t.Fatal(err)
	}
	return p
}

func validConfig(t *testing.T) Config {
	t.Helper()
	sc, err := LoadConfigFromBytes([]byte(`listen: 127.0.0.1:8787
app:
  id: 12345
  private_key_path: PLACEHOLDER
webhook_secret_env: SIEVE_TEST_WH_SECRET
data_dir: PLACEHOLDER_DIR
workers: 2
review:
  pipeline: single
  roles: { reviewer: default }
providers:
  default:
    type: fake
    fixture: /tmp/x.json
`))
	if err != nil {
		t.Fatal(err)
	}
	sc.App.PrivateKeyPath = writeKey(t, 0o600)
	sc.DataDir = t.TempDir()
	return sc
}

func TestValidateHappyPath(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "secret")
	sc := validConfig(t)
	checks, err := sc.Validate()
	if err != nil {
		t.Fatalf("valid config should pass: %v", err)
	}
	for _, c := range checks {
		if c.Err() != nil {
			t.Errorf("check %q failed: %v", c.Label(), c.Err())
		}
	}
	if len(checks) != 6 {
		t.Errorf("want 6 startup checks, got %d", len(checks))
	}
}

func TestValidateBadKeyPerms(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "secret")
	sc := validConfig(t)
	sc.App.PrivateKeyPath = writeKey(t, 0o644) // group/world readable
	_, err := sc.Validate()
	if err == nil {
		t.Fatal("a world-readable key must be rejected")
	}
}

func TestValidateMissingKey(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "secret")
	sc := validConfig(t)
	sc.App.PrivateKeyPath = filepath.Join(t.TempDir(), "nope.pem")
	if _, err := sc.Validate(); err == nil {
		t.Fatal("missing key must be rejected")
	}
}

func TestValidateMissingSecret(t *testing.T) {
	sc := validConfig(t) // SIEVE_TEST_WH_SECRET unset
	if _, err := sc.Validate(); err == nil {
		t.Fatal("missing webhook secret must be rejected")
	}
}

func TestValidateBadAppID(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "secret")
	sc := validConfig(t)
	sc.App.ID = 0
	if _, err := sc.Validate(); err == nil {
		t.Fatal("app.id 0 must be rejected")
	}
}

func TestValidateReviewConfigInvalid(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "secret")
	sc := validConfig(t)
	// A judge pipeline with no judge role is an invalid review config.
	sc.Review.Pipeline = "judge"
	sc.Review.Roles.Generator = "default"
	sc.Review.Roles.Judge = "" // missing
	if _, err := sc.Validate(); err == nil {
		t.Fatal("invalid review config must be rejected at startup")
	}
}

func TestLoadConfigStrictUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.yml")
	if err := os.WriteFile(p, []byte("listen: x\nbogus_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown server.yml key must be rejected")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yml")); err == nil {
		t.Fatal("missing config file must error")
	}
}
