package symbols

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gnanam1990/sieve/internal/grammar"
)

// GrammarExtractor uses a wazero tree-sitter parser pool for C/C++ symbol
// extraction. It is the Stage 8 proof-of-concept for grammar-backed context
// beyond what regex or stdlib AST can offer.
type GrammarExtractor struct {
	pool *grammar.Pool
}

// NewGrammarExtractor creates an extractor backed by pool.
func NewGrammarExtractor(pool *grammar.Pool) *GrammarExtractor {
	return &GrammarExtractor{pool: pool}
}

// Extract implements Extractor for C and C++ source files.
func (e *GrammarExtractor) Extract(ctx context.Context, path string, src []byte) ([]Symbol, error) {
	lang := Language(path)
	if lang != "c" && lang != "cpp" {
		return nil, fmt.Errorf("grammar extractor unsupported language %q", lang)
	}
	parser, err := e.pool.Acquire(ctx, lang)
	if err != nil {
		return nil, fmt.Errorf("acquire parser: %w", err)
	}
	defer e.pool.Release(lang, parser)

	tree, err := parser.Parse(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		return nil, fmt.Errorf("root node: %w", err)
	}

	syms, err := e.extractC(ctx, root, src, path)
	if err != nil {
		return nil, err
	}
	return syms, nil
}

func (e *GrammarExtractor) extractC(ctx context.Context, root *grammar.Node, src []byte, path string) ([]Symbol, error) {
	var syms []Symbol
	var errs []error

	functionQuery := `
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name
    parameters: (parameter_list) @params
  )
) @func
`
	err := e.pool.Query(ctx, Language(path), functionQuery, root, src, func(captures map[string]*grammar.Node, text map[string]string) error {
		node := captures["func"]
		start, err := node.StartByte(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("function start byte: %w", err))
			return nil
		}
		syms = append(syms, Symbol{
			Name:      strings.TrimSpace(text["name"]),
			Kind:      KindFunc,
			Path:      path,
			Line:      lineOf(src, start),
			Signature: fmt.Sprintf("%s%s", text["name"], text["params"]),
			Doc:       precedingComment(src, start),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("function query: %w", err)
	}

	typeQuery := `
(type_definition
  declarator: (type_identifier) @name
) @type
`
	err = e.pool.Query(ctx, Language(path), typeQuery, root, src, func(captures map[string]*grammar.Node, text map[string]string) error {
		node := captures["type"]
		start, err := node.StartByte(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("type start byte: %w", err))
			return nil
		}
		syms = append(syms, Symbol{
			Name: strings.TrimSpace(text["name"]),
			Kind: KindType,
			Path: path,
			Line: lineOf(src, start),
			Doc:  precedingComment(src, start),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("type query: %w", err)
	}

	if len(errs) > 0 {
		return syms, fmt.Errorf("partial extraction: %v", errs[0])
	}
	return syms, nil
}

func clampU64ToInt(v uint64, limit int) int {
	if limit < 0 {
		limit = 0
	}
	m := uint64(limit) //nolint:gosec // bounded after limit >= 0 check
	if v > m {
		v = m
	}
	if v > uint64(math.MaxInt) {
		v = uint64(math.MaxInt)
	}
	return int(v) //nolint:gosec // bounded to MaxInt before conversion
}

func lineOf(src []byte, offset uint64) int {
	off := clampU64ToInt(offset, len(src))
	return bytes.Count(src[:off], []byte("\n")) + 1
}

func precedingComment(src []byte, offset uint64) string {
	off := clampU64ToInt(offset, len(src))
	// Split every line up to offset; the last element is the partial current
	// line, so preceding lines are candidates for a doc comment.
	lines := bytes.Split(src[:off], []byte("\n"))
	var comments []string
	for i := len(lines) - 2; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			break
		}
		switch {
		case bytes.HasPrefix(line, []byte("//")):
			comments = append([]string{string(line)}, comments...)
		case bytes.HasPrefix(line, []byte("/*")) && bytes.HasSuffix(line, []byte("*/")):
			return strings.TrimSpace(string(line))
		default:
			return ""
		}
	}
	if len(comments) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(comments, "\n"))
}
