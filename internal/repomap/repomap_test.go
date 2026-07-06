package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/symbols"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

func TestBuildIndexesGoAndPython(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": `package a
import "fmt"
func Helper() {}
`,
		"b.py": `def foo():
    pass
`,
	})

	m, err := Build(context.Background(), Options{Root: root, Registry: symbols.Default()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m.Entries))
	}
	if _, ok := m.SymbolIndex["Helper"]; !ok {
		t.Errorf("want Helper in symbol index, got %v", m.SymbolIndex)
	}
	if _, ok := m.SymbolIndex["foo"]; !ok {
		t.Errorf("want foo in symbol index, got %v", m.SymbolIndex)
	}
	if paths, ok := m.SymbolIndex["Helper"]; !ok || len(paths) != 1 {
		t.Errorf("want one Helper path, got %v", paths)
	}
	if _, ok := m.ImportIndex["fmt"]; !ok {
		t.Errorf("want fmt in import index, got %v", m.ImportIndex)
	}
}

func TestBuildExclude(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": `package a
func A() {}
`,
		"vendor/b.go": `package b
func B() {}
`,
	})

	m, err := Build(context.Background(), Options{
		Root:     root,
		Registry: symbols.Default(),
		Exclude:  []string{"vendor/**"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	names := entryPaths(m.Entries)
	if slices.Contains(names, "vendor/b.go") {
		t.Errorf("excluded vendor file present in entries: %v", names)
	}
	if !slices.Contains(names, "a.go") {
		t.Errorf("want a.go in entries, got %v", names)
	}
}

func TestBuildMaxFiles(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": `package a
func A() {}
`,
		"b.go": `package b
func B() {}
`,
		"c.go": `package c
func C() {}
`,
	})

	m, err := Build(context.Background(), Options{
		Root:     root,
		Registry: symbols.Default(),
		MaxFiles: 2,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 2 {
		t.Errorf("want 2 entries with max_files=2, got %d", len(m.Entries))
	}
}

func TestBuildLanguageFilter(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": `package a
func A() {}
`,
		"b.py": `def foo():
    pass
`,
	})

	m, err := Build(context.Background(), Options{
		Root:      root,
		Registry:  symbols.Default(),
		Languages: []string{"go"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	langs := entryLanguages(m.Entries)
	if slices.Contains(langs, "python") {
		t.Errorf("python entry should be filtered out: %v", langs)
	}
	if !slices.Contains(langs, "go") {
		t.Errorf("want go entry, got %v", langs)
	}
}

func TestBuildNoSymbols(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"README.md": "# hi\n",
	})

	m, err := Build(context.Background(), Options{Root: root, Registry: symbols.Default()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("want 1 entry for README, got %d", len(m.Entries))
	}
	if len(m.Entries[0].Symbols) != 0 {
		t.Errorf("want no symbols for markdown, got %v", m.Entries[0].Symbols)
	}
}

func TestBuildMaxTokens(t *testing.T) {
	root := t.TempDir()
	// Each line is ~40 bytes + newline, so 300 lines is well above a token=10 budget.
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "func functionNumber%d() { return %d }\n", i, i)
	}
	writeFiles(t, root, map[string]string{
		"big.go": b.String(),
		"small.go": "package small\nfunc Small() {}\n",
	})

	m, err := Build(context.Background(), Options{
		Root:       root,
		Registry:   symbols.Default(),
		MaxTokens:  10,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 1 {
		t.Errorf("want only small.go under token cap, got %v", entryPaths(m.Entries))
	}
	if len(m.Entries) > 0 && m.Entries[0].Path != "small.go" {
		t.Errorf("expected small.go, got %v", m.Entries[0].Path)
	}
}

func TestBuildExtractError(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"x.go": "package x\n",
	})
	m, err := Build(context.Background(), Options{
		Root:     root,
		Registry: symbols.Registry{".go": badExtractor{}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 0 {
		t.Errorf("expected extractor errors to drop files, got %v", entryPaths(m.Entries))
	}
}

type badExtractor struct{}

func (badExtractor) Extract(context.Context, string, []byte) ([]symbols.Symbol, error) {
	return nil, fmt.Errorf("boom")
}

func TestBuildContextCancel(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, Options{Root: root, Registry: symbols.Default()})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestBuildReadError(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go": "package a\nfunc A() {}\n",
	})
	if err := os.Chmod(filepath.Join(root, "a.go"), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(filepath.Join(root, "a.go"), 0o644) //nolint:errcheck // best-effort

	m, err := Build(context.Background(), Options{Root: root, Registry: symbols.Default()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 0 {
		t.Errorf("expected unreadable file to be skipped, got %v", entryPaths(m.Entries))
	}
}

func TestUniqueStrings(t *testing.T) {
	in := []string{"a", "b", "a", "c", "b"}
	got := uniqueStrings(in)
	want := []string{"a", "b", "c"}
	if !slices.Equal(got, want) {
		t.Errorf("uniqueStrings(%v) = %v, want %v", in, got, want)
	}
}

func TestSlicesContains(t *testing.T) {
	if !slicesContains([]string{"Go", "Python"}, "go") {
		t.Error("expected case-insensitive match")
	}
	if slicesContains([]string{"Go"}, "rust") {
		t.Error("expected no match")
	}
}

func entryPaths(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func entryLanguages(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Language
	}
	return out
}
