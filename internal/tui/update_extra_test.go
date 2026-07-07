package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/review"
)

func TestProjectNavigation(t *testing.T) {
	m := New(Options{})
	m.state = stateProjects
	m.recent = []project{
		{Host: "github.com", Owner: "a", Repo: "1"},
		{Host: "github.com", Owner: "b", Repo: "2"},
		{Host: "github.com", Owner: "c", Repo: "3"},
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.recentIdx != 1 {
		t.Fatalf("expected idx 1, got %d", m.recentIdx)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.recentIdx != 0 {
		t.Fatalf("expected wrap to 0, got %d", m.recentIdx)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.recentIdx != 2 {
		t.Fatalf("expected wrap to last, got %d", m.recentIdx)
	}
}

func TestProjectsNewRepoKey(t *testing.T) {
	m := New(Options{})
	m.state = stateProjects
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.state != stateRepoInput {
		t.Fatalf("expected stateRepoInput, got %d", m.state)
	}
}

func TestInputEscReturnsHome(t *testing.T) {
	m := New(Options{})
	m.state = stateRepoInput
	m = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateHome {
		t.Fatalf("expected stateHome, got %d", m.state)
	}
}

func TestEmptyBackspace(t *testing.T) {
	m := New(Options{})
	m.state = stateRepoInput
	m = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.repoInput != "" {
		t.Fatalf("expected empty, got %s", m.repoInput)
	}
}

func TestDetailIgnoreAndSave(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	m := modelWithFinding("fp7", "a.go", findings.SeverityMajor, "bug", "detail")
	m.state = stateDetail
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if m.state != stateModal {
		t.Fatalf("expected modal after ignore, got %d", m.state)
	}

	m.state = stateDetail
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	if m.state != stateModal {
		t.Fatalf("expected modal after save, got %d", m.state)
	}
	if _, err := os.Stat("sieve-report-owner-repo-1.json"); err != nil {
		t.Fatalf("report missing: %v", err)
	}
}

func TestDetailDownvote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	writeStoreEvents(t, dir, "github.com", "owner", "repo", []memory.Event{
		{Type: memory.TypeFinding, Fp: "fp8", Path: "a.go", Cat: "bug", Sev: "major", Title: "x"},
	})
	m := modelWithFinding("fp8", "a.go", findings.SeverityMajor, "bug", "x")
	m.opts.Repo = "owner/repo"
	m.state = stateDetail
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if m.state != stateModal {
		t.Fatalf("expected modal, got %d", m.state)
	}
}

func TestSuggestionsNavigation(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateSuggestions
	m.suggestions = []suggest.Suggestion{
		{Rule: ignore.Rule{Fingerprint: "a"}},
		{Rule: ignore.Rule{Fingerprint: "b"}},
	}
	m.suggestIdx = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.suggestIdx != 1 {
		t.Fatalf("expected idx 1, got %d", m.suggestIdx)
	}
	m = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.suggestIdx != 0 {
		t.Fatalf("expected wrap to 0, got %d", m.suggestIdx)
	}
}

func TestSuggestionsEscBackToFindings(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateSuggestions
	m.suggestions = []suggest.Suggestion{{Rule: ignore.Rule{Fingerprint: "a"}}}
	m = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
}

func TestModalReturnsToSuggestionsWhenPresent(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateModal
	m.suggestions = []suggest.Suggestion{{Rule: ignore.Rule{Fingerprint: "a"}}}
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateSuggestions {
		t.Fatalf("expected stateSuggestions, got %d", m.state)
	}
}

func TestReviewDoneErrorThroughModel(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m = update(m, reviewDoneMsg{err: sampleError()})
	if m.state != stateError {
		t.Fatalf("expected stateError, got %d", m.state)
	}
}

func TestSaveReportNilError(t *testing.T) {
	_, err := saveReport(nil)
	if err == nil {
		t.Fatal("expected error for nil review context")
	}
}

func TestResolveInputsLocal(t *testing.T) {
	m := New(Options{Local: true})
	opts := m.resolveInputs()
	if !opts.Local {
		t.Fatal("expected local")
	}
	if opts.BaseRef != "main" {
		t.Fatalf("expected base main, got %s", opts.BaseRef)
	}
}

func TestCurrentFindingNil(t *testing.T) {
	m := New(Options{})
	if m.currentFinding() != nil {
		t.Fatal("expected nil current finding")
	}
}

func TestRepoPartsInvalid(t *testing.T) {
	m := New(Options{Repo: "not-valid"})
	_, _, ok := m.repoParts()
	if ok {
		t.Fatal("expected invalid repo parts")
	}
}

func TestOpenStoreNilForInvalidRepo(t *testing.T) {
	m := New(Options{Repo: "bad"})
	if m.openStore() != nil {
		t.Fatal("expected nil store for invalid repo")
	}
}

func TestFindingsWithNoActive(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m = update(m, reviewDoneMsg{rc: &review.ReviewContext{Repo: "owner/repo", PRNumber: 1}})
	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
	if m.findIdx != -1 {
		t.Fatalf("expected no selection, got %d", m.findIdx)
	}
}

func TestViewRendersProjectsError(t *testing.T) {
	m := New(Options{})
	m.width = 80
	m.height = 24
	m.state = stateProjects
	m.recentErr = sampleError()
	out := m.View()
	if out == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestViewRendersSuggestionsError(t *testing.T) {
	m := New(Options{})
	m.width = 80
	m.height = 24
	m.state = stateSuggestions
	m.suggestionsErr = sampleError()
	out := m.View()
	if out == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestViewRendersDetailWithSuggestion(t *testing.T) {
	m := modelWithFindingAndSuggestion()
	m.state = stateDetail
	out := m.View()
	if !strings.Contains(out, "fix") {
		t.Fatalf("expected suggestion rendered, got %s", out)
	}
}

func TestCmdReviewErrorMessage(t *testing.T) {
	cmd := cmdReview(Options{Repo: "", Local: false}, newLogger(false))
	msg := cmd()
	rdm, ok := msg.(reviewDoneMsg)
	if !ok {
		t.Fatalf("expected reviewDoneMsg, got %T", msg)
	}
	if rdm.err == nil {
		t.Fatal("expected error for empty repo")
	}
	m := New(Options{})
	m.width = 80
	m.height = 24
	m = update(m, rdm)
	if m.state != stateError {
		t.Fatalf("expected stateError, got %d", m.state)
	}
}

func TestRecordReactionNoFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	store := &memory.Store{Path: path}
	recordReaction(store, "", "main.go", time.Now())
	if _, err := os.Stat(path); err == nil {
		t.Fatal("expected no event written for empty fingerprint")
	}
}

func modelWithFindingAndSuggestion() Model {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	rc := &review.ReviewContext{
		Repo:     "owner/repo",
		PRNumber: 1,
		Gate: &gate.GateResult{
			Inline: []gate.Finding{{
				Finding: findings.Finding{
					Path:       "a.go",
					Line:       5,
					Severity:   findings.SeverityMajor,
					Category:   "bug",
					Title:      "issue",
					Body:       "body",
					Suggestion: "fix",
					Confidence: 0.8,
				},
				Fingerprint: "fp9",
			}},
		},
	}
	return update(m, reviewDoneMsg{rc: rc})
}
