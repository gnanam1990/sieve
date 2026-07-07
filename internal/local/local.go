// Package local reads a git worktree diff and metadata so sieve can review a
// local branch without a GitHub token or App PEM. It shells out to git and
// produces the same shapes the GitHub client returns (PullRequest, PRFile,
// raw diff bytes), so the review pipeline can stay unchanged.
package local

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gnanam1990/sieve/internal/gh"
)

// PullRequest returns synthetic PR metadata for the current branch against
// base. The title is the branch name, the author is the local git user, and
// number is 0 because there is no real PR.
func PullRequest(ctx context.Context, dir, base string) (*gh.PullRequest, error) {
	baseSHA, err := revParse(ctx, dir, base)
	if err != nil {
		return nil, fmt.Errorf("resolve base ref %q: %w", base, err)
	}
	headSHA, err := revParse(ctx, dir, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	headRef, err := revParse(ctx, dir, "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	headRef = strings.TrimSpace(headRef)
	if headRef == "" || headRef == "HEAD" {
		headRef = "HEAD"
	}

	title := headRef
	if title == "HEAD" {
		title = "local review"
	}

	return &gh.PullRequest{
		Number: 0,
		Title:  title,
		Body:   "",
		State:  "open",
		Draft:  false,
		User: struct {
			Login string `json:"login"`
		}{Login: gitUser(dir)},
		Base: struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}{Ref: base, SHA: baseSHA},
		Head: struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}{Ref: headRef, SHA: headSHA},
	}, nil
}

// Diff returns the raw unified diff for base...HEAD. The truncated flag is
// always false because the local diff is not capped like GitHub's API.
func Diff(ctx context.Context, dir, base string) ([]byte, bool, error) {
	out, err := git(ctx, dir, "diff", "--no-color", base+"...HEAD")
	if err != nil {
		return nil, false, fmt.Errorf("git diff %s...HEAD: %w", base, err)
	}
	return out, false, nil
}

// ListFiles returns the files changed between base and HEAD, parsed from
// `git diff --name-status`. Additions/deletions are filled from `git diff
// --numstat` when available.
func ListFiles(ctx context.Context, dir, base string) ([]gh.PRFile, bool, error) {
	out, err := git(ctx, dir, "diff", "--no-color", "--name-status", base+"...HEAD")
	if err != nil {
		return nil, false, fmt.Errorf("git diff --name-status %s...HEAD: %w", base, err)
	}
	files, err := parseNameStatus(string(out))
	if err != nil {
		return nil, false, err
	}
	if err := fillNumstat(ctx, dir, base, files); err != nil {
		// Numstat is best-effort; missing counts do not block the review.
		return files, false, nil
	}
	return files, false, nil
}

// ReadFile reads a path relative to the repo root. It is the local-mode
// equivalent of the GitHub contents API used for file-content attachment and
// learnings loading.
func ReadFile(dir, path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, path)) //nolint:gosec // path is repo-relative
}

// RepoName guesses owner/name from the origin remote, falling back to the
// directory name. It is used for summary/SARIF output when --repo is omitted.
func RepoName(dir string) string {
	out, err := git(context.Background(), dir, "remote", "get-url", "origin")
	if err == nil {
		if r := parseRemoteURL(strings.TrimSpace(string(out))); r != "" {
			return r
		}
	}
	base := filepath.Base(dir)
	if base == "" || base == "/" || base == "." {
		base = "repo"
	}
	return "local/" + base
}

func revParse(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := git(ctx, dir, append([]string{"rev-parse"}, args...)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitUser(dir string) string {
	out, err := git(context.Background(), dir, "config", "user.email")
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	out, err = git(context.Background(), dir, "config", "user.name")
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return "local"
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func parseNameStatus(out string) ([]gh.PRFile, error) {
	var files []gh.PRFile
	scan := bufio.NewScanner(bytes.NewReader([]byte(out)))
	for scan.Scan() {
		line := scan.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid name-status line: %q", line)
		}
		status := strings.ToUpper(parts[0])
		status = strings.TrimRight(status, "0123456789") // R100 -> R

		var f gh.PRFile
		switch status {
		case "A":
			f.Status = "added"
			f.Filename = parts[1]
		case "M":
			f.Status = "modified"
			f.Filename = parts[1]
		case "D":
			f.Status = "deleted"
			f.Filename = parts[1]
		case "R":
			if len(parts) != 3 {
				return nil, fmt.Errorf("invalid rename line: %q", line)
			}
			f.Status = "renamed"
			f.Filename = parts[2]
			f.PreviousFilename = parts[1]
		case "C":
			if len(parts) != 3 {
				return nil, fmt.Errorf("invalid copy line: %q", line)
			}
			f.Status = "copied"
			f.Filename = parts[2]
			f.PreviousFilename = parts[1]
		case "T":
			f.Status = "modified"
			f.Filename = parts[1]
		default:
			// Unknown status (X/U) — keep the path but treat as modified so the
			// review is not silently dropped.
			f.Status = "modified"
			f.Filename = parts[1]
		}
		files = append(files, f)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

func fillNumstat(ctx context.Context, dir, base string, files []gh.PRFile) error {
	out, err := git(ctx, dir, "diff", "--no-color", "--numstat", base+"...HEAD")
	if err != nil {
		return err
	}
	byPath := make(map[string]*gh.PRFile)
	for i := range files {
		byPath[files[i].Filename] = &files[i]
	}
	scan := bufio.NewScanner(bytes.NewReader(out))
	for scan.Scan() {
		line := scan.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		f, ok := byPath[path]
		if !ok {
			continue
		}
		if parts[0] != "-" {
			f.Additions, _ = strconv.Atoi(parts[0])
		}
		if parts[1] != "-" {
			f.Deletions, _ = strconv.Atoi(parts[1])
		}
	}
	return scan.Err()
}

func parseRemoteURL(raw string) string {
	raw = strings.TrimSuffix(raw, ".git")

	// SSH / SCP-like: git@host:owner/repo or host:owner/repo.
	if i := strings.Index(raw, "@"); i >= 0 {
		raw = raw[i+1:]
		if j := strings.Index(raw, ":"); j >= 0 {
			raw = raw[j+1:]
		}
		parts := strings.Split(strings.Trim(raw, "/"), "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
		return ""
	}

	for _, prefix := range []string{"https://", "http://", "git://", "ssh://"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	if i := strings.Index(raw, ":"); i >= 0 {
		// host:path scp-like without user.
		if !strings.Contains(raw[i+1:], "/") {
			return ""
		}
		raw = raw[i+1:]
	}

	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}
