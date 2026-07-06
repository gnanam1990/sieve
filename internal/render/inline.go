package render

import (
	"fmt"
	"strings"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
)

// Inline renders one inline review comment body. anchors is the commentable
// line index for the reviewed diff; it decides whether the finding's
// Suggestion can ship as a committable ```suggestion block.
//
// A committable suggestion is emitted only when all three hold:
//   - Suggestion is non-empty,
//   - the finding anchors to the RIGHT side, and
//   - every line in [Line, EndLine] is commentable RIGHT-side.
//
// Otherwise the suggestion text is still surfaced, as a plain fenced block
// labelled "proposed fix" that the reader applies by hand.
func Inline(f gate.Finding, anchors *findings.Anchors) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**[%s] %s**\n\n", f.Severity, f.Title)
	if body := strings.TrimSpace(f.Body); body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}

	if sug := f.Suggestion; sug != "" {
		b.WriteString("\n")
		if suggestionCommittable(f, anchors) {
			b.WriteString("```suggestion\n")
			b.WriteString(ensureTrailingNewline(sug))
			b.WriteString("```\n")
		} else {
			b.WriteString("_proposed fix_\n\n```\n")
			b.WriteString(ensureTrailingNewline(sug))
			b.WriteString("```\n")
		}
	}

	fmt.Fprintf(&b, "\n<sub>sieve · category `%s` · confidence %.2f</sub>\n", f.Category, f.Confidence)
	// Hidden, versioned fingerprint marker: lets a later run recover this
	// comment's ID by listing review comments and matching the fp (for meta v2
	// cids, reactions, and dismissal detection) without server-side state.
	fmt.Fprintf(&b, "%s%s %s%s\n", FpMarkerPrefix, fpMarkerVersion, f.Fingerprint, FpMarkerSuffix)
	return b.String()
}

// Fingerprint marker delimiters embedded in inline comment bodies. The prefix
// is a version-agnostic locator; the version token follows it.
const (
	FpMarkerPrefix  = "<!-- sieve:fp "
	FpMarkerSuffix  = " -->"
	fpMarkerVersion = "v1"
)

// ParseFpMarker extracts the fingerprint from an inline comment body, or ""
// when absent or malformed. Comment bodies are untrusted (anyone can edit or
// forge one), so parsing is defensive: it strips the version token and accepts
// the value only when it is a well-formed fingerprint (16 lowercase hex chars,
// matching fingerprint.Len).
func ParseFpMarker(body string) string {
	i := strings.Index(body, FpMarkerPrefix)
	if i < 0 {
		return ""
	}
	rest := body[i+len(FpMarkerPrefix):]
	j := strings.Index(rest, FpMarkerSuffix)
	if j < 0 {
		return ""
	}
	inner := strings.TrimSpace(rest[:j]) // "v1 <fp>"
	if sp := strings.IndexByte(inner, ' '); sp >= 0 && strings.HasPrefix(inner, "v") {
		inner = strings.TrimSpace(inner[sp+1:])
	}
	if !wellFormedFp(inner) {
		return ""
	}
	return inner
}

// wellFormedFp reports whether s is a 16-char lowercase-hex fingerprint.
func wellFormedFp(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}

// suggestionCommittable implements the three-part eligibility test.
func suggestionCommittable(f gate.Finding, anchors *findings.Anchors) bool {
	if f.Suggestion == "" || f.Side != findings.SideRight {
		return false
	}
	return anchors.RightRangeCommentable(f.Path, f.Line, f.EndLine)
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
