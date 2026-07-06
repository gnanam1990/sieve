// Package learnings turns sieve's own negative outcomes (👎 reactions and
// dismissed threads) into repository-specific review rules. It clusters
// negatives, drafts one suppressive rule per cluster via a single strong-model
// call, and merges the rules into .sieve/learnings.md under a marker — never
// committing (the maintainer reviews and commits). Rules are injected into
// future review prompts.
package learnings

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/gnanam1990/sieve/internal/memory"
)

// Marker delimits the auto-managed section of .sieve/learnings.md. Anything
// above it is the maintainer's own notes and is preserved verbatim.
const Marker = "<!-- sieve:learnings -->"

const (
	maxAutoRules     = 50  // cap on auto rules; oldest pruned
	overlapThreshold = 0.6 // normalized-title token overlap to cluster / dedupe
	minClusterSize   = 2   // a cluster needs >= this many negatives to draft a rule
	// InjectionCap bounds the learnings text added to a review prompt.
	InjectionCap = 8 << 10
	// MaxRuleLen is the hard cap on a drafted rule's length.
	MaxRuleLen = 200
)

// Negative is one negative outcome: a finding a human 👎'd or dismissed.
type Negative struct {
	Category string
	Path     string
	Title    string
}

// NegativesFromEvents extracts negative findings from an event log: findings
// whose net reaction is negative or whose thread was dismissed.
func NegativesFromEvents(events []memory.Event) []Negative {
	type meta struct{ cat, path, title string }
	byFp := map[string]meta{}
	plus := map[string]int{}
	minus := map[string]int{}
	dismissed := map[string]bool{}
	for _, e := range events {
		switch e.Type {
		case memory.TypeFinding:
			byFp[e.Fp] = meta{e.Cat, e.Path, e.Title}
		case memory.TypeReaction:
			plus[e.Fp] = e.Plus
			minus[e.Fp] = e.Minus
		case memory.TypeDismissed:
			dismissed[e.Fp] = true
		}
	}
	// Deterministic order: by fingerprint.
	fps := make([]string, 0, len(byFp))
	for fp := range byFp {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	var out []Negative
	for _, fp := range fps {
		m := byFp[fp]
		if minus[fp] > plus[fp] || dismissed[fp] {
			out = append(out, Negative{Category: m.cat, Path: m.path, Title: m.title})
		}
	}
	return out
}

// Cluster is a group of similar negatives that justifies one rule.
type Cluster struct {
	Category   string
	PathPrefix string
	Titles     []string
}

// Signals is the number of negatives backing the cluster.
func (c Cluster) Signals() int { return len(c.Titles) }

// ClusterNegatives groups negatives by (category, path-prefix, title token
// overlap >= 0.6). Only clusters with >= minClusterSize are returned, in a
// deterministic order.
func ClusterNegatives(negs []Negative) []Cluster {
	var clusters []Cluster
	tokens := make([][]string, 0)
	for _, n := range negs {
		dir := path.Dir(n.Path)
		nt := normTokens(n.Title)
		placed := false
		for i := range clusters {
			if clusters[i].Category != n.Category || clusters[i].PathPrefix != dir {
				continue
			}
			if jaccard(nt, tokens[i]) >= overlapThreshold {
				clusters[i].Titles = append(clusters[i].Titles, n.Title)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, Cluster{Category: n.Category, PathPrefix: dir, Titles: []string{n.Title}})
			tokens = append(tokens, nt)
		}
	}
	out := clusters[:0]
	for _, c := range clusters {
		if len(c.Titles) >= minClusterSize {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].PathPrefix < out[j].PathPrefix
	})
	return out
}

// Rule is one auto-managed rule with its provenance annotation.
type Rule struct {
	Text    string
	Signals int
	Date    string // YYYY-MM
}

var annotationRe = regexp.MustCompile(`\s*\*\(auto — (\d+) signals?, (\d{4}-\d{2})\)\*\s*$`)

// ParseAuto returns the existing auto rules from a learnings.md body (the lines
// below the marker), stripping annotations.
func ParseAuto(body string) []Rule {
	idx := strings.Index(body, Marker)
	if idx < 0 {
		return nil
	}
	var rules []Rule
	for _, line := range strings.Split(body[idx+len(Marker):], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		text := strings.TrimSpace(line[2:])
		r := Rule{Signals: 1}
		if m := annotationRe.FindStringSubmatch(text); m != nil {
			fmt.Sscanf(m[1], "%d", &r.Signals) //nolint:errcheck // best-effort
			r.Date = m[2]
			text = strings.TrimSpace(annotationRe.ReplaceAllString(text, ""))
		}
		r.Text = text
		rules = append(rules, r)
	}
	return rules
}

// Merge folds new rules into the existing body: it dedupes each new rule
// against the current auto rules by token overlap (>= 0.6 updates in place,
// bumping signals + date; otherwise appends), caps the auto section at 50
// (pruning oldest), and preserves everything above the marker. It returns the
// new body and whether anything changed.
func Merge(existing string, newRules []Rule, yearMonth string) (string, bool) {
	manual := ""
	if idx := strings.Index(existing, Marker); idx >= 0 {
		manual = existing[:idx]
	} else {
		manual = existing
	}
	current := ParseAuto(existing)

	changed := false
	for _, nr := range newRules {
		nrTokens := normTokens(nr.Text)
		matched := -1
		for i := range current {
			if jaccard(nrTokens, normTokens(current[i].Text)) >= overlapThreshold {
				matched = i
				break
			}
		}
		if matched >= 0 {
			current[matched].Signals += nr.Signals
			current[matched].Date = yearMonth
			current[matched].Text = nr.Text // adopt the newest phrasing
			changed = true
		} else {
			current = append(current, Rule{Text: nr.Text, Signals: nr.Signals, Date: yearMonth})
			changed = true
		}
	}
	// Cap: keep the newest maxAutoRules (prune oldest = front).
	if len(current) > maxAutoRules {
		current = current[len(current)-maxAutoRules:]
		changed = true
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(manual, "\n"))
	if strings.TrimSpace(manual) != "" {
		b.WriteString("\n\n")
	}
	b.WriteString(Marker)
	b.WriteString("\n## Repository review rules (auto-managed by sieve — edit above the marker)\n\n")
	for _, r := range current {
		fmt.Fprintf(&b, "- %s *(auto — %d signals, %s)*\n", r.Text, r.Signals, r.Date)
	}
	return b.String(), changed
}

// InjectionText renders the active rules for a review prompt, capped at
// InjectionCap bytes (truncating oldest first). Empty when there are no rules.
func InjectionText(body string) (text string, count int) {
	rules := ParseAuto(body)
	if len(rules) == 0 {
		return "", 0
	}
	// Drop oldest (front) until under the cap.
	for {
		var b strings.Builder
		b.WriteString("Repository-specific review rules — obey them; they override general judgement on what to flag:\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "- %s\n", r.Text)
		}
		if b.Len() <= InjectionCap || len(rules) <= 1 {
			s := b.String()
			if len(s) > InjectionCap { // a single oversized rule: hard-truncate
				s = s[:InjectionCap]
			}
			return s, len(rules)
		}
		rules = rules[1:]
	}
}

// normTokens lowercases and splits into a set of alphanumeric word tokens.
func normTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// jaccard is the token-set overlap |A∩B| / |A∪B|.
func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	inter := 0
	for _, y := range b {
		if set[y] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
