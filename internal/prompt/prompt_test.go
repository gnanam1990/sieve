package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gnanam1990/sieve/internal/diff"
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
