// Package gate is the noise gate: it turns the raw, anchor-valid findings of
// a review into two posting tiers (inline vs walkthrough notes) plus a list
// of findings resolved since the last run.
//
// The pipeline runs in a fixed order — within-run dedupe, confidence floor,
// tier routing, inline cap with overflow demotion, then cross-run dedupe —
// and every stage that discards or moves a finding records why in Stats. The
// gate never drops a finding silently: sub-floor findings are counted, and
// inline overflow is demoted to notes rather than lost.
package gate

import (
	"encoding/json"
	"sort"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/fingerprint"
)

// Tier is a routing destination.
type Tier int

// Tiers.
const (
	TierInline Tier = iota // posted as an inline review comment
	TierNotes              // listed in the walkthrough's collapsed notes section
)

var tierNames = map[Tier]string{TierInline: "inline", TierNotes: "notes"}

func (t Tier) String() string { return tierNames[t] }

// MarshalJSON renders the tier as its lowercase name for the gate JSON block.
func (t Tier) MarshalJSON() ([]byte, error) { return json.Marshal(tierNames[t]) }

// Finding is a reviewed finding decorated with its routing decision, content
// fingerprint, and cross-run repeat flag.
type Finding struct {
	findings.Finding
	Fingerprint string `json:"Fingerprint"`
	Tier        Tier   `json:"Tier"`
	Repeated    bool   `json:"Repeated"` // fingerprint present in the prior run's metadata
}

// Stats accounts for every routing decision.
type Stats struct {
	InputFindings      int // findings handed to the gate (post anchor gate)
	DuplicatesMerged   int // dropped by within-run dedupe
	FindingsBelowFloor int // dropped by the confidence floor
	InlineDemotedByCap int // moved inline -> notes by the cap
	InlineCount        int // final inline tier size
	NotesCount         int // final notes tier size
	RepeatedInline     int // inline findings already posted in a prior run
	RepeatedNotes      int // notes findings seen in a prior run
	ResolvedCount      int // prior fingerprints absent this run
}

// GateResult is the routing outcome.
//
//nolint:revive // spec-mandated name (R3); gate.Result would lose the "gate" cue at call sites
type GateResult struct {
	Inline   []Finding
	Notes    []Finding
	Resolved []PriorFinding
	Stats    Stats
}

// Route applies the full gate pipeline. prior is the fingerprint list decoded
// from the previous walkthrough's metadata (nil on the first run).
func Route(fs []findings.Finding, idx *fingerprint.ContentIndex, prior []PriorFinding, cfg config.Review) GateResult {
	var res GateResult
	res.Stats.InputFindings = len(fs)

	// 1. Within-run dedupe: same path+side+category with overlapping line
	// ranges collapses to the highest-confidence finding.
	deduped, merged := dedupe(fs)
	res.Stats.DuplicatesMerged = merged

	// 2. Confidence floor.
	survivors := make([]findings.Finding, 0, len(deduped))
	for _, f := range deduped {
		if f.Confidence < cfg.MinConfidence {
			res.Stats.FindingsBelowFloor++
			continue
		}
		survivors = append(survivors, f)
	}

	// 3. Tier routing + fingerprint decoration.
	inlineFloor := findings.Severity(cfg.InlineMinSeverity)
	var inline, notes []Finding
	for _, f := range survivors {
		anchor := idx.Anchor(f.Path, string(f.Side), f.Line)
		g := Finding{
			Finding:     f,
			Fingerprint: fingerprint.For(f.Path, string(f.Side), f.Category, f.Title, anchor),
			Tier:        TierNotes,
		}
		if findings.AtLeastAsSevere(f.Severity, inlineFloor) && f.Confidence >= cfg.InlineMinConfidence {
			g.Tier = TierInline
			inline = append(inline, g)
		} else {
			notes = append(notes, g)
		}
	}

	// 4. Inline cap + overflow demotion. Sort by severity desc, confidence
	// desc, path, line; anything beyond the cap becomes a note.
	sortInline(inline)
	if len(inline) > cfg.MaxInlineComments {
		overflow := inline[cfg.MaxInlineComments:]
		inline = inline[:cfg.MaxInlineComments]
		for i := range overflow {
			overflow[i].Tier = TierNotes
		}
		notes = append(notes, overflow...)
		res.Stats.InlineDemotedByCap = len(overflow)
	}
	sortNotes(notes)

	// 5. Cross-run dedupe: mark repeats, compute resolved.
	priorByFp := make(map[string]PriorFinding, len(prior))
	for _, p := range prior {
		priorByFp[p.Fingerprint] = p
	}
	currentFps := make(map[string]bool, len(inline)+len(notes))
	markRepeated(inline, priorByFp, currentFps, &res.Stats.RepeatedInline)
	markRepeated(notes, priorByFp, currentFps, &res.Stats.RepeatedNotes)

	for _, p := range prior {
		if !currentFps[p.Fingerprint] {
			res.Resolved = append(res.Resolved, p)
		}
	}
	sortResolved(res.Resolved)

	res.Inline = inline
	res.Notes = notes
	res.Stats.InlineCount = len(inline)
	res.Stats.NotesCount = len(notes)
	res.Stats.ResolvedCount = len(res.Resolved)
	return res
}

