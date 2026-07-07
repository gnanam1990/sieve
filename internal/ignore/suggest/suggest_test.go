package suggest

import (
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/memory"
)

func TestFromEventsEmpty(t *testing.T) {
	got := FromEvents(nil, time.Now())
	if len(got) != 0 {
		t.Fatalf("want no suggestions for empty events, got %d", len(got))
	}
}

func TestFromEventsDismissedToFingerprint(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp11111111111111", Path: "pkg/foo.go", Sev: "major", Conf: 0.9, Cat: "bug", Title: "nil dereference"},
		{Type: memory.TypeDismissed, Fp: "fp11111111111111"},
	}
	sugs := FromEvents(events, time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC))
	if len(sugs) != 1 {
		t.Fatalf("want 1 suggestion, got %d", len(sugs))
	}
	s := sugs[0]
	if s.Rule.Fingerprint != "fp11111111111111" {
		t.Fatalf("want fingerprint rule, got %+v", s.Rule)
	}
	if !strings.Contains(s.Provenance, "1 dismissal") {
		t.Fatalf("provenance should mention dismissal, got %q", s.Provenance)
	}
	wantExpiry := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if !s.Rule.Expires.Equal(wantExpiry) {
		t.Fatalf("want expiry %v, got %v", wantExpiry, s.Rule.Expires)
	}
}

func TestFromEventsNegativeReaction(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp22222222222222", Path: "pkg/bar.go", Sev: "nit", Conf: 0.7, Cat: "style", Title: "unused import"},
		{Type: memory.TypeReaction, Fp: "fp22222222222222", Cid: 1, Plus: 0, Minus: 1},
	}
	sugs := FromEvents(events, time.Now())
	if len(sugs) != 1 {
		t.Fatalf("want 1 suggestion, got %d", len(sugs))
	}
	if sugs[0].Rule.Fingerprint != "fp22222222222222" {
		t.Fatalf("want fingerprint rule, got %+v", sugs[0].Rule)
	}
	if !strings.Contains(sugs[0].Provenance, "1 negative reaction") {
		t.Fatalf("provenance wrong: %q", sugs[0].Provenance)
	}
}

func TestFromEventsGroupsByDirCategory(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "aaa", Path: "pkg/gen/a.pb.go", Sev: "nit", Cat: "style", Title: "generated file lacks comment"},
		{Type: memory.TypeFinding, Fp: "bbb", Path: "pkg/gen/b.pb.go", Sev: "nit", Cat: "style", Title: "generated file has long line"},
		{Type: memory.TypeDismissed, Fp: "aaa"},
		{Type: memory.TypeDismissed, Fp: "bbb"},
	}
	sugs := FromEvents(events, time.Now())
	if len(sugs) != 1 {
		t.Fatalf("want 1 grouped suggestion, got %d", len(sugs))
	}
	s := sugs[0]
	if s.Rule.Path != "pkg/gen/**" {
		t.Fatalf("want path rule pkg/gen/**, got %q", s.Rule.Path)
	}
	if s.Rule.Category != "style" {
		t.Fatalf("want category style, got %q", s.Rule.Category)
	}
	// The first common token alphabetically is "file".
	if s.Rule.Title != "file" {
		t.Fatalf("want title substring 'file', got %q", s.Rule.Title)
	}
	if s.Rule.Fingerprint != "" {
		t.Fatalf("grouped rule should not use fingerprint, got %q", s.Rule.Fingerprint)
	}
}

func TestFromEventsMultipleGroupsAndSingleton(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "g1", Path: "pkg/gen/x.pb.go", Sev: "nit", Cat: "style", Title: "generated code"},
		{Type: memory.TypeFinding, Fp: "g2", Path: "pkg/gen/y.pb.go", Sev: "nit", Cat: "style", Title: "generated code"},
		{Type: memory.TypeFinding, Fp: "s1", Path: "cmd/main.go", Sev: "major", Cat: "bug", Title: "data race"},
		{Type: memory.TypeDismissed, Fp: "g1"},
		{Type: memory.TypeDismissed, Fp: "g2"},
		{Type: memory.TypeReaction, Fp: "s1", Cid: 5, Plus: 0, Minus: 2},
	}
	sugs := FromEvents(events, time.Now())
	if len(sugs) != 2 {
		t.Fatalf("want 2 suggestions, got %d", len(sugs))
	}
	var hasGroup, hasSingleton bool
	for _, s := range sugs {
		if s.Rule.Path == "pkg/gen/**" {
			hasGroup = true
		}
		if s.Rule.Fingerprint == "s1" {
			hasSingleton = true
		}
	}
	if !hasGroup {
		t.Fatalf("missing grouped suggestion: %+v", sugs)
	}
	if !hasSingleton {
		t.Fatalf("missing singleton fingerprint suggestion: %+v", sugs)
	}
}

func TestFromEventsNoFindingMetadataSkipped(t *testing.T) {
	// A dismissal without a matching finding event should be ignored.
	events := []memory.Event{
		{Type: memory.TypeDismissed, Fp: "orphan"},
	}
	sugs := FromEvents(events, time.Now())
	if len(sugs) != 0 {
		t.Fatalf("want no suggestions, got %d", len(sugs))
	}
}

func TestFromEventsNeutralReactionIgnored(t *testing.T) {
	events := []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp33333333333333", Path: "a.go", Sev: "major", Cat: "bug", Title: "x"},
		{Type: memory.TypeReaction, Fp: "fp33333333333333", Cid: 1, Plus: 1, Minus: 1},
	}
	sugs := FromEvents(events, time.Now())
	if len(sugs) != 0 {
		t.Fatalf("neutral reaction must not produce a suggestion, got %d", len(sugs))
	}
}

func TestCommonTitleToken(t *testing.T) {
	tests := []struct {
		titles []string
		want   string
	}{
		{[]string{"generated code lacks docs", "generated code too long"}, "code"},
		{[]string{"foo bar", "baz qux"}, ""},
		{[]string{"short word", "another short"}, "short"},
	}
	for _, tc := range tests {
		var sigs []Signal
		for _, title := range tc.titles {
			sigs = append(sigs, Signal{Title: title})
		}
		got := commonTitleToken(sigs)
		if got != tc.want {
			t.Errorf("commonTitleToken(%v) = %q, want %q", tc.titles, got, tc.want)
		}
	}
}

func TestDirOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pkg/foo.go", "pkg"},
		{"foo.go", "."},
		{"", "."},
		{"a/b/c/d.go", "a/b/c"},
	}
	for _, c := range cases {
		if got := dirOf(c.in); got != c.want {
			t.Errorf("dirOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
