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
)

// deltaHub is a stateful fake GitHub for the delta re-review matrix. Unlike
// fakeHub it serves a settable head SHA, diff, file list, and compare response,
// and records posted inline comments so CollectCids can recover their IDs.
type deltaHub struct {
	t  *testing.T
	mu sync.Mutex

	headSHA       string
	diff          []byte
	files         string // JSON array for /files
	compareStatus string
	compareFiles  []string

	walkthroughBody string
	walkthroughID   int64
	nextID          int64
	inlineComments  []struct {
		id   int64
		body string
	}

	creates, edits, reviews, inlineTotal int
	lastWalkthrough                      string
}

func newDeltaHub(t *testing.T, diff []byte) *deltaHub {
	return &deltaHub{t: t, headSHA: "sha1", diff: diff, nextID: 1000,
		files: `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`}
}

func (h *deltaHub) server() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(h.handle))
	h.t.Cleanup(srv.Close)
	return srv
}

func (h *deltaHub) handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.Contains(p, "/compare/"):
		fmt.Fprintf(w, `{"status":%q,"files":[%s]}`, h.compareStatus, filesJSON(h.compareFiles))
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/files"):
		fmt.Fprint(w, h.files)
	case r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "diff"):
		w.Write(h.diff) //nolint:errcheck
	case r.Method == http.MethodGet && strings.Contains(p, "/contents/"):
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/comments") && strings.Contains(p, "/issues/"):
		if h.walkthroughBody == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"id":%d,"body":%q,"user":{"login":"sieve"}}]`, h.walkthroughID, h.walkthroughBody)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls/7/comments"):
		var rows []string
		for _, c := range h.inlineComments {
			rows = append(rows, fmt.Sprintf(`{"id":%d,"body":%q,"user":{"login":"sieve"}}`, c.id, c.body))
		}
		fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/comments") && strings.Contains(p, "/issues/"):
		h.creates++
		h.walkthroughID = h.nextID
		h.nextID++
		h.walkthroughBody = bodyField(r)
		h.lastWalkthrough = h.walkthroughBody
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d}`, h.walkthroughID)
	case r.Method == http.MethodPatch && strings.Contains(p, "/issues/comments/"):
		h.edits++
		h.walkthroughBody = bodyField(r)
		h.lastWalkthrough = h.walkthroughBody
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/reviews"):
		h.reviews++
		var payload struct {
			Comments []struct {
				Body string `json:"body"`
			} `json:"comments"`
		}
		_ = json.Unmarshal([]byte(readAll(r)), &payload)
		for _, c := range payload.Comments {
			h.inlineComments = append(h.inlineComments, struct {
				id   int64
				body string
			}{h.nextID, c.Body})
			h.nextID++
			h.inlineTotal++
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	case r.Method == http.MethodGet:
		fmt.Fprintf(w, `{"number":7,"title":"T","body":"b","state":"open","draft":false,
			"user":{"login":"alice"},"base":{"ref":"main","sha":"base"},"head":{"ref":"feat","sha":%q}}`, h.headSHA)
	default:
		h.t.Errorf("unexpected %s %s", r.Method, p)
	}
}

func filesJSON(files []string) string {
	var rows []string
	for _, f := range files {
		rows = append(rows, fmt.Sprintf(`{"filename":%q,"status":"modified"}`, f))
	}
	return strings.Join(rows, ",")
}

