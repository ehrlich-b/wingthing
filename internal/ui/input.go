package ui

import (
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

type InputModel struct {
	textarea textarea.Model
}

func NewInputModel() InputModel {
	ta := textarea.New()
	ta.Placeholder = "Type your message... (Press Enter to send, Ctrl+C to quit)"
	ta.Focus()
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	
	return InputModel{
		textarea: ta,
	}
}

func (m InputModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Don't handle enter here - let parent handle it
			return m, nil
		}
	}
	
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m InputModel) View() string {
	return m.textarea.View()
}

func (m InputModel) Value() string {
	return m.textarea.Value()
}

func (m InputModel) Reset() {
	m.textarea.Reset()
}

func (m InputModel) SetWidth(width int) {
	m.textarea.SetWidth(width - 4) // Account for border
}

func (m InputModel) Focus() tea.Cmd {
	return m.textarea.Focus()
}

func (m InputModel) Blur() {
	m.textarea.Blur()
}
