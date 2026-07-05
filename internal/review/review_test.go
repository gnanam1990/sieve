package review

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeDiff = "diff --git a/main.go b/main.go\n" +
	"--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+extra\n" +
	"diff --git a/go.sum b/go.sum\n" +
	"--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-a\n+b\n" +
	"diff --git a/docs/guide.md b/docs/guide.md\n" +
	"--- a/docs/guide.md\n+++ b/docs/guide.md\n@@ -1 +1 @@\n-x\n+y\n"

func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"main.go","status":"modified"},{"filename":"go.sum","status":"modified"},{"filename":"docs/guide.md","status":"modified"}]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			fmt.Fprint(w, fakeDiff)
		default:
			fmt.Fprint(w, `{"number":9,"title":"Add feature","body":"","state":"open","draft":false,
				"user":{"login":"alice"},"base":{"ref":"main","sha":"basesha123"},"head":{"ref":"feat","sha":"headsha456"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testOptions(t *testing.T, srv *httptest.Server, configYAML string) Options {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	if configYAML != "" {
		if err := os.WriteFile(cfgPath, []byte(configYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Options{
		Repo:       "octo/hello",
		PRNumber:   9,
		Token:      "test-token",
		ConfigPath: cfgPath,
		APIBaseURL: srv.URL,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestBuild(t *testing.T) {
	rc, err := Build(context.Background(), testOptions(t, fakeGitHub(t), "paths:\n  exclude: [\"docs/**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Title != "Add feature" || rc.Author != "alice" || rc.BaseSHA != "basesha123" || rc.HeadSHA != "headsha456" {
		t.Fatalf("bad metadata: %+v", rc)
	}
	if rc.Truncated {
		t.Fatal("nothing was truncated")
	}
	if got := rc.Stats; got.FilesTotal != 3 || got.FilesReviewed != 1 || got.FilesSkipped != 2 || got.LinesAdded != 4 || got.LinesRemoved != 3 {
		t.Fatalf("bad stats: %+v", got)
	}
	if !rc.Files[1].Skipped || !strings.Contains(rc.Files[1].SkipReason, "go.sum") {
		t.Fatalf("go.sum should be default-skipped: %+v", rc.Files[1])
	}
	if !rc.Files[2].Skipped || !strings.HasPrefix(rc.Files[2].SkipReason, "config exclude:") {
		t.Fatalf("docs/guide.md should be config-skipped: %+v", rc.Files[2])
	}
	if rc.Files[0].Skipped {
		t.Fatalf("main.go should be kept: %+v", rc.Files[0])
	}
}

// TestJSONDeterministic asserts the dry-run stdout is byte-identical
// across runs for the same input.
func TestJSONDeterministic(t *testing.T) {
	srv := fakeGitHub(t)
	var outputs [][]byte
	for i := 0; i < 2; i++ {
		rc, err := Build(context.Background(), testOptions(t, srv, ""))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := rc.WriteJSON(&buf); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, buf.Bytes())
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("JSON output differs between identical runs")
	}
	if !bytes.HasPrefix(outputs[0], []byte("{\n  \"Repo\"")) {
		t.Fatalf("unexpected JSON shape: %.80s", outputs[0])
	}
}

func TestWriteSummary(t *testing.T) {
	rc, err := Build(context.Background(), testOptions(t, fakeGitHub(t), ""))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	rc.WriteSummary(&buf)
	out := buf.String()
	for _, want := range []string{"octo/hello#9", "alice", "main.go", "skip: default exclude", "3 files total"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestBuildInvalidRepo(t *testing.T) {
	opts := testOptions(t, fakeGitHub(t), "")
	opts.Repo = "not-a-repo"
	if _, err := Build(context.Background(), opts); err == nil {
		t.Fatal("want error for invalid repo, got nil")
	}
}

func TestBuildBadConfig(t *testing.T) {
	if _, err := Build(context.Background(), testOptions(t, fakeGitHub(t), "review:\n  max_comments: 999\n")); err == nil {
		t.Fatal("want error for invalid config, got nil")
	}
}

func TestBuildMissingToken(t *testing.T) {
	opts := testOptions(t, fakeGitHub(t), "")
	opts.Token = ""
	_, err := Build(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want clear token error, got: %v", err)
	}
}
