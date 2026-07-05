package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestReviewRequiresDryRun(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"review", "--repo", "o/r", "--pr", "1", "--token", "x"}, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "--dry-run") {
		t.Fatalf("error should mention --dry-run: %s", errOut.String())
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
