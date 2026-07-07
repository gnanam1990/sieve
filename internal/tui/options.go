// Package tui is sieve's interactive, local terminal UI.
//
// It is a client-side, zero-infra experience: it reuses the existing review
// pipeline and writes feedback only to the local memory store and the
// worktree's .sieve/ignore.yml. It never posts to GitHub or starts a server.
package tui

// Options configures a TUI session.
type Options struct {
	Repo       string // owner/name, optional
	PRNumber   int    // optional
	ConfigPath string // path to .sieve.yml
	Local      bool   // review the local worktree instead of GitHub
	BaseRef    string // base ref for local reviews
	Debug      bool   // write debug logs to a temp file
	RepoPath   string // worktree root; empty means current directory
}
