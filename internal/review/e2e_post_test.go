package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fakeHub is a stateful in-memory GitHub for the offline --post E2E: it serves
// the stage-1 fixture for reads and records/stores the writes so a second run
// observes the first run's walkthrough.
type fakeHub struct {
	t    *testing.T
	mu   sync.Mutex
	diff []byte

	walkthroughBody string // "" until created
	walkthroughID   int64
	nextID          int64

	creates         int
	edits           int
	reviews         int
	inlineTotal     int
	individual      int
	lastWalkthrough string
}

func newFakeHub(t *testing.T) *fakeHub {
	diffData, err := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeHub{t: t, diff: diffData, nextID: 1000}
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
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/graphql"):
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls/7/comments"):
		fmt.Fprint(w, `[]`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/files"):
		fmt.Fprint(w, `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`)
	case r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "diff"):
		w.Write(h.diff) //nolint:errcheck
	case r.Method == http.MethodGet && strings.Contains(p, "/contents/"):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/comments") && strings.Contains(p, "/issues/"):
		// Return the stored walkthrough (or none).
		if h.walkthroughBody == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"id":%d,"body":%q,"user":{"login":"sieve"}}]`, h.walkthroughID, h.walkthroughBody)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/comments") && strings.Contains(p, "/issues/"):
		// Create walkthrough.
		h.creates++
		h.walkthroughID = h.nextID
		h.nextID++
		h.walkthroughBody = bodyField(r)
		h.lastWalkthrough = h.walkthroughBody
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d}`, h.walkthroughID)
	case r.Method == http.MethodPatch && strings.Contains(p, "/issues/comments/"):
		// Edit walkthrough in place.
		h.edits++
		h.walkthroughBody = bodyField(r)
		h.lastWalkthrough = h.walkthroughBody
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/reviews"):
		h.reviews++
		var payload struct {
			Comments []json.RawMessage `json:"comments"`
		}
		_ = json.Unmarshal([]byte(readAll(r)), &payload)
		h.inlineTotal += len(payload.Comments)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls/7/comments"):
		h.individual++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodGet:
		fmt.Fprint(w, `{"number":7,"title":"Spell out key line numbers","body":"Fixture PR body.","state":"open","draft":false,
			"user":{"login":"alice"},"base":{"ref":"main","sha":"base777"},"head":{"ref":"feat","sha":"head888"}}`)
	default:
		h.t.Errorf("unexpected %s %s", r.Method, p)
	}
}

func readAll(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func bodyField(r *http.Request) string {
	var m struct {
		Body string `json:"body"`
	}
	_ = json.Unmarshal([]byte(readAll(r)), &m)
	return m.Body
}

func postOptions(t *testing.T, srvURL, fixture string) Options {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // isolate the outcome store from the real data dir
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", abs)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		Repo:       "octo/hello",
		PRNumber:   7,
		Token:      "test-token",
		ConfigPath: cfgPath,
		Post:       true,
		APIBaseURL: srvURL,
		Now:        func() string { return "2026-07-06T00:00:00Z" }, // deterministic meta
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestPostIdempotencyAndResolved is Gate 3: run --post three times against a
// stateful fake GitHub and assert the invariants.
func TestPostIdempotencyAndResolved(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.server()

	// --- Run 1: first review. Creates the walkthrough, posts 1 inline. ---
	rc1, err := Run(context.Background(), postOptions(t, srv.URL, "testdata/fake_findings.json"))
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if hub.creates != 1 || hub.edits != 0 {
		t.Fatalf("run 1 should create the walkthrough once: creates=%d edits=%d", hub.creates, hub.edits)
	}
	if hub.reviews != 1 || hub.inlineTotal != 1 {
		t.Fatalf("run 1 should post one inline comment: reviews=%d inline=%d", hub.reviews, hub.inlineTotal)
	}
	if rc1.Gate.Stats.InlineCount != 1 || len(rc1.Gate.Resolved) != 0 {
		t.Fatalf("run 1 gate wrong: %+v", rc1.Gate.Stats)
	}
	run1Walkthrough := hub.lastWalkthrough
	goldenCompareE2E(t, "testdata/walkthrough_run1.golden.md", run1Walkthrough)

	// --- Run 2: identical head SHA. Edits in place, ZERO new inline. ---
	rc2, err := Run(context.Background(), postOptions(t, srv.URL, "testdata/fake_findings.json"))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if hub.creates != 1 || hub.edits != 1 {
		t.Fatalf("run 2 must edit, not create: creates=%d edits=%d", hub.creates, hub.edits)
	}
	if hub.inlineTotal != 1 { // unchanged — no new inline comment
		t.Fatalf("run 2 must post ZERO new inline comments, inlineTotal=%d", hub.inlineTotal)
	}
	if hub.reviews != 1 { // no second review submission (all repeated => empty)
		t.Fatalf("run 2 must not submit a second review, reviews=%d", hub.reviews)
	}
	if rc2.Gate.Stats.RepeatedInline != 1 || len(rc2.Gate.Resolved) != 0 {
		t.Fatalf("run 2 gate: want repeated inline, empty resolved; got %+v resolved=%d", rc2.Gate.Stats, len(rc2.Gate.Resolved))
	}

	// --- Run 3: the finding is fixed (no findings). It resolves; no re-post. ---
	rc3, err := Run(context.Background(), postOptions(t, srv.URL, "testdata/no_findings.json"))
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if hub.creates != 1 || hub.edits != 2 {
		t.Fatalf("run 3 must edit again: creates=%d edits=%d", hub.creates, hub.edits)
	}
	if hub.inlineTotal != 1 || hub.reviews != 1 {
		t.Fatalf("run 3 must not re-post inline: inlineTotal=%d reviews=%d", hub.inlineTotal, hub.reviews)
	}
	if len(rc3.Gate.Resolved) != 1 {
		t.Fatalf("run 3 must resolve the fixed finding, got %d resolved", len(rc3.Gate.Resolved))
	}
	if rc3.Gate.Resolved[0].Path != "alpha.txt" {
		t.Fatalf("resolved path wrong: %+v", rc3.Gate.Resolved[0])
	}
	goldenCompareE2E(t, "testdata/walkthrough_run3_resolved.golden.md", hub.lastWalkthrough)
}

func goldenCompareE2E(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden (run `make golden`): %v", err)
	}
	if d := cmp.Diff(string(want), got); d != "" {
		t.Errorf("golden mismatch %s (-want +got):\n%s", path, d)
	}
}
