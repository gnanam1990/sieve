package grammar

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func loadSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return b
}

func newRuntime(t *testing.T) (*Runtime, context.Context) {
	t.Helper()
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return rt, ctx
}

func TestSupported(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	if !rt.Supported("c") {
		t.Error("expected c to be supported")
	}
	if !rt.Supported("cpp") {
		t.Error("expected cpp to be supported")
	}
	if rt.Supported("go") {
		t.Error("expected go to be unsupported")
	}
}

func TestParseCRoot(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire parser: %v", err)
	}
	defer pool.Release("c", parser)

	src := loadSample(t, "sample.c")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root node: %v", err)
	}
	kind, err := root.Kind(ctx)
	if err != nil {
		t.Fatalf("root kind: %v", err)
	}
	if kind != "translation_unit" {
		t.Errorf("root kind = %q, want translation_unit", kind)
	}
}

func TestQueryFunctions(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire parser: %v", err)
	}
	defer pool.Release("c", parser)

	src := loadSample(t, "sample.c")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root node: %v", err)
	}

	query := `
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name
    parameters: (parameter_list) @params
  )
) @func
`
	var got []map[string]string
	err = rt.Query(ctx, "c", query, root, src, func(_ map[string]*Node, text map[string]string) error {
		got = append(got, map[string]string{
			"name":  text["name"],
			"func":  text["func"],
			"params": text["params"],
		})
		return nil
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 function matches, got %d", len(got))
	}
	names := []string{got[0]["name"], got[1]["name"]}
	sort.Strings(names)
	want := []string{"add", "create"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("function names mismatch (-want +got):\n%s", diff)
	}
}

func TestPoolReuse(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	p1, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	pool.Release("c", p1)
	p2, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	// p2 may be the same parser instance; the important property is that it
	// still works after being returned to the pool.
	src := []byte("int main(void) { return 0; }")
	tree, err := p2.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse after reuse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	kind, err := root.Kind(ctx)
	if err != nil {
		t.Fatalf("kind: %v", err)
	}
	if kind != "translation_unit" {
		t.Errorf("kind = %q, want translation_unit", kind)
	}
	pool.Release("c", p2)
}

func TestUnsupportedLanguage(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	_, err := pool.Acquire(ctx, "go")
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestBadQuery(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := loadSample(t, "sample.c")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	err = rt.Query(ctx, "c", "[this is invalid", root, src, func(map[string]*Node, map[string]string) error { return nil })
	if err == nil {
		t.Fatal("expected error for invalid query")
	}
}

func TestQueryCallbackError(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := loadSample(t, "sample.c")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	marker := errors.New("stop")
	err = rt.Query(ctx, "c", "(function_definition) @f", root, src, func(map[string]*Node, map[string]string) error {
		return marker
	})
	if !errors.Is(err, marker) {
		t.Fatalf("expected marker error, got %v", err)
	}
}

func TestNodeMethods(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := loadSample(t, "sample.c")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	kind, err := root.Kind(ctx)
	if err != nil {
		t.Fatalf("root kind: %v", err)
	}
	if kind != "translation_unit" {
		t.Errorf("kind = %q, want translation_unit", kind)
	}
	start, err := root.StartByte(ctx)
	if err != nil {
		t.Fatalf("start byte: %v", err)
	}
	if start != 0 {
		t.Errorf("start byte = %d, want 0", start)
	}
	if got := string(root.Text(src)); got == "" {
		t.Error("expected non-empty root text")
	}
}

func TestPoolReleaseCap(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parsers := make([]*Parser, 6)
	for i := range parsers {
		p, err := pool.Acquire(ctx, "c")
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		parsers[i] = p
	}
	for _, p := range parsers {
		pool.Release("c", p)
	}
	// After filling the pool beyond its per-language cap, an acquire should
	// still succeed (either reusing a pooled parser or creating a new one).
	p, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire after cap: %v", err)
	}
	pool.Release("c", p)
}

func TestParserClose(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := parser.Close(ctx); err != nil {
		t.Fatalf("close parser: %v", err)
	}
}

func TestReleaseUnseenLanguage(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Releasing under a language key the pool has never seen exercises the lazy
	// channel-creation path in Release. The parser is then discarded because the
	// cross-language key is meaningless in real use.
	pool.Release("cpp", parser)
}

func TestTextWithTruncatedSource(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := []byte("int main(void) { return 0; }")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	got := root.Text(src[:5])
	if !bytes.Equal(got, src[:5]) {
		t.Errorf("Text(truncated) = %q, want %q", got, src[:5])
	}
}

func TestTextStartAfterEnd(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := []byte("int abc; int xyz;")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	// Find an identifier node whose start is beyond a truncated source so the
	// start > clamped-end branch is exercised.
	var idents []*Node
	err = rt.Query(ctx, "c", "(identifier) @name", root, src, func(captures map[string]*Node, _ map[string]string) error {
		idents = append(idents, captures["name"])
		return nil
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var ident *Node
	for _, n := range idents {
		start, _ := n.StartByte(ctx)
		if start > 4 {
			ident = n
			break
		}
	}
	if ident == nil {
		t.Fatal("no identifier beyond offset 4 found")
	}
	got := ident.Text(src[:4])
	if len(got) != 0 {
		t.Errorf("expected empty text when node starts after truncated source, got %q", got)
	}
}

func TestNewParserWithInvalidLanguage(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	_, err := pool.Acquire(ctx, "rust")
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestParseCpp(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "cpp")
	if err != nil {
		t.Fatalf("acquire cpp: %v", err)
	}
	defer pool.Release("cpp", parser)

	src := []byte("int main() { return 0; }")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse cpp: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	kind, err := root.Kind(ctx)
	if err != nil {
		t.Fatalf("kind: %v", err)
	}
	if kind != "translation_unit" {
		t.Errorf("cpp root kind = %q, want translation_unit", kind)
	}
}

func TestQueryUnsupportedLanguage(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := []byte("int x;")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	err = rt.Query(ctx, "go", "(identifier) @id", root, src, func(map[string]*Node, map[string]string) error { return nil })
	if err == nil {
		t.Fatal("expected error for unsupported query language")
	}
}

func TestPoolQuery(t *testing.T) {
	rt, ctx := newRuntime(t)
	defer rt.Close(ctx) //nolint:errcheck // best-effort

	pool := NewPool(rt)
	parser, err := pool.Acquire(ctx, "c")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pool.Release("c", parser)

	src := []byte("int foo(void) { return 1; }")
	tree, err := parser.Parse(ctx, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root, err := tree.RootNode(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	var names []string
	err = pool.Query(ctx, "c", "(identifier) @name", root, src, func(_ map[string]*Node, text map[string]string) error {
		names = append(names, text["name"])
		return nil
	})
	if err != nil {
		t.Fatalf("pool query: %v", err)
	}
	if !slices.Contains(names, "foo") {
		t.Errorf("want foo in identifiers, got %v", names)
	}
}


