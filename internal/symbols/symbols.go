// Package symbols extracts declarations and references from source files for
// richer review context. It is language-pluggable: a registry maps file
// extensions to Extractor implementations, with a regex fallback for unknown
// languages.
package symbols

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gnanam1990/sieve/internal/grammar"
)

// Kind classifies a symbol.
type Kind string

// Symbol kinds.
const (
	KindFunc    Kind = "func"
	KindMethod  Kind = "method"
	KindType    Kind = "type"
	KindVar     Kind = "var"
	KindConst   Kind = "const"
	KindImport  Kind = "import"
	KindField   Kind = "field"
	KindPackage Kind = "package"
	KindUnknown Kind = "unknown"
)

// Symbol is one extracted declaration or reference.
type Symbol struct {
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	Container string `json:"container,omitempty"`
}

// Extractor pulls symbols out of a single source file.
type Extractor interface {
	Extract(ctx context.Context, path string, src []byte) ([]Symbol, error)
}

// Registry maps file extensions to extractors. The zero value is usable and
// falls back to the regex extractor for every language.
type Registry map[string]Extractor

// Default returns a registry with Go handled by the stdlib go/ast extractor
// and everything else by the regex fallback.
func Default() Registry {
	return Registry{
		".go":   &GoASTExtractor{},
		".rs":   &RegexExtractor{},
		".py":   &RegexExtractor{},
		".js":   &RegexExtractor{},
		".ts":   &RegexExtractor{},
		".tsx":  &RegexExtractor{},
		".jsx":  &RegexExtractor{},
		".c":    &RegexExtractor{},
		".cc":   &RegexExtractor{},
		".cpp":  &RegexExtractor{},
		".h":    &RegexExtractor{},
		".hpp":  &RegexExtractor{},
		".cxx":  &RegexExtractor{},
		".java": &RegexExtractor{},
		".rb":   &RegexExtractor{},
		".sh":   &RegexExtractor{},
	}
}

// DefaultWithGrammar returns a registry that uses the grammar-backed
// tree-sitter extractor for C/C++ files and the same extractors as Default
// for everything else. Callers must supply a parser pool initialised for the
// languages they want to enable.
func DefaultWithGrammar(pool *grammar.Pool) Registry {
	ex := NewGrammarExtractor(pool)
	r := Default()
	r[".c"] = ex
	r[".cc"] = ex
	r[".cpp"] = ex
	r[".h"] = ex
	r[".hpp"] = ex
	r[".cxx"] = ex
	return r
}

// Extract picks the extractor by extension and runs it. Unsupported
// extensions fall back to the regex extractor so every file gets at least
// token-level names.
func (r Registry) Extract(ctx context.Context, path string, src []byte) ([]Symbol, error) {
	ext := strings.ToLower(filepath.Ext(path))
	e, ok := r[ext]
	if !ok {
		e = &RegexExtractor{}
	}
	syms, err := e.Extract(ctx, path, src)
	if err != nil {
		return nil, fmt.Errorf("extract symbols %s: %w", path, err)
	}
	for i := range syms {
		syms[i].Path = path
	}
	return syms, nil
}

// Language returns a display name for a file extension.
func Language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".jsx":
		return "jsx"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".sh":
		return "shell"
	default:
		return ""
	}
}
