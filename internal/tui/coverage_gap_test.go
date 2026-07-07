package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
	"github.com/gnanam1990/sieve/internal/review"
)

func TestInitReturnsReviewCommand(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected review command")
	}
	mLocal := New(Options{Local: true})
	if mLocal.Init() == nil {
		t.Fatal("expected local review command")
	}
	mHome := New(Options{})
	if mHome.Init() != nil {
		t.Fatal("expected no command on home screen")
	}
}

func TestHomeQuitKey(t *testing.T) {
	m := New(Options{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func TestFindingsIgnoreWithNoSelection(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateFindings
	m.active = []gate.Finding{}
	m.findIdx = -1
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if m.state != stateFindings {
		t.Fatalf("expected unchanged state, got %d", m.state)
	}
}

func TestSuggestionsEnterApplies(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()

	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateSuggestions
	m.suggestions = []suggest.Suggestion{{Rule: ignore.Rule{Fingerprint: "fp-enter", Reason: "enter"}}}
	m.suggestIdx = 0
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateModal {
		t.Fatalf("expected modal, got %d", m.state)
	}
}

func TestApplyOutOfRangeDoesNothing(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m.state = stateSuggestions
	m.suggestions = []suggest.Suggestion{{Rule: ignore.Rule{Fingerprint: "a"}}}
	m.suggestIdx = -1
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.state != stateSuggestions {
		t.Fatalf("expected unchanged state, got %d", m.state)
	}
}

func TestShowSuggestionsWithNoStore(t *testing.T) {
	m := modelWithFinding("fp", "main.go", findings.SeverityMajor, "bug", "x")
	m.opts.Repo = "not-a-repo"
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.state != stateSuggestions {
		t.Fatalf("expected stateSuggestions, got %d", m.state)
	}
	if m.suggestionsErr == nil {
		t.Fatal("expected error for invalid repo")
	}
}

func TestSeverityIcon(t *testing.T) {
	if severityIcon(findings.SeverityNit) != "⚪" {
		t.Fatalf("unexpected nit icon")
	}
	if severityIcon("unknown") != "•" {
		t.Fatalf("unexpected default icon")
	}
}

func TestBullet(t *testing.T) {
	if bullet(true) != "▶" {
		t.Fatal("expected selected bullet")
	}
	if bullet(false) != " " {
		t.Fatal("expected empty bullet")
	}
}

func TestSuggestionLineFormats(t *testing.T) {
	s := suggest.Suggestion{Rule: ignore.Rule{Path: "pkg/**", Category: "style", Title: "unused"}}
	line := suggestionLine(s)
	if !strings.Contains(line, "pkg/**") || !strings.Contains(line, "style") || !strings.Contains(line, "unused") {
		t.Fatalf("unexpected suggestion line: %s", line)
	}
	fp := suggest.Suggestion{Rule: ignore.Rule{Fingerprint: "abc"}}
	if suggestionLine(fp) != "fingerprint: abc" {
		t.Fatalf("unexpected fingerprint line: %s", suggestionLine(fp))
	}
}

func TestSeverityColorAll(t *testing.T) {
	for _, sev := range []string{"critical", "major", "minor", "nit", "other"} {
		c := severityColor(sev)
		if c == "" {
			t.Fatalf("severity %q produced empty color", sev)
		}
	}
}

func TestActiveFindingsWithoutGate(t *testing.T) {
	rc := &review.ReviewContext{
		Findings: []findings.Finding{{Path: "a.go", Line: 1, Severity: findings.SeverityMajor, Category: "bug", Title: "t"}},
	}
	if activeFindings(rc) != nil {
		t.Fatal("expected nil when gate is absent")
	}
}

func TestNewLoggerDebug(t *testing.T) {
	log := newLogger(true)
	if log == nil {
		t.Fatal("expected logger")
	}
}

func TestRunReviewDebugPath(t *testing.T) {
	_, err := runReview(Options{Repo: "", Local: false, Debug: true}, newLogger(false))
	if err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestSaveReportWriteError(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()
	// make the directory read-only so Create fails
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	_, err := saveReport(&review.ReviewContext{Repo: "owner/repo", PRNumber: 1})
	if err == nil {
		t.Fatal("expected write error")
	}
}
