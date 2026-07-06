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
	// Hidden fingerprint marker: lets a later run recover this comment's ID by
	// listing review comments and matching the fp (for meta v2 cids, reactions,
	// and dismissal detection) without server-side state.
	fmt.Fprintf(&b, "%s%s%s\n", FpMarkerPrefix, f.Fingerprint, FpMarkerSuffix)
	return b.String()
}

// Fingerprint marker delimiters embedded in inline comment bodies.
const (
	FpMarkerPrefix = "<!-- sieve:fp "
	FpMarkerSuffix = " -->"
)

// ParseFpMarker extracts the fingerprint from an inline comment body, or ""
// when absent (a non-sieve comment or a legacy one).
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
	return strings.TrimSpace(rest[:j])
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
