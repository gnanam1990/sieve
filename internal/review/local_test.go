package review

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/sieve/internal/diff"
)

// TestRunLocalReview runs the full review pipeline against a local git worktree
// with no GitHub token. A fake provider returns one finding anchored to an added
// line; the pipeline should keep it.
func TestRunLocalReview(t *testing.T) {
	dir := initGitRepo(t)
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tprintln(\"ok\")\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tvar x *int\n\tprintln(*x)\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "bug")

	// Find the first added line number in the local diff.
	diffBytes, err := gitDiff(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.Parse(diffBytes)
	if err != nil {
		t.Fatal(err)
	}
	line := firstAddedLine(files)
	if line == 0 {
		t.Fatal("no added line found in local diff")
	}

	findingsJSON := fmt.Sprintf(`{"findings":[{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"critical","Confidence":0.95,"Category":"bug","Title":"Nil pointer dereference","Body":"Dereferencing x without checking for nil."}]}`, line)
	fixturePath := writeFixture(t, findingsJSON)

	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixturePath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	rc, err := Run(context.Background(), Options{
		Repo:       "local/test",
		ConfigPath: cfgPath,
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(rc.Findings), rc.Findings)
	}
	f := rc.Findings[0]
	if f.Path != "service.go" || f.Title != "Nil pointer dereference" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if rc.Stats.Requests != 1 {
		t.Fatalf("want 1 request, got %d", rc.Stats.Requests)
	}
}

// TestRunLocalReviewIgnores verifies that active .sieve/ignore.yml rules drop
// matching findings before tier routing and report them in Stats.IgnoredCount.
func TestRunLocalReviewIgnores(t *testing.T) {
	dir := initGitRepo(t)

	// Create an ignore rule that suppresses any style finding.
	writeRepoFile(t, dir, ".sieve/ignore.yml", "ignore:\n  - category: style\n    reason: accepted style\n")

	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tprintln(\"ok\")\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tvar x *int\n\tprintln(*x)\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "bug")

	diffBytes, err := gitDiff(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.Parse(diffBytes)
	if err != nil {
		t.Fatal(err)
	}
	line := firstAddedLine(files)
	if line == 0 {
		t.Fatal("no added line found in local diff")
	}

	findingsJSON := fmt.Sprintf(`{"findings":[
		{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"minor","Confidence":0.7,"Category":"style","Title":"Style issue","Body":"style"},
		{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"critical","Confidence":0.95,"Category":"bug","Title":"Nil pointer dereference","Body":"Dereferencing x without checking for nil."}
	]}`, line, line)
	fixturePath := writeFixture(t, findingsJSON)

	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixturePath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	rc, err := Run(context.Background(), Options{
		Repo:       "local/test",
		ConfigPath: cfgPath,
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rc.Gate == nil {
		t.Fatal("expected gate result")
	}
	active := len(rc.Gate.Inline) + len(rc.Gate.Notes)
	if active != 1 {
		t.Fatalf("want 1 active finding, got %d (inline %d, notes %d)", active, len(rc.Gate.Inline), len(rc.Gate.Notes))
	}
	if rc.Gate.Stats.IgnoredCount != 1 {
		t.Fatalf("want IgnoredCount 1, got %d", rc.Gate.Stats.IgnoredCount)
	}
	if rc.Stats.IgnoredCount != 1 {
		t.Fatalf("want Stats.IgnoredCount 1, got %d", rc.Stats.IgnoredCount)
	}
	if len(rc.Gate.Ignored) != 1 || rc.Gate.Ignored[0].Category != "style" {
		t.Fatalf("want 1 ignored style finding, got %+v", rc.Gate.Ignored)
	}
	if rc.Gate.Inline[0].Title != "Nil pointer dereference" {
		t.Fatalf("expected bug to survive, got %+v", rc.Gate.Inline[0])
	}
}

// TestBuildLocalDryRun verifies the stage-1 context can be assembled from a
// local worktree without any token or provider configuration.
func TestBuildLocalDryRun(t *testing.T) {
	dir := initGitRepo(t)
	writeRepoFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "change")

	rc, err := Build(context.Background(), Options{
		Repo:       "local/test",
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
		ConfigPath: filepath.Join(t.TempDir(), ".sieve.yml"),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rc.PRNumber != 0 {
		t.Errorf("PRNumber = %d, want 0", rc.PRNumber)
	}
	if rc.Title != "feat" {
		t.Errorf("Title = %q, want feat", rc.Title)
	}
	if rc.Stats.FilesReviewed != 1 {
		t.Errorf("FilesReviewed = %d, want 1", rc.Stats.FilesReviewed)
	}
	if rc.Author == "" {
		t.Error("Author should be set from git config")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "local@example.com")
	runGit(t, dir, "config", "user.name", "Local User")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitDiff(dir, base string) ([]byte, error) {
	cmd := exec.Command("git", "diff", "--no-color", base+"...HEAD")
	cmd.Dir = dir
	return cmd.Output()
}

func firstAddedLine(files []diff.FileDiff) int {
	for _, f := range files {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Kind == diff.AddedLine {
					return l.NewNum
				}
			}
		}
	}
	return 0
}