// dedupe collapses findings that share path+side+category and whose line
// ranges overlap, keeping the highest-confidence member. Input is sorted
// deterministically first so the survivor is stable across runs.
func dedupe(fs []findings.Finding) (kept []findings.Finding, merged int) {
	sorted := append([]findings.Finding(nil), fs...)
	findings.Sort(sorted)
	for _, f := range sorted {
		dup := -1
		for i, k := range kept {
			if f.Path == k.Path && f.Side == k.Side && f.Category == k.Category && rangesOverlap(f, k) {
				dup = i
				break
			}
		}
		if dup < 0 {
			kept = append(kept, f)
			continue
		}
		merged++
		if f.Confidence > kept[dup].Confidence {
			kept[dup] = f
		}
	}
	return kept, merged
}

// rangesOverlap reports whether two findings' anchored line intervals
// intersect. A single-line finding is the interval [Line, Line].
func rangesOverlap(a, b findings.Finding) bool {
	aLo, aHi := span(a)
	bLo, bHi := span(b)
	return aLo <= bHi && bLo <= aHi
}

func span(f findings.Finding) (lo, hi int) {
	hi = f.EndLine
	if hi == 0 {
		hi = f.Line
	}
	return f.Line, hi
}

func markRepeated(fs []Finding, prior map[string]PriorFinding, current map[string]bool, counter *int) {
	for i := range fs {
		current[fs[i].Fingerprint] = true
		if _, ok := prior[fs[i].Fingerprint]; ok {
			fs[i].Repeated = true
			*counter++
		}
	}
}

func sortInline(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool { return lessInline(fs[i], fs[j]) })
}

// lessInline is the cap ordering: severity desc, confidence desc, path, line.
func lessInline(a, b Finding) bool {
	if ra, rb := findings.Rank(a.Severity), findings.Rank(b.Severity); ra != rb {
		return ra < rb
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Line < b.Line
}

// sortNotes orders notes for stable rendering: severity, path, line.
func sortNotes(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if ra, rb := findings.Rank(fs[i].Severity), findings.Rank(fs[j].Severity); ra != rb {
			return ra < rb
		}
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		return fs[i].Line < fs[j].Line
	})
}

func sortResolved(ps []PriorFinding) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ra, rb := findings.Rank(findings.Severity(ps[i].Severity)), findings.Rank(findings.Severity(ps[j].Severity)); ra != rb {
			return ra < rb
		}
		if ps[i].Path != ps[j].Path {
			return ps[i].Path < ps[j].Path
		}
		return ps[i].Fingerprint < ps[j].Fingerprint
	})
}
