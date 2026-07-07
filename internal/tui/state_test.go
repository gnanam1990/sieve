package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
)

func TestHomeToRepoInput(t *testing.T) {
	m := New(Options{})
	m.width = 80
	m.height = 24
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.state != stateRepoInput {
		t.Fatalf("expected stateRepoInput, got %d", m.state)
	}
}

func TestHomeToLocalReview(t *testing.T) {
	m := New(Options{})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.state != stateReviewing {
		t.Fatalf("expected stateReviewing, got %d", m.state)
	}
	if !m.opts.Local {
		t.Fatal("expected local mode")
	}
}

func TestHomeToProjects(t *testing.T) {
	m := New(Options{})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.state != stateProjects {
		t.Fatalf("expected stateProjects, got %d", m.state)
	}
}

func TestProjectSelection(t *testing.T) {
	m := New(Options{})
	m.state = stateProjects
	m.recent = []project{{Host: "github.com", Owner: "o", Repo: "r"}}
	m.recentIdx = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != statePRInput {
		t.Fatalf("expected statePRInput, got %d", m.state)
	}
	if m.opts.Repo != "o/r" {
		t.Fatalf("expected repo o/r, got %s", m.opts.Repo)
	}
}

func TestProjectEscReturnsHome(t *testing.T) {
	m := New(Options{})
	m.state = stateProjects
	m = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateHome {
		t.Fatalf("expected stateHome, got %d", m.state)
	}
}

func TestRepoInputTypingAndConfirm(t *testing.T) {
	m := New(Options{})
	m.state = stateRepoInput
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.repoInput != "own/r" {
		t.Fatalf("expected own/r, got %s", m.repoInput)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != statePRInput {
		t.Fatalf("expected statePRInput, got %d", m.state)
	}
	if m.opts.Repo != "own/r" {
		t.Fatalf("expected repo own/r, got %s", m.opts.Repo)
	}
}

func TestPRInputAcceptsOnlyDigits(t *testing.T) {
	m := New(Options{})
	m.state = statePRInput
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if m.prInput != "12" {
		t.Fatalf("expected 12, got %s", m.prInput)
	}
}

func TestPRInputConfirmStartsReview(t *testing.T) {
	m := New(Options{})
	m.state = statePRInput
	m.opts.Repo = "owner/repo"
	m.prInput = "42"
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(Model)
	if mm.state != stateReviewing {
		t.Fatalf("expected stateReviewing, got %d", mm.state)
	}
	if cmd == nil {
		t.Fatal("expected review command")
	}
}

func TestPRInputMissingFieldsShowsError(t *testing.T) {
	m := New(Options{})
	m.state = statePRInput
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateError {
		t.Fatalf("expected stateError, got %d", m.state)
	}
}

func TestBackspaceDropsLastRune(t *testing.T) {
	m := New(Options{})
	m.state = stateRepoInput
	m.repoInput = "abc"
	m = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.repoInput != "ab" {
		t.Fatalf("expected ab, got %s", m.repoInput)
	}
}

func TestFindingsDownvoteCreatesReaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	writeStoreEvents(t, dir, "github.com", "owner", "repo", []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp1", Path: "main.go", Cat: "bug", Sev: "major", Title: "x"},
	})

	m := modelWithFinding("fp1", "main.go", findings.SeverityMajor, "bug", "x")
	m.opts.Repo = "owner/repo"
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if m.state != stateModal {
		t.Fatalf("expected stateModal, got %d", m.state)
	}

	path := filepath.Join(dir, "sieve", "github.com", "owner", "repo", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !strings.Contains(string(data), `"type":"reaction"`) {
		t.Fatalf("store missing reaction event: %s", string(data))
	}
}

func TestFindingsIgnoreWritesRule(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	m := modelWithFinding("fp2", "main.go", findings.SeverityMajor, "bug", "y")
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if m.state != stateModal {
		t.Fatalf("expected stateModal, got %d", m.state)
	}
	data, err := os.ReadFile(".sieve/ignore.yml")
	if err != nil {
		t.Fatalf("read ignore file: %v", err)
	}
	if !strings.Contains(string(data), "fp2") {
		t.Fatalf("ignore file missing fingerprint: %s", string(data))
	}
}

