package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gnanam1990/sieve/internal/ignore"
	"github.com/gnanam1990/sieve/internal/ignore/suggest"
)

// Init starts the first command.
func (m Model) Init() tea.Cmd {
	if m.state == stateReviewing {
		opts := m.resolveInputs()
		return cmdReview(opts, m.log)
	}
	return nil
}

// Update is the Bubble Tea update loop.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		return m.handleKey(msg)

	case reviewDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			m.state = stateError
			return m, nil
		}
		m.rc = msg.rc
		m.opts.Repo = msg.rc.Repo
		m.active = activeFindings(msg.rc)
		if len(m.active) > 0 {
			m.findIdx = 0
		} else {
			m.findIdx = -1
		}
		// Seed the local memory store with this run so down-votes/ignores can be
		// turned into suggestions later.
		store := m.openStore()
		recordReviewOutcomes(store, msg.rc, m.active, time.Now().UTC().Format(time.RFC3339))
		m.state = stateFindings
		m.statusMsg = fmt.Sprintf("review complete · %d finding(s)", len(m.active))
	}

	if m.state == stateReviewing {
		m.spinner++
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if isQuit(msg) {
		return m, tea.Quit
	}

	switch m.state {
	case stateHome:
		return m.handleHomeKey(msg)
	case stateProjects:
		return m.handleProjectsKey(msg)
	case stateRepoInput, statePRInput:
		return m.handleInputKey(msg)
	case stateFindings:
		return m.handleFindingsKey(msg)
	case stateDetail:
		return m.handleDetailKey(msg)
	case stateSuggestions:
		return m.handleSuggestionsKey(msg)
	case stateModal:
		m.state = stateFindings
		if len(m.suggestions) > 0 {
			m.state = stateSuggestions
		}
		return m, nil
	case stateError:
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleHomeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r":
		m.state = stateRepoInput
		m.repoInput = ""
		return m, nil
	case "l":
		m.opts.Local = true
		m.state = stateReviewing
		return m, cmdReview(m.resolveInputs(), m.log)
	case "p":
		m.state = stateProjects
		m.recent, m.recentErr = listProjects()
		m.recentIdx = 0
		return m, nil
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleProjectsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.recentIdx > 0 {
			m.recentIdx--
		} else if len(m.recent) > 0 {
			m.recentIdx = len(m.recent) - 1
		}
	case tea.KeyDown:
		if m.recentIdx < len(m.recent)-1 {
			m.recentIdx++
		} else {
			m.recentIdx = 0
		}
	case tea.KeyEnter:
		if len(m.recent) > 0 {
			p := m.recent[m.recentIdx]
			m.opts.Repo = p.repoKey()
			m.repoInput = p.repoKey()
			m.state = statePRInput
			m.prInput = ""
		}
	case tea.KeyEsc:
		m.state = stateHome
	}
	if msg.String() == "n" {
		m.state = stateRepoInput
		m.repoInput = ""
	}
	return m, nil
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.state = stateHome
		return m, nil
	case tea.KeyEnter:
		if m.state == stateRepoInput {
			m.opts.Repo = strings.TrimSpace(m.repoInput)
			if m.opts.Local {
				m.state = stateReviewing
				return m, cmdReview(m.resolveInputs(), m.log)
			}
			m.state = statePRInput
			m.prInput = ""
			return m, nil
		}
		// statePRInput
		opts := m.resolveInputs()
		if opts.Repo == "" || opts.PRNumber <= 0 {
			m.err = fmt.Errorf("enter a repo as owner/name and a PR number")
			m.state = stateError
			return m, nil
		}
		m.state = stateReviewing
		return m, cmdReview(opts, m.log)
	case tea.KeyBackspace:
		if m.state == stateRepoInput {
			m.repoInput = dropLastRune(m.repoInput)
		} else {
			m.prInput = dropLastRune(m.prInput)
		}
		return m, nil
	}

	if msg.Type == tea.KeyRunes {
		s := string(msg.Runes)
		if m.state == stateRepoInput {
			m.repoInput += s
		} else {
			// PR input only accepts digits.
			for _, r := range s {
				if r >= '0' && r <= '9' {
					m.prInput += string(r)
				}
			}
		}
	}
	return m, nil
}

