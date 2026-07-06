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
	"strings"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
	"github.com/gnanam1990/sieve/internal/version"
)

const (
	exitOK      = 0
	exitError   = 1
	exitPartial = 2 // truncated context, a failed review batch, or a failed inline post
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
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "learnings":
		return runLearnings(args[1:], stdout, stderr)
	case "stats":
		return runStats(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.Info())
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
		doPost   = fs.Bool("post", false, "post results to the PR (walkthrough + inline review); the ONLY way to enable writes")
		full     = fs.Bool("full", false, "force a full re-review (disable incremental delta review)")
		jsonOnly = fs.Bool("json-only", false, "suppress the stderr summary (CI use)")
		debug    = fs.Bool("debug", false, "debug logging")
		apiURL   = fs.String("api-url", "", "GitHub API base URL override (testing)")
	)
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *dryRun && *doPost {
		fmt.Fprintln(stderr, "error: --dry-run and --post are mutually exclusive")
		return exitError
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	// R4 fork safety: inside a GitHub Action, a fork PR runs with a read-only
	// token and no secrets (the model key is withheld). Detect it before any
	// provider call and skip cleanly rather than failing cryptically. A
	// --dry-run needs no secrets, so it is allowed to proceed.
	if !*dryRun && os.Getenv("GITHUB_ACTIONS") == "true" {
		if ev := gh.EventFromEnv(); ev.IsFork() {
			forkSkipNotice(stderr, ev)
			return exitOK
		}
	}

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
		Post:       *doPost,
		Full:       *full,
		APIBaseURL: *apiURL,
		Log:        logger,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		// R4: in Actions, surface a fix hint in the step summary. The error text
		// names the missing env var (never its value) — see review.newProvider.
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			writeStepSummary("### sieve failed\n\n```\n" + err.Error() + "\n```\n\n" +
				"If this is a missing API key, set the secret named by the action's " +
				"`api_key_env_name` input (default `SIEVE_API_KEY`) in your workflow `env:`. " +
				"See [docs/forks.md](docs/forks.md).\n")
		}
		return exitError
	}
	if err := rc.WriteJSON(stdout); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if !*jsonOnly {
		rc.WriteSummary(stderr)
	}
	if rc.Truncated || rc.Stats.BatchesFailed > 0 || rc.Stats.InlinePostFailed > 0 {
		return exitPartial
	}
	return exitOK
}

// forkSkipNotice reports a fork-PR skip to stderr and the Actions step summary
// and is the reason the run exits 0 rather than failing.
func forkSkipNotice(stderr io.Writer, ev gh.Event) {
	head := ev.HeadRepo
	if head == "" {
		head = "(deleted fork)"
	}
	msg := fmt.Sprintf("fork PR (%s → %s): secrets are unavailable to this workflow; "+
		"skipping review. Same-repo PRs are the supported surface — see docs/forks.md.",
		head, ev.BaseRepo)
	fmt.Fprintln(stderr, "notice:", msg)
	writeStepSummary("### sieve skipped a fork PR\n\n" + msg + "\n")
}

// writeStepSummary appends markdown to the GitHub Actions step summary when
// running under Actions; a no-op otherwise.
func writeStepSummary(md string) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644) //nolint:gosec // Actions-provided path
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck // best-effort summary
	_, _ = io.WriteString(f, md)
}

// runSync rebuilds the local outcome store for a PR from GitHub.
func runSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository as owner/name (default: $GITHUB_REPOSITORY)")
	pr := fs.Int("pr", 0, "pull request number")
	token := fs.String("token", "", "GitHub token (default: $GITHUB_TOKEN)")
	debug := fs.Bool("debug", false, "debug logging")
	apiURL := fs.String("api-url", "", "GitHub API base URL override (testing)")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	logger := newLogger(stderr, *debug)
	resolveRepoPR(repo, pr, token)
	if *repo == "" || *pr == 0 {
		fmt.Fprintln(stderr, "error: sync needs --repo and --pr")
		return exitError
	}
	n, err := review.Sync(context.Background(), review.Options{Repo: *repo, PRNumber: *pr, Token: *token, APIBaseURL: *apiURL, Log: logger})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "synced %d events from GitHub\n", n)
	return exitOK
}

