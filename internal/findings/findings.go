// Package findings defines the review finding schema, response parsing,
// anchor validation against the parsed diff, and deterministic sorting.
//
// Anchor validation is the anti-hallucination gate: a finding that does
// not land on a commentable diff line is dropped, never repaired.
package findings

import (
	"fmt"
	"sort"
	"strings"
)

// Severity buckets, most severe first.
type Severity string

// Severity values.
const (
	SeverityCritical Severity = "critical"
	SeverityMajor    Severity = "major"
	SeverityMinor    Severity = "minor"
	SeverityNit      Severity = "nit"
)

var severityRank = map[Severity]int{
	SeverityCritical: 0,
	SeverityMajor:    1,
	SeverityMinor:    2,
	SeverityNit:      3,
}

// Side is the diff side a finding anchors to, matching the GitHub Reviews
// API vocabulary.
type Side string

// Side values.
const (
	SideRight Side = "RIGHT" // new file; Line is a NewNum
	SideLeft  Side = "LEFT"  // old file; Line is an OldNum
)

var validCategories = map[string]bool{
	"bug": true, "security": true, "perf": true,
	"correctness": true, "test": true, "style": true,
}

// MaxTitleLen bounds finding titles.
const MaxTitleLen = 120

// Finding is one review comment candidate.
type Finding struct {
	Path       string
	Line       int // NewNum for RIGHT, OldNum for LEFT
	EndLine    int `json:",omitempty"` // 0 = single line; else range end, start = Line
	Side       Side
	Severity   Severity
	Confidence float64 // 0..1, calibrated probability the finding is real and actionable
	Category   string  // bug|security|perf|correctness|test|style
	Title      string  // one line, imperative
	Body       string  // markdown: the why + the fix
	Suggestion string  `json:",omitempty"` // replacement code for the exact range (rendered stage 3+)
}

// Rank returns a severity's ordinal (0 = critical, 3 = nit); lower is more
// severe. Unknown severities rank after every known one.
func Rank(s Severity) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return len(severityRank)
}

// AtLeastAsSevere reports whether s is at least as severe as floor
// (critical > major > minor > nit).
func AtLeastAsSevere(s, floor Severity) bool {
	return Rank(s) <= Rank(floor)
}

// Sort orders findings deterministically: severity (critical first), then
// path, then line.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if severityRank[fs[i].Severity] != severityRank[fs[j].Severity] {
			return severityRank[fs[i].Severity] < severityRank[fs[j].Severity]
		}
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		return fs[i].Line < fs[j].Line
	})
}

// validateShape checks everything about a finding except its anchor.
func validateShape(f Finding) error {
	if _, ok := severityRank[f.Severity]; !ok {
		return fmt.Errorf("invalid severity %q", f.Severity)
	}
	if !validCategories[f.Category] {
		return fmt.Errorf("invalid category %q", f.Category)
	}
	if f.Side != SideRight && f.Side != SideLeft {
		return fmt.Errorf("invalid side %q", f.Side)
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return fmt.Errorf("confidence %g outside [0,1]", f.Confidence)
	}
	title := strings.TrimSpace(f.Title)
	if title == "" {
		return fmt.Errorf("empty title")
	}
	if len(f.Title) > MaxTitleLen {
		return fmt.Errorf("title exceeds %d chars", MaxTitleLen)
	}
	if f.Line < 1 {
		return fmt.Errorf("line %d < 1", f.Line)
	}
	if f.EndLine != 0 && f.EndLine < f.Line {
		return fmt.Errorf("end line %d before line %d", f.EndLine, f.Line)
	}
	return nil
}