func deltaOptions(t *testing.T, srvURL, fixture string, full bool) Options {
	t.Helper()
	abs, _ := filepath.Abs(fixture)
	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", abs)), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		Repo: "octo/hello", PRNumber: 7, Token: "t", ConfigPath: cfgPath,
		Post: true, Full: full, APIBaseURL: srvURL,
		Now: func() string { return "2026-07-06T00:00:00Z" },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func writeFixture(t *testing.T, findingsJSON string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fx.json")
	if err := os.WriteFile(p, []byte(findingsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDeltaNormalPush: after a first full review, a push that changes only
// beta.txt reviews only beta.txt and carries alpha.txt's finding forward.
func TestDeltaNormalPush(t *testing.T) {
	diffData, err := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	if err != nil {
		t.Fatal(err)
	}
	hub := newDeltaHub(t, diffData)
	srv := hub.server()

	full := writeFixture(t, `{"findings":[
		{"Path":"alpha.txt","Line":3,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"alpha bug","Body":"why"},
		{"Path":"beta.txt","Line":5,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"beta bug","Body":"why"}]}`)
	delta := writeFixture(t, `{"findings":[
		{"Path":"beta.txt","Line":5,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"beta bug","Body":"why"}]}`)

	// Run 1: full review (new PR), both findings posted inline.
	hub.headSHA = "sha1"
	rc1, err := Run(context.Background(), deltaOptions(t, srv.URL, full, false))
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if rc1.Stats.FullReviewReason == "" || hub.inlineTotal != 2 {
		t.Fatalf("run1 should be a full review with 2 inlines: reason=%q inlines=%d", rc1.Stats.FullReviewReason, hub.inlineTotal)
	}

	// Run 2: push changes only beta.txt.
	hub.headSHA = "sha2"
	hub.compareStatus = "ahead"
	hub.compareFiles = []string{"beta.txt"}
	rc2, err := Run(context.Background(), deltaOptions(t, srv.URL, delta, false))
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if rc2.Stats.FullReviewReason != "" {
		t.Fatalf("run2 should be a delta, got full: %q", rc2.Stats.FullReviewReason)
	}
	if rc2.Stats.FilesDeltaReviewed != 1 {
		t.Fatalf("run2 must delta-review exactly beta.txt, got %d files", rc2.Stats.FilesDeltaReviewed)
	}
	if rc2.Stats.FindingsCarriedForward != 1 {
		t.Fatalf("alpha finding must carry forward, got %d", rc2.Stats.FindingsCarriedForward)
	}
	if rc2.Stats.TokensSaved <= 0 {
		t.Fatalf("delta must report tokens saved, got %d", rc2.Stats.TokensSaved)
	}
	// beta re-emerged (repeated, not re-posted) and alpha carried (also not
	// re-posted): no NEW inline comments beyond the original 2.
	if hub.inlineTotal != 2 {
		t.Fatalf("delta must post zero new inlines, total=%d", hub.inlineTotal)
	}
	// Both findings present in the union (1 fresh beta + 1 carried alpha).
	if got := len(rc2.Gate.Inline); got != 2 {
		t.Fatalf("union must hold both inline findings, got %d", got)
	}
	goldenCompareE2E(t, "testdata/walkthrough_delta.golden.md", hub.lastWalkthrough)
}

// TestDeltaForcePushFallback: a diverged compare (force-push/rebase) falls back
// to a full re-review.
func TestDeltaForcePushFallback(t *testing.T) {
	diffData, _ := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	hub := newDeltaHub(t, diffData)
	srv := hub.server()
	fx := writeFixture(t, `{"findings":[{"Path":"beta.txt","Line":5,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"beta bug","Body":"why"}]}`)

	hub.headSHA = "sha1"
	if _, err := Run(context.Background(), deltaOptions(t, srv.URL, fx, false)); err != nil {
		t.Fatal(err)
	}
	// Force-push: new SHA, compare diverged.
	hub.headSHA = "sha2"
	hub.compareStatus = "diverged"
	hub.compareFiles = []string{"beta.txt"}
	rc, err := Run(context.Background(), deltaOptions(t, srv.URL, fx, false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rc.Stats.FullReviewReason, "ancestor") {
		t.Fatalf("force-push must fall back to full review, reason=%q", rc.Stats.FullReviewReason)
	}
	if rc.Stats.FilesDeltaReviewed != 0 {
		t.Fatalf("full fallback must not delta-review, got %d", rc.Stats.FilesDeltaReviewed)
	}
}

// TestDeltaForceFullFlag: --full disables delta even when a clean delta is
// available.
func TestDeltaForceFullFlag(t *testing.T) {
	diffData, _ := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	hub := newDeltaHub(t, diffData)
	srv := hub.server()
	fx := writeFixture(t, `{"findings":[{"Path":"beta.txt","Line":5,"Side":"RIGHT","Severity":"major","Confidence":0.9,"Category":"bug","Title":"beta bug","Body":"why"}]}`)

	hub.headSHA = "sha1"
	if _, err := Run(context.Background(), deltaOptions(t, srv.URL, fx, false)); err != nil {
		t.Fatal(err)
	}
	hub.headSHA = "sha2"
	hub.compareStatus = "ahead"
	hub.compareFiles = []string{"beta.txt"}
	rc, err := Run(context.Background(), deltaOptions(t, srv.URL, fx, true)) // --full
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rc.Stats.FullReviewReason, "full") {
		t.Fatalf("--full must force a full review, reason=%q", rc.Stats.FullReviewReason)
	}
}