func TestFindingsSaveReport(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	m := modelWithFinding("fp3", "main.go", findings.SeverityMajor, "bug", "z")
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	if m.state != stateModal {
		t.Fatalf("expected stateModal, got %d", m.state)
	}
	if _, err := os.Stat("sieve-report-owner-repo-1.json"); err != nil {
		t.Fatalf("report file missing: %v", err)
	}
}

func TestSuggestionsFlow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	writeStoreEvents(t, dir, "github.com", "owner", "repo", []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp4", Path: "pkg/x.go", Cat: "style", Sev: "nit", Title: "unused param"},
		{Type: memory.TypeReaction, Fp: "fp4", Plus: 0, Minus: 1},
	})

	m := modelWithFinding("fp4", "pkg/x.go", findings.SeverityNit, "style", "unused param")
	m.opts.Repo = "owner/repo"
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.state != stateSuggestions {
		t.Fatalf("expected stateSuggestions, got %d", m.state)
	}
	if len(m.suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if m.suggestIdx != 0 {
		t.Fatalf("expected first suggestion selected, got %d", m.suggestIdx)
	}
}

func TestApplySuggestionWritesRule(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.opts.Repo = "owner/repo"
	m.state = stateSuggestions
	m.suggestions = []suggest.Suggestion{{
		Rule: ignore.Rule{Fingerprint: "fp5", Reason: "tui test"},
	}}
	m.suggestIdx = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.state != stateModal {
		t.Fatalf("expected stateModal, got %d", m.state)
	}
	data, err := os.ReadFile(".sieve/ignore.yml")
	if err != nil {
		t.Fatalf("read ignore file: %v", err)
	}
	if !strings.Contains(string(data), "fp5") {
		t.Fatalf("ignore file missing suggestion fingerprint: %s", string(data))
	}
}

func TestModalDismiss(t *testing.T) {
	m := modelWithFinding("fp6", "main.go", findings.SeverityMajor, "bug", "z")
	m.state = stateModal
	m.suggestions = nil
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
}

func TestErrorQuits(t *testing.T) {
	m := New(Options{})
	m.state = stateError
	m.err = sampleError()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func TestResolveInputs(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 5})
	opts := m.resolveInputs()
	if opts.Repo != "owner/repo" || opts.PRNumber != 5 {
		t.Fatalf("unexpected opts: %+v", opts)
	}
}

func TestViewForEachState(t *testing.T) {
	states := []state{stateHome, stateProjects, stateRepoInput, statePRInput, stateReviewing, stateFindings, stateDetail, stateSuggestions, stateModal, stateError}
	for _, s := range states {
		m := New(Options{})
		m.width = 80
		m.height = 24
		m.state = s
		m.err = sampleError()
		m.modalTitle = "t"
		m.modalBody = "b"
		m.recent = []project{{Host: "github.com", Owner: "o", Repo: "r"}}
		m.active = []gate.Finding{{Finding: findings.Finding{Path: "a.go", Line: 1, Severity: findings.SeverityMajor, Category: "bug", Title: "t", Body: "b"}}}
		m.findIdx = 0
		m.suggestions = []suggest.Suggestion{{Rule: ignore.Rule{Fingerprint: "fp"}}}
		m.suggestIdx = 0
		out := m.View()
		if out == "" {
			t.Fatalf("state %d produced empty view", s)
		}
	}
}

func writeStoreEvents(t *testing.T, root, host, owner, repo string, events []memory.Event) {
	t.Helper()
	dir := filepath.Join(root, "sieve", host, owner, repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := &memory.Store{Path: filepath.Join(dir, "events.jsonl")}
	store.Append(events...)
}

func modelWithFinding(fp, path string, sev findings.Severity, cat, title string) Model {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	rc := &review.ReviewContext{
		Repo:     "owner/repo",
		PRNumber: 1,
		Gate: &gate.GateResult{
			Inline: []gate.Finding{{
				Finding: findings.Finding{
					Path:       path,
					Line:       10,
					Severity:   sev,
					Category:   cat,
					Title:      title,
					Body:       "body",
					Confidence: 0.8,
				},
				Fingerprint: fp,
			}},
		},
	}
	return update(m, reviewDoneMsg{rc: rc})
}
