package tui

import tea "github.com/charmbracelet/bubbletea"

// keyMap collects the key bindings for each TUI screen.
type keyMap struct {
	Quit      tea.KeyType
	Up        tea.KeyType
	Down      tea.KeyType
	Enter     tea.KeyType
	Back      tea.KeyType
	ReviewPR  rune
	Local     rune
	Projects  rune
	Ignore    rune
	Downvote  rune
	Suggest   rune
	Save      rune
	Apply     rune
}

// defaultKeys is the canonical key map.
var defaultKeys = keyMap{
	Quit:      tea.KeyCtrlC,
	Up:        tea.KeyUp,
	Down:      tea.KeyDown,
	Enter:     tea.KeyEnter,
	Back:      tea.KeyEsc,
	ReviewPR:  'r',
	Local:     'l',
	Projects:  'p',
	Ignore:    'i',
	Downvote:  'd',
	Suggest:   's',
	Save:      'S',
	Apply:     'a',
}

// isQuit reports true for the global quit chords.
func isQuit(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyCtrlC || msg.String() == "q"
}
