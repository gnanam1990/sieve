package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/webhook"
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

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	return string(b), err
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
	if !strings.Contains(errOut.String(), "providers.default.model") {
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

// TestForkPRSkipsCleanly is R4: a fork PR under Actions exits 0 with a notice
// and a step-summary entry, never reaching a provider call.
func TestForkPRSkipsCleanly(t *testing.T) {
	dir := t.TempDir()
	eventPath := dir + "/event.json"
	if err := writeFile(eventPath, `{
		"pull_request": {"number": 5, "head": {"repo": {"full_name": "forker/app"}}, "base": {"repo": {"full_name": "acme/app"}}},
		"repository": {"full_name": "acme/app"}
	}`); err != nil {
		t.Fatal(err)
	}
	summaryPath := dir + "/summary.md"
	if err := writeFile(summaryPath, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_PATH", eventPath)
	t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)

	var out, errOut bytes.Buffer
	// No --api-url and no server: if it did not skip, it would try to reach
	// GitHub and fail — so exit 0 proves the skip happened first.
	code := run([]string{"review", "--repo", "acme/app", "--pr", "5", "--token", "x", "--post"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("fork PR must exit 0, got %d; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "fork PR") {
		t.Fatalf("expected fork notice on stderr:\n%s", errOut.String())
	}
	summary, _ := readFile(summaryPath)
	if !strings.Contains(summary, "fork PR") || !strings.Contains(summary, "skipped") {
		t.Fatalf("step summary missing fork notice:\n%s", summary)
	}
}

// TestForkSkipNotAppliedToDryRun: a --dry-run needs no secrets, so it is not
// short-circuited by the fork guard.
func TestForkSkipNotAppliedToDryRun(t *testing.T) {
	dir := t.TempDir()
	eventPath := dir + "/event.json"
	_ = writeFile(eventPath, `{"pull_request":{"number":5,"head":{"repo":{"full_name":"forker/app"}}},"repository":{"full_name":"acme/app"}}`)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_PATH", eventPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"main.go","status":"modified"}]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			fmt.Fprint(w, "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n")
		default:
			fmt.Fprint(w, `{"number":5,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "acme/app", "--pr", "5", "--token", "x",
		"--dry-run", "--api-url", srv.URL, "--config", dir + "/.sieve.yml"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("dry-run on a fork should still run, got %d; stderr:\n%s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "fork PR") {
		t.Fatal("dry-run must not trigger the fork skip")
	}
}

// TestMissingKeyWritesStepSummary is R4: a fatal error under Actions writes a
// fix hint to the step summary, and the error names the env var, not its value.
func TestMissingKeyWritesStepSummary(t *testing.T) {
	dir := t.TempDir()
	summaryPath := dir + "/summary.md"
	_ = writeFile(summaryPath, "")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)
	t.Setenv("GITHUB_EVENT_PATH", "")

	// Config demands a real provider whose key env var is unset -> fatal.
	cfgPath := dir + "/.sieve.yml"
	_ = writeFile(cfgPath, "provider:\n  type: anthropic\n  model: m\n  api_key_env: DEFINITELY_UNSET_KEY_VAR\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			fmt.Fprint(w, "")
		case strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			fmt.Fprint(w, `[]`) // walkthrough locate: none yet
		default:
			fmt.Fprint(w, `{"number":1,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "a/b", "--pr", "1", "--token", "x", "--post",
		"--api-url", srv.URL, "--config", cfgPath}, &out, &errOut)
	if code != exitError {
		t.Fatalf("missing key must exit 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "DEFINITELY_UNSET_KEY_VAR") {
		t.Fatalf("error must name the env var: %s", errOut.String())
	}
	summary, _ := readFile(summaryPath)
	if !strings.Contains(summary, "sieve failed") || !strings.Contains(summary, "api_key_env_name") {
		t.Fatalf("step summary missing fix hint:\n%s", summary)
	}
}

func TestStatsEmptyStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"stats", "--repo", "o/r"}, &out, &errOut); code != exitOK {
		t.Fatalf("stats exit %d; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "CATEGORY") || !strings.Contains(out.String(), "0 runs") {
		t.Fatalf("stats output wrong:\n%s", out.String())
	}
	// --json path.
	out.Reset()
	if code := run([]string{"stats", "--repo", "o/r", "--json"}, &out, &errOut); code != exitOK {
		t.Fatal("stats --json failed")
	}
	if !strings.Contains(out.String(), `"Totals"`) {
		t.Fatalf("stats --json wrong:\n%s", out.String())
	}
}

func TestStatsRequiresRepo(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	var out, errOut bytes.Buffer
	if code := run([]string{"stats"}, &out, &errOut); code != exitError {
		t.Fatalf("stats without repo must error, got %d", code)
	}
}

func TestAdminCLI(t *testing.T) {
	stats := webhook.AdminStats{
		Version:       "v-test",
		UptimeSeconds: 42,
		QueueDepth:    3,
		DeadLetters:   1,
		Running:       []string{"o/r#7"},
		RecentDead:    nil,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(stats)
	}))
	defer srv.Close()

	t.Setenv("TEST_ADMIN_SECRET", "secret")
	var out, errOut bytes.Buffer
	code := run([]string{"admin", "--url", srv.URL + "/admin", "--secret-env", "TEST_ADMIN_SECRET"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("admin exit %d; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Version:") || !strings.Contains(out.String(), "o/r#7") {
		t.Fatalf("admin output wrong:\n%s", out.String())
	}

	// Missing secret.
	out.Reset()
	errOut.Reset()
	t.Setenv("TEST_ADMIN_SECRET", "")
	code = run([]string{"admin", "--url", srv.URL + "/admin", "--secret-env", "TEST_ADMIN_SECRET"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("admin without secret must error, got %d", code)
	}
}

func TestLearningsNoNegatives(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	cfg := writeFakeFixture(t) // valid provider config (not reached; store is empty)
	if code := run([]string{"learnings", "--repo", "o/r", "--config", cfg}, &out, &errOut); code != exitOK {
		t.Fatalf("learnings exit %d; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no new learnings") {
		t.Fatalf("empty store must yield no learnings:\n%s", out.String())
	}
}

func TestSyncCLI(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/graphql"):
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`)
		case strings.Contains(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/comments"):
			fmt.Fprint(w, `[]`) // no walkthrough
		case strings.HasSuffix(r.URL.Path, "/pulls/1/comments"):
			fmt.Fprint(w, `[]`) // no inline comments
		default:
			fmt.Fprint(w, `{"number":1}`)
		}
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run([]string{"sync", "--repo", "o/r", "--pr", "1", "--token", "x", "--api-url", srv.URL}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("sync exit %d; stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "synced 0 events") {
		t.Fatalf("sync output wrong:\n%s", out.String())
	}
}

func TestReviewSarifOutput(t *testing.T) {
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
		default:
			fmt.Fprint(w, `{"number":1,"title":"T","state":"open","user":{"login":"a"},"base":{"sha":"b"},"head":{"sha":"h"}}`)
		}
	}))
	defer srv.Close()

	sarifPath := filepath.Join(t.TempDir(), "sieve.sarif")
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x",
		"--api-url", srv.URL, "--config", fixture, "--sarif", sarifPath}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, errOut.String())
	}

	data, err := os.ReadFile(sarifPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("sarif file not created: %v", err)
	}
	if !strings.Contains(string(data), `"version": "2.1.0"`) {
		t.Fatalf("sarif missing version marker:\n%s", string(data))
	}
	if !strings.Contains(string(data), `"sieve/bug"`) {
		t.Fatalf("sarif missing rule id:\n%s", string(data))
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
