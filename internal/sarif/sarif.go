// Package sarif converts sieve findings into the Static Analysis Results
// Interchange Format (SARIF) v2.1.0 so they can be uploaded to GitHub's
// Security tab via github/codeql-action/upload-sarif or consumed by other
// SARIF-aware tools.
//
// It is a zero-dependency writer: we build the minimal valid SARIF document
// by hand rather than importing a schema-heavy library. Only the fields required
// by GitHub's SARIF upload validator are emitted.
package sarif

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gnanam1990/sieve/internal/gate"
)

// Report is the top-level SARIF log object.
type Report struct {
	Version string `json:"version"`
	Schema  string `json:"$schema"`
	Runs    []Run  `json:"runs"`
}

// Run is one analysis run.
type Run struct {
	Tool      Tool       `json:"tool"`
	Results   []Result   `json:"results"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

// Tool describes the analyzer.
type Tool struct {
	Driver Driver `json:"driver"`
}

// Driver is the tool metadata.
type Driver struct {
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	InformationURI string `json:"informationUri,omitempty"`
	Rules          []Rule `json:"rules,omitempty"`
}

// Rule is a finding category definition.
type Rule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	ShortDescription Desc           `json:"shortDescription"`
	FullDescription  Desc           `json:"fullDescription,omitempty"`
	Help             *Desc          `json:"help,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

// Desc is a SARIF message object.
type Desc struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

// Result is one finding.
type Result struct {
	RuleID       string            `json:"ruleId"`
	Level        string            `json:"level"`
	Message      Desc              `json:"message"`
	Locations    []Location        `json:"locations"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
	Properties   map[string]any    `json:"properties,omitempty"`
}

// Location is a physical + logical place in the code.
type Location struct {
	PhysicalLocation PhysicalLocation `json:"physicalLocation"`
}

// PhysicalLocation wraps a file region.
type PhysicalLocation struct {
	ArtifactLocation ArtifactLocation `json:"artifactLocation"`
	Region           Region           `json:"region"`
}

// ArtifactLocation points to a file.
type ArtifactLocation struct {
	URI string `json:"uri"`
}

// Region is a line (and optional end-line) range.
type Region struct {
	StartLine   int `json:"startLine"`
	EndLine     int `json:"endLine,omitempty"`
	StartColumn int `json:"startColumn,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
}

// Artifact declares files referenced by results.
type Artifact struct {
	Location ArtifactLocation `json:"location"`
}

// Options controls report generation.
type Options struct {
	Version string // sieve version, e.g. "v0.2.0"
	Repo    string // owner/name, used in artifact URIs if not absolute
	BaseSHA string // optional, stored in run automationDetails
}

// FromGate converts a GateResult into a SARIF Report containing only the
// active findings (inline + notes). Resolved findings are intentionally omitted
// because SARIF reports describe current issues, not historical ones.
func FromGate(g *gate.GateResult, opts Options) Report {
	if g == nil {
		return emptyReport(opts)
	}
	ruleIDs := map[string]Rule{}
	var results []Result
	for _, f := range g.Inline {
		results = append(results, findingToResult(f, ruleIDs))
	}
	for _, f := range g.Notes {
		results = append(results, findingToResult(f, ruleIDs))
	}

	rules := make([]Rule, 0, len(ruleIDs))
	for _, r := range ruleIDs {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })

	artifacts := collectArtifacts(results)

	return Report{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []Run{{
			Tool: Tool{Driver: Driver{
				Name:           "sieve",
				Version:        opts.Version,
				InformationURI: "https://github.com/gnanam1990/sieve",
				Rules:          rules,
			}},
			Results:   results,
			Artifacts: artifacts,
		}},
	}
}

// emptyReport returns a valid SARIF log with zero results.
func emptyReport(opts Options) Report {
	return Report{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []Run{{
			Tool: Tool{Driver: Driver{
				Name:           "sieve",
				Version:        opts.Version,
				InformationURI: "https://github.com/gnanam1990/sieve",
			}},
			Results: []Result{},
		}},
	}
}

// findingToResult converts one gated finding to a SARIF result.
func findingToResult(f gate.Finding, ruleIDs map[string]Rule) Result {
	ruleID := ruleIDFor(f.Category)
	if _, ok := ruleIDs[ruleID]; !ok {
		ruleIDs[ruleID] = Rule{
			ID:               ruleID,
			Name:             ruleID,
			ShortDescription: Desc{Text: fmt.Sprintf("%s issue", title(f.Category))},
			FullDescription:  Desc{Text: fmt.Sprintf("Findings categorized as %s", f.Category)},
			Properties: map[string]any{
				"tags": []string{f.Category},
			},
		}
	}

	endLine := f.EndLine
	if endLine == 0 {
		endLine = f.Line
	}
	// GitHub expects column 1-based; full-line markers use startColumn 1 with
	// no endColumn.
	region := Region{
		StartLine: f.Line,
		EndLine:   endLine,
	}

	result := Result{
		RuleID:  ruleID,
		Level:   levelFor(string(f.Severity)),
		Message: Desc{Text: f.Title, Markdown: fmt.Sprintf("**%s**\n\n%s", f.Title, f.Body)},
		Locations: []Location{{
			PhysicalLocation: PhysicalLocation{
				ArtifactLocation: ArtifactLocation{URI: f.Path},
				Region:           region,
			},
		}},
		Fingerprints: map[string]string{
			"sieveFingerprint": f.Fingerprint,
		},
		Properties: map[string]any{
			"confidence": f.Confidence,
			"severity":   f.Severity,
			"side":       f.Side,
			"tier":       f.Tier.String(),
			"repeated":   f.Repeated,
			"category":   f.Category,
		},
	}
	return result
}

// ruleIDFor builds a stable rule id from a category.
func ruleIDFor(category string) string {
	return "sieve/" + category
}

// levelFor maps sieve severity to SARIF level.
// SARIF levels: none, note, warning, error.
func levelFor(severity string) string {
	switch severity {
	case "critical":
		return "error"
	case "major":
		return "error"
	case "minor":
		return "warning"
	default: // nit
		return "note"
	}
}

// collectArtifacts builds the artifact list from referenced result paths.
func collectArtifacts(results []Result) []Artifact {
	seen := map[string]bool{}
	var out []Artifact
	for _, r := range results {
		for _, loc := range r.Locations {
			uri := loc.PhysicalLocation.ArtifactLocation.URI
			if !seen[uri] {
				seen[uri] = true
				out = append(out, Artifact{Location: ArtifactLocation{URI: uri}})
			}
		}
	}
	return out
}

// WriteJSON emits the SARIF report with 2-space indent and trailing newline.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// title upper-cases the first letter of s.
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
