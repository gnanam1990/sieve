package sarif

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
)

func TestFromGateEmpty(t *testing.T) {
	report := FromGate(nil, Options{Version: "v0.2.0"})
	if report.Version != "2.1.0" {
		t.Fatalf("version = %q, want 2.1.0", report.Version)
	}
	if len(report.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(report.Runs))
	}
	if len(report.Runs[0].Results) != 0 {
		t.Fatalf("results = %d, want 0", len(report.Runs[0].Results))
	}
	if report.Runs[0].Tool.Driver.Name != "sieve" {
		t.Fatalf("tool name = %q, want sieve", report.Runs[0].Tool.Driver.Name)
	}
}

func TestFromGateMapsFindings(t *testing.T) {
	g := &gate.GateResult{
		Inline: []gate.Finding{{
			Finding: findings.Finding{
				Path:       "cmd/main.go",
				Line:       10,
				EndLine:    12,
				Side:       findings.SideRight,
				Severity:   findings.SeverityCritical,
				Confidence: 0.95,
				Category:   "security",
				Title:      "Use constant-time comparison",
				Body:       "Timing attack risk.",
			},
			Fingerprint: "fp1",
			Tier:        gate.TierInline,
			Repeated:    false,
		}},
		Notes: []gate.Finding{{
			Finding: findings.Finding{
				Path:       "internal/util.go",
				Line:       4,
				Side:       findings.SideRight,
				Severity:   findings.SeverityNit,
				Confidence: 0.75,
				Category:   "style",
				Title:      "Rename variable",
				Body:       "i is not idiomatic here.",
			},
			Fingerprint: "fp2",
			Tier:        gate.TierNotes,
			Repeated:    true,
		}},
	}

	report := FromGate(g, Options{Version: "v0.2.0"})
	if len(report.Runs[0].Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Runs[0].Results))
	}

	res := report.Runs[0].Results[0]
	if res.RuleID != "sieve/security" {
		t.Errorf("rule id = %q, want sieve/security", res.RuleID)
	}
	if res.Level != "error" {
		t.Errorf("level = %q, want error", res.Level)
	}
	if res.Message.Text != "Use constant-time comparison" {
		t.Errorf("message text = %q", res.Message.Text)
	}
	if len(res.Locations) != 1 {
		t.Fatalf("locations = %d, want 1", len(res.Locations))
	}
	loc := res.Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "cmd/main.go" {
		t.Errorf("uri = %q", loc.ArtifactLocation.URI)
	}
	if loc.Region.StartLine != 10 || loc.Region.EndLine != 12 {
		t.Errorf("region = %+v", loc.Region)
	}
	if res.Fingerprints["sieveFingerprint"] != "fp1" {
		t.Errorf("fingerprint = %q", res.Fingerprints["sieveFingerprint"])
	}
	if v, _ := res.Properties["confidence"].(float64); v != 0.95 {
		t.Errorf("confidence property = %v", v)
	}

	note := report.Runs[0].Results[1]
	if note.Level != "note" {
		t.Errorf("note level = %q, want note", note.Level)
	}
	if v, _ := note.Properties["repeated"].(bool); !v {
		t.Errorf("repeated property = %v, want true", v)
	}

	rules := report.Runs[0].Tool.Driver.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	ids := map[string]bool{}
	for _, r := range rules {
		ids[r.ID] = true
	}
	if !ids["sieve/security"] || !ids["sieve/style"] {
		t.Errorf("unexpected rule ids: %v", ids)
	}

	arts := report.Runs[0].Artifacts
	if len(arts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(arts))
	}
}

func TestSeverityToLevel(t *testing.T) {
	cases := []struct {
		sev  findings.Severity
		want string
	}{
		{findings.SeverityCritical, "error"},
		{findings.SeverityMajor, "error"},
		{findings.SeverityMinor, "warning"},
		{findings.SeverityNit, "note"},
	}
	for _, tc := range cases {
		if got := levelFor(string(tc.sev)); got != tc.want {
			t.Errorf("levelFor(%s) = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	report := FromGate(&gate.GateResult{}, Options{Version: "v0.2.0"})
	var buf bytes.Buffer
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("write: %v", err)
	}

	var parsed Report
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if parsed.Version != "2.1.0" {
		t.Errorf("parsed version = %q", parsed.Version)
	}
}

// TestGolden verifies the SARIF JSON shape against a checked-in golden file.
// Set UPDATE=true to regenerate it.
func TestGolden(t *testing.T) {
	g := &gate.GateResult{
		Inline: []gate.Finding{{
			Finding: findings.Finding{
				Path:       "main.go",
				Line:       7,
				EndLine:    9,
				Side:       findings.SideRight,
				Severity:   findings.SeverityMajor,
				Confidence: 0.88,
				Category:   "bug",
				Title:      "Unchecked error from scanner",
				Body:       "scanner.Err() is ignored.",
			},
			Fingerprint: "abc123",
			Tier:        gate.TierInline,
		}},
		Notes: []gate.Finding{{
			Finding: findings.Finding{
				Path:       "main.go",
				Line:       12,
				Side:       findings.SideRight,
				Severity:   findings.SeverityNit,
				Confidence: 0.65,
				Category:   "style",
				Title:      "Short variable name",
				Body:       "Use a clearer name.",
			},
			Fingerprint: "def456",
			Tier:        gate.TierNotes,
			Repeated:    true,
		}},
	}
	report := FromGate(g, Options{Version: "v0.2.0"})

	var got bytes.Buffer
	if err := WriteJSON(&got, report); err != nil {
		t.Fatalf("write: %v", err)
	}

	golden := filepath.Join("testdata", "golden.json")
	if os.Getenv("UPDATE") == "true" {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(golden) //nolint:gosec // testdata path
	if err != nil {
		t.Fatalf("read golden: %v (run UPDATE=true to create it)", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got.String(), string(want))
	}
}
