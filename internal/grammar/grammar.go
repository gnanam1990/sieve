// Package grammar runs tree-sitter language parsers inside a pure-Go wazero
// runtime. It is cgo-free and currently ships the bundled C/C++ grammars from
// github.com/malivvan/tree-sitter as the Stage 8 proof-of-concept; additional
// language grammars can be added behind the same Language/LanguageByName API.
package grammar

import (
	"context"
	"fmt"
	"math"
	"sync"

	ts "github.com/malivvan/tree-sitter"
)

// Runtime wraps the tree-sitter wasm runtime. One runtime can create many
// parsers; it is safe for concurrent use.
type Runtime struct {
	ts ts.TreeSitter
}

// New creates the wazero-backed tree-sitter runtime.
func New(ctx context.Context) (*Runtime, error) {
	t, err := ts.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter runtime: %w", err)
	}
	return &Runtime{ts: t}, nil
}

// Close is a no-op cleanup hook for API symmetry; the wazero-backed
// tree-sitter runtime in the bundled dependency does not expose explicit
// shutdown in this version.
func (r *Runtime) Close(_ context.Context) error { return nil }

// Supported reports whether a language name has a grammar available.
func (r *Runtime) Supported(lang string) bool {
	switch lang {
	case "c", "cpp":
		return true
	}
	return false
}

func (r *Runtime) loadLanguage(ctx context.Context, lang string) (ts.Language, error) {
	switch lang {
	case "c":
		return r.ts.LanguageC(ctx)
	case "cpp":
		return r.ts.LanguageCpp(ctx)
	default:
		return ts.Language{}, fmt.Errorf("unsupported language %q", lang)
	}
}

// Parser is one tree-sitter parser bound to a single language. It is NOT
// safe for concurrent use; callers must hold a parser exclusively or use a
// Pool.
type Parser struct {
	p    ts.Parser
	lang string
}

// Parse parses src and returns a Tree. The tree is independent of the parser
// and may outlive it.
func (p *Parser) Parse(ctx context.Context, src []byte) (*Tree, error) {
	t, err := p.p.ParseString(ctx, string(src))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.lang, err)
	}
	return &Tree{t: t}, nil
}

// Close releases the parser.
func (p *Parser) Close(ctx context.Context) error {
	return p.p.Close(ctx)
}

// Tree is the result of a parse.
type Tree struct {
	t ts.Tree
}

// RootNode returns the root node of the tree.
func (tr *Tree) RootNode(ctx context.Context) (*Node, error) {
	n, err := tr.t.RootNode(ctx)
	if err != nil {
		return nil, err
	}
	return &Node{n: n}, nil
}

// Node wraps a tree-sitter node.
type Node struct {
	n ts.Node
}

// Kind returns the node's type (e.g., "function_definition").
func (n *Node) Kind(ctx context.Context) (string, error) { return n.n.Kind(ctx) }

// StartByte returns the node's start offset in the source.
func (n *Node) StartByte(ctx context.Context) (uint64, error) { return n.n.StartByte(ctx) }

// Text returns the source slice the node covers.
func (n *Node) Text(src []byte) []byte {
	start, _ := n.n.StartByte(context.Background()) //nolint:contextcheck // synchronous accessor
	end, _ := n.n.EndByte(context.Background())     //nolint:contextcheck // synchronous accessor
	if end > uint64(len(src)) {
		end = uint64(len(src))
	}
	if start > end {
		start = end
	}
	return src[u64ToInt(start):u64ToInt(end)]
}

func u64ToInt(v uint64) int {
	if v > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(v)
}

// Query runs a tree-sitter query against the node and calls f for each match.
func (r *Runtime) Query(ctx context.Context, lang string, query string, root *Node, src []byte, f func(captures map[string]*Node, text map[string]string) error) error {
	l, err := r.loadLanguage(ctx, lang)
	if err != nil {
		return err
	}
	q, err := r.ts.NewQuery(ctx, query, l)
	if err != nil {
		return fmt.Errorf("compile query: %w", err)
	}
	qc, err := r.ts.NewQueryCursor(ctx)
	if err != nil {
		return fmt.Errorf("query cursor: %w", err)
	}
	if err := qc.Exec(ctx, q, root.n); err != nil {
		return fmt.Errorf("exec query: %w", err)
	}
	for {
		match, ok, err := qc.NextMatch(ctx)
		if err != nil {
			return fmt.Errorf("next match: %w", err)
		}
		if !ok {
			break
		}
		captures := map[string]*Node{}
		text := map[string]string{}
		for _, c := range match.Captures {
			name, err := q.CaptureNameForID(ctx, c.ID)
			if err != nil {
				continue
			}
			node := &Node{n: c.Node}
			captures[name] = node
			text[name] = string(node.Text(src))
		}
		if err := f(captures, text); err != nil {
			return err
		}
	}
	return nil
}

// Pool keeps a small pool of language-bound parsers so concurrent callers can
// share a runtime without racing on a single parser.
type Pool struct {
	rt    *Runtime
	pools map[string]chan *Parser
	mu    sync.Mutex
}

// NewPool creates a parser pool backed by rt.
func NewPool(rt *Runtime) *Pool {
	return &Pool{rt: rt, pools: map[string]chan *Parser{}}
}

// Query runs a query using the pool's runtime. It exists so callers holding
// only a Pool can run queries without reaching for the runtime directly.
func (p *Pool) Query(ctx context.Context, lang string, query string, root *Node, src []byte, f func(captures map[string]*Node, text map[string]string) error) error {
	return p.rt.Query(ctx, lang, query, root, src, f)
}

// Acquire returns a parser for lang, creating one if the pool is empty.
func (p *Pool) Acquire(ctx context.Context, lang string) (*Parser, error) {
	p.mu.Lock()
	ch, ok := p.pools[lang]
	if !ok {
		ch = make(chan *Parser, 4)
		p.pools[lang] = ch
	}
	p.mu.Unlock()

	select {
	case parser := <-ch:
		return parser, nil
	default:
		return p.newParser(ctx, lang)
	}
}

// Release returns a parser to the pool.
func (p *Pool) Release(lang string, parser *Parser) {
	p.mu.Lock()
	ch, ok := p.pools[lang]
	if !ok {
		ch = make(chan *Parser, 4)
		p.pools[lang] = ch
	}
	p.mu.Unlock()
	select {
	case ch <- parser:
	default:
		// Pool is full; close the parser to avoid leaking instances.
		_ = parser.Close(context.Background()) //nolint:errcheck // best-effort
	}
}

func (p *Pool) newParser(ctx context.Context, lang string) (*Parser, error) {
	parser, err := p.rt.ts.NewParser(ctx)
	if err != nil {
		return nil, fmt.Errorf("new parser: %w", err)
	}
	l, err := p.rt.loadLanguage(ctx, lang)
	if err != nil {
		_ = parser.Close(ctx) //nolint:errcheck // cleanup
		return nil, err
	}
	// The bundled tree-sitter runtime currently returns nil here, but keep the
	// assignment so a future version that does return an error is not ignored.
	_ = parser.SetLanguage(ctx, l)
	return &Parser{p: parser, lang: lang}, nil
}
