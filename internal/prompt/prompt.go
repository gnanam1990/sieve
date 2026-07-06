// Package prompt assembles the system and per-batch user prompts and
// greedy-packs files into token-budgeted batches.
package prompt

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/findings"
)

//go:embed templates/system.md
var systemTemplate string

// System renders the embedded reviewer system prompt.
func System() (string, error) {
	tpl, err := template.New("system").Parse(systemTemplate)
	if err != nil {
		return "", fmt.Errorf("parse system template: %w", err)
	}
	var sb strings.Builder
	err = tpl.Execute(&sb, struct{ MaxTitleLen int }{findings.MaxTitleLen})
	if err != nil {
		return "", fmt.Errorf("render system template: %w", err)
	}
	return sb.String(), nil
}

// maxBatchTokens is the greedy-packing target for estimated input tokens
// per request. Estimation is bytes/4 — crude but stable, and it only needs
// to keep requests comfortably inside model context windows.
const maxBatchTokens = 24_000

// maxPRBodyBytes truncates the PR description in the prompt.
const maxPRBodyBytes = 2 * 1024

// estimateTokens is the bytes/4 heuristic used for budgeting.
func estimateTokens(b int) int { return b / 4 }

// File is one kept file offered for review.
type File struct {
	Path    string
	Status  string
	Diff    diff.FileDiff
	Content []byte // full new-file content; nil to skip attachment
}

// Input is everything the user prompts are built from.
type Input struct {
	Title string
	Body  string
	Files []File
}

// Batch is one provider request's worth of files.
type Batch struct {
	User      string
	Files     []string // paths included, in order
	Truncated []string // paths whose diff was cut to fit the budget
}

// BuildBatches greedy-packs whole files into batches of at most
// maxBatchTokens estimated tokens. A single file exceeding the budget
// alone gets its hunks included in order until the budget is hit, is
// marked truncated, and loses its content attachment.
func BuildBatches(in Input) []Batch {
	header := renderHeader(in)
	headerTok := estimateTokens(len(header))
	budget := maxBatchTokens - headerTok

	var batches []Batch
	var cur Batch
	var curBody strings.Builder
	curTok := 0

	flush := func() {
		if len(cur.Files) == 0 {
			return
		}
		cur.User = header + curBody.String()
		batches = append(batches, cur)
		cur = Batch{}
		curBody.Reset()
		curTok = 0
	}

	for _, f := range in.Files {
		section := renderFile(f)
		tok := estimateTokens(len(section))
		if tok > budget {
			// Oversized alone: drop content attachment, then cut hunks.
			f.Content = nil
			section = renderFileTruncated(f, budget)
			tok = estimateTokens(len(section))
			flush()
			cur.Files = append(cur.Files, f.Path)
			cur.Truncated = append(cur.Truncated, f.Path)
			curBody.WriteString(section)
			curTok = tok
			flush()
			continue
		}
		if curTok+tok > budget {
			flush()
		}
		cur.Files = append(cur.Files, f.Path)
		curBody.WriteString(section)
		curTok += tok
	}
	flush()
	return batches
}

func renderHeader(in Input) string {
	body := in.Body
	if len(body) > maxPRBodyBytes {
		body = body[:maxPRBodyBytes] + "\n[...truncated]"
	}
	var sb strings.Builder
	sb.WriteString("# Pull request\n\nTitle: " + in.Title + "\n")
	if strings.TrimSpace(body) != "" {
		sb.WriteString("\nDescription:\n" + body + "\n")
	}
	sb.WriteString("\n# Changed files\n")
	return sb.String()
}

func renderFile(f File) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## %s (%s)\n\nDiff:\n", f.Path, f.Status)
	for _, h := range f.Diff.Hunks {
		writeHunk(&sb, h)
	}
	if f.Content != nil {
		fmt.Fprintf(&sb, "\nFull file content of %s:\n```\n", f.Path)
		for i, line := range splitContentLines(f.Content) {
			fmt.Fprintf(&sb, "%d: %s\n", i+1, line)
		}
		sb.WriteString("```\n")
	}
	return sb.String()
}

// renderFileTruncated includes hunks in order until the byte budget is
// spent, then notes the cut. No content attachment.
func renderFileTruncated(f File, budgetTokens int) string {
	budgetBytes := budgetTokens * 4
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## %s (%s)\n\nDiff (truncated to fit review budget):\n", f.Path, f.Status)
	included := 0
	for _, h := range f.Diff.Hunks {
		var hb strings.Builder
		writeHunk(&hb, h)
		if sb.Len()+hb.Len() > budgetBytes {
			break
		}
		sb.WriteString(hb.String())
		included++
	}
	fmt.Fprintf(&sb, "\n[%d of %d hunks shown; remainder omitted]\n", included, len(f.Diff.Hunks))
	return sb.String()
}

// writeHunk renders a hunk with explicit per-line anchors: R:<NewNum> for
// lines commentable on the RIGHT side, L:<OldNum> for the LEFT side, so
// the model cites numbers that actually exist.
func writeHunk(sb *strings.Builder, h diff.Hunk) {
	fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@ %s\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines, h.Header)
	for _, l := range h.Lines {
		switch l.Kind {
		case diff.AddedLine:
			fmt.Fprintf(sb, "R:%d + %s\n", l.NewNum, l.Content)
		case diff.RemovedLine:
			fmt.Fprintf(sb, "L:%d - %s\n", l.OldNum, l.Content)
		case diff.Context:
			fmt.Fprintf(sb, "R:%d   %s\n", l.NewNum, l.Content)
		}
	}
}

func splitContentLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
