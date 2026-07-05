package gh

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/diff"
)

func testClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(NewStaticTokenSource("test-token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	c.RetryBase = time.Millisecond
	return c, srv
}

func TestNewRequiresTokenSource(t *testing.T) {
	if _, err := New(nil, slog.Default()); err == nil {
		t.Fatal("want error for nil TokenSource, got nil")
	}
}

// TestStaticTokenSourceEmpty asserts the fail-fast contract survives the move
// from a bare string: an empty static token errors at resolution time.
func TestStaticTokenSourceEmpty(t *testing.T) {
	if _, err := NewStaticTokenSource("").Token(context.Background()); err == nil {
		t.Fatal("empty static token must error")
	}
	got, err := NewStaticTokenSource("tok").Token(context.Background())
	if err != nil || got != "tok" {
		t.Fatalf("got %q err %v, want tok", got, err)
	}
}

// TestMissingTokenSurfacesOnRequest verifies an empty token is reported on
// the first fetch (build no longer rejects it at construction).
func TestMissingTokenSurfacesOnRequest(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"number":1}`)
	}))
	c.Tokens = NewStaticTokenSource("")
	_, err := c.GetPR(context.Background(), "o", "r", 1)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want token error on request, got %v", err)
	}
}

func TestGetPR(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/hello/pulls/7" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("bad auth header %q", got)
		}
		fmt.Fprint(w, `{"number":7,"title":"T","body":"B","state":"open","draft":true,
			"user":{"login":"alice"},
			"base":{"ref":"main","sha":"aaa"},"head":{"ref":"feat","sha":"bbb"}}`)
	}))
	pr, err := c.GetPR(context.Background(), "octo", "hello", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "T" || pr.User.Login != "alice" || pr.Base.SHA != "aaa" || pr.Head.SHA != "bbb" || !pr.Draft {
		t.Fatalf("bad PR decode: %+v", pr)
	}
}

func TestGetDiffSmall(t *testing.T) {
	const body = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github.v3.diff" {
			t.Errorf("bad accept header %q", got)
		}
		fmt.Fprint(w, body)
	}))
	data, truncated, err := c.GetDiff(context.Background(), "o", "r", 1)
	if err != nil || truncated || string(data) != body {
		t.Fatalf("data=%q truncated=%v err=%v", data, truncated, err)
	}
}

func TestGetDiffTruncatesAtFileBoundary(t *testing.T) {
	// Build a >5MB diff of many complete file entries.
	entry := func(i int) string {
		return fmt.Sprintf("diff --git a/f%d b/f%d\n--- a/f%d\n+++ b/f%d\n@@ -1 +1 @@\n-%s\n+%s\n",
			i, i, i, i, strings.Repeat("x", 500), strings.Repeat("y", 500))
	}
	var sb strings.Builder
	for i := 0; sb.Len() <= MaxDiffBytes; i++ {
		sb.WriteString(entry(i))
	}
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, sb.String())
	}))
	data, truncated, err := c.GetDiff(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("want truncated=true")
	}
	if len(data) > MaxDiffBytes {
		t.Fatalf("kept %d bytes, cap is %d", len(data), MaxDiffBytes)
	}
	// The truncated diff must still parse: only complete file entries kept.
	files, err := diff.Parse(data)
	if err != nil {
		t.Fatalf("truncated diff no longer parses: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("truncated diff lost all files")
	}
}

func TestListFilesPagination(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n := 100
		if page == "2" {
			n = 37
		}
		var rows []string
		for i := 0; i < n; i++ {
			rows = append(rows, fmt.Sprintf(`{"filename":"p%s_f%d.go","status":"modified","additions":1,"deletions":0}`, page, i))
		}
		fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
	}))
	files, truncated, err := c.ListFiles(context.Background(), "o", "r", 1)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if len(files) != 137 {
		t.Fatalf("got %d files, want 137", len(files))
	}
}

func TestListFilesCap(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rows := make([]string, 100)
		for i := range rows {
			rows[i] = `{"filename":"f.go","status":"modified"}`
		}
		fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
	}))
	files, truncated, err := c.ListFiles(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(files) != FilesCap {
		t.Fatalf("got %d files truncated=%v, want %d files truncated=true", len(files), truncated, FilesCap)
	}
}

func TestGetContents(t *testing.T) {
	content := "package main\n\nfunc main() {}\n"
	// GitHub wraps base64 in newlines.
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	wrapped := enc[:10] + "\n" + enc[10:]
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "abc123" {
			t.Errorf("missing ref param: %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"encoding":"base64","content":%q}`, wrapped)
	}))
	data, err := c.GetContents(context.Background(), "o", "r", "main.go", "abc123")
	if err != nil || string(data) != content {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestRetryOn500ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"number":1}`)
	}))
	if _, err := c.GetPR(context.Background(), "o", "r", 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("got %d calls, want 3", calls.Load())
	}
}

func TestRetryHonorsRetryAfterOn403(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"secondary rate limit"}`)
			return
		}
		fmt.Fprint(w, `{"number":1}`)
	}))
	if _, err := c.GetPR(context.Background(), "o", "r", 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("got %d calls, want 2", calls.Load())
	}
}

func TestRetryOn429(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"number":1}`)
	}))
	if _, err := c.GetPR(context.Background(), "o", "r", 1); err != nil {
		t.Fatal(err)
	}
}

func TestNoRetryOn404(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	_, err := c.GetPR(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("error should carry GitHub's message, got: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("404 must not be retried, got %d calls", calls.Load())
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	if _, err := c.GetPR(context.Background(), "o", "r", 1); err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if calls.Load() != maxAttempts {
		t.Fatalf("got %d calls, want %d", calls.Load(), maxAttempts)
	}
}

func TestListIssueCommentsPagination(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/issues/7/comments") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		n := 100
		if page == "2" {
			n = 3
		}
		rows := make([]string, n)
		for i := range rows {
			rows[i] = fmt.Sprintf(`{"id":%d,"body":"c%s_%d","user":{"login":"bot"}}`, i, page, i)
		}
		fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
	}))
	comments, err := c.ListIssueComments(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 103 || comments[0].User.Login != "bot" {
		t.Fatalf("got %d comments (%+v)", len(comments), comments[0])
	}
}

func TestSendAppliesAuthAndVersion(t *testing.T) {
	c, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth %q", got)
		}
		if r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Error("missing api-version header")
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("default accept not applied: %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	// Method chosen by the caller (here the test stands in for internal/post).
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.BaseURL+"/x", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Send(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPRNumberFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"action":"opened","pull_request":{"number":42}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_EVENT_PATH", path)
	if n := PRNumberFromEnv(); n != 42 {
		t.Fatalf("got %d, want 42", n)
	}
	t.Setenv("GITHUB_EVENT_PATH", "")
	if n := PRNumberFromEnv(); n != 0 {
		t.Fatalf("got %d, want 0 when unset", n)
	}
}

func TestRepoFromEnv(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "octo/hello")
	if RepoFromEnv() != "octo/hello" {
		t.Fatal("GITHUB_REPOSITORY not read")
	}
}
