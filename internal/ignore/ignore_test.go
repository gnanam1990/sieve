package ignore

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseEmpty(t *testing.T) {
	rs, err := Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if !rs.IsEmpty() {
		t.Fatalf("want empty, got %+v", rs)
	}
}

func TestParseFullRule(t *testing.T) {
	data := []byte(`ignore:
  - fingerprint: abcdef0123456789
    path: "vendor/**"
    category: style
    severity: nit
    title: generated
    reason: generated protobuf
    expires: 2026-08-01
`)
	rs, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rs.Rules))
	}
	r := rs.Rules[0]
	if r.Fingerprint != "abcdef0123456789" || r.Path != "vendor/**" || r.Category != "style" || r.Severity != "nit" {
		t.Fatalf("fields wrong: %+v", r)
	}
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !r.Expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", r.Expires, want)
	}
}

func TestParseRejectsBareRule(t *testing.T) {
	_, err := Parse([]byte(`ignore:
  - reason: no matcher
`))
	if err == nil {
		t.Fatal("expected error for rule with no matcher")
	}
}

func TestParseRejectsBadExpires(t *testing.T) {
	_, err := Parse([]byte(`ignore:
  - category: style
    expires: not-a-date
`))
	if err == nil {
		t.Fatal("expected error for bad expires")
	}
}

func TestMatchFingerprint(t *testing.T) {
	rs := Rules{Rules: []Rule{{Fingerprint: "deadbeefdeadbeef"}}}
	if !rs.Match("deadbeefdeadbeef", "a.go", "bug", "major", "x") {
		t.Fatal("exact fingerprint should match")
	}
	if rs.Match("otherfingerprint00", "a.go", "bug", "major", "x") {
		t.Fatal("different fingerprint should not match")
	}
}

func TestMatchCategoryAndSeverity(t *testing.T) {
	rs := Rules{Rules: []Rule{{Category: "style", Severity: "nit"}}}
	if !rs.Match("fp", "a.go", "style", "nit", "x") {
		t.Fatal("style+nit should match")
	}
	if rs.Match("fp", "a.go", "style", "major", "x") {
		t.Fatal("major should not match")
	}
	if rs.Match("fp", "a.go", "bug", "nit", "x") {
		t.Fatal("bug should not match")
	}
}

func TestMatchTitleSubstring(t *testing.T) {
	rs := Rules{Rules: []Rule{{Title: "unused"}}}
	if !rs.Match("fp", "a.go", "style", "nit", "Unused import in foo") {
		t.Fatal("title substring should match")
	}
	if rs.Match("fp", "a.go", "style", "nit", "Missing error check") {
		t.Fatal("title substring should not match")
	}
}

func TestMatchPathGlob(t *testing.T) {
	rs := Rules{Rules: []Rule{{Path: "vendor/**"}}}
	if !rs.Match("fp", "vendor/lib.go", "bug", "major", "x") {
		t.Fatal("vendor/lib.go should match")
	}
	if !rs.Match("fp", "vendor/sub/pkg.go", "bug", "major", "x") {
		t.Fatal("vendor/sub/pkg.go should match")
	}
	if rs.Match("fp", "internal/vendor.go", "bug", "major", "x") {
		t.Fatal("internal/vendor.go should not match")
	}
}

func TestMatchPathGlobStarExtension(t *testing.T) {
	rs := Rules{Rules: []Rule{{Path: "**/*.pb.go"}}}
	if !rs.Match("fp", "api/foo.pb.go", "style", "nit", "x") {
		t.Fatal("api/foo.pb.go should match")
	}
	if rs.Match("fp", "api/foo.go", "style", "nit", "x") {
		t.Fatal("api/foo.go should not match")
	}
}

func TestMatchPathExact(t *testing.T) {
	rs := Rules{Rules: []Rule{{Path: "cmd/sieve/main.go"}}}
	if !rs.Match("fp", "cmd/sieve/main.go", "bug", "major", "x") {
		t.Fatal("exact path should match")
	}
	if rs.Match("fp", "cmd/sieve/main_test.go", "bug", "major", "x") {
		t.Fatal("similar path should not match")
	}
}

