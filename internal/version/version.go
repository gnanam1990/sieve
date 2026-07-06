// Package version holds build metadata, stamped at release time via ldflags:
//
//	go build -ldflags "\
//	  -X github.com/gnanam1990/sieve/internal/version.Version=v0.1.0 \
//	  -X github.com/gnanam1990/sieve/internal/version.Commit=abc1234 \
//	  -X github.com/gnanam1990/sieve/internal/version.Date=2026-07-06T00:00:00Z"
//
// Dev builds leave the defaults, so `sieve version` prints "dev".
package version

import (
	"fmt"
	"runtime"
)

// Build metadata. Version is also rendered in the stage-3 walkthrough footer,
// so a release binary shows a real tag there.
var (
	Version = "dev"     // release tag, e.g. v0.1.0
	Commit  = "none"    // short git SHA
	Date    = "unknown" // RFC3339 build timestamp
)

// String is the one-line version, e.g. "v0.1.0". Used in the walkthrough footer.
func String() string { return Version }

// Info is the full multi-field build report printed by `sieve version`.
func Info() string {
	return fmt.Sprintf(
		"sieve %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  platform: %s/%s",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH,
	)
}
