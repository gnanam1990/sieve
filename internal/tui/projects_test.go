package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListProjectsEmptyDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	got, err := listProjects()
	if err != nil {
		t.Fatalf("listProjects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no projects, got %d", len(got))
	}
}

func TestListProjectsDiscoversRepos(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)

	dirs := []string{
		filepath.Join(root, "sieve", "github.com", "owner-a", "repo1"),
		filepath.Join(root, "sieve", "github.com", "owner-b", "repo2"),
		filepath.Join(root, "sieve", "github.com", "owner-a", "repo3"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(d, "events.jsonl"), []byte("{}\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("write events: %v", err)
		}
	}
	// empty repo dir without events.jsonl should be ignored
	if err := os.MkdirAll(filepath.Join(root, "sieve", "github.com", "owner-c", "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := listProjects()
	if err != nil {
		t.Fatalf("listProjects: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(got))
	}
	if got[0].repoKey() != "owner-a/repo1" {
		t.Fatalf("unexpected first project: %s", got[0].repoKey())
	}
	if got[1].repoKey() != "owner-a/repo3" {
		t.Fatalf("unexpected second project: %s", got[1].repoKey())
	}
	if got[2].repoKey() != "owner-b/repo2" {
		t.Fatalf("unexpected third project: %s", got[2].repoKey())
	}
}
