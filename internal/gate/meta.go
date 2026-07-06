package gate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/gnanam1990/sieve/internal/findings"
)

// MetaVersion is the schema version stamped into the walkthrough metadata.
// v2 (stage 5) adds compact active-finding records and a rolling resolved list
// so a later run can carry findings forward without re-reviewing every file.
const MetaVersion = 2

// Caps bound the hidden JSON so a long-lived PR never grows it without limit.
// findings overflow drops the oldest notes-tier records first (they re-emerge
// on any full re-review, so nothing is permanently lost); resolved is a rolling
// window.
const (
	maxMetaFindings = 100
	maxMetaResolved = 100
)

// PriorFinding is a v1 metadata entry (fingerprint + minimal context). Retained
// for decoding stage-3 walkthroughs; v2 uses CompactFinding.
type PriorFinding struct {
	Fingerprint string `json:"f"`
	Path        string `json:"p"`
	Severity    string `json:"s"`
}

// CompactFinding is one active finding recorded in the v2 metadata, carrying
// just enough to carry it forward on a delta re-review and to re-render it —
// without a fresh LLM call.
//
// Beyond the spec's {f,p,l,sd,s,c,t,cid} it also stores Category and EndLine:
// Category is required to recompute the content fingerprint for the "anchor
// gone" resolution check (R4's fp includes category), and EndLine preserves
// range findings. Both are small; noted in STAGE_NOTES.
type CompactFinding struct {
	Fp       string  `json:"f"`
	Path     string  `json:"p"`
	Line     int     `json:"l"`
	Side     string  `json:"sd"`         // "R" | "L"
	Severity string  `json:"s"`          // critical|major|minor|nit
	Conf     float64 `json:"c"`          // confidence 0..1
	Title    string  `json:"t"`          // truncated to 80 chars
	Cid      int64   `json:"cid"`        // inline comment ID; 0 if not posted inline
	Category string  `json:"cat"`        // category — for anchor-gone fp recompute
	EndLine  int     `json:"el,omitempty"` // range end; 0 = single line
	Tier     string  `json:"tr,omitempty"` // "inline" | "notes" — for cap-overflow eviction order
}

// Meta is the hidden, base64-encoded JSON carried inside the walkthrough
// comment. It lets a later run detect repeated/resolved findings and carry
// active ones forward without any server-side state.
type Meta struct {
	Version  int              `json:"v"`
	HeadSHA  string           `json:"head_sha"`
	Ts       string           `json:"ts"`
	Findings []CompactFinding `json:"findings,omitempty"` // v2: currently-active findings
	Resolved []string         `json:"resolved,omitempty"` // v2: rolling resolved fingerprints
	Fps      []PriorFinding   `json:"fps,omitempty"`      // v1 back-compat (decode only)
}

// IsV1 reports whether the decoded meta predates the compact records (stage-3
// v1). A v1 block cannot seed a delta review, so the caller must force one full
// re-review to populate v2 metadata.
func (m Meta) IsV1() bool { return m.Version < 2 || (len(m.Findings) == 0 && len(m.Fps) > 0) }

// PriorForRoute returns the prior active findings as compact records for
// gate.Route, synthesizing minimal records (fingerprint/path/severity only)
// from a v1 fps list so cross-run repeated/resolved still works on an upgrade.
func (m Meta) PriorForRoute() []CompactFinding {
	if len(m.Findings) > 0 {
		return m.Findings
	}
	out := make([]CompactFinding, 0, len(m.Fps))
	for _, p := range m.Fps {
		out = append(out, CompactFinding{Fp: p.Fingerprint, Path: p.Path, Severity: p.Severity})
	}
	return out
}

