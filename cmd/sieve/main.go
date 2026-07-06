// Command sieve is a zero-infra, provider-agnostic PR reviewer.
// Stage 1: read-only dry run — fetch, parse, filter, dump ReviewContext.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/review"
	"github.com/gnanam1990/sieve/internal/version"
)

const (
	exitOK      = 0
	exitError   = 1
	exitPartial = 2 // context was truncated (diff cap or file-listing cap)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	switch args[0] {
	case "review":
		return runReview(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "sieve "+version.Version)
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return exitError
	}
}

func runReview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		repo     = fs.String("repo", "", "repository as owner/name (default: $GITHUB_REPOSITORY)")
		pr       = fs.Int("pr", 0, "pull request number (default: from $GITHUB_EVENT_PATH)")
		token    = fs.String("token", "", "GitHub token (default: $GITHUB_TOKEN)")
		cfgPath  = fs.String("config", config.DefaultFile, "path to config file")
		dryRun   = fs.Bool("dry-run", false, "fetch + parse + filter, write ReviewContext JSON, no writes")
		jsonOnly = fs.Bool("json-only", false, "suppress the stderr summary (CI use)")
		debug    = fs.Bool("debug", false, "debug logging")
		apiURL   = fs.String("api-url", "", "GitHub API base URL override (testing)")
	)
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	if *repo == "" {
		*repo = gh.RepoFromEnv()
	}
	if *pr == 0 {
		*pr = gh.PRNumberFromEnv()
	}
	if *token == "" {
		*token = os.Getenv("GITHUB_TOKEN")
	}
	if *repo == "" || *pr == 0 {
		fmt.Fprintln(stderr, "error: --repo and --pr are required (or GITHUB_REPOSITORY / GITHUB_EVENT_PATH in Actions)")
		return exitError
	}

	rc, err := review.Run(context.Background(), review.Options{
		Repo:       *repo,
		PRNumber:   *pr,
		Token:      *token,
		ConfigPath: *cfgPath,
		DryRun:     *dryRun,
		APIBaseURL: *apiURL,
		Log:        logger,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if err := rc.WriteJSON(stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if !*jsonOnly {
		rc.WriteSummary(stderr)
	}
	if rc.Truncated || rc.Stats.BatchesFailed > 0 {
		return exitPartial
	}
	return exitOK
}

func usage(w io.Writer) {
	fmt.Fprint(w, `sieve — zero-infra PR reviewer

usage:
  sieve review --repo owner/name --pr N             LLM review, findings on stdout (read-only)
  sieve review --repo owner/name --pr N --dry-run   context dump only, no LLM calls
  sieve version                                     print version

review flags:
  --repo       repository as owner/name (default: $GITHUB_REPOSITORY)
  --pr         pull request number (default: pull_request.number from $GITHUB_EVENT_PATH)
  --token      GitHub token (default: $GITHUB_TOKEN)
  --config     config file (default: .sieve.yml)
  --dry-run    skip the LLM pass; no GitHub writes ever happen either way
  --json-only  suppress the stderr summary
  --debug      debug logging
`)
}