func TestNegatedPath(t *testing.T) {
	rs := Rules{Rules: []Rule{{Path: "!vendor/**", Category: "style"}}}
	if !rs.Match("fp", "cmd/main.go", "style", "nit", "x") {
		t.Fatal("non-vendor style should match")
	}
	if rs.Match("fp", "vendor/lib.go", "style", "nit", "x") {
		t.Fatal("vendor style should not match negated rule")
	}
}

func TestActiveRemovesExpired(t *testing.T) {
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	rs := Rules{Rules: []Rule{
		{Category: "style", Expires: future},
		{Category: "bug", Expires: past},
		{Category: "perf"},
	}}
	active := rs.Active(time.Now())
	if len(active.Rules) != 2 {
		t.Fatalf("want 2 active rules, got %d", len(active.Rules))
	}
	for _, r := range active.Rules {
		if r.Category == "bug" {
			t.Fatal("expired bug rule should be removed")
		}
	}
}

func TestFilterCompact(t *testing.T) {
	rs := Rules{Rules: []Rule{{Category: "style"}}}
	cfs := []CompactFinding{
		{Fp: "a", Path: "a.go", Category: "style", Severity: "nit", Title: "x"},
		{Fp: "b", Path: "b.go", Category: "bug", Severity: "major", Title: "y"},
	}
	got := rs.FilterCompact(cfs)
	if len(got) != 1 || got[0].Fp != "b" {
		t.Fatalf("want only bug finding, got %+v", got)
	}
}

func TestFilterCompactNoRules(t *testing.T) {
	cfs := []CompactFinding{{Fp: "a"}}
	got := Rules{}.FilterCompact(cfs)
	if len(got) != 1 {
		t.Fatalf("empty rules should keep all, got %+v", got)
	}
}

func TestAddReplacesFingerprint(t *testing.T) {
	rs := Rules{Rules: []Rule{{Fingerprint: "fp1", Reason: "old"}}}
	rs, replaced := rs.Add(Rule{Fingerprint: "fp1", Reason: "new"})
	if !replaced || len(rs.Rules) != 1 || rs.Rules[0].Reason != "new" {
		t.Fatalf("replacement failed: %+v", rs)
	}
}

func TestAddAppendsNewFingerprint(t *testing.T) {
	rs := Rules{Rules: []Rule{{Fingerprint: "fp1"}}}
	rs, _ = rs.Add(Rule{Fingerprint: "fp2"})
	if len(rs.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rs.Rules))
	}
}

func TestWriteYAML(t *testing.T) {
	rs := Rules{Rules: []Rule{{
		Fingerprint: "fp1",
		Path:        "vendor/**",
		Category:    "style",
		Reason:      "generated",
		Expires:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}}
	var buf bytes.Buffer
	if err := rs.WriteYAML(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"fingerprint: fp1", "path: vendor/**", "category: style", "reason: generated", "expires:", "2026-08-01"} {
		if !strings.Contains(out, want) {
			t.Fatalf("YAML missing %q: %s", want, out)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	rs := Rules{Rules: []Rule{{
		Fingerprint: "fp1",
		Path:        "**/*.pb.go",
		Category:    "style",
		Severity:    "nit",
		Title:       "generated",
		Reason:      "protobuf",
		Expires:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}}
	var buf bytes.Buffer
	if err := rs.WriteYAML(&buf); err != nil {
		t.Fatal(err)
	}
	rs2, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs2.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rs2.Rules))
	}
	r := rs2.Rules[0]
	if r.Fingerprint != "fp1" || r.Path != "**/*.pb.go" || r.Category != "style" || r.Severity != "nit" || r.Title != "generated" || r.Reason != "protobuf" {
		t.Fatalf("round-trip fields wrong: %+v", r)
	}
}
