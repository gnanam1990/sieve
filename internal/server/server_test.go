package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHub is a PR-agnostic mock of the GitHub App + REST surface the daemon
// touches: it mints installation tokens and serves just enough of the review
// endpoints for a --post run to complete.
type fakeHub struct {
	t          *testing.T
	diff       []byte
	sieveYML   string // if set, served as the repo's .sieve.yml at head
	mu         sync.Mutex
	tokenCalls int32
	walkBody   string
	creates    int32
	reviews    int32
}

func (h *fakeHub) server() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(h.handle))
	h.t.Cleanup(srv.Close)
	return srv
}

func (h *fakeHub) handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.Contains(p, "/access_tokens"):
		atomic.AddInt32(&h.tokenCalls, 1)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_x","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/graphql"):
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/files"):
		fmt.Fprint(w, `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`)
	case r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "diff"):
		w.Write(h.diff) //nolint:errcheck
	case r.Method == http.MethodGet && strings.Contains(p, "/contents/.sieve.yml") && h.sieveYML != "":
		enc := base64.StdEncoding.EncodeToString([]byte(h.sieveYML))
		fmt.Fprintf(w, `{"encoding":"base64","content":%q}`, enc)
	case r.Method == http.MethodGet && strings.Contains(p, "/contents/"):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == http.MethodGet && strings.Contains(p, "/issues/") && strings.HasSuffix(p, "/comments"):
		if h.walkBody == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"id":1,"body":%q,"user":{"login":"sieve"}}]`, h.walkBody)
	case r.Method == http.MethodPost && strings.Contains(p, "/issues/") && strings.HasSuffix(p, "/comments"):
		atomic.AddInt32(&h.creates, 1)
		var m struct {
			Body string `json:"body"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		h.walkBody = m.Body
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/reviews"):
		atomic.AddInt32(&h.reviews, 1)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodGet:
		fmt.Fprint(w, `{"number":7,"title":"Spell out key line numbers","body":"body","state":"open","draft":false,
			"user":{"login":"alice"},"base":{"ref":"main","sha":"base777"},"head":{"ref":"feat","sha":"head888"}}`)
	default:
		h.t.Errorf("unexpected %s %s", r.Method, p)
	}
}

func readDiff() ([]byte, error) {
	return os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
}

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func prWebhook(t *testing.T, repo string, num int, sha string, install int64) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action":       "opened",
		"number":       num,
		"pull_request": map[string]any{"draft": false, "head": map[string]any{"sha": sha}},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": install},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestServerE2E: a signed pull_request webhook drives the daemon end-to-end —
// mint an App token, run the review, post the walkthrough.
func TestServerE2E(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // isolate the outcome store
	t.Setenv("SIEVE_TEST_WH_SECRET", "whsecret")

	diff, err := readDiff()
	if err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{t: t, diff: diff}
	srv := hub.server()

	fixture, err := filepath.Abs("testdata/findings.json")
	if err != nil {
		t.Fatal(err)
	}
	scYAML := fmt.Sprintf(`app:
  id: 999
  private_key_path: %s
webhook_secret_env: SIEVE_TEST_WH_SECRET
data_dir: %s
workers: 1
review:
  pipeline: single
  roles: { reviewer: default }
providers:
  default:
    type: fake
    fixture: %q
`, writeKey(t, 0o600), t.TempDir(), fixture)

	sc, err := LoadConfigFromBytes([]byte(scYAML))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{APIBaseURL: srv.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	body := prWebhook(t, "octo/hello", 7, "head888", 555)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d1")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("whsecret"), body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("webhook must be accepted, got %d", w.Code)
	}

	waitFor(t, func() bool { return atomic.LoadInt32(&hub.creates) == 1 })
	if atomic.LoadInt32(&hub.tokenCalls) == 0 {
		t.Error("App installation token was never minted")
	}
	if atomic.LoadInt32(&hub.reviews) != 1 {
		t.Errorf("expected one inline review POST, got %d", hub.reviews)
	}
	hub.mu.Lock()
	gotWalk := hub.walkBody
	hub.mu.Unlock()
	if !strings.Contains(gotWalk, "sieve review") {
		t.Errorf("walkthrough body missing expected header:\n%s", gotWalk)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestServerRejectsBadWebhook: a bad signature never enqueues a review.
func TestServerRejectsBadWebhook(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "whsecret")
	diff, _ := readDiff()
	hub := &fakeHub{t: t, diff: diff}
	srv := hub.server()
	fixture, _ := filepath.Abs("testdata/findings.json")
	scYAML := fmt.Sprintf("app: {id: 1, private_key_path: %s}\nwebhook_secret_env: SIEVE_TEST_WH_SECRET\ndata_dir: %s\nworkers: 1\nreview: {roles: {reviewer: default}}\nproviders: {default: {type: fake, fixture: %q}}\n",
		writeKey(t, 0o600), t.TempDir(), fixture)
	sc, err := LoadConfigFromBytes([]byte(scYAML))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{APIBaseURL: srv.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	body := prWebhook(t, "octo/hello", 7, "head888", 555)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("WRONG"), body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature must 401, got %d", w.Code)
	}
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&hub.tokenCalls) != 0 || atomic.LoadInt32(&hub.creates) != 0 {
		t.Error("a rejected webhook must not trigger any review")
	}
	_ = s.Shutdown()
}

