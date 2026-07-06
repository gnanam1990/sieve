package blast

import (
	"slices"
	"testing"

	"github.com/gnanam1990/sieve/internal/repomap"
	"github.com/gnanam1990/sieve/internal/symbols"
)

func TestComputeDirectAndIndirect(t *testing.T) {
	rm := &repomap.Map{
		Entries: []repomap.Entry{
			{
				Path: "changed.go",
				Symbols: []symbols.Symbol{
					{Name: "Helper", Kind: symbols.KindFunc, Path: "changed.go"},
					{Name: "fmt", Kind: symbols.KindImport, Path: "changed.go"},
				},
				Imports: []string{"fmt"},
			},
			{
				Path: "direct.go",
				Symbols: []symbols.Symbol{
					{Name: "DirectFn", Kind: symbols.KindFunc, Path: "direct.go"},
					{Name: "fmt", Kind: symbols.KindImport, Path: "direct.go"},
				},
				Imports: []string{"Helper"},
			},
			{
				Path: "indirect.go",
				Symbols: []symbols.Symbol{
					{Name: "Consumer", Kind: symbols.KindFunc, Path: "indirect.go"},
				},
				Imports: []string{"DirectFn"},
			},
		},
		SymbolIndex: map[string]map[string]struct{}{
			"Helper":   {"changed.go": {}},
			"DirectFn": {"direct.go": {}},
			"Consumer": {"indirect.go": {}},
		},
		ImportIndex: map[string]map[string]struct{}{
			"fmt":      {"changed.go": {}, "direct.go": {}},
			"Helper":   {"direct.go": {}},
			"DirectFn": {"indirect.go": {}},
		},
	}

	r := Compute(rm, []string{"changed.go"})
	if !slices.Contains(r.Direct, "direct.go") {
		t.Errorf("want direct.go in direct, got %v", r.Direct)
	}
	if slices.Contains(r.Direct, "changed.go") {
		t.Errorf("changed file should not appear in direct: %v", r.Direct)
	}
	if !slices.Contains(r.Indirect, "indirect.go") {
		t.Errorf("want indirect.go in indirect, got %v", r.Indirect)
	}
}

func TestComputeNoRadius(t *testing.T) {
	rm := &repomap.Map{
		Entries: []repomap.Entry{
			{Path: "a.go", Symbols: []symbols.Symbol{{Name: "A", Kind: symbols.KindFunc, Path: "a.go"}}},
			{Path: "b.go", Symbols: []symbols.Symbol{{Name: "B", Kind: symbols.KindFunc, Path: "b.go"}}},
		},
		SymbolIndex: map[string]map[string]struct{}{
			"A": {"a.go": {}},
			"B": {"b.go": {}},
		},
		ImportIndex: map[string]map[string]struct{}{},
	}

	r := Compute(rm, []string{"a.go"})
	if len(r.Direct) != 0 || len(r.Indirect) != 0 {
		t.Errorf("want empty radius, got direct=%v indirect=%v", r.Direct, r.Indirect)
	}
}