// EncodeMeta serializes Meta to compact JSON and base64-encodes it for
// embedding in an HTML comment.
func EncodeMeta(m Meta) string {
	b, _ := json.Marshal(m) //nolint:errcheck // Meta marshals cleanly by construction
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeMeta reverses EncodeMeta. It tolerates unknown JSON fields on purpose
// (forward-compat: a newer sieve may add keys an older one must still read) and
// decodes both v1 (fps) and v2 (findings/resolved) shapes.
func DecodeMeta(s string) (Meta, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Meta{}, fmt.Errorf("decode meta base64: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("decode meta json: %w", err)
	}
	return m, nil
}

// ShortSide compacts a findings.Side to "R"/"L"; ExpandSide reverses it.
func ShortSide(s findings.Side) string {
	if s == findings.SideLeft {
		return "L"
	}
	return "R"
}

// ExpandSide reverses ShortSide.
func ExpandSide(s string) findings.Side {
	if s == "L" {
		return findings.SideLeft
	}
	return findings.SideRight
}

// truncateTitle bounds a compact record's title.
func truncateTitle(s string) string {
	const maxLen = 80
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// compactOf builds a compact record from a routed finding and its posted
// comment ID (0 if not posted inline).
func compactOf(f Finding, cid int64) CompactFinding {
	return CompactFinding{
		Fp:       f.Fingerprint,
		Path:     f.Path,
		Line:     f.Line,
		EndLine:  f.EndLine,
		Side:     ShortSide(f.Side),
		Severity: string(f.Severity),
		Conf:     f.Confidence,
		Title:    truncateTitle(f.Title),
		Cid:      cid,
		Category: f.Category,
		Tier:     f.Tier.String(),
	}
}

// BuildMeta produces the v2 metadata block for the run that just completed.
//
//   - active: the currently-active findings (fresh + carried), most-severe
//     first, each with its inline comment ID (0 if notes-tier or unposted).
//   - priorResolved: the rolling resolved list from the prior meta.
//   - newlyResolved: fingerprints resolved this run.
//
// findings is capped at maxMetaFindings, evicting notes-tier records first (by
// eviction order they re-emerge on the next full re-review). resolved is a
// rolling window capped at maxMetaResolved, newest last, deduped.
func BuildMeta(headSHA, ts string, active []CompactFinding, priorResolved, newlyResolved []string) Meta {
	capped := capFindings(active)
	resolved := rollResolved(priorResolved, newlyResolved)
	return Meta{
		Version:  MetaVersion,
		HeadSHA:  headSHA,
		Ts:       ts,
		Findings: capped,
		Resolved: resolved,
	}
}

// capFindings enforces maxMetaFindings, dropping notes-tier records before
// inline-tier ones (inline records anchor live comment threads, so they must
// survive; dropped notes re-emerge on the next full re-review).
func capFindings(fs []CompactFinding) []CompactFinding {
	if len(fs) <= maxMetaFindings {
		return fs
	}
	inline := make([]CompactFinding, 0, len(fs))
	notes := make([]CompactFinding, 0, len(fs))
	for _, f := range fs {
		if f.Tier == "notes" {
			notes = append(notes, f)
		} else {
			inline = append(inline, f)
		}
	}
	kept := inline
	for _, n := range notes {
		if len(kept) >= maxMetaFindings {
			break
		}
		kept = append(kept, n)
	}
	if len(kept) > maxMetaFindings {
		kept = kept[:maxMetaFindings]
	}
	return kept
}

// rollResolved appends newly-resolved fingerprints to the prior rolling list,
// deduping (keeping the newest position) and capping at maxMetaResolved with
// oldest-first eviction.
func rollResolved(prior, newly []string) []string {
	seen := make(map[string]bool)
	var out []string
	// newest last: prior first, then newly; dedupe keeps last occurrence.
	for _, fp := range append(append([]string{}, prior...), newly...) {
		if fp == "" {
			continue
		}
		if seen[fp] {
			// move to newest position: drop earlier, re-append
			for i, e := range out {
				if e == fp {
					out = append(out[:i], out[i+1:]...)
					break
				}
			}
		}
		seen[fp] = true
		out = append(out, fp)
	}
	if len(out) > maxMetaResolved {
		out = out[len(out)-maxMetaResolved:]
	}
	return out
}