// runLearnings drafts repository rules from negative outcomes and updates
// .sieve/learnings.md in the worktree (never commits).
func runLearnings(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("learnings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository as owner/name (default: $GITHUB_REPOSITORY)")
	cfgPath := fs.String("config", config.DefaultFile, "path to config file")
	debug := fs.Bool("debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	logger := newLogger(stderr, *debug)
	if *repo == "" {
		*repo = gh.RepoFromEnv()
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "error: learnings needs --repo")
		return exitError
	}
	diff, err := review.Learnings(context.Background(), review.Options{Repo: *repo, ConfigPath: *cfgPath, Log: logger})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if diff == "" {
		fmt.Fprintln(stdout, "no new learnings")
		return exitOK
	}
	fmt.Fprint(stdout, diff)
	fmt.Fprintf(stderr, "\nupdated %s — review and commit it yourself (sieve never commits)\n", review.LearningsFile)
	return exitOK
}

// runStats renders the local outcome store as a table (or JSON).
func runStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "repository as owner/name (default: $GITHUB_REPOSITORY)")
	jsonOut := fs.Bool("json", false, "emit JSON instead of a table")
	debug := fs.Bool("debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	logger := newLogger(stderr, *debug)
	if *repo == "" {
		*repo = gh.RepoFromEnv()
	}
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" {
		fmt.Fprintln(stderr, "error: stats needs --repo owner/name")
		return exitError
	}
	store := memory.Open("github.com", owner, name, logger)
	events, corrupt, err := store.Read()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if corrupt > 0 {
		fmt.Fprintf(stderr, "warning: skipped %d corrupt event line(s)\n", corrupt)
	}
	stats := memory.Aggregate(events)
	totals := memory.Sum(events)
	if *jsonOut {
		if err := memory.WriteStatsJSON(stdout, stats, totals); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		return exitOK
	}
	memory.WriteStats(stdout, stats, totals)
	return exitOK
}

// newLogger builds the stderr slog logger at the requested level.
func newLogger(stderr io.Writer, debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

// resolveRepoPR fills repo/pr/token from the environment when unset.
func resolveRepoPR(repo *string, pr *int, token *string) {
	if *repo == "" {
		*repo = gh.RepoFromEnv()
	}
	if *pr == 0 {
		*pr = gh.PRNumberFromEnv()
	}
	if *token == "" {
		*token = os.Getenv("GITHUB_TOKEN")
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `sieve — zero-infra PR reviewer

usage:
  sieve review --repo owner/name --pr N             LLM review, findings on stdout (read-only)
  sieve review --repo owner/name --pr N --post      review AND post the results to the PR
  sieve review --repo owner/name --pr N --dry-run   context dump only, no LLM calls
  sieve sync --repo owner/name --pr N               rebuild the local outcome store from GitHub
  sieve learnings --repo owner/name                 draft repo rules from outcomes -> .sieve/learnings.md
  sieve stats --repo owner/name [--json]            per-category addressed-rate + reactions
  sieve version                                     print version

review flags:
  --repo       repository as owner/name (default: $GITHUB_REPOSITORY)
  --pr         pull request number (default: pull_request.number from $GITHUB_EVENT_PATH)
  --token      GitHub token (default: $GITHUB_TOKEN)
  --config     config file (default: .sieve.yml)
  --dry-run    skip the LLM pass; no GitHub writes ever happen either way
  --post       post the walkthrough + inline review to the PR — the ONLY switch
               that enables writes; no config key can turn posting on
  --full       force a full re-review (disable incremental delta review)
  --json-only  suppress the stderr summary
  --debug      debug logging

exit codes: 0 ok · 1 error · 2 partial (truncated input, failed batch, or a
failed inline comment post)
`)
}
