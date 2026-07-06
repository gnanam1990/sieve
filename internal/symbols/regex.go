package symbols

import (
	"context"
	"regexp"
	"strings"
)

// RegexExtractor is a language-agnostic fallback that uses simple heuristics to
// pull function, class, and method names. It is intentionally low-fidelity:
// it never fails and gives the model *some* named anchors even for unsupported
// languages.
type RegexExtractor struct{}

var (
	funcLike = regexp.MustCompile(`(?m)^[ \t]*(?:func(?:tion)?\s+|def\s+|class\s+|void\s+|int\s+|bool\s+|string\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	classLike = regexp.MustCompile(`(?m)^[ \t]*(?:class|struct|interface|enum|type)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	methodLike = regexp.MustCompile(`(?m)^[ \t]*(?:func\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\s*\*?\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

// Extract runs regexes over src and returns discovered symbols.
func (RegexExtractor) Extract(_ context.Context, path string, src []byte) ([]Symbol, error) {
	seen := map[string]bool{}
	var syms []Symbol
	lines := strings.Split(string(src), "\n")

	for i, line := range lines {
		for _, m := range funcLike.FindAllStringSubmatchIndex(line, -1) {
			name := line[m[2]:m[3]]
			if skipToken(name) {
				continue
			}
			key := "func:" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			syms = append(syms, Symbol{Name: name, Kind: KindFunc, Path: path, Line: i + 1})
		}
		for _, m := range classLike.FindAllStringSubmatchIndex(line, -1) {
			name := line[m[2]:m[3]]
			if skipToken(name) {
				continue
			}
			key := "type:" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			syms = append(syms, Symbol{Name: name, Kind: KindType, Path: path, Line: i + 1})
		}
	}
	return syms, nil
}

// skipToken rejects obvious keyword matches that the regex might capture.
func skipToken(s string) bool {
	switch s {
	case "if", "for", "while", "switch", "catch", "return", "function", "def", "class", "struct", "interface", "enum", "type", "void", "int", "bool", "string":
		return true
	}
	return false
}
