package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/diff"
)

func TestRunReviewLocalWithFakeProvider(t *testing.T) {
	dir := initGitRepo(t)
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tprintln(\"ok\")\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tvar x *int\n\tprintln(*x)\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "bug")

	line := firstAddedLine(t, dir)
	findingsJSON := fmt.Sprintf(`{"findings":[{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"critical","Confidence":0.95,"Category":"bug","Title":"Nil pointer dereference","Body":"Dereferencing x without checking for nil."}]}`, line)
	fixturePath := writeFixture(t, findingsJSON)

	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixturePath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	rc, err := runReview(Options{
		Repo:       "local/test",
		ConfigPath: cfgPath,
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
	}, newLogger(false))
	if err != nil {
		t.Fatalf("runReview: %v", err)
	}
	if len(rc.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(rc.Findings))
	}
}

func TestCmdReviewReturnsMessage(t *testing.T) {
	dir := initGitRepo(t)
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tprintln(\"ok\")\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tvar x *int\n\tprintln(*x)\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "bug")

	line := firstAddedLine(t, dir)
	findingsJSON := fmt.Sprintf(`{"findings":[{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"critical","Confidence":0.95,"Category":"bug","Title":"Nil pointer dereference","Body":"Dereferencing x without checking for nil."}]}`, line)
	fixturePath := writeFixture(t, findingsJSON)

	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixturePath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := cmdReview(Options{
		Repo:       "local/test",
		ConfigPath: cfgPath,
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
	}, newLogger(false))

	msg := cmd()
	rdm, ok := msg.(reviewDoneMsg)
	if !ok {
		t.Fatalf("expected reviewDoneMsg, got %T", msg)
	}
	if rdm.err != nil {
		t.Fatalf("unexpected error: %v", rdm.err)
	}
	if rdm.rc == nil {
		t.Fatal("expected review context")
	}
}

func TestRunReviewMissingRepoReturnsError(t *testing.T) {
	_, err := runReview(Options{Repo: "", Local: false}, newLogger(false))
	if err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestReviewDoneModelFlow(t *testing.T) {
	dir := initGitRepo(t)
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tprintln(\"ok\")\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeRepoFile(t, dir, "service.go", "package service\n\nfunc Run() {\n\tvar x *int\n\tprintln(*x)\n}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "bug")

	line := firstAddedLine(t, dir)
	findingsJSON := fmt.Sprintf(`{"findings":[{"Path":"service.go","Line":%d,"Side":"RIGHT","Severity":"critical","Confidence":0.95,"Category":"bug","Title":"Nil pointer dereference","Body":"Dereferencing x without checking for nil."}]}`, line)
	fixturePath := writeFixture(t, findingsJSON)

	cfgPath := filepath.Join(t.TempDir(), ".sieve.yml")
	cfgYAML := fmt.Sprintf("provider:\n  type: fake\n  fixture: %q\n", fixturePath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := cmdReview(Options{
		Repo:       "local/test",
		ConfigPath: cfgPath,
		Local:      true,
		BaseRef:    "main",
		RepoPath:   dir,
	}, newLogger(false))

	msg := cmd()
	rdm := msg.(reviewDoneMsg)

	m := New(Options{})
	m.width = 80
	m.height = 24
	m = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = update(m, rdm)

	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
	if len(m.active) == 0 {
		t.Fatal("expected active findings after review")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
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

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	return path
}

func firstAddedLine(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "diff", "--no-color", "main...HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	files, err := diff.Parse(out)
	if err != nil {
		t.Fatalf("parse diff: %v", err)
	}
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
