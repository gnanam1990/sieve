package post

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/render"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recorder captures every request the Poster makes.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Method string
	Path   string
	Query  string
	Body   string
}

func (r *recorder) add(req *http.Request, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, recorded{req.Method, req.URL.Path, req.URL.RawQuery, body})
}

func (r *recorder) writes() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recorded
	for _, rec := range r.reqs {
		if rec.Method != http.MethodGet {
			out = append(out, rec)
		}
	}
	return out
}

func testPoster(t *testing.T, h http.Handler) *Poster {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := gh.New(gh.NewStaticTokenSource("tok"), discard())
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	c.RetryBase = time.Millisecond
	return &Poster{Client: c, Owner: "o", Repo: "r", PR: 7, Log: discard()}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func sampleMeta() gate.Meta {
	return gate.Meta{
		Version: gate.MetaVersion,
		HeadSHA: "priorhead",
		Fps:     []gate.PriorFinding{{Fingerprint: "abc123", Path: "a.go", Severity: "major"}},
		Ts:      "2026-07-05T00:00:00Z",
	}
}

func walkthroughBody() string {
	return render.WalkthroughMarker + "\n" + render.MetaComment(sampleMeta()) + "\n## sieve review\nbody\n"
}

// --- walkthrough create vs edit ---

func TestLocateAbsentThenCreate(t *testing.T) {
	rec := &recorder{}
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r, readBody(r))
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/7/comments"):
			fmt.Fprint(w, `[]`) // no existing comments
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":555}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	loc, err := p.LocateWalkthrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loc.Found {
		t.Fatal("no walkthrough should be found")
	}
	if err := p.UpsertWalkthrough(context.Background(), loc, "NEW BODY"); err != nil {
		t.Fatal(err)
	}
	writes := rec.writes()
	if len(writes) != 1 || writes[0].Method != http.MethodPost {
		t.Fatalf("expected one POST create, got %+v", writes)
	}
	if !strings.Contains(writes[0].Body, "NEW BODY") {
		t.Errorf("create body missing: %s", writes[0].Body)
	}
}

func TestLocatePresentThenEdit(t *testing.T) {
	rec := &recorder{}
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r, readBody(r))
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/7/comments"):
			fmt.Fprintf(w, `[{"id":42,"body":%q,"user":{"login":"sieve"}}]`, walkthroughBody())
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/42"):
			fmt.Fprint(w, `{"id":42}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	loc, err := p.LocateWalkthrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Found || loc.CommentID != 42 {
		t.Fatalf("walkthrough not located: %+v", loc)
	}
	if !loc.HasMeta || loc.Meta.HeadSHA != "priorhead" || len(loc.Meta.Fps) != 1 {
		t.Fatalf("prior meta not decoded: %+v", loc.Meta)
	}
	if err := p.UpsertWalkthrough(context.Background(), loc, "EDITED BODY"); err != nil {
		t.Fatal(err)
	}
	writes := rec.writes()
	if len(writes) != 1 || writes[0].Method != http.MethodPatch || !strings.Contains(writes[0].Path, "/issues/comments/42") {
		t.Fatalf("expected one PATCH edit, got %+v", writes)
	}
}

func TestMarkerDiscoveryAcrossPages(t *testing.T) {
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `[{"id":99,"body":%q,"user":{"login":"sieve"}}]`, walkthroughBody())
			return
		}
		// Page 1: a full page of unrelated comments to force pagination.
		rows := make([]string, 100)
		for i := range rows {
			rows[i] = fmt.Sprintf(`{"id":%d,"body":"chatter","user":{"login":"human"}}`, i)
		}
		fmt.Fprint(w, "["+strings.Join(rows, ",")+"]")
	}))
	loc, err := p.LocateWalkthrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Found || loc.CommentID != 99 {
		t.Fatalf("walkthrough on page 2 not found: %+v", loc)
	}
}

func TestMultipleWalkthroughsWarnsUsesFirst(t *testing.T) {
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":10,"body":%q,"user":{"login":"sieve"}},{"id":20,"body":%q,"user":{"login":"sieve"}}]`,
			walkthroughBody(), walkthroughBody())
	}))
	loc, err := p.LocateWalkthrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Found || loc.CommentID != 10 {
		t.Fatalf("should use the first match, got %+v", loc)
	}
}

func TestLocateCorruptMetaDoesNotFail(t *testing.T) {
	body := render.WalkthroughMarker + "\n<!-- sieve:meta v1 !!!bad!!! -->\nbody\n"
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":7,"body":%q,"user":{"login":"sieve"}}]`, body)
	}))
	loc, err := p.LocateWalkthrough(context.Background())
	if err != nil {
		t.Fatalf("corrupt meta must not fail location: %v", err)
	}
	if !loc.Found || loc.HasMeta {
		t.Fatalf("expected found without usable meta: %+v", loc)
	}
}

