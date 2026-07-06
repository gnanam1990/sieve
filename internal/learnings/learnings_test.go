package learnings

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/provider"
)

func TestNegativesFromEvents(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "a", Cat: "style", Path: "x/a.go", Title: "Prefer errors.Is"},
		{Type: memory.TypeFinding, Fp: "b", Cat: "style", Path: "x/b.go", Title: "Prefer errors.Is here"},
		{Type: memory.TypeFinding, Fp: "c", Cat: "bug", Path: "y/c.go", Title: "Nil deref"},
		{Type: memory.TypeReaction, Fp: "a", Minus: 2},        // negative
		{Type: memory.TypeReaction, Fp: "c", Plus: 3, Minus: 1}, // net positive -> not negative
		{Type: memory.TypeDismissed, Fp: "b"},                 // negative
	}
	negs := NegativesFromEvents(events)
	if len(negs) != 2 {
		t.Fatalf("want 2 negatives (a,b), got %d: %+v", len(negs), negs)
	}
}

func TestClusterNegatives(t *testing.T) {
	negs := []Negative{
		{Category: "style", Path: "internal/x/a.go", Title: "Prefer errors.Is over =="},
		{Category: "style", Path: "internal/x/b.go", Title: "Prefer errors.Is over equality"},
		{Category: "style", Path: "internal/x/c.go", Title: "Prefer errors.Is comparison"},
		{Category: "bug", Path: "internal/x/d.go", Title: "Unrelated nil deref"}, // singleton -> dropped
		{Category: "style", Path: "other/e.go", Title: "Prefer errors.Is"},       // different dir -> own cluster (singleton, dropped)
	}
	clusters := ClusterNegatives(negs)
	if len(clusters) != 1 {
		t.Fatalf("want 1 cluster (>=2), got %d: %+v", len(clusters), clusters)
	}
	if clusters[0].Category != "style" || clusters[0].PathPrefix != "internal/x" || clusters[0].Signals() != 3 {
		t.Fatalf("cluster wrong: %+v", clusters[0])
	}
}

// scriptedProvider returns canned replies in order.
type scriptedProvider struct {
	replies []string
	i       int
}

func (s *scriptedProvider) Name() string { return "scripted" }
func (s *scriptedProvider) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	r := s.replies[min(s.i, len(s.replies)-1)]
	s.i++
	return provider.Response{Text: r}, nil
}

func TestDraftRuleGolden(t *testing.T) {
	c := Cluster{Category: "style", PathPrefix: "internal/x", Titles: []string{"Prefer errors.Is over ==", "Prefer errors.Is over equality"}}

	// Golden-pin the prompt.
	prompt := draftSystem + "\n\n---\n\n" + DraftPrompt(c)
	goldenCompare(t, "testdata/draft_prompt.golden.txt", prompt)

	// First reply is prose (invalid) -> corrective retry -> strict JSON.
	p := &scriptedProvider{replies: []string{
		"Sure! Here's a rule: don't flag errors.Is.",
		`{"rule": "Do not flag equality comparisons of errors in this area as a style issue"}`,
	}}
	r, err := DraftRule(context.Background(), p, 256, 0.1, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Text, "Do not flag") || r.Signals != 2 {
		t.Fatalf("drafted rule wrong: %+v", r)
	}
}

func TestValidateRuleRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		strings.Repeat("x", MaxRuleLen+1),
		"Do not flag `errors.Is` calls",       // verbatim code
		"Do not flag the issue on line 42",     // line ref
		"Do not flag this. Also do not flag that.", // two sentences
	} {
		if err := validateRule(bad); err == nil {
			t.Errorf("expected rejection for %q", bad)
		}
	}
	if err := validateRule("Do not flag error equality comparisons here as a style concern."); err != nil {
		t.Errorf("valid rule rejected: %v", err)
	}
}

func TestMergePreservesManualAndDedupes(t *testing.T) {
	existing := "# My notes\n\nDon't touch these.\n\n" + Marker +
		"\n## Repository review rules (auto-managed by sieve — edit above the marker)\n\n" +
		"- Do not flag error equality comparisons as style *(auto — 2 signals, 2026-05)*\n"

	// One new rule matching the existing (update), one novel (append).
	body, changed := Merge(existing, []Rule{
		{Text: "Do not flag error equality comparisons as a style issue", Signals: 3},
		{Text: "Do not flag missing doc comments on internal helpers", Signals: 2},
	}, "2026-07")
	if !changed {
		t.Fatal("merge should report a change")
	}
	if !strings.Contains(body, "# My notes") || !strings.Contains(body, "Don't touch these.") {
		t.Fatal("manual content above the marker must be preserved")
	}
	// Updated rule: signals bumped 2+3=5, date 2026-07.
	if !strings.Contains(body, "5 signals, 2026-07") {
		t.Fatalf("dedupe/update failed:\n%s", body)
	}
	// Novel rule appended.
	if !strings.Contains(body, "missing doc comments") {
		t.Fatal("novel rule must be appended")
	}
	if got := len(ParseAuto(body)); got != 2 {
		t.Fatalf("want 2 auto rules, got %d", got)
	}
}

func TestInjectionTruncatesOldestFirst(t *testing.T) {
	var b strings.Builder
	b.WriteString(Marker + "\n\n")
	// Many long rules to exceed the 8 KB cap.
	long := strings.Repeat("x", 300)
	for i := 0; i < 40; i++ {
		b.WriteString("- " + long + " *(auto — 1 signals, 2026-07)*\n")
	}
	text, count := InjectionText(b.String())
	if len(text) > InjectionCap {
		t.Fatalf("injection exceeds cap: %d", len(text))
	}
	if count == 0 || count == 40 {
		t.Fatalf("expected some rules dropped, got %d", count)
	}
	if !strings.Contains(text, "override general judgement") {
		t.Fatal("injection header missing")
	}
}

func TestInjectionEmptyWhenNoRules(t *testing.T) {
	text, count := InjectionText("no marker here")
	if text != "" || count != 0 {
		t.Fatalf("no rules -> empty, got %q %d", text, count)
	}
}

func goldenCompare(t *testing.T, path, got string) {
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
	if string(want) != got {
		t.Errorf("golden mismatch %s:\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
