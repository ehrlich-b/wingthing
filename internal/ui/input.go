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
	ta.Placeholder = "Type your message... (Ctrl+Enter for new line, Enter to send)"
	ta.Focus()
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	
	// Disable built-in newline handling so we can control it
	ta.KeyMap.InsertNewline.SetEnabled(false)
	
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
		case "ctrl+j", "ctrl+enter":
			// Handle ctrl+j (which is ctrl+enter in most terminals) to insert newline
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune{'\n'},
			})
			return m, cmd
		case "enter":
			// Block plain enter - let parent handle it for sending
			return m, nil
		}
	}
	
	// Let textarea handle all other keys
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m InputModel) View() string {
	return m.textarea.View()
}

func (m InputModel) Value() string {
	return m.textarea.Value()
}

func (m *InputModel) Reset() {
	m.textarea.Reset()
}

func (m *InputModel) SetThinking(thinking bool) {
	if thinking {
		m.textarea.Placeholder = "Please wait..."
		m.textarea.Blur()
	} else {
		m.textarea.Placeholder = "Type your message... (Ctrl+Enter for new line, Enter to send)"
		m.textarea.Focus()
	}
}

func (m InputModel) SetWidth(width int) {
	// Set a reasonable width, accounting for padding and borders
	if width > 8 {
		m.textarea.SetWidth(width - 4)
	} else {
		m.textarea.SetWidth(60) // Minimum reasonable width
	}
}

func (m InputModel) Focus() tea.Cmd {
	return m.textarea.Focus()
}

func (m InputModel) Blur() {
	m.textarea.Blur()
}
