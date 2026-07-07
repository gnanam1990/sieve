// Command sieve is a zero-infra, provider-agnostic PR reviewer.
// Stage 1: read-only dry run — fetch, parse, filter, dump ReviewContext.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/local"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
	"github.com/gnanam1990/sieve/internal/sarif"
	"github.com/gnanam1990/sieve/internal/server"
	"github.com/gnanam1990/sieve/internal/version"
	"github.com/gnanam1990/sieve/internal/webhook"
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
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "admin":
		return runAdmin(args[1:], stdout, stderr)
	case "ignore":
		return runIgnore(args[1:], stdout, stderr)
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
		repo      = fs.String("repo", "", "repository as owner/name (default: $GITHUB_REPOSITORY)")
		pr        = fs.Int("pr", 0, "pull request number (default: from $GITHUB_EVENT_PATH)")
		token     = fs.String("token", "", "GitHub token (default: $GITHUB_TOKEN)")
		cfgPath   = fs.String("config", config.DefaultFile, "path to config file")
		dryRun    = fs.Bool("dry-run", false, "fetch + parse + filter, write ReviewContext JSON, no writes")
		doPost    = fs.Bool("post", false, "post results to the PR (walkthrough + inline review); the ONLY way to enable writes")
		full      = fs.Bool("full", false, "force a full re-review (disable incremental delta review)")
		jsonOnly  = fs.Bool("json-only", false, "suppress the stderr summary (CI use)")
		debug     = fs.Bool("debug", false, "debug logging")
		apiURL    = fs.String("api-url", "", "GitHub API base URL override (testing)")
		sarifOut  = fs.String("sarif", "", "write a SARIF v2.1.0 report to this file for github/codeql-action/upload-sarif")
		localMode = fs.Bool("local", false, "review the local git worktree against --base (no GitHub token needed)")
		baseRef   = fs.String("base", "main", "base ref for --local review (default: main)")
	)
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *dryRun && *doPost {
		fmt.Fprintln(stderr, "error: --dry-run and --post are mutually exclusive")
		return exitError
	}
	if *localMode && *doPost {
		fmt.Fprintln(stderr, "error: --local and --post are mutually exclusive")
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

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	if *localMode {
		if *repo == "" {
			*repo = local.RepoName(cwd)
		}
	} else {
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
		RepoPath:   cwd,
		Local:      *localMode,
		BaseRef:    *baseRef,
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
	if *sarifOut != "" {
		if err := writeSarif(*sarifOut, rc); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
	}
	if rc.Truncated || rc.Stats.BatchesFailed > 0 || rc.Stats.InlinePostFailed > 0 {
		return exitPartial
	}
	return exitOK
}

// runAdmin queries a running sieve daemon's /admin endpoint.
func runAdmin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "http://127.0.0.1:8787/admin", "daemon /admin URL")
	secretEnv := fs.String("secret-env", "SIEVE_ADMIN_SECRET", "env var holding the admin password")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	secret := os.Getenv(*secretEnv)
	if secret == "" {
		fmt.Fprintf(stderr, "error: %s is unset\n", *secretEnv)
		return exitError
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, *url, nil)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	req.SetBasicAuth("admin", secret)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(stderr, "error: %s\n%s\n", resp.Status, body)
		return exitError
	}
	var stats webhook.AdminStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Version:\t%s\n", stats.Version)
	fmt.Fprintf(tw, "Uptime (s):\t%.0f\n", stats.UptimeSeconds)
	fmt.Fprintf(tw, "Queue depth:\t%d\n", stats.QueueDepth)
	fmt.Fprintf(tw, "Dead letters:\t%d\n", stats.DeadLetters)
	fmt.Fprintf(tw, "Running jobs:\t%d\n", len(stats.Running))
	fmt.Fprintf(tw, "Recent dead:\t%d\n", len(stats.RecentDead))
	_ = tw.Flush()

	if len(stats.Running) > 0 {
		fmt.Fprintln(stdout, "\nRunning:")
		for _, r := range stats.Running {
			fmt.Fprintf(stdout, "  %s\n", r)
		}
	}
	if len(stats.RecentDead) > 0 {
		fmt.Fprintln(stdout, "\nRecent dead letters:")
		for _, d := range stats.RecentDead {
			fmt.Fprintf(stdout, "  %s#%d @ %s  attempts=%d  err=%q\n", d.Repo, d.PR, d.Timestamp.Format(time.RFC3339), d.Attempts, d.Error)
		}
	}
	return exitOK
}

// writeSarif emits a SARIF report for the active gate findings.
func writeSarif(path string, rc *review.ReviewContext) error {
	opts := sarif.Options{
		Version: version.String(),
		Repo:    rc.Repo,
		BaseSHA: rc.BaseSHA,
	}
	report := sarif.FromGate(rc.Gate, opts)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create sarif file: %w", err)
	}
	if err := sarif.WriteJSON(f, report); err != nil {
		_ = f.Close()
		return fmt.Errorf("write sarif file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close sarif file: %w", err)
	}
	return nil
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