// buildServer wires a daemon against the hub with a fake provider.
func buildServer(t *testing.T, hub *fakeHub) *Server {
	t.Helper()
	srv := hub.server()
	fixture, _ := filepath.Abs("testdata/findings.json")
	scYAML := fmt.Sprintf("app: {id: 999, private_key_path: %s}\nwebhook_secret_env: SIEVE_TEST_WH_SECRET\ndata_dir: %s\nworkers: 1\nreview: {pipeline: single, roles: {reviewer: default}, min_confidence: 0.1}\nproviders: {default: {type: fake, fixture: %q}}\n",
		writeKey(t, 0o600), t.TempDir(), fixture)
	sc, err := LoadConfigFromBytes([]byte(scYAML))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(sc, Options{APIBaseURL: srv.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestServerAppliesRepoConfig: a repo .sieve.yml at head is fetched and its
// review-only overrides merged over the server config (verified by the review
// still completing and posting).
func TestServerAppliesRepoConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SIEVE_TEST_WH_SECRET", "whsecret")
	diff, _ := readDiff()
	hub := &fakeHub{t: t, diff: diff, sieveYML: "review:\n  min_confidence: 0.2\n  max_inline_comments: 5\n"}
	s := buildServer(t, hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	body := prWebhook(t, "octo/hello", 7, "head888", 555)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d1")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("whsecret"), body))
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	waitFor(t, func() bool { return atomic.LoadInt32(&hub.creates) == 1 })
	_ = s.Shutdown()
}

// TestServerServeOverListener exercises the production Serve path (real
// listener) and graceful shutdown on context cancel.
func TestServerServeOverListener(t *testing.T) {
	t.Setenv("SIEVE_TEST_WH_SECRET", "whsecret")
	diff, _ := readDiff()
	hub := &fakeHub{t: t, diff: diff}
	s := buildServer(t, hub)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s.sc.Listen = addr
	s.httpSrv.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	// Poll /healthz until the listener is up.
	var up bool
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close() //nolint:errcheck
			up = resp.StatusCode == http.StatusOK
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !up {
		cancel()
		<-done
		t.Fatal("healthz never came up")
	}
	cancel() // trigger graceful shutdown
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}

func TestCheckAccessors(t *testing.T) {
	c := Check{label: "x", err: nil}
	if c.Label() != "x" || c.Err() != nil {
		t.Fatalf("accessors wrong: %q %v", c.Label(), c.Err())
	}
}

// TestMetricsEndpoint verifies /metrics exposes queue + review counters after a
// webhook-driven review.
func TestMetricsEndpoint(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SIEVE_TEST_WH_SECRET", "whsecret")
	diff, _ := readDiff()
	hub := &fakeHub{t: t, diff: diff}
	s := buildServer(t, hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	body := prWebhook(t, "octo/hello", 7, "head888", 555)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d2")
	req.Header.Set("X-Hub-Signature-256", sign([]byte("whsecret"), body))
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	waitFor(t, func() bool { return atomic.LoadInt32(&hub.creates) == 1 })

	// Wait for the review to finish and the counter to be recorded; under -race
	// and -shuffle the /metrics scrape can race with runReview's final counter
	// increment, so poll the endpoint until the expected counter appears.
	var bodyStr string
	waitFor(t, func() bool {
		rec := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		s.Handler().ServeHTTP(rec, req2)
		if rec.Code != http.StatusOK {
			t.Fatalf("/metrics status %d", rec.Code)
		}
		bodyStr = rec.Body.String()
		return strings.Contains(bodyStr, "sieve_reviews_total{outcome=\"ok\",pipeline=\"single\"} 1")
	})
	for _, want := range []string{
		"# TYPE sieve_queue_depth gauge",
		"sieve_workers 1",
		"sieve_reviews_total{outcome=\"ok\",pipeline=\"single\"} 1",
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("missing %q in /metrics:\n%s", want, bodyStr)
		}
	}
	_ = s.Shutdown()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
