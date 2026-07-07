package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4"))

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#5A3FD7")).
			Foreground(lipgloss.Color("#FAFAFA"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))

	bodyStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	modalStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7D56F4")).
				Padding(1, 2)
)

// severityColor maps a finding severity to a lipgloss color.
func severityColor(sev string) lipgloss.Color {
	switch sev {
	case "critical":
		return lipgloss.Color("#FF6B6B")
	case "major":
		return lipgloss.Color("#FFA500")
	case "minor":
		return lipgloss.Color("#FFD93D")
	case "nit":
		return lipgloss.Color("#A0A0A0")
	default:
		return lipgloss.Color("#FAFAFA")
	}
}
