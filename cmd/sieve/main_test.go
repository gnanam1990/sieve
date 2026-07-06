package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeFixture writes a fake-provider config whose fixture yields one valid
// inline finding on the shared multi-hunk diff, and returns the config path.
func writeFakeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "findings.json")
	if err := os.WriteFile(fixture, []byte(`{"findings":[{"Path":"alpha.txt","Line":3,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"Issue","Body":"why"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".sieve.yml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixture)), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readFixtureDiff(t *testing.T) ([]byte, error) {
	t.Helper()
	return os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
}

func TestRunNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "sieve ") {
		t.Fatalf("bad version output: %q", out.String())
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Fatal("help should print usage")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"deploy"}, &out, &errOut); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "deploy") {
		t.Fatal("error should name the unknown command")
	}
}

// Without --dry-run the LLM pass runs, which needs a complete provider
// config; the default (anthropic, no model) must fail fast and clearly.
func TestReviewWithoutProviderConfigFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			fmt.Fprint(w, "")
		default:
			fmt.Fprint(w, `{"number":1,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x",
		"--api-url", srv.URL, "--config", t.TempDir() + "/.sieve.yml"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "provider.model") {
		t.Fatalf("error should name the missing config key: %s", errOut.String())
	}
}

func TestReviewRequiresRepoAndPR(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("GITHUB_EVENT_PATH", "")
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--dry-run", "--token", "x"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
}

func TestReviewDryRunEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"main.go","status":"modified"}]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			fmt.Fprint(w, "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n")
		default:
			fmt.Fprint(w, `{"number":1,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x",
		"--dry-run", "--api-url", srv.URL, "--config", t.TempDir() + "/.sieve.yml"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"PRNumber": 1`) {
		t.Fatalf("stdout should hold ReviewContext JSON:\n%.200s", out.String())
	}
	if !strings.Contains(errOut.String(), "files total") {
		t.Fatalf("stderr should hold summary:\n%s", errOut.String())
	}

	// --json-only suppresses the summary.
	out.Reset()
	errOut.Reset()
	code = run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x",
		"--dry-run", "--json-only", "--api-url", srv.URL, "--config", t.TempDir() + "/.sieve.yml"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if strings.Contains(errOut.String(), "files total") {
		t.Fatal("--json-only must suppress the summary")
	}
}

func TestPostAndDryRunMutuallyExclusive(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x", "--dry-run", "--post"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got: %s", errOut.String())
	}
}

// TestPostPartialFailureExitsTwo: a --post run whose walkthrough succeeds but
// whose inline review submission fails must exit 2 (partial).
func TestPostPartialFailureExitsTwo(t *testing.T) {
	fixture := writeFakeFixture(t)
	diffData, _ := readFixtureDiff(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			w.Write(diffData) //nolint:errcheck
		case strings.Contains(r.URL.Path, "/contents/"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			fmt.Fprint(w, `[]`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/"):
			w.WriteHeader(http.StatusCreated) // walkthrough create OK
			fmt.Fprint(w, `{"id":1}`)
		case strings.HasSuffix(r.URL.Path, "/reviews"), strings.HasSuffix(r.URL.Path, "/pulls/1/comments"):
			w.WriteHeader(http.StatusInternalServerError) // inline submission fails hard
			fmt.Fprint(w, `{"message":"boom"}`)
		default:
			fmt.Fprint(w, `{"number":1,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x", "--post",
		"--api-url", srv.URL, "--config", fixture}, &out, &errOut)
	if code != exitPartial {
		t.Fatalf("exit %d, want %d (partial); stderr:\n%s", code, exitPartial, errOut.String())
	}
}

func TestReviewErrorExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x",
		"--dry-run", "--api-url", srv.URL, "--config", t.TempDir() + "/.sieve.yml"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
}
