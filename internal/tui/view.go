package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/gnanam1990/sieve/internal/findings"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
)

// View renders the current screen.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "initializing..."
	}

	switch m.state {
	case stateHome:
		return m.viewHome()
	case stateProjects:
		return m.viewProjects()
	case stateRepoInput, statePRInput:
		return m.viewInput()
	case stateReviewing:
		return m.viewReviewing()
	case stateFindings:
		return m.viewFindings()
	case stateDetail:
		return m.viewDetail()
	case stateSuggestions:
		return m.viewSuggestions()
	case stateModal:
		return m.viewModal()
	case stateError:
		return m.viewError()
	default:
		return m.viewHome()
	}
}

func (m Model) viewHome() string {
	return m.page("sieve TUI",
		"[r] review a PR\n"+
			"[l] review local worktree\n"+
			"[p] pick a recent project\n"+
			"[q] quit",
		"local, zero-infra PR review")
}

func (m Model) viewProjects() string {
	var b strings.Builder
	if m.recentErr != nil {
		fmt.Fprintf(&b, "%s\n\n", errorStyle.Render(m.recentErr.Error()))
	}
	if len(m.recent) == 0 {
		b.WriteString(mutedStyle.Render("no recent projects found\n"))
	} else {
		for i, p := range m.recent {
			line := fmt.Sprintf("%s %s", bullet(i == m.recentIdx), p.repoKey())
			if i == m.recentIdx {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}
	return m.page("recent projects", b.String(), "[↵] select  [n] new repo  [esc] back  [ctrl+c] quit")
}

func (m Model) viewInput() string {
	label := "Repository (owner/name):"
	value := m.repoInput
	cursor := "_"
	if m.state == statePRInput {
		label = fmt.Sprintf("PR number for %s:", m.opts.Repo)
		value = m.prInput
	}
	if value != "" {
		cursor = ""
	}
	body := fmt.Sprintf("%s\n%s%s", label, value, cursor)
	return m.page("input", body, "[enter] confirm  [esc] back  [ctrl+c] quit")
}

func (m Model) viewReviewing() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := frames[m.spinner%len(frames)]
	repo := m.opts.Repo
	if repo == "" {
		repo = "local worktree"
	}
	detail := repo
	if !m.opts.Local && m.opts.PRNumber > 0 {
		detail = fmt.Sprintf("%s#%d", repo, m.opts.PRNumber)
	}
	body := fmt.Sprintf("%s reviewing %s...", frame, detail)
	return m.page("reviewing", body, "please wait")
}

func (m Model) viewFindings() string {
	header := fmt.Sprintf("%s · %d finding(s)", m.opts.Repo, len(m.active))
	if m.opts.Local {
		header = fmt.Sprintf("local · %s · %d finding(s)", m.opts.Repo, len(m.active))
	}

	var b strings.Builder
	for i, f := range m.active {
		line := fmt.Sprintf("%s %s:%d  %s", severityIcon(f.Severity), f.Path, f.Line, f.Title)
		if i == m.findIdx {
			line = selectedStyle.Render(line)
		} else {
			line = lipgloss.NewStyle().Foreground(severityColor(string(f.Severity))).Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(m.active) == 0 {
		b.WriteString(mutedStyle.Render("no findings\n"))
	}
	return m.page(header, b.String(), "[↵] detail  [d] down-vote  [i] ignore  [s] suggestions  [S] save report  [esc] back  [ctrl+c] quit")
}

func (m Model) viewDetail() string {
	f := m.currentFinding()
	if f == nil {
		return m.page("detail", "no finding selected", "")
	}
	loc := fmt.Sprintf("%s:%d", f.Path, f.Line)
	if f.EndLine > 0 {
		loc = fmt.Sprintf("%s:%d-%d", f.Path, f.Line, f.EndLine)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", severityIcon(f.Severity), f.Title)
	fmt.Fprintf(&b, "%s\n", mutedStyle.Render(loc))
	fmt.Fprintf(&b, "category: %s  confidence: %.2f  tier: %s\n\n", f.Category, f.Confidence, f.Tier)
	if f.Body != "" {
		b.WriteString(bodyStyle.Render(f.Body) + "\n\n")
	}
	if f.Suggestion != "" {
		b.WriteString("Suggested fix:\n")
		b.WriteString(bodyStyle.Render(f.Suggestion) + "\n")
	}
	return m.page("finding detail", b.String(), "[d] down-vote  [i] ignore  [S] save report  [esc/↵] back  [ctrl+c] quit")
}

func (m Model) viewSuggestions() string {
	var b strings.Builder
	if m.suggestionsErr != nil {
		fmt.Fprintf(&b, "%s\n\n", errorStyle.Render(m.suggestionsErr.Error()))
	}
	if len(m.suggestions) == 0 {
		b.WriteString(mutedStyle.Render("no ignore suggestions from this repo's memory\n"))
	} else {
		for i, s := range m.suggestions {
			desc := suggestionLine(s)
			line := fmt.Sprintf("%s %s", bullet(i == m.suggestIdx), desc)
			if i == m.suggestIdx {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
			if s.Rule.Reason != "" {
				b.WriteString(mutedStyle.Render("    "+s.Rule.Reason) + "\n")
			}
		}
	}
	return m.page("ignore suggestions", b.String(), "[a/↵] apply selected  [esc] back  [ctrl+c] quit")
}

func (m Model) viewModal() string {
	return m.page(m.modalTitle, m.modalBody, "[enter/esc] dismiss")
}

func (m Model) viewError() string {
	return m.page("error", errorStyle.Render(m.err.Error()), "[q/ctrl+c] quit")
}

func (m Model) page(title, body, footer string) string {
	w := m.width
	if w < 20 {
		w = 20
	}
	renderedTitle := headerStyle.Width(w - 2).Render(titleStyle.Render("sieve") + " " + title)
	content := lipgloss.NewStyle().Width(w).Padding(1, 2).Render(body)
	f := helpStyle.Width(w).Padding(0, 2).Render(footer)
	return renderedTitle + "\n" + content + "\n" + f
}

func severityIcon(s findings.Severity) string {
	switch s {
	case findings.SeverityCritical:
		return "🔴"
	case findings.SeverityMajor:
		return "🟠"
	case findings.SeverityMinor:
		return "🟡"
	case findings.SeverityNit:
		return "⚪"
	default:
		return "•"
	}
}

func bullet(selected bool) string {
	if selected {
		return "▶"
	}
	return " "
}

func suggestionLine(s suggest.Suggestion) string {
	parts := []string{}
	if s.Rule.Fingerprint != "" {
		parts = append(parts, "fingerprint: "+s.Rule.Fingerprint)
	}
	if s.Rule.Path != "" {
		parts = append(parts, "path: "+s.Rule.Path)
	}
	if s.Rule.Category != "" {
		parts = append(parts, "category: "+s.Rule.Category)
	}
	if s.Rule.Title != "" {
		parts = append(parts, "title: "+s.Rule.Title)
	}
	return strings.Join(parts, " · ")
}
