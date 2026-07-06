// Package render turns gate results into the markdown sieve posts: the single
// walkthrough comment (with its hidden metadata block) and the inline review
// comment bodies, including committable suggestion blocks.
//
// Rendering is pure and deterministic — no clock, no network — so the same
// gate result always yields byte-identical markdown, which is what the golden
// tests and the cross-run idempotency guarantee rely on.
package render

import (
	"fmt"
	"strings"

	"github.com/gnanam1990/sieve/internal/gate"
)

// WalkthroughMarker is the hidden HTML comment that identifies sieve's
// walkthrough comment on a PR. Locating it is how a later run edits in place
// instead of posting a duplicate.
const WalkthroughMarker = "<!-- sieve:walkthrough -->"

// metaLocate is the stable, version-agnostic locator for the metadata line.
// The actual schema version lives inside the JSON (Meta.Version); the marker
// carries a display version (`v1`/`v2`) after this prefix, so a v2 sieve still
// finds and reads a v1 walkthrough and vice-versa.
const metaLocate = "<!-- sieve:meta "
const metaSuffix = " -->"

// MetaComment renders the hidden, base64-encoded metadata line, tagging it with
// the schema version for readability.
func MetaComment(m gate.Meta) string {
	return fmt.Sprintf("%sv%d %s%s", metaLocate, m.Version, gate.EncodeMeta(m), metaSuffix)
}

// ExtractMeta pulls the metadata block out of an existing walkthrough body,
// tolerant of the v1 and v2 marker tags. The bool is false when no metadata
// line is present (a legacy or hand-edited comment); a present-but-corrupt
// block returns an error so the caller can warn rather than silently lose
// cross-run dedupe.
func ExtractMeta(body string) (gate.Meta, bool, error) {
	i := strings.Index(body, metaLocate)
	if i < 0 {
		return gate.Meta{}, false, nil
	}
	rest := body[i+len(metaLocate):]
	j := strings.Index(rest, metaSuffix)
	if j < 0 {
		return gate.Meta{}, false, fmt.Errorf("meta block not terminated")
	}
	inner := strings.TrimSpace(rest[:j]) // "vN <base64>"
	// Drop the leading display-version token (e.g. "v2 ").
	if sp := strings.IndexByte(inner, ' '); sp >= 0 && strings.HasPrefix(inner, "v") {
		inner = inner[sp+1:]
	}
	m, err := gate.DecodeMeta(strings.TrimSpace(inner))
	if err != nil {
		return gate.Meta{}, false, err
	}
	return m, true, nil
}
