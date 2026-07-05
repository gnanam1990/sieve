package diff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const fixtureDir = "../../testdata/diffs"

func fixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixtureDir, "*.diff"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found in %s: %v", fixtureDir, err)
	}
	return paths
}

func parseFixture(t *testing.T, path string) []FileDiff {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(%s): %v", filepath.Base(path), err)
	}
	return files
}

func TestParseGolden(t *testing.T) {
	for _, path := range fixtures(t) {
		name := strings.TrimSuffix(filepath.Base(path), ".diff")
		t.Run(name, func(t *testing.T) {
			files := parseFixture(t, path)
			got, err := json.MarshalIndent(files, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join(fixtureDir, name+".golden.json")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden (run `make golden`): %v", err)
			}
			if diff := cmp.Diff(string(want), string(got)); diff != "" {
				t.Errorf("golden mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAnchorConsistency reconstructs old/new numbering purely from hunk
// headers and asserts every line's numbers are monotonic and consistent.
// This is the anchor guarantee: NewNum is valid for Reviews API side=RIGHT,
// OldNum for side=LEFT.
func TestAnchorConsistency(t *testing.T) {
	for _, path := range fixtures(t) {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			for _, fd := range parseFixture(t, path) {
				if fd.Status == Binary && len(fd.Hunks) != 0 {
					t.Fatalf("%s: binary file has hunks", fd.NewPath)
				}
				prevOldEnd, prevNewEnd := 0, 0
				for hi, h := range fd.Hunks {
					if h.OldStart < prevOldEnd || h.NewStart < prevNewEnd {
						t.Fatalf("%s hunk %d: overlaps previous hunk", fd.NewPath, hi)
					}
					oldN, newN := h.OldStart, h.NewStart
					var oldCount, newCount int
					for li, l := range h.Lines {
						switch l.Kind {
						case Context:
							if l.OldNum != oldN || l.NewNum != newN {
								t.Fatalf("%s hunk %d line %d: context nums (%d,%d), want (%d,%d)", fd.NewPath, hi, li, l.OldNum, l.NewNum, oldN, newN)
							}
							oldN++
							newN++
							oldCount++
							newCount++
						case AddedLine:
							if l.NewNum != newN || l.OldNum != 0 {
								t.Fatalf("%s hunk %d line %d: added nums (%d,%d), want (0,%d)", fd.NewPath, hi, li, l.OldNum, l.NewNum, newN)
							}
							newN++
							newCount++
						case RemovedLine:
							if l.OldNum != oldN || l.NewNum != 0 {
								t.Fatalf("%s hunk %d line %d: removed nums (%d,%d), want (%d,0)", fd.NewPath, hi, li, l.OldNum, l.NewNum, oldN)
							}
							oldN++
							oldCount++
						}
					}
					if oldCount != h.OldLines || newCount != h.NewLines {
						t.Fatalf("%s hunk %d: counted %d/%d lines, header says %d/%d", fd.NewPath, hi, oldCount, newCount, h.OldLines, h.NewLines)
					}
					prevOldEnd, prevNewEnd = oldN, newN
				}
			}
		})
	}
}

func TestParseStatuses(t *testing.T) {
	cases := []struct {
		fixture  string
		status   FileStatus
		oldPath  string
		newPath  string
		numHunks int
	}{
		{"create.diff", Added, "", "created.txt", 1},
		{"delete.diff", Deleted, "beta.txt", "", 1},
		{"rename_pure.diff", Renamed, "alpha.txt", "omega.txt", 0},
		{"rename_with_edits.diff", Renamed, "omega.txt", "gamma.txt", 1},
		{"binary.diff", Binary, "img.bin", "img.bin", 0},
		{"mode_change_only.diff", Modified, "run.sh", "run.sh", 0},
		{"copy.diff", Copied, "code.go", "code_copy.go", 0},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			files := parseFixture(t, filepath.Join(fixtureDir, c.fixture))
			if len(files) != 1 {
				t.Fatalf("got %d files, want 1", len(files))
			}
			fd := files[0]
			if fd.Status != c.status || fd.OldPath != c.oldPath || fd.NewPath != c.newPath || len(fd.Hunks) != c.numHunks {
				t.Fatalf("got status=%v old=%q new=%q hunks=%d, want status=%v old=%q new=%q hunks=%d",
					fd.Status, fd.OldPath, fd.NewPath, len(fd.Hunks), c.status, c.oldPath, c.newPath, c.numHunks)
			}
		})
	}
}

func TestParseCRLFPreserved(t *testing.T) {
	files := parseFixture(t, filepath.Join(fixtureDir, "crlf.diff"))
	var sawCR bool
	for _, l := range files[0].Hunks[0].Lines {
		if strings.HasSuffix(l.Content, "\r") {
			sawCR = true
		}
	}
	if !sawCR {
		t.Fatal("CRLF fixture lines lost their \\r bytes")
	}
}

func TestParseNoEOF(t *testing.T) {
	files := parseFixture(t, filepath.Join(fixtureDir, "no_eof.diff"))
	lines := files[0].Hunks[0].Lines
	var flagged int
	for _, l := range lines {
		if l.NoEOF {
			flagged++
		}
	}
	if flagged != 2 {
		t.Fatalf("want NoEOF on removed and added final lines (2), got %d", flagged)
	}
}

func TestParseHunkFunctionContext(t *testing.T) {
	files := parseFixture(t, filepath.Join(fixtureDir, "hunk_context.diff"))
	h := files[0].Hunks[0]
	if h.Header == "" {
		t.Fatal("expected function context after @@, got empty header")
	}
}

func TestParseUTF8(t *testing.T) {
	files := parseFixture(t, filepath.Join(fixtureDir, "utf8_emoji.diff"))
	var found bool
	for _, l := range files[0].Hunks[0].Lines {
		if l.Kind == AddedLine && strings.Contains(l.Content, "🎉") {
			found = true
		}
	}
	if !found {
		t.Fatal("emoji content not preserved")
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"malformed hunk header": "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ bogus @@\n x\n",
		"truncated hunk":        "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n x\n",
		"garbage in hunk":       "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n x\n*bad\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestParseEmpty(t *testing.T) {
	files, err := Parse(nil)
	if err != nil || len(files) != 0 {
		t.Fatalf("Parse(nil) = %v, %v; want empty, nil", files, err)
	}
}

func TestParseQuotedPaths(t *testing.T) {
	in := "diff --git \"a/sp ace.txt\" \"b/sp ace.txt\"\n--- \"a/sp ace.txt\"\n+++ \"b/sp ace.txt\"\n@@ -1 +1 @@\n-old\n+new\n"
	files, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if files[0].NewPath != "sp ace.txt" {
		t.Fatalf("got %q, want %q", files[0].NewPath, "sp ace.txt")
	}
}

func TestStatusJSONRoundTrip(t *testing.T) {
	for s := range statusNames {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var back FileStatus
		if err := json.Unmarshal(b, &back); err != nil || back != s {
			t.Fatalf("round trip %v -> %s -> %v (%v)", s, b, back, err)
		}
	}
	for k := range kindNames {
		b, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		var back LineKind
		if err := json.Unmarshal(b, &back); err != nil || back != k {
			t.Fatalf("round trip %v -> %s -> %v (%v)", k, b, back, err)
		}
	}
}
