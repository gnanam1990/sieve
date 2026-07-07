// Package ignore loads and matches repository-level suppression rules so sieve
// can drop findings a maintainer has already reviewed and accepted.
//
// Rules live in .sieve/ignore.yml (repo-controlled, like .sieve.yml). A rule
// matches when every non-empty field it specifies matches a finding. Empty fields
// are wildcards. The supported fields are:
//
//   - fingerprint: exact 16-char sieve fingerprint
//   - path:        glob pattern with * and ** (e.g. "vendor/**" or "**/*.pb.go")
//   - category:    exact finding category (bug|security|perf|correctness|test|style)
//   - severity:    exact severity (critical|major|minor|nit)
//   - title:       substring match against the finding title
//   - reason:      human note (not used for matching)
//   - expires:     YYYY-MM-DD after which the rule is inactive
//
// The file is intentionally separate from .sieve.yml so suppression history can
// be committed and reviewed independently.
package ignore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultFile is the default ignore-rule file path, relative to the repo root.
const DefaultFile = ".sieve/ignore.yml"

// Marker delimits a hand-written preamble from the machine-managed rule list.
// Anything above the marker is preserved by `sieve ignore`; everything below is
// rewritten.
const Marker = "# sieve:ignore"

// Rule matches one class of findings. All non-empty fields must match.
type Rule struct {
	Fingerprint string    `yaml:"fingerprint,omitempty"`
	Path        string    `yaml:"path,omitempty"`
	Category    string    `yaml:"category,omitempty"`
	Severity    string    `yaml:"severity,omitempty"`
	Title       string    `yaml:"title,omitempty"`
	Reason      string    `yaml:"reason,omitempty"`
	Expires     time.Time `yaml:"-"`
}

// UnmarshalYAML decodes a rule and parses expires as YYYY-MM-DD.
func (r *Rule) UnmarshalYAML(n *yaml.Node) error {
	type raw struct {
		Fingerprint string `yaml:"fingerprint,omitempty"`
		Path        string `yaml:"path,omitempty"`
		Category    string `yaml:"category,omitempty"`
		Severity    string `yaml:"severity,omitempty"`
		Title       string `yaml:"title,omitempty"`
		Reason      string `yaml:"reason,omitempty"`
		Expires     string `yaml:"expires,omitempty"`
	}
	var v raw
	if err := n.Decode(&v); err != nil {
		return err
	}
	*r = Rule{
		Fingerprint: v.Fingerprint,
		Path:        v.Path,
		Category:    v.Category,
		Severity:    v.Severity,
		Title:       v.Title,
		Reason:      v.Reason,
	}
	if err := r.normalize(); err != nil {
		return err
	}
	if v.Expires != "" {
		t, err := time.Parse("2006-01-02", v.Expires)
		if err != nil {
			return fmt.Errorf("invalid expires %q: %w", v.Expires, err)
		}
		r.Expires = t
	}
	return nil
}

// Rules is a loaded rule set.
type Rules struct {
	Rules []Rule
}

// Fetcher resolves a repository-relative file path to its contents. Missing files
// are reported as (nil, nil) so callers can treat them as an empty rule set.
type Fetcher interface {
	Fetch(ctx context.Context, path string) ([]byte, error)
}

// IsEmpty reports whether the rule set has no active rules.
func (rs Rules) IsEmpty() bool { return len(rs.Rules) == 0 }

// Match reports whether a finding matches any active rule.
func (rs Rules) Match(fp, path, category, severity, title string) bool {
	for _, r := range rs.Rules {
		if r.match(fp, path, category, severity, title) {
			return true
		}
	}
	return false
}

func (r Rule) match(fp, path, category, severity, title string) bool {
	if r.Fingerprint != "" && r.Fingerprint != fp {
		return false
	}
	if r.Path != "" && !matchGlob(r.Path, path) {
		return false
	}
	if r.Category != "" && r.Category != category {
		return false
	}
	if r.Severity != "" && r.Severity != severity {
		return false
	}
	if r.Title != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(r.Title)) {
		return false
	}
	return true
}

// Active returns a copy of rs with expired rules removed, evaluated at now.
func (rs Rules) Active(now time.Time) Rules {
	var out Rules
	for _, r := range rs.Rules {
		if !r.Expires.IsZero() && r.Expires.Before(now) {
			continue
		}
		out.Rules = append(out.Rules, r)
	}
	return out
}

// Load fetches DefaultFile and parses it. A missing file yields an empty rule set.
func Load(ctx context.Context, fetcher Fetcher) (Rules, error) {
	data, err := fetcher.Fetch(ctx, DefaultFile)
	if err != nil {
		return Rules{}, err
	}
	if len(data) == 0 {
		return Rules{}, nil
	}
	return Parse(data)
}

