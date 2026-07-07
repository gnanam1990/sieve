package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/review"
)

func update(m Model, msg tea.Msg) Model {
	out, _ := m.Update(msg)
	return out.(Model)
}

func TestNewDefaultsToHome(t *testing.T) {
	m := New(Options{})
	if m.state != stateHome {
		t.Fatalf("empty opts should land on home, got %d", m.state)
	}
}

func TestNewWithRepoAndPRSkipsToReview(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 42})
	if m.state != stateReviewing {
		t.Fatalf("repo+pr should skip to reviewing, got %d", m.state)
	}
}

func TestNewLocalSkipsToReview(t *testing.T) {
	m := New(Options{Local: true})
	if m.state != stateReviewing {
		t.Fatalf("local should skip to reviewing, got %d", m.state)
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := New(Options{})
	m = update(m,tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.width != 80 || m.height != 24 {
		t.Fatalf("expected 80x24, got %dx%d", m.width, m.height)
	}
}

func TestReviewDonePopulatesFindings(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m = update(m,tea.WindowSizeMsg{Width: 80, Height: 24})

	rc := sampleReviewContext()
	m = update(m,reviewDoneMsg{rc: rc})

	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
	if len(m.active) != 2 {
		t.Fatalf("expected 2 active findings, got %d", len(m.active))
	}
	if m.findIdx != 0 {
		t.Fatalf("expected first finding selected, got %d", m.findIdx)
	}
}

func TestReviewDoneErrorShowsError(t *testing.T) {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m = update(m,reviewDoneMsg{err: sampleError()})
	if m.state != stateError {
		t.Fatalf("expected stateError, got %d", m.state)
	}
}

func TestFindingsNavigationWraps(t *testing.T) {
	m := modelWithFindings()
	m = update(m,tea.KeyMsg{Type: tea.KeyDown})
	if m.findIdx != 1 {
		t.Fatalf("expected index 1, got %d", m.findIdx)
	}
	m = update(m,tea.KeyMsg{Type: tea.KeyDown})
	if m.findIdx != 0 {
		t.Fatalf("expected wrap to 0, got %d", m.findIdx)
	}
	m = update(m,tea.KeyMsg{Type: tea.KeyUp})
	if m.findIdx != 1 {
		t.Fatalf("expected wrap to last, got %d", m.findIdx)
	}
}

func TestEnterOpensDetail(t *testing.T) {
	m := modelWithFindings()
	m = update(m,tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateDetail {
		t.Fatalf("expected stateDetail, got %d", m.state)
	}
}

func TestDetailBackReturnsToFindings(t *testing.T) {
	m := modelWithFindings()
	m.state = stateDetail
	m = update(m,tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateFindings {
		t.Fatalf("expected stateFindings, got %d", m.state)
	}
}

func TestActiveFindingsSorting(t *testing.T) {
	rc := sampleReviewContext()
	got := activeFindings(rc)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(got))
	}
	if got[0].Severity != findings.SeverityCritical {
		t.Fatalf("expected critical first, got %s", got[0].Severity)
	}
}

func TestViewRendersHome(t *testing.T) {
	m := New(Options{})
	m.width = 80
	m.height = 24
	out := m.View()
	if !strings.Contains(out, "review a PR") {
		t.Fatalf("home view missing menu item: %s", out)
	}
}

func TestViewRendersFindings(t *testing.T) {
	m := modelWithFindings()
	out := m.View()
	if !strings.Contains(out, "leak") {
		t.Fatalf("findings view missing title: %s", out)
	}
}

func TestViewRendersDetail(t *testing.T) {
	m := modelWithFindings()
	m.state = stateDetail
	out := m.View()
	if !strings.Contains(out, "goroutine") {
		t.Fatalf("detail view missing body: %s", out)
	}
}

func sampleReviewContext() *review.ReviewContext {
	return &review.ReviewContext{
		Repo:     "owner/repo",
		PRNumber: 1,
		Gate: &gate.GateResult{
			Inline: []gate.Finding{
				{Finding: findings.Finding{
					Path:     "main.go",
					Line:     10,
					Severity: findings.SeverityMajor,
					Category: "bug",
					Title:    "unchecked error",
					Body:     "handle the error",
					Confidence: 0.85,
				}},
			},
			Notes: []gate.Finding{
				{Finding: findings.Finding{
					Path:     "server.go",
					Line:     5,
					Severity: findings.SeverityCritical,
					Category: "bug",
					Title:    "goroutine leak",
					Body:     "waitgroup missing",
					Confidence: 0.95,
				}},
			},
		},
	}
}

func modelWithFindings() Model {
	m := New(Options{Repo: "owner/repo", PRNumber: 1})
	m.width = 80
	m.height = 24
	m = update(m, reviewDoneMsg{rc: sampleReviewContext()})
	return m
}

func sampleError() error {
	return errors.New("boom")
}
