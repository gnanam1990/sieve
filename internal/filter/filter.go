// Package filter decides which files of a PR are worth reviewing.
//
// Evaluation order: binary status -> default excludes -> configured globs.
// Globs use doublestar `**` semantics and are matched against NewPath,
// falling back to OldPath for deletes.
package filter

import (
	"fmt"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/gnanam1990/sieve/internal/diff"
)

// DefaultExcludes are always-on noise filters. go.mod is deliberately NOT
// here — dependency changes are reviewable; go.sum is noise.
var DefaultExcludes = []string{
	// vendored / build output directories
	"**/vendor/**", "**/node_modules/**", "**/dist/**", "**/build/**", "**/.next/**", "**/target/**",
	// minified / derived assets
	"**/*.min.js", "**/*.min.css", "**/*.map",
	// generated code
	"**/*.pb.go", "**/*_generated.go", "**/*.gen.go",
	// lockfiles
	"**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml", "**/bun.lockb",
	"**/go.sum", "**/Cargo.lock", "**/poetry.lock", "**/Pipfile.lock", "**/composer.lock", "**/Gemfile.lock",
	// snapshots
	"**/*.snap",
	// images / fonts / archives
	"**/*.png", "**/*.jpg", "**/*.jpeg", "**/*.gif", "**/*.webp", "**/*.ico",
	"**/*.woff", "**/*.woff2", "**/*.ttf", "**/*.eot",
	"**/*.zip", "**/*.gz", "**/*.tar",
}

// Result is one file with its keep/skip decision.
type Result struct {
	File       diff.FileDiff
	Skipped    bool
	SkipReason string
}

// Apply evaluates every file against binary status, default excludes, and
// the configured globs, in that order. Invalid configured globs are
// reported as an error rather than silently never matching.
func Apply(files []diff.FileDiff, configGlobs []string) ([]Result, error) {
	for _, g := range configGlobs {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("invalid exclude glob %q", g)
		}
	}
	results := make([]Result, 0, len(files))
	for _, f := range files {
		r := Result{File: f}
		path := f.NewPath
		if path == "" {
			path = f.OldPath
		}
		if f.Status == diff.Binary {
			r.Skipped, r.SkipReason = true, "binary file"
		} else if pat, ok := matchAny(DefaultExcludes, path); ok {
			r.Skipped, r.SkipReason = true, "default exclude: "+pat
		} else if pat, ok := matchAny(configGlobs, path); ok {
			r.Skipped, r.SkipReason = true, "config exclude: "+pat
		}
		results = append(results, r)
	}
	return results, nil
}

func matchAny(patterns []string, path string) (string, bool) {
	for _, p := range patterns {
		if ok, err := doublestar.Match(p, path); err == nil && ok {
			return p, true
		}
	}
	return "", false
}
