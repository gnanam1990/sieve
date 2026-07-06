package review

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/render"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// inlineBody renders a realistic sieve inline comment body for a fingerprint.
func inlineBody(fp, cat string, conf float64) string {
	return render.Inline(gate.Finding{
		Finding:     findings.Finding{Path: "a.go", Line: 1, Side: findings.SideRight, Severity: findings.SeverityMajor, Confidence: conf, Category: cat, Title: "t"},
		Fingerprint: fp,
	}, findings.NewAnchors(nil))
}

// TestSyncEquivalence is B1's hard requirement: delete the store, `sync` from
// GitHub, and the B2/B3 aggregates match the live store. Idempotent: syncing
// again yields the same aggregates.
func TestSyncEquivalence(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fpA, fpB, fpC := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"

	// Live store, as recordOutcomes would write it: A/B active, C fixed
	// (anchor-gone), A got a 👎, B's thread was dismissed.
	store := memory.Open("github.com", "octo", "hello", discardLog())
	store.Append(
		memory.Event{Type: memory.TypeRun, PR: 7, InTok: 500, OutTok: 20},
		memory.Event{Type: memory.TypeFinding, Fp: fpA, Path: "a.go", Sev: "major", Conf: 0.90, Cat: "bug", Tier: "inline", Cid: 100},
		memory.Event{Type: memory.TypeFinding, Fp: fpB, Path: "a.go", Sev: "major", Conf: 0.80, Cat: "bug", Tier: "inline", Cid: 101},
		memory.Event{Type: memory.TypeFinding, Fp: fpC, Path: "a.go", Sev: "major", Conf: 0.95, Cat: "bug", Tier: "inline", Cid: 102},
		memory.Event{Type: memory.TypeResolved, Fp: fpC, Path: "a.go", How: memory.ResolvedAnchorGone},
		memory.Event{Type: memory.TypeReaction, Fp: fpA, Cid: 100, Minus: 1},
		memory.Event{Type: memory.TypeDismissed, Fp: fpB, Cid: 101},
	)
	liveEvents, _, _ := store.Read()
	liveAgg := memory.Aggregate(liveEvents)

	// GitHub view: walkthrough meta lists A and B active (C resolved); the
	// inline comments are A (👎), B, C; thread B is resolved.
	meta := gate.BuildMeta("head", "ts", []gate.CompactFinding{
		{Fp: fpA, Path: "a.go", Line: 1, Side: "R", Severity: "major", Conf: 0.90, Category: "bug", Title: "t", Cid: 100, Tier: "inline"},
		{Fp: fpB, Path: "a.go", Line: 1, Side: "R", Severity: "major", Conf: 0.80, Category: "bug", Title: "t", Cid: 101, Tier: "inline"},
	}, nil, []string{fpC})
	walkthrough := render.WalkthroughMarker + "\n" + render.MetaComment(meta) + "\n## sieve review\nbody\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/graphql":
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
				{"isResolved":true,"comments":{"nodes":[{"databaseId":101,"body":%q}]}}]}}}}}`, inlineBody(fpB, "bug", 0.80))
		case r.Method == http.MethodGet && p == "/repos/octo/hello/issues/7/comments":
			fmt.Fprintf(w, `[{"id":9,"body":%q,"user":{"login":"sieve"}}]`, walkthrough)
		case r.Method == http.MethodGet && p == "/repos/octo/hello/pulls/7/comments":
			fmt.Fprintf(w, `[
				{"id":100,"path":"a.go","body":%q,"reactions":{"-1":1}},
				{"id":101,"path":"a.go","body":%q},
				{"id":102,"path":"a.go","body":%q}]`,
				inlineBody(fpA, "bug", 0.90), inlineBody(fpB, "bug", 0.80), inlineBody(fpC, "bug", 0.95))
		default:
			fmt.Fprint(w, `{"number":7}`)
		}
	}))
	defer srv.Close()

	opts := Options{Repo: "octo/hello", PRNumber: 7, Token: "t", APIBaseURL: srv.URL,
		Now: func() string { return "syncts" }, Log: discardLog()}

	if err := store.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	synced, _, _ := store.Read()
	syncedAgg := memory.Aggregate(synced)

	if d := cmp.Diff(liveAgg, syncedAgg); d != "" {
		t.Fatalf("sync aggregates differ from live (-live +sync):\n%s", d)
	}

	// Idempotent: a second sync produces the same aggregates.
	if _, err := Sync(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	synced2, _, _ := store.Read()
	if d := cmp.Diff(syncedAgg, memory.Aggregate(synced2)); d != "" {
		t.Fatalf("sync is not idempotent (-first +second):\n%s", d)
	}
}
