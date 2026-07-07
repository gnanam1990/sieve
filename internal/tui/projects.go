package tui

import (
	"os"
	"path/filepath"
	"sort"
)

// dataRoot returns the base directory where sieve stores per-repo event logs.
func dataRoot() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sieve"), nil
}

// listProjects scans the local data directory for repos that have an
// events.jsonl store. It returns them sorted by owner/name.
func listProjects() ([]project, error) {
	root, err := dataRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []project
	for _, host := range entries {
		if !host.IsDir() {
			continue
		}
		hostPath := filepath.Join(root, host.Name())
		owners, err := os.ReadDir(hostPath)
		if err != nil {
			continue
		}
		for _, owner := range owners {
			if !owner.IsDir() {
				continue
			}
			ownerPath := filepath.Join(hostPath, owner.Name())
			repos, err := os.ReadDir(ownerPath)
			if err != nil {
				continue
			}
			for _, repo := range repos {
				if !repo.IsDir() {
					continue
				}
				repoPath := filepath.Join(ownerPath, repo.Name())
				if _, err := os.Stat(filepath.Join(repoPath, "events.jsonl")); err != nil {
					continue
				}
				out = append(out, project{
					Host:  host.Name(),
					Owner: owner.Name(),
					Repo:  repo.Name(),
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Repo < out[j].Repo
	})
	return out, nil
}