func TestUpsertCreateErrorStatus(t *testing.T) {
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible"}`)
	}))
	loc, _ := p.LocateWalkthrough(context.Background())
	err := p.UpsertWalkthrough(context.Background(), loc, "body")
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("want surfaced GitHub error, got %v", err)
	}
}

// --- inline review submission ---

func inlineFinding(path string, line int, sev findings.Severity) gate.Finding {
	return gate.Finding{
		Finding: findings.Finding{
			Path: path, Line: line, Side: findings.SideRight,
			Severity: sev, Confidence: 0.9, Category: "bug", Title: "T " + path, Body: "why",
		},
		Tier: gate.TierInline,
	}
}

func TestBuildInlineCommentsSkipsRepeatedAndMapsRange(t *testing.T) {
	repeated := inlineFinding("a.go", 1, findings.SeverityCritical)
	repeated.Repeated = true
	rng := inlineFinding("b.go", 5, findings.SeverityMajor)
	rng.EndLine = 8
	single := inlineFinding("c.go", 12, findings.SeverityMajor)

	comments := BuildInlineComments([]gate.Finding{repeated, rng, single}, findings.NewAnchors(nil))
	if len(comments) != 2 {
		t.Fatalf("repeated finding must be skipped, got %d comments", len(comments))
	}
	// Range comment: GitHub Line is the end, StartLine the start.
	var b, c *InlineComment
	for i := range comments {
		switch comments[i].Path {
		case "b.go":
			b = &comments[i]
		case "c.go":
			c = &comments[i]
		}
	}
	if b == nil || b.Line != 8 || b.StartLine != 5 || b.StartSide != "RIGHT" {
		t.Fatalf("range mapping wrong: %+v", b)
	}
	if c == nil || c.Line != 12 || c.StartLine != 0 {
		t.Fatalf("single-line mapping wrong: %+v", c)
	}
}

func TestSubmitInlineReviewBatch(t *testing.T) {
	rec := &recorder{}
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r, readBody(r))
		if strings.HasSuffix(r.URL.Path, "/pulls/7/reviews") {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	comments := BuildInlineComments([]gate.Finding{
		inlineFinding("a.go", 1, findings.SeverityCritical),
		inlineFinding("b.go", 2, findings.SeverityMajor),
	}, findings.NewAnchors(nil))

	failed, err := p.SubmitInlineReview(context.Background(), "headSHA", comments)
	if err != nil || failed != 0 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	writes := rec.writes()
	if len(writes) != 1 {
		t.Fatalf("must be ONE review submission, got %d requests", len(writes))
	}
	var payload reviewPayload
	if err := json.Unmarshal([]byte(writes[0].Body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != "COMMENT" || payload.CommitID != "headSHA" || len(payload.Comments) != 2 {
		t.Fatalf("bad review payload: %+v", payload)
	}
}

func TestSubmitInlineReviewEmptyNoRequest(t *testing.T) {
	rec := &recorder{}
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r, readBody(r))
	}))
	failed, err := p.SubmitInlineReview(context.Background(), "h", nil)
	if err != nil || failed != 0 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	if len(rec.writes()) != 0 {
		t.Fatal("empty comment set must issue no request")
	}
}

// TestSubmitInlineReview422Fallback: the batch is rejected with 422, so each
// comment is posted individually; one of them fails, which must be counted.
func TestSubmitInlineReview422Fallback(t *testing.T) {
	rec := &recorder{}
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		rec.add(r, body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"line must be part of the diff"}`)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			// Reject the comment that targets bad.go, accept the rest.
			if strings.Contains(body, "bad.go") {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"bad line"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	comments := BuildInlineComments([]gate.Finding{
		inlineFinding("good.go", 1, findings.SeverityCritical),
		inlineFinding("bad.go", 2, findings.SeverityMajor),
		inlineFinding("good2.go", 3, findings.SeverityMajor),
	}, findings.NewAnchors(nil))

	failed, err := p.SubmitInlineReview(context.Background(), "h", comments)
	if err != nil {
		t.Fatalf("fallback should not hard-error: %v", err)
	}
	if failed != 1 {
		t.Fatalf("want 1 failed comment, got %d", failed)
	}
	writes := rec.writes()
	// 1 batch review (422) + 3 individual comment posts.
	if len(writes) != 4 {
		t.Fatalf("want 4 writes (1 batch + 3 individual), got %d", len(writes))
	}
}

func TestSubmitInlineReviewHardError(t *testing.T) {
	p := testPoster(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	comments := BuildInlineComments([]gate.Finding{inlineFinding("a.go", 1, findings.SeverityCritical)}, findings.NewAnchors(nil))
	failed, err := p.SubmitInlineReview(context.Background(), "h", comments)
	if err == nil || failed != 1 {
		t.Fatalf("hard 500 must report all failed: failed=%d err=%v", failed, err)
	}
}
