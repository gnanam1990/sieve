// Package version holds the build version, set via ldflags:
//
//	go build -ldflags "-X github.com/gnanam1990/sieve/internal/version.Version=v0.1.0"
package version

// Version is the sieve build version.
var Version = "dev"
