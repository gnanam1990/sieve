package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "local@example.com")
	runGit(t, dir, "config", "user.name", "Local User")
	return dir
}

func TestRepoNameFromRemoteURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo", "owner/repo"},
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"git://github.com/owner/repo.git", "owner/repo"},
		{"https://gitlab.com/group/sub/repo.git", "sub/repo"},
		{"/absolute/path/to/repo.git", "to/repo"},
	}
	for _, c := range cases {
		if got := parseRemoteURL(c.in); got != c.want {
			t.Errorf("parseRemoteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRepoNameFromDirectory(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Base(dir)
	want := "local/" + base
	if got := RepoName(dir); got != want {
		t.Errorf("RepoName(%q) = %q, want %q", dir, got, want)
	}
}

func TestPullRequestLocal(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "a\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")
	baseSHA := runGitOut(t, dir, "rev-parse", "HEAD")

	runGit(t, dir, "checkout", "-b", "feat")
	writeFile(t, dir, "a.txt", "a\nb\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "change")

	pr, err := PullRequest(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 0 {
		t.Errorf("Number = %d, want 0", pr.Number)
	}
	if pr.Title != "feat" {
		t.Errorf("Title = %q, want feat", pr.Title)
	}
	if pr.User.Login != "local@example.com" {
		t.Errorf("User.Login = %q, want local@example.com", pr.User.Login)
	}
	if pr.Base.SHA != baseSHA[:len(pr.Base.SHA)] {
		t.Errorf("Base.SHA = %q, want %q", pr.Base.SHA, baseSHA)
	}
}

func TestDiffLocal(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "a\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeFile(t, dir, "a.txt", "a\nb\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "change")

	data, truncated, err := Diff(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("local diff should never be truncated")
	}
	if !contains(string(data), "+b") {
		t.Errorf("diff missing added line:\n%s", data)
	}
}

func TestListFilesLocal(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b.txt", "x\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feat")
	writeFile(t, dir, "a.txt", "a\nb\n")
	writeFile(t, dir, "c.txt", "new\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "change")

	files, truncated, err := ListFiles(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("local listing should never be truncated")
	}
	byName := map[string]string{}
	for _, f := range files {
		byName[f.Filename] = f.Status
	}
	if byName["a.txt"] != "modified" {
		t.Errorf("a.txt status = %q, want modified", byName["a.txt"])
	}
	if byName["c.txt"] != "added" {
		t.Errorf("c.txt status = %q, want added", byName["c.txt"])
	}
	if _, ok := byName["b.txt"]; ok {
		t.Error("b.txt should not appear in changed files")
	}
}

func TestReadFileLocal(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "nested/file.go", "package x\n")
	got, err := ReadFile(dir, "nested/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package x\n" {
		t.Errorf("ReadFile = %q, want package x", got)
	}
}

func TestParseNameStatusRenameAndCopy(t *testing.T) {
	input := "R100\told.go\tnew.go\nC100\ta.go\tb.go\nT\tc.go\nU\td.go\n"
	files, err := parseNameStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"new.go": "renamed",
		"b.go":   "copied",
		"c.go":   "modified",
		"d.go":   "modified",
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Filename] = f.Status
	}
	for path, status := range want {
		if got[path] != status {
			t.Errorf("%s status = %q, want %q", path, got[path], status)
		}
		if path == "new.go" && files[0].PreviousFilename != "old.go" {
			t.Errorf("rename previous filename = %q, want old.go", files[0].PreviousFilename)
		}
	}
}

func TestParseNameStatusInvalid(t *testing.T) {
	if _, err := parseNameStatus("nonsense"); err == nil {
		t.Fatal("want error for invalid name-status line")
	}
	if _, err := parseNameStatus("R100\tonly-one-column"); err == nil {
		t.Fatal("want error for invalid rename line")
	}
	if _, err := parseNameStatus("C100\tonly-one-column"); err == nil {
		t.Fatal("want error for invalid copy line")
	}
}

func TestPullRequestGitUserFallback(t *testing.T) {
	dir := initRepo(t)
	// Keep the commit valid using the local identity, then unset it and disable
	// global config so gitUser has nothing to fall back to.
	writeFile(t, dir, "a.txt", "a\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")
	runGit(t, dir, "config", "--unset", "user.email")
	runGit(t, dir, "config", "--unset", "user.name")

	// gitUser runs with the process environment; temporarily clear global config.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	pr, err := PullRequest(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pr.User.Login != "local" {
		t.Errorf("User.Login = %q, want local", pr.User.Login)
	}
}

func TestRepoNameFromRemoteInRepo(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "remote", "add", "origin", "git@github.com:owner/repo.git")
	if got := RepoName(dir); got != "owner/repo" {
		t.Errorf("RepoName = %q, want owner/repo", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && findSub(s, sub) }

func findSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
