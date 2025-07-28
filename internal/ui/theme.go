package ui

import (
	"github.com/charmbracelet/lipgloss"
)

type Theme struct {
	// Message styles
	UserMessage        lipgloss.Style
	UserMessageContent lipgloss.Style
	AgentMessage       lipgloss.Style
	SystemMessage      lipgloss.Style
	ErrorMessage       lipgloss.Style

	// Input styles
	InputBorder lipgloss.Style

	// Modal styles
	ModalBorder         lipgloss.Style
	ModalTitle          lipgloss.Style
	ModalContent        lipgloss.Style
	ModalOption         lipgloss.Style
	ModalSelectedOption lipgloss.Style
	ModalHelp           lipgloss.Style
}

func DefaultTheme() Theme {
	return Theme{
		UserMessage: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")). // Blue
			Bold(true).
			MarginLeft(2),

		UserMessageContent: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")). // Light gray
			MarginLeft(2),

		AgentMessage: lipgloss.NewStyle().
			Foreground(lipgloss.Color("76")). // Green
			MarginLeft(2),

		SystemMessage: lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")). // Gray
			Italic(true).
			MarginLeft(2),

		ErrorMessage: lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")). // Red
			Bold(true).
			MarginLeft(2),

		InputBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1),

		ModalBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2).
			Width(60).
			Background(lipgloss.Color("235")),

		ModalTitle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true).
			Align(lipgloss.Center),

		ModalContent: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Width(56).
			Align(lipgloss.Left),

		ModalOption: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")),

		ModalSelectedOption: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true),

		ModalHelp: lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true).
			Align(lipgloss.Center),
	}
}
