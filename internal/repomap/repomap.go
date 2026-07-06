// Package repomap builds a repo-wide symbol and import index from source files.
// It is the second layer of Stage 8 context depth: the default "symbols" depth
// attaches extracted symbols only for changed files; "repomap" attaches a wider
// repo map capped by max_files / max_tokens / context_langs.
package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/gnanam1990/sieve/internal/symbols"
)

// Entry is one file's extracted context.
type Entry struct {
	Path     string           `json:"path"`
	Language string           `json:"language"`
	Symbols  []symbols.Symbol `json:"symbols,omitempty"`
	Imports  []string         `json:"imports,omitempty"`
}

// Map is the repo-wide index.
type Map struct {
	Entries []Entry

	// SymbolIndex maps a symbol name to the paths that define it.
	SymbolIndex map[string]map[string]struct{}
	// ImportIndex maps an import path/name to the files that import it.
	ImportIndex map[string]map[string]struct{}
}

// Options controls how Build walks the repository.
type Options struct {
	Root        string
	Include     []string // globs; empty means all source files
	Exclude     []string // globs
	MaxFiles    int      // 0 = unlimited
	MaxTokens   int      // rough token budget; 0 = unlimited
	Languages   []string // empty = all
	Registry    symbols.Registry
}

// Build scans the repository and returns a Map.
func Build(ctx context.Context, opts Options) (*Map, error) {
	if opts.Registry == nil {
		opts.Registry = symbols.Default()
	}
	if opts.Root == "" {
		opts.Root = "."
	}

	m := &Map{
		SymbolIndex: map[string]map[string]struct{}{},
		ImportIndex: map[string]map[string]struct{}{},
	}

	include := opts.Include
	if len(include) == 0 {
		include = []string{"**/*"}
	}

	var matched []string
	fsys := os.DirFS(opts.Root)
	for _, pattern := range include {
		hits, err := doublestar.Glob(fsys, pattern)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", pattern, err)
		}
		matched = append(matched, hits...)
	}
	sort.Strings(matched)
	matched = uniqueStrings(matched)

	files := make([]string, 0, len(matched))
fileLoop:
	for _, rel := range matched {
		for _, exc := range opts.Exclude {
			ok, err := doublestar.Match(exc, rel)
			if err != nil {
				return nil, fmt.Errorf("exclude pattern %q: %w", exc, err)
			}
			if ok {
				continue fileLoop
			}
		}
		full := filepath.Join(opts.Root, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		if len(opts.Languages) > 0 {
			lang := symbols.Language(rel)
			if lang == "" || !slicesContains(opts.Languages, lang) {
				continue
			}
		}
		files = append(files, rel)
	}

	if opts.MaxFiles > 0 && len(files) > opts.MaxFiles {
		files = files[:opts.MaxFiles]
	}

	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		src, err := os.ReadFile(filepath.Join(opts.Root, path))
		if err != nil {
			continue
		}
		if opts.MaxTokens > 0 && tokenLen(string(src)) > opts.MaxTokens {
			continue
		}
		syms, err := opts.Registry.Extract(ctx, path, src)
		if err != nil {
			continue
		}
		entry := Entry{
			Path:     path,
			Language: symbols.Language(path),
			Symbols:  syms,
		}
		for _, s := range syms {
			if s.Name == "" {
				continue
			}
			if s.Kind == symbols.KindImport {
				entry.Imports = append(entry.Imports, s.Name)
				addToIndex(m.ImportIndex, s.Name, path)
			} else {
				addToIndex(m.SymbolIndex, s.Name, path)
			}
		}
		m.Entries = append(m.Entries, entry)
	}

	return m, nil
}

func addToIndex(idx map[string]map[string]struct{}, key, path string) {
	if _, ok := idx[key]; !ok {
		idx[key] = map[string]struct{}{}
	}
	idx[key][path] = struct{}{}
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// tokenLen is a fast, lossy token estimator: (bytes / 4) + newline bonus.
// It is good enough for the coarse budget guard.
func tokenLen(s string) int {
	n := len(s) / 4
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}