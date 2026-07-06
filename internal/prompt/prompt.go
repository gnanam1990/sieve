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

//go:embed templates/generator.md
var generatorTemplate string

//go:embed templates/judge.md
var judgeTemplate string

// System renders the embedded single-reviewer system prompt.
func System() (string, error) { return renderTitled("system", systemTemplate) }

// Generator renders the liberal generator system prompt (judge pipeline).
func Generator() (string, error) { return renderTitled("generator", generatorTemplate) }

// JudgeSystem returns the judge system prompt (no template variables).
func JudgeSystem() string { return judgeTemplate }

// renderTitled renders a template whose only variable is MaxTitleLen.
func renderTitled(name, tpl string) (string, error) {
	t, err := template.New(name).Parse(tpl)
	if err != nil {
		return "", fmt.Errorf("parse %s template: %w", name, err)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, struct{ MaxTitleLen int }{findings.MaxTitleLen}); err != nil {
		return "", fmt.Errorf("render %s template: %w", name, err)
	}
	return sb.String(), nil
}

// minJudgeDiffTokens is the diff-context floor the judge always keeps, even
// when the findings block is large. The judge is worthless without code to
// verify against, so the findings budget can never squeeze the diff below this.
const minJudgeDiffTokens = maxBatchTokens / 3

// JudgeUser builds the judge's per-file user prompt: the file's annotated diff
// (the same context the generator saw) followed by the numbered findings to
// verify by index. It trims the diff the same way BuildBatches does — dropping
// the content attachment, then truncating hunks — but always reserves at least
// minJudgeDiffTokens of diff so a large findings block can never strip the
// judge of the code it must verify against. (An unusually large findings block
// may push the total over maxBatchTokens; the findings themselves are never
// dropped — they are the thing being judged.)
func JudgeUser(f File, fs []findings.Finding) string {
	findingsBlock := renderFindings(fs)
	budget := maxBatchTokens - estimateTokens(len(findingsBlock))
	if budget < minJudgeDiffTokens {
		budget = minJudgeDiffTokens
	}
	section := renderFile(f)
	if estimateTokens(len(section)) > budget {
		f.Content = nil // drop the full-file attachment first
		section = renderFile(f)
		if estimateTokens(len(section)) > budget {
			section = renderFileTruncated(f, budget)
		}
	}
	return section + findingsBlock
}

// renderFindings numbers the generator's findings for the judge to verify by
// index — the same order the verdicts must come back in.
func renderFindings(fs []findings.Finding) string {
	var b strings.Builder
	b.WriteString("\n## Findings to verify\n\n")
	for i, fn := range fs {
		fmt.Fprintf(&b, "[%d] Side %s Line %d", i, fn.Side, fn.Line)
		if fn.EndLine > 0 {
			fmt.Fprintf(&b, "-%d", fn.EndLine)
		}
		fmt.Fprintf(&b, " severity=%s confidence=%.2f category=%s\n    %s\n    %s\n\n",
			fn.Severity, fn.Confidence, fn.Category, fn.Title, strings.ReplaceAll(strings.TrimSpace(fn.Body), "\n", " "))
	}
	return b.String()
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
	Title         string
	Body          string
	Files         []File
	ExtraContext  string // optional repository-context section appended to the header
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
	if strings.TrimSpace(in.ExtraContext) != "" {
		sb.WriteString("\n# Repository context\n\n" + strings.TrimSpace(in.ExtraContext) + "\n")
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
