// Package suggest turns negative review outcomes into concrete ignore-rule
// proposals for .sieve/ignore.yml. It reads the local outcome store (dismissals
// and down-votes), joins each signal with its finding metadata, and generates
// the least-broad rule that covers the signal group.
package suggest

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/memory"
)

// defaultExpiryDays is the default lifetime of a suggested rule so stale
// suppressions are reviewed again.
const defaultExpiryDays = 90

// Signal is one negative outcome backed by a finding event.
type Signal struct {
	Fp        string
	Path      string
	Category  string
	Severity  string
	Title     string
	Dismissed int
	Negative  int
}

// Suggestion is one proposed ignore rule with its provenance.
type Suggestion struct {
	Rule       ignore.Rule
	Signals    []Signal
	Provenance string
}

// FromEvents reads the local outcome store, extracts negative signals, and
// returns concrete ignore-rule suggestions ordered from most to least specific.
// The rules carry a default 90-day expiry evaluated at now.
func FromEvents(events []memory.Event, now time.Time) []Suggestion {
	byFp := latestFindingByFp(events)
	signals := collectSignals(events, byFp)
	if len(signals) == 0 {
		return nil
	}

	groups := groupByDirCategory(signals)
	expiry := defaultExpiry(now)
	var out []Suggestion
	for _, g := range groups {
		out = append(out, suggestionForGroup(g, expiry))
	}
	return out
}

// latestFindingByFp returns the most recent finding event per fingerprint.
// Dismissal/reaction events only carry the fingerprint, so we join against this
// map to recover path, category, severity, and title.
func latestFindingByFp(events []memory.Event) map[string]memory.Event {
	out := make(map[string]memory.Event)
	for _, e := range events {
		if e.Type != memory.TypeFinding || e.Fp == "" {
			continue
		}
		out[e.Fp] = e
	}
	return out
}

// collectSignals joins negative events with their finding metadata and dedupes
// by fingerprint, merging dismissal and negative-reaction counts.
func collectSignals(events []memory.Event, byFp map[string]memory.Event) []Signal {
	type agg struct {
		f         memory.Event
		dismissed int
		negative  int
	}
	m := make(map[string]*agg)

	for _, e := range events {
		if e.Fp == "" {
			continue
		}
		switch e.Type {
		case memory.TypeDismissed:
			if m[e.Fp] == nil {
				m[e.Fp] = &agg{f: byFp[e.Fp]}
			}
			m[e.Fp].dismissed++
		case memory.TypeReaction:
			if e.Minus <= e.Plus {
				continue
			}
			if m[e.Fp] == nil {
				m[e.Fp] = &agg{f: byFp[e.Fp]}
			}
			m[e.Fp].negative++
		}
	}

	fps := make([]string, 0, len(m))
	for fp := range m {
		fps = append(fps, fp)
	}
	sort.Strings(fps)

	var out []Signal
	for _, fp := range fps {
		a := m[fp]
		if a.f.Fp == "" {
			continue // no finding metadata for this fingerprint
		}
		out = append(out, Signal{
			Fp:        fp,
			Path:      a.f.Path,
			Category:  a.f.Cat,
			Severity:  a.f.Sev,
			Title:     a.f.Title,
			Dismissed: a.dismissed,
			Negative:  a.negative,
		})
	}
	return out
}

// groupByDirCategory groups signals that share a directory and category. Signals
// that do not share dir+category with any other signal become singleton groups
// (they will get a fingerprint rule).
func groupByDirCategory(signals []Signal) [][]Signal {
	key := func(s Signal) string { return dirOf(s.Path) + "\x00" + s.Category }
	byKey := make(map[string][]Signal)
	for _, s := range signals {
		byKey[key(s)] = append(byKey[key(s)], s)
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out [][]Signal
	for _, k := range keys {
		g := byKey[k]
		if len(g) == 1 {
			// Singletons each become their own fingerprint group.
			out = append(out, []Signal{g[0]})
			continue
		}
		out = append(out, g)
	}
	return out
}

// suggestionForGroup builds the least-broad rule covering the group.
//   - singleton group -> fingerprint rule
//   - multi-signal group with common title token -> path dir + category + title
//   - multi-signal group without common title token -> path dir + category
func suggestionForGroup(signals []Signal, expiry time.Time) Suggestion {
	if len(signals) == 1 {
		s := signals[0]
		return Suggestion{
			Rule: ignore.Rule{
				Fingerprint: s.Fp,
				Reason:      provenance(signals),
				Expires:     expiry,
			},
			Signals:    signals,
			Provenance: provenance(signals),
		}
	}

	d := dirOf(signals[0].Path)
	cat := signals[0].Category
	commonTitle := commonTitleToken(signals)

	rule := ignore.Rule{
		Path:    d + "/**",
		Reason:  provenance(signals),
		Expires: expiry,
	}
	if cat != "" {
		rule.Category = cat
	}
	if commonTitle != "" {
		rule.Title = commonTitle
	}

	return Suggestion{
		Rule:       rule,
		Signals:    signals,
		Provenance: provenance(signals),
	}
}

// provenance describes the signal counts behind the suggestion.
func provenance(signals []Signal) string {
	d, n := 0, 0
	for _, s := range signals {
		d += s.Dismissed
		n += s.Negative
	}
	parts := make([]string, 0, 2)
	if d > 0 {
		parts = append(parts, countPhrase(d, "dismissal", "dismissals"))
	}
	if n > 0 {
		parts = append(parts, countPhrase(n, "negative reaction", "negative reactions"))
	}
	return strings.Join(parts, ", ")
}

func countPhrase(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// dirOf returns the directory of a path, or "." for a bare filename.
func dirOf(p string) string {
	if p == "" {
		return "."
	}
	d := path.Dir(p)
	if d == "." && !strings.Contains(p, "/") {
		return "."
	}
	return d
}

// commonTitleToken returns one token present in every signal title, or "".
func commonTitleToken(signals []Signal) string {
	if len(signals) == 0 {
		return ""
	}
	intersection := normTokens(signals[0].Title)
	for i := 1; i < len(signals); i++ {
		toks := normTokens(signals[i].Title)
		set := make(map[string]bool, len(toks))
		for _, t := range toks {
			set[t] = true
		}
		filtered := make([]string, 0, len(intersection))
		for _, t := range intersection {
			if set[t] {
				filtered = append(filtered, t)
			}
		}
		intersection = filtered
		if len(intersection) == 0 {
			return ""
		}
	}
	sort.Strings(intersection)
	return intersection[0]
}

// normTokens lowercases and splits a title into meaningful word tokens,
// dropping duplicates within the title.
func normTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool)
	var out []string
	for _, f := range fields {
		if seen[f] || len(f) < 3 {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// defaultExpiry returns now + 90 days at UTC midnight.
func defaultExpiry(now time.Time) time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, defaultExpiryDays)
}