// runServe runs the self-host daemon: validate server.yml, then receive
// webhooks and review PRs until SIGTERM/SIGINT.
func runServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/sieve/server.yml", "path to server.yml")
	debug := fs.Bool("debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	sc, err := server.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	// Startup validation: one line per check, then "ready" or the first failure.
	checks, verr := sc.Validate()
	for _, c := range checks {
		mark := "ok"
		if c.Err() != nil {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "[%s] %s\n", mark, c.Label())
	}
	if verr != nil {
		fmt.Fprintln(stderr, "error:", verr)
		return exitError
	}

	logger := newLogger(stderr, *debug)
	srv, err := server.New(sc, server.Options{Log: logger})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	fmt.Fprintln(stdout, "ready")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	return exitOK
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

// runIgnore adds a suppression rule to .sieve/ignore.yml in the current
// worktree. It never commits — the maintainer reviews and commits the change.
func runIgnore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ignore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		fp       = fs.String("fingerprint", "", "exact sieve fingerprint to ignore")
		path     = fs.String("path", "", "path glob pattern (e.g. 'vendor/**' or '**/*.pb.go')")
		category = fs.String("category", "", "finding category to ignore (bug|security|perf|correctness|test|style)")
		severity = fs.String("severity", "", "severity to ignore (critical|major|minor|nit)")
		title    = fs.String("title", "", "substring match against finding titles")
		reason   = fs.String("reason", "", "human note explaining the rule")
		expires  = fs.String("expires", "", "expiration date YYYY-MM-DD")
		file     = fs.String("file", ignore.DefaultFile, "ignore file path")
		debug    = fs.Bool("debug", false, "debug logging")
	)
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *fp == "" && *path == "" && *category == "" && *severity == "" && *title == "" {
		fmt.Fprintln(stderr, "error: ignore needs at least one of --fingerprint, --path, --category, --severity, --title")
		return exitError
	}

	logger := newLogger(stderr, *debug)

	rule := ignore.Rule{
		Fingerprint: *fp,
		Path:        *path,
		Category:    *category,
		Severity:    *severity,
		Title:       *title,
		Reason:      *reason,
	}
	if *expires != "" {
		if _, err := time.Parse("2006-01-02", *expires); err != nil {
			fmt.Fprintf(stderr, "error: invalid --expires %q: %v\n", *expires, err)
			return exitError
		}
		rule.Expires, _ = time.Parse("2006-01-02", *expires)
	}

	manual, managed, hasMarker, err := readIgnoreFile(*file, logger)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	rules, err := ignore.Parse([]byte(managed))
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	rules, _ = rules.Add(rule)

	if err := os.MkdirAll(filepath.Dir(*file), 0o755); err != nil { //nolint:gosec // worktree dir
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	f, err := os.Create(*file)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	defer f.Close() //nolint:errcheck // best-effort

	if strings.TrimSpace(manual) != "" {
		fmt.Fprint(f, strings.TrimRight(manual, "\n"))
		fmt.Fprint(f, "\n\n")
	}
	if hasMarker || strings.TrimSpace(manual) != "" {
		fmt.Fprintln(f, ignore.Marker)
	}
	if err := rules.WriteYAML(f); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "added rule to %s\n", *file)
	return exitOK
}

// readIgnoreFile splits a .sieve/ignore.yml into a hand-written preamble and the
// machine-managed rule block. A missing file is an empty managed block.
func readIgnoreFile(path string, log *slog.Logger) (manual, managed string, hasMarker bool, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // worktree path
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	body := string(data)
	idx := strings.Index(body, ignore.Marker)
	if idx < 0 {
		return "", body, false, nil
	}
	manual = body[:idx]
	managed = strings.TrimPrefix(body[idx+len(ignore.Marker):], "\n")
	return manual, managed, true, nil
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
  sieve review --local [--base main]                 review the local git worktree without GitHub
  sieve sync --repo owner/name --pr N               rebuild the local outcome store from GitHub
  sieve learnings --repo owner/name                 draft repo rules from outcomes -> .sieve/learnings.md
  sieve stats --repo owner/name [--json]            per-category addressed-rate + reactions
  sieve ignore --fingerprint FP                     add a suppression rule to .sieve/ignore.yml
  sieve serve --config /etc/sieve/server.yml        run the self-host daemon (webhooks + App auth)
  sieve admin --url URL --secret-env VAR            query a daemon's /admin endpoint
  sieve version                                     print version

review flags:
  --repo       repository as owner/name (default: $GITHUB_REPOSITORY; --local infers from remote)
  --pr         pull request number (default: pull_request.number from $GITHUB_EVENT_PATH)
  --token      GitHub token (default: $GITHUB_TOKEN)
  --config     config file (default: .sieve.yml)
  --dry-run    skip the LLM pass; no GitHub writes ever happen either way
  --post       post the walkthrough + inline review to the PR — the ONLY switch
               that enables writes; no config key can turn posting on
  --local      review the current git worktree against --base; no token or PR needed
  --base       base ref for --local review (default: main)
  --full       force a full re-review (disable incremental delta review)
  --json-only  suppress the stderr summary
  --sarif      write a SARIF v2.1.0 report to this file for GitHub Security tab upload
  --debug      debug logging

ignore flags:
  --fingerprint  exact sieve fingerprint to ignore
  --path         path glob pattern (e.g. 'vendor/**' or '**/*.pb.go')
  --category     finding category to ignore
  --severity     severity to ignore
  --title        substring match against finding titles
  --reason       human note explaining the rule
  --expires      expiration date YYYY-MM-DD
  --file         ignore file path (default: .sieve/ignore.yml)

exit codes: 0 ok · 1 error · 2 partial (truncated input, failed batch, or a
failed inline comment post)
`)
}
