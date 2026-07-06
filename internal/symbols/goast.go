package symbols

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

// GoASTExtractor uses the stdlib go/parser and go/ast to extract Go symbols.
// It is the high-quality extractor for Go files and does not depend on any
// external grammar or WASM runtime.
type GoASTExtractor struct{}

// Extract parses src and returns top-level declarations plus imports.
func (GoASTExtractor) Extract(_ context.Context, path string, src []byte) ([]Symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		// A parse error is not fatal for context; return what we can. The
		// caller can fall back to regex if it needs more resilience.
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var syms []Symbol
	if f.Name != nil {
		syms = append(syms, Symbol{
			Name: f.Name.Name,
			Kind: KindPackage,
			Path: path,
			Line: fset.Position(f.Name.Pos()).Line,
		})
	}

	for _, imp := range f.Imports {
		name := ""
		if imp.Path != nil {
			name, _ = strconvUnquote(imp.Path.Value)
		}
		if name == "" {
			continue
		}
		kind := KindImport
		if imp.Name != nil {
			switch imp.Name.Name {
			case ".", "_":
				kind = KindImport
			}
		}
		syms = append(syms, Symbol{
			Name: name,
			Kind: kind,
			Path: path,
			Line: fset.Position(imp.Pos()).Line,
		})
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms = append(syms, funcSymbol(fset, path, d))
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					syms = append(syms, typeSymbols(fset, path, s, d.Doc)...)
				case *ast.ValueSpec:
					kind := KindVar
					if d.Tok == token.CONST {
						kind = KindConst
					}
					for _, name := range s.Names {
						syms = append(syms, Symbol{
							Name:      name.Name,
							Kind:      kind,
							Path:      path,
							Line:      fset.Position(name.Pos()).Line,
							Signature: typeString(s.Type),
							Doc:       docText(d.Doc),
						})
					}
				}
			}
		}
	}
	return syms, nil
}

func funcSymbol(fset *token.FileSet, path string, d *ast.FuncDecl) Symbol {
	sig := ""
	if d.Type != nil {
		sig = typeString(d.Type)
	}
	kind := KindFunc
	receiver := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = KindMethod
		receiver = typeString(d.Recv.List[0].Type)
	}
	return Symbol{
		Name:      d.Name.Name,
		Kind:      kind,
		Path:      path,
		Line:      fset.Position(d.Name.Pos()).Line,
		Signature: sig,
		Doc:       docText(d.Doc),
		Receiver:  receiver,
	}
}

func typeSymbols(fset *token.FileSet, path string, s *ast.TypeSpec, doc *ast.CommentGroup) []Symbol {
	sym := Symbol{
		Name: s.Name.Name,
		Kind: KindType,
		Path: path,
		Line: fset.Position(s.Name.Pos()).Line,
		Doc:  docText(doc),
	}
	if s.Type != nil {
		sym.Signature = typeString(s.Type)
		if st, ok := s.Type.(*ast.StructType); ok {
			var fields []Symbol
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					fields = append(fields, Symbol{
						Name:      name.Name,
						Kind:      KindField,
						Path:      path,
						Line:      fset.Position(name.Pos()).Line,
						Signature: typeString(field.Type),
						Container: s.Name.Name,
					})
				}
			}
			return append([]Symbol{sym}, fields...)
		}
	}
	return []Symbol{sym}
}

func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	lines := make([]string, 0, len(cg.List))
	for _, c := range cg.List {
		t := strings.TrimPrefix(c.Text, "//")
		t = strings.TrimPrefix(t, "/*")
		t = strings.TrimSuffix(t, "*/")
		lines = append(lines, strings.TrimSpace(t))
	}
	return strings.Join(lines, " ")
}

func typeString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var sb strings.Builder
	if err := printer.Fprint(&sb, token.NewFileSet(), expr); err != nil {
		return ""
	}
	return sb.String()
}

func strconvUnquote(s string) (string, error) {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], nil
	}
	return s, nil
}
