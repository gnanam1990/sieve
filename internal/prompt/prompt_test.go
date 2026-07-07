package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gnanam1990/sieve/internal/diff"
	"github.com/gnanam1990/sieve/internal/findings"
)

func fixtureInput(t *testing.T) Input {
	t.Helper()
	data, err := os.ReadFile("../../testdata/diffs/multi_file_multi_hunk.diff")
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Title: "Spell out key line numbers", Body: "Fixture PR body."}
	for _, fd := range files {
		f := File{Path: fd.NewPath, Status: fd.Status.String(), Diff: fd}
		if fd.NewPath == "alpha.txt" {
			f.Content = []byte("alpha line one\nalpha line two\n")
		}
		in.Files = append(in.Files, f)
	}
	return in
}

func checkGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden (run `make golden`): %v", err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("golden mismatch (-want +got):\n%s", diff)
	}
}

// TestSystemGolden pins the rendered system prompt: any prompt change must
// show up as a reviewable golden diff forever.
func TestSystemGolden(t *testing.T) {
	sys, err := System()
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"JSON only", "R:<n>", "L:<n>", `{"findings": [`, "<=120 chars"} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt missing %q", must)
		}
	}
	checkGolden(t, "system.golden.md", sys)
}

// TestUserPromptGolden pins the rendered user prompt for a fixture.
func TestUserPromptGolden(t *testing.T) {
	batches := BuildBatches(fixtureInput(t))
	if len(batches) != 1 {
		t.Fatalf("fixture should fit one batch, got %d", len(batches))
	}
	checkGolden(t, "user_batch.golden.md", batches[0].User)
}

// TestAnnotationsMatchParsedNumbers cross-checks every R:/L: annotation in
// the rendered prompt against the fixture's parsed NewNum/OldNum sets.
func TestAnnotationsMatchParsedNumbers(t *testing.T) {
	in := fixtureInput(t)
	batches := BuildBatches(in)

	valid := map[string]bool{}
	for _, f := range in.Files {
		for _, h := range f.Diff.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.AddedLine:
					valid[fmt.Sprintf("R:%d", l.NewNum)] = true
				case diff.RemovedLine:
					valid[fmt.Sprintf("L:%d", l.OldNum)] = true
				case diff.Context:
					valid[fmt.Sprintf("R:%d", l.NewNum)] = true
				}
			}
		}
	}
	var checked int
	for _, b := range batches {
		for _, line := range strings.Split(b.User, "\n") {
			if !strings.HasPrefix(line, "R:") && !strings.HasPrefix(line, "L:") {
				continue
			}
			anchor, _, ok := strings.Cut(line, " ")
			if !ok {
				t.Fatalf("malformed annotated line %q", line)
			}
			if !valid[anchor] {
				t.Errorf("prompt cites %q which is not a parsed anchor", anchor)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no annotated lines found in prompt")
	}
}

func TestBuildBatchesSplitsOnBudget(t *testing.T) {
	// Two files of ~60KB rendered each cannot share a 96KB budget.
	mk := func(path string) File {
		var lines []diff.Line
		for i := 1; i <= 1500; i++ {
			lines = append(lines, diff.Line{Kind: diff.AddedLine, NewNum: i, Content: strings.Repeat("x", 38)})
		}
		return File{Path: path, Status: "added", Diff: diff.FileDiff{
			NewPath: path, Status: diff.Added,
			Hunks: []diff.Hunk{{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 1500, Lines: lines}},
		}}
	}
	batches := BuildBatches(Input{Title: "big", Files: []File{mk("a.go"), mk("b.go")}})
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}
	if batches[0].Files[0] != "a.go" || batches[1].Files[0] != "b.go" {
		t.Fatalf("bad packing: %+v", batches)
	}
	if len(batches[0].Truncated) != 0 {
		t.Fatalf("files under budget must not be truncated")
	}
}

