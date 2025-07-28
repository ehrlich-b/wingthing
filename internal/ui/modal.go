package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ModalType int

const (
	ModalNone ModalType = iota
	ModalPermissionRequest
	ModalSlashCommands
)

type ModalModel struct {
	modalType ModalType
	content   string
	theme     Theme
	selected  int
	options   []string
}

func NewModalModel() ModalModel {
	return ModalModel{
		modalType: ModalNone,
		theme:     DefaultTheme(),
	}
}

func (m ModalModel) Init() tea.Cmd {
	return nil
}

func (m ModalModel) Update(msg tea.Msg) (ModalModel, tea.Cmd) {
	if !m.IsOpen() {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.modalType {
		case ModalPermissionRequest:
			switch msg.String() {
			case "y", "enter":
				// TODO: Send permission granted
				m.modalType = ModalNone
				return m, nil
			case "n", "esc":
				// TODO: Send permission denied
				m.modalType = ModalNone
				return m, nil
			case "a":
				// TODO: Always allow
				m.modalType = ModalNone
				return m, nil
			case "d":
				// TODO: Always deny
				m.modalType = ModalNone
				return m, nil
			case "up", "k":
				if m.selected > 0 {
					m.selected--
				}
			case "down", "j":
				if m.selected < len(m.options)-1 {
					m.selected++
				}
			}
		case ModalSlashCommands:
			switch msg.String() {
			case "esc":
				m.modalType = ModalNone
				return m, nil
			case "up", "k":
				if m.selected > 0 {
					m.selected--
				}
			case "down", "j":
				if m.selected < len(m.options)-1 {
					m.selected++
				}
			case "enter":
				// TODO: Execute selected slash command
				m.modalType = ModalNone
				return m, nil
			}
		}
	}

	return m, nil
}

func (m ModalModel) View() string {
	if !m.IsOpen() {
		return ""
	}

	switch m.modalType {
	case ModalPermissionRequest:
		return m.renderPermissionModal()
	case ModalSlashCommands:
		return m.renderSlashCommandsModal()
	}

	return ""
}

func (m ModalModel) IsOpen() bool {
	return m.modalType != ModalNone
}

func (m *ModalModel) ShowPermissionRequest(content string) {
	m.modalType = ModalPermissionRequest
	m.content = content
	m.selected = 0
	m.options = []string{"Allow Once", "Always Allow", "Deny", "Always Deny"}
}

func (m *ModalModel) ShowSlashCommands(commands []string) {
	m.modalType = ModalSlashCommands
	m.selected = 0
	m.options = commands
}

func (m ModalModel) renderPermissionModal() string {
	title := m.theme.ModalTitle.Render("Permission Required")
	content := m.theme.ModalContent.Render(m.content)

	var options []string
	for i, option := range m.options {
		if i == m.selected {
			options = append(options, m.theme.ModalSelectedOption.Render("> "+option))
		} else {
			options = append(options, m.theme.ModalOption.Render("  "+option))
		}
	}

	help := m.theme.ModalHelp.Render("Use ↑/↓ to navigate, Enter to select, Esc to cancel")

	modal := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		content,
		"",
		lipgloss.JoinVertical(lipgloss.Left, options...),
		"",
		help,
	)

	return m.theme.ModalBorder.Render(modal)
}

func (m ModalModel) renderSlashCommandsModal() string {
	title := m.theme.ModalTitle.Render("Slash Commands")

	var options []string
	for i, option := range m.options {
		if i == m.selected {
			options = append(options, m.theme.ModalSelectedOption.Render("> "+option))
		} else {
			options = append(options, m.theme.ModalOption.Render("  "+option))
		}
	}

	help := m.theme.ModalHelp.Render("Use ↑/↓ to navigate, Enter to select, Esc to cancel")

	modal := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		lipgloss.JoinVertical(lipgloss.Left, options...),
		"",
		help,
	)

	return m.theme.ModalBorder.Render(modal)
}
