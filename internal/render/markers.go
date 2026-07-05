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

const metaPrefix = "<!-- sieve:meta v1 "
const metaSuffix = " -->"

// MetaComment renders the hidden, base64-encoded metadata line.
func MetaComment(m gate.Meta) string {
	return metaPrefix + gate.EncodeMeta(m) + metaSuffix
}

// ExtractMeta pulls the metadata block out of an existing walkthrough body.
// The bool is false when no metadata line is present (e.g. a legacy or
// hand-edited comment); a present-but-corrupt block returns an error so the
// caller can warn rather than silently lose cross-run dedupe.
func ExtractMeta(body string) (gate.Meta, bool, error) {
	i := strings.Index(body, metaPrefix)
	if i < 0 {
		return gate.Meta{}, false, nil
	}
	rest := body[i+len(metaPrefix):]
	j := strings.Index(rest, metaSuffix)
	if j < 0 {
		return gate.Meta{}, false, fmt.Errorf("meta block not terminated")
	}
	m, err := gate.DecodeMeta(strings.TrimSpace(rest[:j]))
	if err != nil {
		return gate.Meta{}, false, err
	}
	return m, true, nil
}