// TestBuildBatchesWithCapShrinksBatch: a provider-level max_input_tokens cap
// lowers the per-batch budget below the default.
func TestBuildBatchesWithCapShrinksBatch(t *testing.T) {
	mk := func(path string) File {
		var lines []diff.Line
		for i := 1; i <= 1500; i++ {
			lines = append(lines, diff.Line{Kind: diff.AddedLine, NewNum: i, Content: strings.Repeat("x", 38)})
		}
		return File{Path: path, Status: "added", Diff: diff.FileDiff{
			NewPath: path, Status: diff.Added,
			Hunks: []diff.Hunk{{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 1500, Lines: lines}},
		}}
	}
	in := Input{Title: "big", Files: []File{mk("a.go"), mk("b.go")}}
	// With a 12k cap, each ~15k-token file must land in its own batch and be
	// truncated, while the default 24k cap would hold one file fully.
	batches := BuildBatchesWithCap(in, 12000)
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}
	for i, b := range batches {
		if estimateTokens(len(b.User)) > 12000 {
			t.Fatalf("batch %d over 12k cap: %d tokens", i, estimateTokens(len(b.User)))
		}
		if len(b.Truncated) != 1 {
			t.Fatalf("batch %d should truncate its oversized file: %+v", i, b.Truncated)
		}
	}
}

func TestBuildBatchesTruncatesOversizedFile(t *testing.T) {
	var hunks []diff.Hunk
	for h := 0; h < 40; h++ {
		var lines []diff.Line
		for i := 1; i <= 500; i++ {
			lines = append(lines, diff.Line{Kind: diff.AddedLine, NewNum: h*1000 + i, Content: strings.Repeat("y", 40)})
		}
		hunks = append(hunks, diff.Hunk{OldStart: 1, OldLines: 0, NewStart: h*1000 + 1, NewLines: 500, Lines: lines})
	}
	huge := File{
		Path: "huge.go", Status: "modified",
		Diff:    diff.FileDiff{NewPath: "huge.go", Status: diff.Modified, Hunks: hunks},
		Content: []byte("should be dropped"),
	}
	batches := BuildBatches(Input{Title: "t", Files: []File{huge}})
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	b := batches[0]
	if len(b.Truncated) != 1 || b.Truncated[0] != "huge.go" {
		t.Fatalf("oversized file must be marked truncated: %+v", b)
	}
	if estimateTokens(len(b.User)) > maxBatchTokens {
		t.Fatalf("batch still over budget: %d tokens", estimateTokens(len(b.User)))
	}
	if strings.Contains(b.User, "Full file content") {
		t.Fatal("truncated file must lose its content attachment")
	}
	if !strings.Contains(b.User, "hunks shown; remainder omitted") {
		t.Fatal("truncation note missing")
	}
}

// TestGeneratorGolden pins the liberal generator prompt (judge pipeline).
func TestGeneratorGolden(t *testing.T) {
	gen, err := Generator()
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"generator", "liberal", "judge", `{"findings": [`, "R:<n>"} {
		if !strings.Contains(gen, must) {
			t.Errorf("generator prompt missing %q", must)
		}
	}
	checkGolden(t, "generator.golden.md", gen)
}

// TestJudgeSystemGolden pins the judge verification prompt.
func TestJudgeSystemGolden(t *testing.T) {
	js := JudgeSystem()
	for _, must := range []string{"judge", `{"verdicts":`, "keep", "may NOT raise severity", "exactly once"} {
		if !strings.Contains(js, must) {
			t.Errorf("judge prompt missing %q", must)
		}
	}
	checkGolden(t, "judge.golden.md", js)
}

// TestJudgeUserGolden pins the judge's per-file user prompt: the reviewed
// diff followed by the numbered findings to verify.
func TestJudgeUserGolden(t *testing.T) {
	in := fixtureInput(t)
	var alpha File
	for _, f := range in.Files {
		if f.Path == "alpha.txt" {
			alpha = f
		}
	}
	fs := []findings.Finding{
		{Path: "alpha.txt", Line: 3, Side: findings.SideRight, Severity: findings.SeverityMajor, Confidence: 0.8, Category: "bug", Title: "Off-by-one on the boundary", Body: "The loop\nreads past the end."},
		{Path: "alpha.txt", Line: 5, EndLine: 6, Side: findings.SideRight, Severity: findings.SeverityMinor, Confidence: 0.5, Category: "correctness", Title: "Unchecked error", Body: "err is ignored."},
	}
	got := JudgeUser(alpha, fs)
	for _, must := range []string{"## alpha.txt", "## Findings to verify", "[0] Side RIGHT Line 3", "[1] Side RIGHT Line 5-6", "severity=major", "confidence=0.80"} {
		if !strings.Contains(got, must) {
			t.Errorf("judge user prompt missing %q", must)
		}
	}
	checkGolden(t, "judge_user.golden.md", got)
}

