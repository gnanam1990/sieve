// Package blast computes a blast radius from a repo map and a set of changed
// files. It is the third layer of Stage 8 context depth: it surfaces files
// outside the diff that are likely affected because they import or define
// symbols touched by the change.
package blast

import (
	"sort"

	"github.com/gnanam1990/sieve/internal/repomap"
	"github.com/gnanam1990/sieve/internal/symbols"
)

// Radius holds direct and indirect affected files.
type Radius struct {
	Direct   []string `json:"direct"`
	Indirect []string `json:"indirect"`
}

// Compute returns files outside changed that are related via the repo map.
// Direct files import symbols defined in changed files or define symbols that
// changed files import. Indirect files import from direct files.
func Compute(rm *repomap.Map, changed []string) *Radius {
	changedSet := map[string]struct{}{}
	for _, p := range changed {
		changedSet[p] = struct{}{}
	}

	definedByChanged := map[string]struct{}{} // symbol names
	importsFromChanged := map[string]struct{}{}

	for _, e := range rm.Entries {
		if _, ok := changedSet[e.Path]; !ok {
			continue
		}
		for _, s := range e.Symbols {
			if s.Name != "" {
				definedByChanged[s.Name] = struct{}{}
			}
		}
		for _, imp := range e.Imports {
			importsFromChanged[imp] = struct{}{}
		}
	}

	directSet := map[string]struct{}{}
	for _, e := range rm.Entries {
		if _, ok := changedSet[e.Path]; ok {
			continue
		}
		// File is related if it imports a symbol defined in the change.
		for _, imp := range e.Imports {
			if _, ok := definedByChanged[imp]; ok {
				directSet[e.Path] = struct{}{}
				break
			}
		}
		// Or if it defines a symbol that the change imports.
		for _, s := range e.Symbols {
			if s.Kind == symbols.KindImport {
				continue
			}
			if _, ok := importsFromChanged[s.Name]; ok {
				directSet[e.Path] = struct{}{}
				break
			}
		}
	}

	// Indirect: files that import symbols exported by direct files.
	directExports := map[string]struct{}{}
	for _, e := range rm.Entries {
		if _, ok := directSet[e.Path]; !ok {
			continue
		}
		for _, s := range e.Symbols {
			if s.Kind == symbols.KindImport {
				continue
			}
			if s.Name != "" {
				directExports[s.Name] = struct{}{}
			}
		}
	}

	indirectSet := map[string]struct{}{}
	for sym := range directExports {
		for p := range rm.ImportIndex[sym] {
			if _, ok := changedSet[p]; ok {
				continue
			}
			if _, ok := directSet[p]; ok {
				continue
			}
			indirectSet[p] = struct{}{}
		}
	}

	return &Radius{
		Direct:   sortedStrings(directSet),
		Indirect: sortedStrings(indirectSet),
	}
}

func sortedStrings(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
