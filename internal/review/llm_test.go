package review

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/prompt"
	"github.com/gnanam1990/sieve/internal/provider"
)

// fixtureGitHub serves a stage-1 fixture diff as the PR.
func fixtureGitHub(t *testing.T, draft bool) *httptest.Server {
	t.Helper()
	diffData, err := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			w.Write(diffData) //nolint:errcheck
		case strings.Contains(r.URL.Path, "/contents/"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		default:
			fmt.Fprintf(w, `{"number":7,"title":"Spell out key line numbers","body":"Fixture PR body.","state":"open","draft":%v,
				"user":{"login":"alice"},"base":{"ref":"main","sha":"base777"},"head":{"ref":"feat","sha":"head888"}}`, draft)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeProviderOptions(t *testing.T, srv *httptest.Server, fixture string) Options {
	t.Helper()
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
		APIBaseURL: srv.URL,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestRunEndToEndFakeGolden is the offline E2E (gate 3): fake provider
// over a stage-1 fixture must produce deterministic findings JSON. The
// canned response carries 1 valid + 1 bad-anchor + 1 bad-severity finding;
// exactly one survives the gate.
func TestRunEndToEndFakeGolden(t *testing.T) {
	rc, err := Run(context.Background(), fakeProviderOptions(t, fixtureGitHub(t, false), "testdata/fake_findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 || rc.Findings[0].Path != "alpha.txt" || rc.Findings[0].Line != 3 {
		t.Fatalf("want exactly the valid finding to survive, got %+v", rc.Findings)
	}
	if rc.Stats.FindingsTotal != 1 || rc.Stats.FindingsDropped != 2 {
		t.Fatalf("drop accounting wrong: %+v", rc.Stats)
	}
	if rc.Stats.BatchesFailed != 0 || rc.Stats.Requests != 1 {
		t.Fatalf("stats wrong: %+v", rc.Stats)
	}
	if rc.Stats.InputTokens == 0 || rc.Stats.OutputTokens == 0 {
		t.Fatalf("usage not aggregated: %+v", rc.Stats)
	}

	var buf bytes.Buffer
	if err := rc.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	goldenPath := "testdata/e2e_fake.golden.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden (run `make golden`): %v", err)
	}
	if diff := cmp.Diff(string(want), buf.String()); diff != "" {
		t.Errorf("golden mismatch (-want +got):\n%s", diff)
	}
}

// TestReviewInjectsLearnings: a repository learnings file at the PR head is
// fetched and folded into the generation prompt (learningsCount > 0).
func TestReviewInjectsLearnings(t *testing.T) {
	diffData, err := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	if err != nil {
		t.Fatal(err)
	}
	learnBody := "<!-- sieve:learnings -->\n\n- Do not flag error equality comparisons *(auto — 2 signals, 2026-05)*\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(learnBody))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"alpha.txt","status":"modified"},{"filename":"beta.txt","status":"modified"}]`)
		case strings.Contains(r.URL.Path, "/contents/.sieve/learnings.md"):
			fmt.Fprintf(w, `{"encoding":"base64","content":%q}`, b64)
		case strings.Contains(r.URL.Path, "/contents/"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case strings.Contains(r.Header.Get("Accept"), "diff"):
			w.Write(diffData) //nolint:errcheck
		default:
			fmt.Fprint(w, `{"number":7,"title":"t","body":"b","state":"open","draft":false,
				"user":{"login":"alice"},"base":{"ref":"main","sha":"base777"},"head":{"ref":"feat","sha":"head888"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	rc, err := Run(context.Background(), fakeProviderOptions(t, srv, "testdata/fake_findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.learningsCount == 0 {
		t.Fatalf("learnings should have been injected, got count=%d", rc.learningsCount)
	}
}

// TestRunDraftSkipsLLM: a draft PR with review_drafts unset exits with the
// stage-1 context and zero provider traffic.
func TestRunDraftSkipsLLM(t *testing.T) {
	rc, err := Run(context.Background(), fakeProviderOptions(t, fixtureGitHub(t, true), "testdata/fake_findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Draft || len(rc.Findings) != 0 || rc.Stats.Requests != 0 {
		t.Fatalf("draft must skip the LLM pass: draft=%v findings=%d requests=%d", rc.Draft, len(rc.Findings), rc.Stats.Requests)
	}
}

// TestRunPartialFailure: a provider that never returns valid JSON fails
// the batch after the corrective retry; Run still succeeds with the batch
// counted, which maps to exit code 2 in main.
func TestRunPartialFailure(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.txt")
	if err := os.WriteFile(garbage, []byte("I could not find any issues, great PR!"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := Run(context.Background(), fakeProviderOptions(t, fixtureGitHub(t, false), garbage))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Stats.BatchesFailed != 1 || len(rc.Findings) != 0 {
		t.Fatalf("want 1 failed batch, got %+v", rc.Stats)
	}
	if rc.Stats.Requests != 2 {
		t.Fatalf("corrective retry must add a second request, got %d", rc.Stats.Requests)
	}
}

// scripted provider for white-box runBatch tests.
type scripted struct {
	texts []string
	errs  []error
	calls int
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	i := min(s.calls, len(s.texts)-1)
	s.calls++
	if s.errs != nil && s.errs[i] != nil {
		return provider.Response{}, s.errs[i]
	}
	return provider.Response{Text: s.texts[i], Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

// testProvider is the resolved provider config a runBatch call now takes.
func testProvider() config.Provider {
	return config.Provider{Model: "m", MaxTokens: 4096, TimeoutSeconds: 120}
}

func TestRunBatchCorrectiveRetrySucceeds(t *testing.T) {
	p := &scripted{texts: []string{
		"Sure! Here are my findings in prose form.",
		`{"findings":[]}`,
	}}
	r := runBatch(context.Background(), p, "sys", prompt.Batch{User: "u"}, testProvider(), time.Minute)
	if r.err != nil {
		t.Fatalf("corrective retry should succeed: %v", r.err)
	}
	if r.requests != 2 || p.calls != 2 {
		t.Fatalf("requests=%d calls=%d, want 2/2", r.requests, p.calls)
	}
	if r.usage.InputTokens != 20 || r.usage.OutputTokens != 10 {
		t.Fatalf("usage must accumulate across the retry: %+v", r.usage)
	}
}

func TestRunBatchCorrectiveRetryAppendsNote(t *testing.T) {
	var secondUser string
	p := &recordingProvider{onSecond: func(req provider.Request) { secondUser = req.User }}
	runBatch(context.Background(), p, "sys", prompt.Batch{User: "original"}, testProvider(), time.Minute)
	if !strings.HasPrefix(secondUser, "original") || !strings.Contains(secondUser, "not valid JSON") {
		t.Fatalf("corrective note must be appended to the original prompt, got %q", secondUser)
	}
}

type recordingProvider struct {
	calls    int
	onSecond func(provider.Request)
}

func (r *recordingProvider) Name() string { return "recording" }

func (r *recordingProvider) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	r.calls++
	if r.calls == 2 && r.onSecond != nil {
		r.onSecond(req)
	}
	return provider.Response{Text: "not json"}, nil
}

func TestRunBatchProviderError(t *testing.T) {
	p := &scripted{texts: []string{""}, errs: []error{errors.New("boom")}}
	r := runBatch(context.Background(), p, "sys", prompt.Batch{User: "u"}, testProvider(), time.Minute)
	if r.err == nil || r.requests != 1 {
		t.Fatalf("provider error must fail the batch without corrective retry: err=%v requests=%d", r.err, r.requests)
	}
}

func TestNewProviderFrom(t *testing.T) {
	opts := Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Setenv("SIEVE_TEST_KEY", "sk-test")
	cases := []struct {
		name    string
		p       config.Provider
		wantErr bool
	}{
		{"anthropic with key", config.Provider{Type: "anthropic", Model: "m", APIKeyEnv: "SIEVE_TEST_KEY"}, false},
		{"openai-compat with key", config.Provider{Type: "openai-compat", Model: "m", APIKeyEnv: "SIEVE_TEST_KEY", BaseURL: "https://api.example.com/v1"}, false},
		{"fake", config.Provider{Type: "fake", Fixture: "x.json"}, false},
		{"missing key", config.Provider{Type: "anthropic", Model: "m", APIKeyEnv: "SIEVE_UNSET_KEY_XYZ"}, true},
		{"unknown type", config.Provider{Type: "bogus"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := newProviderFrom(c.p, opts, func() {})
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && p == nil {
				t.Fatal("expected a provider")
			}
		})
	}
}

func TestPrimaryProvider(t *testing.T) {
	single := config.Config{Providers: map[string]config.Provider{"default": {Model: "m"}}}
	single.Review.Pipeline = "single"
	single.Review.Roles.Reviewer = "default"
	if rp, err := primaryProvider(single); err != nil || rp.Model != "m" {
		t.Fatalf("single: rp=%+v err=%v", rp, err)
	}

	// Judge's primary is the first active role (the generator).
	judge := config.Config{Providers: map[string]config.Provider{"g": {Model: "gm"}, "j": {Model: "jm"}}}
	judge.Review.Pipeline = "judge"
	judge.Review.Roles.Generator = "g"
	judge.Review.Roles.Judge = "j"
	if rp, err := primaryProvider(judge); err != nil || rp.Model != "gm" {
		t.Fatalf("judge: rp=%+v err=%v", rp, err)
	}

	broken := config.Config{Providers: map[string]config.Provider{}}
	broken.Review.Pipeline = "single"
	broken.Review.Roles.Reviewer = "missing"
	if _, err := primaryProvider(broken); err == nil {
		t.Fatal("undefined role must error")
	}
}