// TestJudgeUserTruncatesOversizedFile: the judge context self-limits the same
// way BuildBatches does — dropping content, then cutting hunks.
func TestJudgeUserTruncatesOversizedFile(t *testing.T) {
	var hunks []diff.Hunk
	for h := 0; h < 40; h++ {
		var lines []diff.Line
		for i := 1; i <= 500; i++ {
			lines = append(lines, diff.Line{Kind: diff.AddedLine, NewNum: h*1000 + i, Content: strings.Repeat("y", 40)})
		}
		hunks = append(hunks, diff.Hunk{OldStart: 1, OldLines: 0, NewStart: h*1000 + 1, NewLines: 500, Lines: lines})
	}
	huge := File{
		Path: "huge.go", Status: "modified",
		Diff:    diff.FileDiff{NewPath: "huge.go", Status: diff.Modified, Hunks: hunks},
		Content: []byte("should be dropped"),
	}
	fs := []findings.Finding{{Path: "huge.go", Line: 1, Side: findings.SideRight, Severity: findings.SeverityMajor, Confidence: 0.9, Category: "bug", Title: "x", Body: "y"}}
	got := JudgeUser(huge, fs)
	if estimateTokens(len(got)) > maxBatchTokens {
		t.Fatalf("judge prompt over budget: %d tokens", estimateTokens(len(got)))
	}
	if strings.Contains(got, "Full file content") {
		t.Fatal("oversized judge context must drop the content attachment")
	}
	if !strings.Contains(got, "hunks shown; remainder omitted") {
		t.Fatal("truncation note missing")
	}
	if !strings.Contains(got, "## Findings to verify") {
		t.Fatal("findings block must survive truncation")
	}
}

// TestJudgeUserKeepsDiffUnderHugeFindingsBlock: a findings block larger than
// the whole batch budget must NOT strip the diff to zero hunks — the judge
// always keeps at least the reserved diff floor of context.
func TestJudgeUserKeepsDiffUnderHugeFindingsBlock(t *testing.T) {
	// A modest diff with several hunks the judge should be able to see.
	var lines []diff.Line
	for i := 1; i <= 30; i++ {
		lines = append(lines, diff.Line{Kind: diff.AddedLine, NewNum: i, Content: "code line " + strings.Repeat("x", 10)})
	}
	f := File{Path: "a.go", Status: "modified", Diff: diff.FileDiff{
		NewPath: "a.go", Status: diff.Modified,
		Hunks: []diff.Hunk{{OldStart: 1, OldLines: 0, NewStart: 1, NewLines: 30, Lines: lines}},
	}}
	// A findings block that alone dwarfs maxBatchTokens (many verbose findings).
	var fs []findings.Finding
	for i := 0; i < 400; i++ {
		fs = append(fs, findings.Finding{
			Path: "a.go", Line: 1, Side: findings.SideRight, Severity: findings.SeverityMajor,
			Confidence: 0.7, Category: "bug", Title: "finding number " + strings.Repeat("t", 20),
			Body: strings.Repeat("why this is a problem and how to fix it. ", 10),
		})
	}
	got := JudgeUser(f, fs)
	if estimateTokens(len(renderFindings(fs))) <= maxBatchTokens {
		t.Fatal("test setup: findings block should exceed the batch budget")
	}
	if !strings.Contains(got, "R:1 + code line") {
		t.Error("judge prompt stripped ALL diff context under a huge findings block")
	}
	if !strings.Contains(got, "## Findings to verify") {
		t.Error("findings block must always be present")
	}
}

func TestPRBodyTruncated(t *testing.T) {
	in := fixtureInput(t)
	in.Body = strings.Repeat("b", 3000)
	batches := BuildBatches(in)
	if !strings.Contains(batches[0].User, "[...truncated]") {
		t.Fatal("long PR body must be truncated")
	}
	if strings.Contains(batches[0].User, strings.Repeat("b", 2100)) {
		t.Fatal("body not actually cut")
	}
}
