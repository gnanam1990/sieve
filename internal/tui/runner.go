package tui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/local"
	"github.com/gnanam1990/sieve/internal/review"
)

// reviewDoneMsg carries the result of a background review run.
type reviewDoneMsg struct {
	rc  *review.ReviewContext
	err error
}

// cmdReview returns a Bubble Tea command that runs sieve's review pipeline in
// the background so the UI stays responsive.
func cmdReview(opts Options, log *slog.Logger) tea.Cmd {
	return func() tea.Msg {
		rc, err := runReview(opts, log)
		return reviewDoneMsg{rc: rc, err: err}
	}
}

func runReview(opts Options, log *slog.Logger) (*review.ReviewContext, error) {
	if opts.RepoPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		opts.RepoPath = cwd
	}

	if opts.Local {
		if opts.Repo == "" {
			opts.Repo = local.RepoName(opts.RepoPath)
		}
		if opts.BaseRef == "" {
			opts.BaseRef = "main"
		}
	}

	level := slog.LevelInfo
	if !opts.Debug {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}))
	if opts.Debug {
		path := filepath.Join(os.TempDir(), "sieve-tui-review.log")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // debug log
		if err == nil {
			logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
		}
	}
	_ = log // surface-level logger reserved for future UI log panel

	return review.Run(context.Background(), review.Options{
		Repo:       opts.Repo,
		PRNumber:   opts.PRNumber,
		ConfigPath: opts.ConfigPath,
		Log:        logger,
		RepoPath:   opts.RepoPath,
		Local:      opts.Local,
		BaseRef:    opts.BaseRef,
	})
}
