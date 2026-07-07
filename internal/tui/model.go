package tui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
	"github.com/gnanam1990/sieve/internal/local"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
)

// state is the current TUI screen.
type state int

const (
	stateHome state = iota
	stateProjects
	stateRepoInput
	statePRInput
	stateReviewing
	stateFindings
	stateDetail
	stateSuggestions
	stateModal
	stateError
)

// project is a recently-used repo discovered from the local memory store.
type project struct {
	Host  string
	Owner string
	Repo  string
}

// repoKey returns the shorthand owner/name form used in menus.
func (p project) repoKey() string { return p.Owner + "/" + p.Repo }

// Model is the Bubble Tea model for the sieve TUI.
type Model struct {
	opts Options

	state   state
	width   int
	height  int
	spinner int

	recent      []project
	recentErr   error
	recentIdx   int

	repoInput string
	prInput   string

	rc      *review.ReviewContext
	active  []gate.Finding
	findIdx int

	suggestions    []suggest.Suggestion
	suggestionsErr error
	suggestIdx     int

	modalTitle string
	modalBody  string
	statusMsg  string
	err        error

	log *slog.Logger
}

// New builds the initial model. If enough flags are provided it skips straight
// to the review screen; otherwise it lands on the home menu.
func New(opts Options) Model {
	m := Model{
		opts:       opts,
		state:      stateHome,
		repoInput:  opts.Repo,
		prInput:    prInput(opts.PRNumber),
		findIdx:    -1,
		suggestIdx: -1,
	}

	if opts.Local {
		m.state = stateReviewing
	} else if opts.Repo != "" && opts.PRNumber > 0 {
		m.state = stateReviewing
	}

	m.log = newLogger(opts.Debug)
	return m
}

func prInput(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if !debug {
		level = slog.LevelError
	}
	w := io.Discard
	if debug {
		path := filepath.Join(os.TempDir(), "sieve-tui.log")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // debug log
		if err == nil {
			w = f
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// Start runs the interactive program. It blocks until the user exits.
func Start(opts Options) error {
	p := tea.NewProgram(New(opts), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// resolveInputs fills repo/pr/base and decides whether the session is local.
func (m *Model) resolveInputs() Options {
	opts := m.opts
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
		return opts
	}
	if opts.Repo == "" {
		opts.Repo = strings.TrimSpace(m.repoInput)
	}
	if opts.PRNumber == 0 && strings.TrimSpace(m.prInput) != "" {
		var n int
		_, err := fmt.Sscanf(strings.TrimSpace(m.prInput), "%d", &n)
		if err == nil {
			opts.PRNumber = n
		}
	}
	return opts
}

// activeFindings returns the gate-routed findings to present in the UI.
func activeFindings(rc *review.ReviewContext) []gate.Finding {
	if rc == nil {
		return nil
	}
	if rc.Gate != nil {
		out := append([]gate.Finding(nil), rc.Gate.Inline...)
		out = append(out, rc.Gate.Notes...)
		sort.SliceStable(out, func(i, j int) bool {
			if findings.Rank(out[i].Severity) != findings.Rank(out[j].Severity) {
				return findings.Rank(out[i].Severity) < findings.Rank(out[j].Severity)
			}
			if out[i].Path != out[j].Path {
				return out[i].Path < out[j].Path
			}
			return out[i].Line < out[j].Line
		})
		return out
	}
	return nil
}

// currentFinding returns the selected finding, or nil.
func (m *Model) currentFinding() *gate.Finding {
	if m.findIdx < 0 || m.findIdx >= len(m.active) {
		return nil
	}
	f := m.active[m.findIdx]
	return &f
}

// repoParts splits the current repo into owner/name and reports whether it is
// well-formed enough to use as a memory key.
func (m *Model) repoParts() (owner, name string, ok bool) {
	return splitRepo(m.opts.Repo)
}

func splitRepo(repo string) (owner, name string, ok bool) {
	repo = strings.TrimSpace(repo)
	owner, name, ok = strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != ""
}

// openStore returns the memory store for the active repo, or nil if the repo is
// not a usable owner/name pair.
func (m *Model) openStore() *memory.Store {
	owner, name, ok := m.repoParts()
	if !ok {
		return nil
	}
	return memory.Open("github.com", owner, name, m.log)
}

// recordReaction appends a down-vote event for the given fingerprint.
func recordReaction(store *memory.Store, fp, path string, now time.Time) {
	if store == nil || fp == "" {
		return
	}
	store.Append(memory.Event{
		Ts:     now.UTC().Format(time.RFC3339),
		Type:   memory.TypeReaction,
		Fp:     fp,
		Path:   path,
		Plus:   0,
		Minus:  1,
	})
}

// saveReport writes the full ReviewContext JSON to a file in the current
// directory and returns the path.
func saveReport(rc *review.ReviewContext) (string, error) {
	if rc == nil {
		return "", fmt.Errorf("no review result to save")
	}
	name := fmt.Sprintf("sieve-report-%s-%d.json", strings.ReplaceAll(rc.Repo, "/", "-"), rc.PRNumber)
	f, err := os.Create(name) //nolint:gosec // local report file
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // best-effort
	if err := rc.WriteJSON(f); err != nil {
		return "", err
	}
	return name, nil
}

// recordReviewOutcomes appends a run event and one finding event per active
// finding to the local store. This gives future `sieve ignore --suggest` and the
// TUI's own suggestion panel the metadata needed to join down-votes/dismissals.
func recordReviewOutcomes(store *memory.Store, rc *review.ReviewContext, active []gate.Finding, ts string) {
	if store == nil || rc == nil {
		return
	}
	inline, notes := 0, 0
	if rc.Gate != nil {
		inline = rc.Gate.Stats.InlineCount
		notes = rc.Gate.Stats.NotesCount
	}
	events := []memory.Event{{
		Ts: ts, Type: memory.TypeRun, PR: rc.PRNumber, HeadSHA: rc.HeadSHA,
		Model: "tui", InTok: rc.Stats.InputTokens, OutTok: rc.Stats.OutputTokens,
		Inline: inline, Notes: notes, Dropped: rc.Stats.FindingsDropped,
	}}
	for _, f := range active {
		events = append(events, memory.Event{
			Ts:     ts,
			Type:   memory.TypeFinding,
			Fp:     f.Fingerprint,
			Path:   f.Path,
			Sev:    string(f.Severity),
			Conf:   f.Confidence,
			Cat:    f.Category,
			Title:  f.Title,
		})
	}
	store.Append(events...)
}