// Parse decodes YAML rule data. The top-level key must be "ignore".
func Parse(data []byte) (Rules, error) {
	var wrapper struct {
		Ignore []Rule `yaml:"ignore"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return Rules{}, fmt.Errorf("parse %s: %w", DefaultFile, err)
	}
	for i := range wrapper.Ignore {
		if err := wrapper.Ignore[i].normalize(); err != nil {
			return Rules{}, fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return Rules{Rules: wrapper.Ignore}, nil
}

func (r *Rule) normalize() error {
	r.Fingerprint = strings.TrimSpace(r.Fingerprint)
	r.Path = strings.TrimSpace(r.Path)
	r.Category = strings.TrimSpace(r.Category)
	r.Severity = strings.TrimSpace(r.Severity)
	r.Title = strings.TrimSpace(r.Title)
	r.Reason = strings.TrimSpace(r.Reason)
	if !r.hasMatcher() {
		return fmt.Errorf("rule has no matcher (need one of fingerprint, path, category, severity, title)")
	}
	return nil
}

func (r Rule) hasMatcher() bool {
	return r.Fingerprint != "" || r.Path != "" || r.Category != "" || r.Severity != "" || r.Title != ""
}

// FilterCompact drops compact findings matched by the active rule set.
func (rs Rules) FilterCompact(cfs []CompactFinding) []CompactFinding {
	if rs.IsEmpty() {
		return cfs
	}
	out := make([]CompactFinding, 0, len(cfs))
	for _, cf := range cfs {
		if !rs.Match(cf.Fp, cf.Path, cf.Category, cf.Severity, cf.Title) {
			out = append(out, cf)
		}
	}
	return out
}

// CompactFinding is the minimal shape gate uses to represent a prior finding.
// It mirrors gate.CompactFinding so ignore stays decoupled from gate internals.
type CompactFinding struct {
	Fp       string
	Path     string
	Category string
	Severity string
	Title    string
}

// Add merges a new rule into rs. If the new rule has a non-empty fingerprint and
// an existing rule with the same fingerprint exists, it replaces the old one and
// returns true. Otherwise the rule is appended.
func (rs Rules) Add(n Rule) (Rules, bool) {
	n.Fingerprint = strings.TrimSpace(n.Fingerprint)
	n.Path = strings.TrimSpace(n.Path)
	n.Category = strings.TrimSpace(n.Category)
	n.Severity = strings.TrimSpace(n.Severity)
	n.Title = strings.TrimSpace(n.Title)
	n.Reason = strings.TrimSpace(n.Reason)
	if err := n.normalize(); err != nil {
		return rs, false
	}
	if n.Fingerprint != "" {
		for i := range rs.Rules {
			if rs.Rules[i].Fingerprint == n.Fingerprint {
				rs.Rules[i] = n
				return rs, true
			}
		}
	}
	rs.Rules = append(rs.Rules, n)
	return rs, false
}

// WriteYAML emits the rule set as YAML. The output is deterministic: rules are
// ordered as stored and map keys are emitted in a fixed order.
func (rs Rules) WriteYAML(w io.Writer) error {
	type outRule struct {
		Fingerprint string `yaml:"fingerprint,omitempty"`
		Path        string `yaml:"path,omitempty"`
		Category    string `yaml:"category,omitempty"`
		Severity    string `yaml:"severity,omitempty"`
		Title       string `yaml:"title,omitempty"`
		Reason      string `yaml:"reason,omitempty"`
		Expires     string `yaml:"expires,omitempty"`
	}
	var wrapper struct {
		Ignore []outRule `yaml:"ignore"`
	}
	for _, r := range rs.Rules {
		var exp string
		if !r.Expires.IsZero() {
			exp = r.Expires.Format("2006-01-02")
		}
		wrapper.Ignore = append(wrapper.Ignore, outRule{
			Fingerprint: r.Fingerprint,
			Path:        r.Path,
			Category:    r.Category,
			Severity:    r.Severity,
			Title:       r.Title,
			Reason:      r.Reason,
			Expires:     exp,
		})
	}
	b, err := yaml.Marshal(&wrapper)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// matchGlob implements path globbing with * (one segment, any chars) and **
// (any number of segments). Patterns always use forward slashes.
func matchGlob(pattern, s string) bool {
	if strings.HasPrefix(pattern, "!") {
		return !matchGlob(strings.TrimPrefix(pattern, "!"), s)
	}
	return matchSegments(splitPath(pattern), splitPath(s))
}

func splitPath(s string) []string {
	return strings.Split(strings.Trim(s, "/"), "/")
}

func matchSegments(pat, str []string) bool {
	if len(pat) == 0 {
		return len(str) == 0
	}
	if pat[0] == "**" {
		// ** can match zero or more segments.
		for i := 0; i <= len(str); i++ {
			if matchSegments(pat[1:], str[i:]) {
				return true
			}
		}
		return false
	}
	if len(str) == 0 {
		return false
	}
	if !matchSegment(pat[0], str[0]) {
		return false
	}
	return matchSegments(pat[1:], str[1:])
}

func matchSegment(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	// Fast path: exact match.
	if pattern == segment {
		return true
	}
	// Slow path: * within a segment only.
	pi, si := 0, 0
	for pi < len(pattern) && si < len(segment) {
		if pattern[pi] == '*' {
			// * matches any run of characters; advance pattern and try each suffix.
			for end := si; end <= len(segment); end++ {
				if matchSegment(pattern[pi+1:], segment[end:]) {
					return true
				}
			}
			return false
		}
		if pattern[pi] != segment[si] {
			return false
		}
		pi++
		si++
	}
	return pi == len(pattern) && si == len(segment)
}
