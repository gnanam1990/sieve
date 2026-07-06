package symbols

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gnanam1990/sieve/internal/grammar"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGoASTExtractor(t *testing.T) {
	src := readFixture(t, "sample.go")
	ex := GoASTExtractor{}
	syms, err := ex.Extract(context.Background(), "testdata/sample.go", src)
	if err != nil {
		t.Fatal(err)
	}

	want := []Symbol{
		{Name: "sample", Kind: KindPackage, Path: "testdata/sample.go", Line: 2},
		{Name: "context", Kind: KindImport, Path: "testdata/sample.go", Line: 5},
		{Name: "fmt", Kind: KindImport, Path: "testdata/sample.go", Line: 6},
		{Name: "MaxItems", Kind: KindConst, Path: "testdata/sample.go", Line: 10, Doc: "MaxItems is the maximum number of items."},
		{Name: "Item", Kind: KindType, Path: "testdata/sample.go", Line: 13, Signature: "struct {\n\tName\tstring\n\tValue\tint\n}", Doc: "Item is one item."},
		{Name: "Name", Kind: KindField, Container: "Item", Path: "testdata/sample.go", Line: 14, Signature: "string"},
		{Name: "Value", Kind: KindField, Container: "Item", Path: "testdata/sample.go", Line: 15, Signature: "int"},
		{Name: "Processor", Kind: KindType, Path: "testdata/sample.go", Line: 19, Signature: "struct{}", Doc: "Processor does things."},
		{Name: "Process", Kind: KindMethod, Receiver: "*Processor", Path: "testdata/sample.go", Line: 22, Signature: "func(ctx context.Context, it Item) error", Doc: "Process processes an item."},
		{Name: "Helper", Kind: KindFunc, Path: "testdata/sample.go", Line: 30, Signature: "func(items []Item) int", Doc: "Helper is a package-level helper."},
	}

	if len(syms) != len(want) {
		t.Fatalf("want %d symbols, got %d: %+v", len(want), len(syms), syms)
	}
	for i, w := range want {
		g := syms[i]
		if g.Name != w.Name || g.Kind != w.Kind || g.Line != w.Line || g.Container != w.Container || g.Signature != w.Signature || g.Doc != w.Doc || g.Receiver != w.Receiver {
			t.Errorf("symbol %d: want %+v, got %+v", i, w, g)
		}
	}
}

