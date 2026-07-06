package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gnanam1990/sieve/internal/config"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/gh/appauth"
	"github.com/gnanam1990/sieve/internal/queue"
	"github.com/gnanam1990/sieve/internal/review"
	"github.com/gnanam1990/sieve/internal/version"
	"github.com/gnanam1990/sieve/internal/webhook"
)

// drainTimeout bounds how long shutdown waits for in-flight reviews.
const drainTimeout = 10 * time.Minute

// maxRepoConfigBytes caps the decoded .sieve.yml size the daemon will parse.
// GitHub itself bounds contents responses to 1 MB, but a misconfigured proxy
// or an operator-pointed BaseURL could let a low-privilege PR author feed a
// huge blob into memory before MergeRepoReview runs — a DoS amplifier against
// a queue worker. A real .sieve.yml is tiny; we cap well above any realistic
// review block. See TestRepoConfigSizeCap.
const maxRepoConfigBytes = 1 << 16 // 64 KiB

// Options are host-level overrides, mainly for tests.
type Options struct {
	APIBaseURL string // "" = api.github.com; tests point at httptest
	Log        *slog.Logger
	Now        func() time.Time // App JWT clock; tests pin it
}

// Server is the wired daemon: App auth + webhook receiver + review queue.
type Server struct {
	sc        Config
	baseCfg   config.Config
	appClient *appauth.Client
	q         *queue.Queue
	wh        *webhook.Handler
	mux       http.Handler
	httpSrv   *http.Server
	apiBase   string
	log       *slog.Logger
}

// New wires the daemon from a validated Config. The webhook secret is read
// from the process env named by webhook_secret_env.
func New(sc Config, opts Options) (*Server, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	baseCfg, err := sc.reviewConfig()
	if err != nil {
		return nil, err
	}
	keyBytes, err := os.ReadFile(sc.App.PrivateKeyPath) //nolint:gosec // operator key path; checkKeyPerms already verified mode + regular file
	if err != nil {
		return nil, fmt.Errorf("read app private key: %w", err)
	}
	key, err := appauth.LoadPrivateKey(keyBytes)
	// Zero the PEM bytes now that they've been parsed — defense in depth against
	// a long-lived daemon being core-dumped. (Go's GC/escape analysis makes this
	// imperfect, but it raises the bar per the STANDING RULES on key hygiene.)
	for i := range keyBytes {
		keyBytes[i] = 0
	}
	if err != nil {
		return nil, err
	}
	appClient := appauth.New(sc.App.ID, key)
	if opts.APIBaseURL != "" {
		appClient.BaseURL = opts.APIBaseURL
	}
	if opts.Now != nil {
		appClient.Now = opts.Now
	}

	s := &Server{
		sc:        sc,
		baseCfg:   baseCfg,
		appClient: appClient,
		apiBase:   opts.APIBaseURL,
		log:       log,
	}

	q, err := queue.Open(queue.Options{
		Dir: sc.DataDir, Workers: sc.Workers, Run: s.runReview, Log: log,
	})
	if err != nil {
		return nil, err
	}
	s.q = q

	secret := os.Getenv(sc.WebhookSecretEnv)
	wh, err := webhook.New(webhook.Config{
		Secret:       []byte(secret),
		ReposAllow:   sc.ReposAllow,
		ReviewDrafts: baseCfg.Review.ReviewDrafts,
		Enqueue:      q.Enqueue,
		QueueStats:   func() (int, int) { return q.Depth(), q.DeadLetters() },
		Version:      version.Version,
		Log:          log,
	}, sc.DataDir)
	if err != nil {
		return nil, err
	}
	s.wh = wh
	s.mux = wh.Mux()
	s.httpSrv = &http.Server{
		Addr:              sc.Listen,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Handler exposes the HTTP routes (for tests that drive it via httptest).
func (s *Server) Handler() http.Handler { return s.mux }

// Start launches the worker pool without binding a listener (used by tests).
func (s *Server) Start(ctx context.Context) { s.q.Start(ctx) }

// Serve starts the workers and the HTTP listener, blocking until ctx is
// cancelled (SIGTERM) or the listener fails, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	s.q.Start(ctx)
	s.log.Info("sieve daemon ready", "listen", s.sc.Listen, "workers", s.sc.Workers)
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		return s.Shutdown()
	case err := <-errCh:
		_ = s.Shutdown()
		return err
	}
}

// Shutdown stops HTTP intake, drains in-flight reviews (bounded), and closes the
// queue and delivery logs. Un-started jobs remain durable for the next start.
func (s *Server) Shutdown() error {
	httpCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(httpCtx)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), drainTimeout)
	defer drainCancel()
	qErr := s.q.Shutdown(drainCtx)
	whErr := s.wh.Close()
	if qErr != nil {
		return qErr
	}
	return whErr
}

// runReview is the queue's job executor: mint an installation token, merge the
// repo's review overrides over the server config, and run the pipeline with
// --post semantics.
func (s *Server) runReview(ctx context.Context, job queue.Job) error {
	ts := s.appClient.TokenSource(job.InstallationID)
	cfg := s.repoConfig(ctx, ts, job)
	_, err := review.Run(ctx, review.Options{
		Repo:        job.Repo,
		PRNumber:    job.PR,
		TokenSource: ts,
		Config:      &cfg,
		Post:        true,
		APIBaseURL:  s.apiBase,
		Log:         s.log,
	})
	return err
}

// repoConfig fetches the repo's .sieve.yml at the head SHA and merges its
// review-only overrides over the server config. Absent or invalid repo config
// falls back to the server config unchanged.
func (s *Server) repoConfig(ctx context.Context, ts gh.TokenSource, job queue.Job) config.Config {
	owner, name, ok := strings.Cut(job.Repo, "/")
	if !ok {
		return s.baseCfg
	}
	client, err := gh.New(ts, s.log)
	if err != nil {
		return s.baseCfg
	}
	if s.apiBase != "" {
		client.BaseURL = s.apiBase
	}
	content, err := client.GetContents(ctx, owner, name, ".sieve.yml", job.HeadSHA)
	if err != nil {
		return s.baseCfg // no repo config (common)
	}
	if len(content) > maxRepoConfigBytes {
		s.log.Warn("ignoring oversized repo .sieve.yml", "repo", job.Repo, "bytes", len(content), "cap", maxRepoConfigBytes)
		return s.baseCfg // a real review block is tiny; refuse the amplifier
	}
	merged, err := config.MergeRepoReview(s.baseCfg, content)
	if err != nil {
		s.log.Warn("ignoring invalid repo .sieve.yml", "repo", job.Repo, "err", err)
		return s.baseCfg
	}
	return merged
}