func dropLastRune(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

func (m Model) handleFindingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.findIdx > 0 {
			m.findIdx--
		} else if len(m.active) > 0 {
			m.findIdx = len(m.active) - 1
		}
	case tea.KeyDown:
		if m.findIdx < len(m.active)-1 {
			m.findIdx++
		} else {
			m.findIdx = 0
		}
	case tea.KeyEnter:
		if len(m.active) > 0 {
			m.state = stateDetail
		}
	case tea.KeyEsc:
		m.state = stateHome
	}

	switch msg.String() {
	case "d":
		return m.downvoteCurrent()
	case "i":
		return m.ignoreCurrent()
	case "s":
		return m.showSuggestions()
	case "S":
		return m.saveCurrentReport()
	}
	return m, nil
}

func (m Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyEnter:
		m.state = stateFindings
	}
	switch msg.String() {
	case "d":
		return m.downvoteCurrent()
	case "i":
		return m.ignoreCurrent()
	case "S":
		return m.saveCurrentReport()
	case "q":
		m.state = stateFindings
	}
	return m, nil
}

func (m Model) handleSuggestionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.suggestIdx > 0 {
			m.suggestIdx--
		} else if len(m.suggestions) > 0 {
			m.suggestIdx = len(m.suggestions) - 1
		}
	case tea.KeyDown:
		if m.suggestIdx < len(m.suggestions)-1 {
			m.suggestIdx++
		} else {
			m.suggestIdx = 0
		}
	case tea.KeyEnter, tea.KeyRunes:
		if msg.String() == "a" || msg.Type == tea.KeyEnter {
			return m.applySelectedSuggestion()
		}
	case tea.KeyEsc:
		m.state = stateFindings
	}
	return m, nil
}

func (m Model) downvoteCurrent() (tea.Model, tea.Cmd) {
	f := m.currentFinding()
	if f == nil {
		return m, nil
	}
	store := m.openStore()
	recordReaction(store, f.Fingerprint, f.Path, time.Now())
	m.modalTitle = "Down-voted"
	m.modalBody = fmt.Sprintf("Recorded a negative reaction for\n%s:%d\n%s", f.Path, f.Line, f.Title)
	m.state = stateModal
	return m, nil
}

func (m Model) ignoreCurrent() (tea.Model, tea.Cmd) {
	f := m.currentFinding()
	if f == nil {
		return m, nil
	}
	rule := ignore.Rule{
		Fingerprint: f.Fingerprint,
		Reason:      "suppressed from the TUI",
	}
	path := ignore.DefaultFile
	if err := applyIgnoreRule(path, rule); err != nil {
		m.err = err
		m.state = stateError
		return m, nil
	}
	m.modalTitle = "Ignored"
	m.modalBody = fmt.Sprintf("Added fingerprint rule to %s\n%s", path, f.Fingerprint)
	m.state = stateModal
	return m, nil
}

func (m Model) saveCurrentReport() (tea.Model, tea.Cmd) {
	path, err := saveReport(m.rc)
	if err != nil {
		m.err = err
		m.state = stateError
		return m, nil
	}
	m.modalTitle = "Report saved"
	m.modalBody = path
	m.state = stateModal
	return m, nil
}

func (m Model) showSuggestions() (tea.Model, tea.Cmd) {
	store := m.openStore()
	if store == nil {
		m.suggestions = nil
		m.suggestionsErr = fmt.Errorf("no local memory store for this repo")
		m.state = stateSuggestions
		return m, nil
	}
	events, _, err := store.Read()
	if err != nil {
		m.suggestionsErr = err
		m.suggestions = nil
		m.state = stateSuggestions
		return m, nil
	}
	m.suggestions = suggest.FromEvents(events, time.Now())
	m.suggestionsErr = nil
	if len(m.suggestions) > 0 {
		m.suggestIdx = 0
	} else {
		m.suggestIdx = -1
	}
	m.state = stateSuggestions
	return m, nil
}

func (m Model) applySelectedSuggestion() (tea.Model, tea.Cmd) {
	if m.suggestIdx < 0 || m.suggestIdx >= len(m.suggestions) {
		return m, nil
	}
	s := m.suggestions[m.suggestIdx]
	path := ignore.DefaultFile
	if err := applyIgnoreRule(path, s.Rule); err != nil {
		m.err = err
		m.state = stateError
		return m, nil
	}
	m.modalTitle = "Suggestion applied"
	m.modalBody = fmt.Sprintf("Wrote rule to %s\n%s", path, s.Rule.Reason)
	m.state = stateModal
	return m, nil
}