func TestGoASTExtractorParseError(t *testing.T) {
	src := []byte("package sample\nfunc unclosed( {\n")
	ex := GoASTExtractor{}
	_, err := ex.Extract(context.Background(), "bad.go", src)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRegexExtractor(t *testing.T) {
	src := []byte(`
def process(item):
    return item.value

class Processor:
    def handle(self, item):
        process(item)
`)
	ex := RegexExtractor{}
	syms, err := ex.Extract(context.Background(), "sample.py", src)
	if err != nil {
		t.Fatal(err)
	}
	names := symbolNames(syms)
	for _, want := range []string{"process", "Processor", "handle"} {
		if !slices.Contains(names, want) {
			t.Errorf("want %q in names, got %v", want, names)
		}
	}
}

func TestRegistryExtract(t *testing.T) {
	r := Default()
	goSrc := readFixture(t, "sample.go")
	syms, err := r.Extract(context.Background(), "sample.go", goSrc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(symbolNames(syms), "Helper") {
		t.Errorf("want Helper from GoAST extractor, got %v", symbolNames(syms))
	}

	pySrc := []byte("def foo(): pass\n")
	syms, err = r.Extract(context.Background(), "script.py", pySrc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(symbolNames(syms), "foo") {
		t.Errorf("want foo from regex extractor, got %v", symbolNames(syms))
	}

	// Unknown extension falls back to regex.
	syms, err = r.Extract(context.Background(), "weird.ext", []byte("void bar() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(symbolNames(syms), "bar") {
		t.Errorf("want bar from regex fallback, got %v", symbolNames(syms))
	}
}

func TestLanguage(t *testing.T) {
	cases := map[string]string{
		"a.go":       "go",
		"b.rs":       "rust",
		"c.py":       "python",
		"d.js":       "javascript",
		"e.ts":       "typescript",
		"f.tsx":      "typescript",
		"g.jsx":      "jsx",
		"h.c":        "c",
		"i.cpp":      "cpp",
		"j.hpp":      "cpp",
		"k.java":     "java",
		"l.rb":       "ruby",
		"m.sh":       "shell",
		"unknown.xyz": "",
	}
	for path, want := range cases {
		if got := Language(path); got != want {
			t.Errorf("Language(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSkipToken(t *testing.T) {
	for _, yes := range []string{"if", "for", "class", "struct", "int", "bool", "function"} {
		if !skipToken(yes) {
			t.Errorf("skipToken(%q) should be true", yes)
		}
	}
	if skipToken("Process") {
		t.Error("skipToken(\"Process\") should be false")
	}
}

func TestStrconvUnquote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`"foo"`, "foo"},
		{`'bar'`, "bar"},
		{`noquotes`, "noquotes"},
	}
	for _, c := range cases {
		got, err := strconvUnquote(c.in)
		if err != nil {
			t.Fatalf("strconvUnquote(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("strconvUnquote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDocTextBlockComment(t *testing.T) {
	ex := GoASTExtractor{}
	src := []byte(`package x
/* Foo is a type. */
type Foo int
`)
	syms, err := ex.Extract(context.Background(), "x.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range syms {
		if s.Name == "Foo" && s.Doc != "Foo is a type." {
			t.Errorf("want block comment in Doc, got %q", s.Doc)
		}
	}
}

func TestRegistryExtractError(t *testing.T) {
	r := Registry{".bad": badExtractor{}}
	_, err := r.Extract(context.Background(), "file.bad", []byte("x"))
	if err == nil {
		t.Fatal("expected error from bad extractor")
	}
}

type badExtractor struct{}

func (badExtractor) Extract(context.Context, string, []byte) ([]Symbol, error) {
	return nil, fmt.Errorf("boom")
}

func TestRegexExtractorSkipsKeywords(t *testing.T) {
	src := []byte(`
if (x) foo()
def real():
    return 1
class iffy:
    pass
`)
	ex := RegexExtractor{}
	syms, err := ex.Extract(context.Background(), "x.py", src)
	if err != nil {
		t.Fatal(err)
	}
	names := symbolNames(syms)
	for _, bad := range []string{"if", "return", "class"} {
		if slices.Contains(names, bad) {
			t.Errorf("regex should not extract keyword %q, got %v", bad, names)
		}
	}
}

func symbolNames(syms []Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

func TestGrammarExtractor(t *testing.T) {
	ctx := context.Background()
	rt, err := grammar.New(ctx)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)

	src := readFixture(t, "sample.c")
	ex := NewGrammarExtractor(pool)
	syms, err := ex.Extract(ctx, "testdata/sample.c", src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	names := symbolNames(syms)
	for _, want := range []string{"add", "create", "Point"} {
		if !slices.Contains(names, want) {
			t.Errorf("want %q in symbols, got %v", want, names)
		}
	}

	var addSym *Symbol
	for i := range syms {
		if syms[i].Name == "add" {
			addSym = &syms[i]
			break
		}
	}
	if addSym == nil {
		t.Fatal("add symbol not found")
	}
	if addSym.Kind != KindFunc {
		t.Errorf("add kind = %q, want %q", addSym.Kind, KindFunc)
	}
	if addSym.Line != 4 {
		t.Errorf("add line = %d, want 4", addSym.Line)
	}
	if addSym.Doc != "/* add adds two integers. */" {
		t.Errorf("add doc = %q, want block comment", addSym.Doc)
	}
}

func TestGrammarExtractorUnsupportedLanguage(t *testing.T) {
	ctx := context.Background()
	rt, err := grammar.New(ctx)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)
	ex := NewGrammarExtractor(pool)
	_, err = ex.Extract(ctx, "file.go", []byte("package x\n"))
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestDefaultWithGrammar(t *testing.T) {
	ctx := context.Background()
	rt, err := grammar.New(ctx)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)
	r := DefaultWithGrammar(pool)

	goSrc := readFixture(t, "sample.go")
	syms, err := r.Extract(ctx, "sample.go", goSrc)
	if err != nil {
		t.Fatalf("extract go: %v", err)
	}
	if !slices.Contains(symbolNames(syms), "Helper") {
		t.Errorf("want Helper from GoAST, got %v", symbolNames(syms))
	}

	cSrc := readFixture(t, "sample.c")
	syms, err = r.Extract(ctx, "sample.c", cSrc)
	if err != nil {
		t.Fatalf("extract c: %v", err)
	}
	if !slices.Contains(symbolNames(syms), "add") {
		t.Errorf("want add from grammar extractor, got %v", symbolNames(syms))
	}
}

func TestGrammarExtractorLineComment(t *testing.T) {
	ctx := context.Background()
	rt, err := grammar.New(ctx)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer rt.Close(ctx) //nolint:errcheck // best-effort
	pool := grammar.NewPool(rt)
	src := []byte("// foo returns one.\nint foo(void) { return 1; }\n")
	syms, err := NewGrammarExtractor(pool).Extract(ctx, "x.c", src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var foo *Symbol
	for i := range syms {
		if syms[i].Name == "foo" {
			foo = &syms[i]
			break
		}
	}
	if foo == nil {
		t.Fatal("foo symbol not found")
	}
	if !strings.Contains(foo.Doc, "// foo returns one.") {
		t.Errorf("want line comment in doc, got %q", foo.Doc)
	}
}

func TestTypeStringVariations(t *testing.T) {
	src := []byte(`package x

type I interface{}
type M map[string]int
type S []int
type P *int
type F func(int) error
`)
	ex := GoASTExtractor{}
	syms, err := ex.Extract(context.Background(), "x.go", src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := map[string]string{
		"I": "interface{}",
		"M": "map[string]int",
		"S": "[]int",
		"P": "*int",
		"F": "func(int) error",
	}
	for _, s := range syms {
		if w, ok := want[s.Name]; ok {
			if s.Signature != w {
				t.Errorf("%s signature = %q, want %q", s.Name, s.Signature, w)
			}
			delete(want, s.Name)
		}
	}
	for k := range want {
		t.Errorf("missing symbol %s", k)
	}
}

func TestLineCommentDoc(t *testing.T) {
	src := []byte(`package x
// Foo is a function.
func Foo() {}
`)
	ex := GoASTExtractor{}
	syms, err := ex.Extract(context.Background(), "x.go", src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, s := range syms {
		if s.Name == "Foo" && s.Doc != "Foo is a function." {
			t.Errorf("want line comment in doc, got %q", s.Doc)
		}
	}
}

func TestLineOfHelpers(t *testing.T) {
	src := []byte("line1\nline2\nline3\n")
	if lineOf(src, 0) != 1 {
		t.Errorf("lineOf(0) = %d, want 1", lineOf(src, 0))
	}
	if lineOf(src, 7) != 2 { // inside line2
		t.Errorf("lineOf(7) = %d, want 2", lineOf(src, 7))
	}
	if lineOf(src, 100) != 4 { // beyond end
		t.Errorf("lineOf(100) = %d, want 4", lineOf(src, 100))
	}
}

func TestPrecedingCommentLine(t *testing.T) {
	src := []byte("// line comment\nint x;\n")
	got := string(precedingComment(src, uint64(strings.Index(string(src), "int"))))
	if got != "// line comment" {
		t.Errorf("precedingComment = %q, want line comment", got)
	}
}

func TestPrecedingCommentNone(t *testing.T) {
	src := []byte("\nint x;\n")
	got := precedingComment(src, uint64(strings.Index(string(src), "int")))
	if len(got) != 0 {
		t.Errorf("precedingComment = %q, want empty", got)
	}
}

func TestPrecedingCommentCode(t *testing.T) {
	src := []byte("int a;\nint x;\n")
	got := precedingComment(src, uint64(strings.Index(string(src[4:]), "int")+4))
	if len(got) != 0 {
		t.Errorf("precedingComment = %q, want empty", got)
	}
}
