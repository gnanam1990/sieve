package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
)

// MaxCommentBytes is GitHub's hard limit on a comment body. The walkthrough is
// rendered to fit under it by truncating optional sections in a fixed order;
// the metadata block is never truncated.
const MaxCommentBytes = 65536

var sevMarker = map[findings.Severity]string{
	findings.SeverityCritical: "🔴 critical",
	findings.SeverityMajor:    "🟠 major",
	findings.SeverityMinor:    "🟡 minor",
	findings.SeverityNit:      "⚪ nit",
}

// SkippedFile is one entry of the skipped-files section.
type SkippedFile struct {
	Path   string
	Reason string
}

// WalkthroughInput is everything the walkthrough comment renders from.
type WalkthroughInput struct {
	Result        gate.GateResult
	Meta          gate.Meta // encoded into the hidden metadata block
	Skipped       []SkippedFile
	FilesReviewed int
	FilesSkipped  int
	Model         string
	Learnings     int  // repository rules applied (>0 shows in the footer)
	Calibrated    bool // runtime confidence calibration was on
	InputTokens   int
	OutputTokens  int
	Version       string
}

type truncFlags struct {
	notes    bool
	resolved bool
	skipped  bool
}

// Walkthrough renders the full walkthrough body, shrinking it under
// MaxCommentBytes by truncating notes, then resolved, then skipped — in that
// order — while always preserving the marker, metadata, stats, and inline
// table.
func Walkthrough(in WalkthroughInput) string {
	for _, tr := range []truncFlags{
		{},
		{notes: true},
		{notes: true, resolved: true},
		{notes: true, resolved: true, skipped: true},
	} {
		body := build(in, tr)
		if len(body) <= MaxCommentBytes {
			return body
		}
	}
	// Even fully truncated the essentials exceed the cap (pathological): return
	// the smallest form anyway — the metadata block is intact, which is what
	// cross-run dedupe depends on.
	return build(in, truncFlags{notes: true, resolved: true, skipped: true})
}

func build(in WalkthroughInput, tr truncFlags) string {
	st := in.Result.Stats
	var b strings.Builder
	b.WriteString(WalkthroughMarker)
	b.WriteByte('\n')
	b.WriteString(MetaComment(in.Meta))
	b.WriteByte('\n')
	b.WriteString("## sieve review\n")
	fmt.Fprintf(&b, "**%s** · %d notes · %d resolved · %d files reviewed, %d skipped\n",
		pluralize(st.InlineCount, "finding", "findings"),
		st.NotesCount, st.ResolvedCount, in.FilesReviewed, in.FilesSkipped)

	if len(in.Result.Inline) > 0 {
		b.WriteString("\n| Severity | Finding | Where |\n|---|---|---|\n")
		for _, f := range in.Result.Inline {
			fmt.Fprintf(&b, "| %s | %s | `%s` |\n", sevMarker[f.Severity], escapeCell(f.Title), where(f.Finding))
		}
	}

	if st.NotesCount > 0 {
		b.WriteString("\n")
		b.WriteString(notesSection(in.Result.Notes, tr.notes))
	}
	if st.ResolvedCount > 0 {
		b.WriteString("\n")
		b.WriteString(resolvedSection(in.Result.Resolved, tr.resolved))
	}
	if len(in.Skipped) > 0 {
		b.WriteString("\n")
		b.WriteString(skippedSection(in.Skipped, tr.skipped))
	}

	extra := ""
	if in.Learnings > 0 {
		extra += fmt.Sprintf(" · learnings: %d rules active", in.Learnings)
	}
	if in.Calibrated {
		extra += " · calibration: on"
	}
	fmt.Fprintf(&b, "\n<sub>model `%s` · tokens in %s / out %s · sieve %s%s</sub>\n",
		in.Model, humanK(in.InputTokens), humanK(in.OutputTokens), in.Version, extra)
	return b.String()
}

func notesSection(notes []gate.Finding, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>📝 Notes (%d)</summary>\n\n", len(notes))
	if truncated {
		fmt.Fprintf(&b, "…and %d more notes — see JSON output\n", len(notes))
	} else {
		byFile := groupByPath(notes)
		for _, path := range byFile.order {
			fmt.Fprintf(&b, "**`%s`**\n\n", path)
			for _, f := range byFile.groups[path] {
				repeated := ""
				if f.Repeated {
					repeated = " _(repeated)_"
				}
				fmt.Fprintf(&b, "- %s · %s (`%s`)%s\n", sevMarker[f.Severity], f.Title, where(f.Finding), repeated)
				if body := strings.TrimSpace(f.Body); body != "" {
					b.WriteString(indent(body))
					b.WriteByte('\n')
				}
			}
		}
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

func resolvedSection(resolved []gate.CompactFinding, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>✅ Resolved since last review (%d)</summary>\n\n", len(resolved))
	if truncated {
		fmt.Fprintf(&b, "…and %d more — see JSON output\n", len(resolved))
	} else {
		for _, r := range resolved {
			fmt.Fprintf(&b, "- %s — `%s`\n", sevMarker[findings.Severity(r.Severity)], r.Path)
		}
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

func skippedSection(skipped []SkippedFile, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>⏭️ Skipped files (%d)</summary>\n\n", len(skipped))
	if truncated {
		fmt.Fprintf(&b, "…and %d more — see JSON output\n", len(skipped))
	} else {
		for _, s := range skipped {
			fmt.Fprintf(&b, "- `%s` — %s\n", s.Path, s.Reason)
		}
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

type pathGroups struct {
	order  []string
	groups map[string][]gate.Finding
}

// groupByPath buckets notes by file. Paths appear in sorted order and each
// bucket keeps its findings in severity/line order for stable rendering.
func groupByPath(notes []gate.Finding) pathGroups {
	g := pathGroups{groups: map[string][]gate.Finding{}}
	for _, f := range notes {
		if _, ok := g.groups[f.Path]; !ok {
			g.order = append(g.order, f.Path)
		}
		g.groups[f.Path] = append(g.groups[f.Path], f)
	}
	sort.Strings(g.order)
	for path := range g.groups {
		fs := g.groups[path]
		sort.SliceStable(fs, func(i, j int) bool {
			if ri, rj := findings.Rank(fs[i].Severity), findings.Rank(fs[j].Severity); ri != rj {
				return ri < rj
			}
			return fs[i].Line < fs[j].Line
		})
		g.groups[path] = fs
	}
	return g
}

func where(f findings.Finding) string {
	if f.EndLine > 0 {
		return fmt.Sprintf("%s:%d-%d", f.Path, f.Line, f.EndLine)
	}
	return fmt.Sprintf("%s:%d", f.Path, f.Line)
}

// humanK renders a token count compactly: 18234 -> "18.2k", 840 -> "840".
func humanK(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// escapeCell makes a title safe inside a markdown table cell.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

// indent prefixes each line of a note body with two spaces so it nests under
// its list item.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
