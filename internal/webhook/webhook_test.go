package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gnanam1990/sieve/internal/queue"
)

var testSecret = []byte("shh-webhook-secret")

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// recorder captures enqueued jobs.
type recorder struct {
	mu   sync.Mutex
	jobs []queue.Job
	err  error
}

func (r *recorder) enqueue(j queue.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.jobs = append(r.jobs, j)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

func newHandler(t *testing.T, rec *recorder, allow []string, drafts bool) *Handler {
	t.Helper()
	h, err := New(Config{
		Secret:       testSecret,
		ReposAllow:   allow,
		ReviewDrafts: drafts,
		Enqueue:      rec.enqueue,
		QueueStats:   func() (int, int) { return 3, 1 },
		Version:      "v-test",
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func post(t *testing.T, h *Handler, event, delivery string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	h.Mux().ServeHTTP(w, req)
	return w
}

func prBody(t *testing.T, action, repo string, num int, sha string, draft bool) []byte {
	t.Helper()
	m := map[string]any{
		"action":       action,
		"number":       num,
		"pull_request": map[string]any{"draft": draft, "head": map[string]any{"sha": sha}},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": 42},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWebhookEnqueuesPullRequest(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "opened", "org/repo", 7, "deadbeef", false)
	w := post(t, h, "pull_request", "d1", body, sign(testSecret, body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 enqueue, got %d", rec.count())
	}
	j := rec.jobs[0]
	if j.Repo != "org/repo" || j.PR != 7 || j.HeadSHA != "deadbeef" || j.InstallationID != 42 || j.DeliveryID != "d1" {
		t.Fatalf("job fields wrong: %+v", j)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "opened", "org/repo", 7, "sha", false)
	// Wrong secret → wrong signature.
	w := post(t, h, "pull_request", "d1", body, sign([]byte("wrong"), body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if rec.count() != 0 {
		t.Fatal("bad signature must not enqueue")
	}
	if h.RejectedCount() != 1 {
		t.Errorf("rejected counter should be 1, got %d", h.RejectedCount())
	}
	// Missing signature header is also a rejection, not a bypass.
	w = post(t, h, "pull_request", "d2", body, "")
	if w.Code != http.StatusUnauthorized || rec.count() != 0 {
		t.Fatalf("missing signature must 401, got %d", w.Code)
	}
}

func TestWebhookBodyCap(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	big := make([]byte, maxBody+10)
	for i := range big {
		big[i] = 'a'
	}
	w := post(t, h, "pull_request", "d1", big, sign(testSecret, big))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body must 413, got %d", w.Code)
	}
}

func TestWebhookPingAndUnknownAndInstallation(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	for _, ev := range []string{"ping", "star", "installation", "installation_repositories"} {
		body := []byte(`{"zen":"x","installation":{"id":1}}`)
		w := post(t, h, ev, "d-"+ev, body, sign(testSecret, body))
		if w.Code != http.StatusOK {
			t.Errorf("event %q must 200, got %d", ev, w.Code)
		}
	}
	if rec.count() != 0 {
		t.Fatal("non-PR events must not enqueue")
	}
}

func TestWebhookIgnoresNonEnqueueActions(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "closed", "org/repo", 7, "sha", false)
	w := post(t, h, "pull_request", "d1", body, sign(testSecret, body))
	if w.Code != http.StatusOK || rec.count() != 0 {
		t.Fatalf("closed action must 200 + no enqueue, got %d / %d", w.Code, rec.count())
	}
}

func TestWebhookReposAllow(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, []string{"org/*"}, false)
	in := prBody(t, "opened", "org/allowed", 1, "s", false)
	if w := post(t, h, "pull_request", "d1", in, sign(testSecret, in)); w.Code != http.StatusAccepted {
		t.Fatalf("allowed repo must enqueue, got %d", w.Code)
	}
	out := prBody(t, "opened", "other/denied", 2, "s", false)
	if w := post(t, h, "pull_request", "d2", out, sign(testSecret, out)); w.Code != http.StatusOK {
		t.Fatalf("disallowed repo must 200 + skip, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("only the allowed repo should enqueue, got %d", rec.count())
	}
}

func TestWebhookDraftPolicy(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false) // review_drafts off
	draft := prBody(t, "opened", "org/repo", 1, "s", true)
	if w := post(t, h, "pull_request", "d1", draft, sign(testSecret, draft)); w.Code != http.StatusOK {
		t.Fatalf("draft with review_drafts off must skip, got %d", w.Code)
	}
	if rec.count() != 0 {
		t.Fatal("draft must not enqueue when review_drafts is off")
	}

	rec2 := &recorder{}
	h2 := newHandler(t, rec2, nil, true) // review_drafts on
	if w := post(t, h2, "pull_request", "d2", draft, sign(testSecret, draft)); w.Code != http.StatusAccepted {
		t.Fatalf("draft with review_drafts on must enqueue, got %d", w.Code)
	}
}

func TestWebhookDedupe(t *testing.T) {
	rec := &recorder{}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "opened", "org/repo", 7, "sha", false)
	sig := sign(testSecret, body)
	if w := post(t, h, "pull_request", "same", body, sig); w.Code != http.StatusAccepted {
		t.Fatalf("first delivery want 202, got %d", w.Code)
	}
	if w := post(t, h, "pull_request", "same", body, sig); w.Code != http.StatusOK {
		t.Fatalf("redelivery want 200 drop, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("redelivery must not enqueue twice, got %d", rec.count())
	}
}

func TestWebhookDedupePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	cfg := func() Config {
		return Config{Secret: testSecret, Enqueue: rec.enqueue, Version: "v", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	h1, err := New(cfg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	body := prBody(t, "opened", "org/repo", 7, "sha", false)
	sig := sign(testSecret, body)
	post(t, h1, "pull_request", "persist-me", body, sig)
	if err := h1.Close(); err != nil {
		t.Fatal(err)
	}

	// New handler on the same dir loads the delivery log; the redelivery drops.
	h2, err := New(cfg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	w := post(t, h2, "pull_request", "persist-me", body, sig)
	if w.Code != http.StatusOK {
		t.Fatalf("redelivery after restart must drop (200), got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("delivery must dedupe across restart, enqueued %d times", rec.count())
	}
}

func TestWebhookEnqueueFailureNotRecorded(t *testing.T) {
	rec := &recorder{err: errFake}
	h := newHandler(t, rec, nil, false)
	body := prBody(t, "opened", "org/repo", 7, "sha", false)
	sig := sign(testSecret, body)
	if w := post(t, h, "pull_request", "d1", body, sig); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("enqueue failure must 503, got %d", w.Code)
	}
	// The delivery must NOT have been recorded — a retry (now succeeding) enqueues.
	rec.mu.Lock()
	rec.err = nil
	rec.mu.Unlock()
	if w := post(t, h, "pull_request", "d1", body, sig); w.Code != http.StatusAccepted {
		t.Fatalf("redelivery after failure must be reprocessed, got %d", w.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("want exactly one successful enqueue, got %d", rec.count())
	}
}

func TestHealthz(t *testing.T) {
	h := newHandler(t, &recorder{}, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("healthz want 200, got %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] != "v-test" || out["queue_depth"].(float64) != 3 || out["dead_letters"].(float64) != 1 {
		t.Fatalf("healthz payload wrong: %v", out)
	}
}

func TestAdminDisabledWhenNoSecret(t *testing.T) {
	h := newHandler(t, &recorder{}, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	h.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("admin without secret want 404, got %d", w.Code)
	}
}

func TestAdminRequiresBasicAuth(t *testing.T) {
	rec := &recorder{}
	h, err := New(Config{
		Secret:      testSecret,
		AdminSecret: []byte("admin-pass"),
		AdminStats:  func() AdminStats { return AdminStats{Running: []string{"org/repo#7"}} },
		ReposAllow:  nil,
		Enqueue:     rec.enqueue,
		QueueStats:  func() (int, int) { return 2, 1 },
		Version:     "v-test",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// No auth.
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	h.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("admin without auth want 401, got %d", w.Code)
	}

	// Wrong password.
	req2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req2.SetBasicAuth("admin", "wrong")
	w2 := httptest.NewRecorder()
	h.Mux().ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("admin with wrong pass want 401, got %d", w2.Code)
	}

	// Correct password.
	req3 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req3.SetBasicAuth("admin", "admin-pass")
	w3 := httptest.NewRecorder()
	h.Mux().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("admin with correct pass want 200, got %d", w3.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] != "v-test" || out["queue_depth"].(float64) != 2 || out["dead_letters"].(float64) != 1 {
		t.Fatalf("admin payload wrong: %v", out)
	}
	running := out["running"].([]any)
	if len(running) != 1 || running[0] != "org/repo#7" {
		t.Fatalf("running wrong: %v", running)
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	h := newHandler(t, &recorder{}, nil, false)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()
	h.Mux().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /webhook want 405, got %d", w.Code)
	}
}

func TestVerifySignatureUnit(t *testing.T) {
	body := []byte("payload")
	good := sign(testSecret, body)
	if !verifySignature(testSecret, body, good) {
		t.Error("valid signature must verify")
	}
	if verifySignature(testSecret, body, "sha256=deadbeef") {
		t.Error("wrong digest must fail")
	}
	if verifySignature(testSecret, body, "sha1=abc") {
		t.Error("wrong algo prefix must fail")
	}
	if verifySignature(testSecret, body, "sha256=nothex!!") {
		t.Error("non-hex must fail")
	}
	if verifySignature(testSecret, body, "") {
		t.Error("empty header must fail")
	}
}

var errFake = fakeErr("enqueue down")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
