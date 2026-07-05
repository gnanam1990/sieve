package gate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// MetaVersion is the schema version stamped into the walkthrough metadata.
const MetaVersion = 1

// maxMetaFps bounds how many fingerprints the metadata block carries, so a
// long-lived PR can never grow the hidden JSON without limit. When the cap is
// exceeded the least-severe (tail) entries are dropped first.
//
// The spec calls for "resolved-oldest-first" eviction. Because sieve carries
// only the *current* run's fingerprints in the block (resolved findings are
// derived from the prior block, a one-run window, and are not re-persisted),
// there are no resolved entries to evict; the cap therefore degenerates to
// dropping the least-severe current findings, which is the intended
// size-bounding behavior. See STAGE_NOTES for the rationale.
const maxMetaFps = 200

// PriorFinding is one entry of the walkthrough metadata's fingerprint list:
// the content fingerprint plus enough context to render a Resolved row
// without re-deriving it.
type PriorFinding struct {
	Fingerprint string `json:"f"`
	Path        string `json:"p"`
	Severity    string `json:"s"`
}

// Meta is the hidden, base64-encoded JSON carried inside the walkthrough
// comment. It lets a later run detect repeated and resolved findings without
// any server-side state.
type Meta struct {
	Version int            `json:"v"`
	HeadSHA string         `json:"head_sha"`
	Fps     []PriorFinding `json:"fps"`
	Ts      string         `json:"ts"`
}

// EncodeMeta serializes Meta to compact JSON and base64-encodes it for
// embedding in an HTML comment.
func EncodeMeta(m Meta) string {
	b, _ := json.Marshal(m) //nolint:errcheck // Meta marshals cleanly by construction
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeMeta reverses EncodeMeta. It tolerates unknown JSON fields on purpose
// (forward-compat: a newer sieve may add keys a older one must still read).
func DecodeMeta(s string) (Meta, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Meta{}, fmt.Errorf("decode meta base64: %w", err)
	}
	var m Meta
	// Plain Unmarshal (no DisallowUnknownFields) ignores unknown keys.
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("decode meta json: %w", err)
	}
	return m, nil
}

// BuildMeta produces the metadata block for the run that just completed: the
// fingerprints of every kept finding (inline then notes, most-severe first),
// capped at maxMetaFps.
func BuildMeta(headSHA, ts string, res GateResult) Meta {
	fps := make([]PriorFinding, 0, len(res.Inline)+len(res.Notes))
	add := func(fs []Finding) {
		for _, f := range fs {
			fps = append(fps, PriorFinding{
				Fingerprint: f.Fingerprint,
				Path:        f.Path,
				Severity:    string(f.Severity),
			})
		}
	}
	add(res.Inline)
	add(res.Notes)
	if len(fps) > maxMetaFps {
		fps = fps[:maxMetaFps]
	}
	return Meta{Version: MetaVersion, HeadSHA: headSHA, Fps: fps, Ts: ts}
}
