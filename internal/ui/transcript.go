package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type Message struct {
	Role    string // "user", "agent", "system"
	Type    string // "plan", "tool", "observation", "final", etc.
	Content string
}

type TranscriptModel struct {
	viewport viewport.Model
	messages []Message
	theme    Theme
}

func NewTranscriptModel() TranscriptModel {
	vp := viewport.New(0, 0)
	// Don't set initial content - let updateContent handle it

	return TranscriptModel{
		viewport: vp,
		messages: []Message{},
		theme:    DefaultTheme(),
	}
}

func (m TranscriptModel) Init() tea.Cmd {
	return nil
}

func (m TranscriptModel) Update(msg tea.Msg) (TranscriptModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m TranscriptModel) View() string {
	if len(m.messages) == 0 {
		return m.theme.SystemMessage.Render("Welcome to Wingthing! Type a message to get started.")
	}

	var lines []string
	for _, msg := range m.messages {
		var renderedMsg string
		switch msg.Role {
		case "user":
			renderedMsg = m.theme.UserMessage.Render("You: " + msg.Content)
		case "agent":
			header := fmt.Sprintf("Assistant (%s):", msg.Type)
			if msg.Type == "Error" {
				renderedMsg = m.theme.ErrorMessage.Render("❌ " + header + " " + msg.Content)
			} else {
				renderedMsg = m.theme.AgentMessage.Render(header + " " + msg.Content)
			}
		case "system":
			if msg.Type == "thinking" {
				renderedMsg = m.theme.SystemMessage.Render("🤔 " + msg.Content)
			} else {
				renderedMsg = m.theme.SystemMessage.Render(msg.Content)
			}
		}

		// Handle word wrapping for long content
		if len(msg.Content) > 80 {
			wrappedLines := strings.Split(renderedMsg, "\n")
			for _, line := range wrappedLines {
				for len(line) > m.viewport.Width-4 && m.viewport.Width > 4 {
					splitPoint := m.viewport.Width - 4
					lines = append(lines, line[:splitPoint])
					line = "  " + line[splitPoint:]
				}
				lines = append(lines, line)
			}
		} else {
			lines = append(lines, renderedMsg)
		}
		lines = append(lines, "") // Add spacing between messages
	}

	content := strings.Join(lines, "\n")

	// Simple height-based scrolling - show last N lines that fit
	if m.viewport.Height > 0 {
		contentLines := strings.Split(content, "\n")
		if len(contentLines) > m.viewport.Height {
			// Show the last viewport.Height lines
			visibleLines := contentLines[len(contentLines)-m.viewport.Height:]
			content = strings.Join(visibleLines, "\n")
		}
	}

	return content
}

func (m TranscriptModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	m.updateContent()
}

func (m *TranscriptModel) AddUserMessage(content string) {
	m.messages = append(m.messages, Message{
		Role:    "user",
		Content: content,
	})
	m.updateContent()
}

func (m *TranscriptModel) AddAgentMessage(msgType, content string) {
	m.messages = append(m.messages, Message{
		Role:    "agent",
		Type:    msgType,
		Content: content,
	})
	m.updateContent()
}

func (m *TranscriptModel) AddThinkingMessage() {
	m.messages = append(m.messages, Message{
		Role:    "system",
		Type:    "thinking",
		Content: "Thinking...",
	})
	m.updateContent()
}

func (m *TranscriptModel) updateContent() {
	var lines []string

	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			lines = append(lines, m.theme.UserMessage.Render("You: "+msg.Content))
		case "agent":
			header := fmt.Sprintf("Assistant (%s):", msg.Type)
			if msg.Type == "Error" {
				lines = append(lines, m.theme.ErrorMessage.Render("❌ "+header+" "+msg.Content))
			} else {
				lines = append(lines, m.theme.AgentMessage.Render(header+" "+msg.Content))
			}
		case "system":
			if msg.Type == "thinking" {
				lines = append(lines, m.theme.SystemMessage.Render("🤔 "+msg.Content))
			} else {
				lines = append(lines, m.theme.SystemMessage.Render(msg.Content))
			}
		}
		lines = append(lines, "") // Add spacing
	}

	content := strings.Join(lines, "\n")
	m.viewport.SetContent(content)

	// Only call GotoBottom if viewport is properly sized
	if m.viewport.Height > 0 && m.viewport.Width > 0 {
		m.viewport.GotoBottom()
	}
}

// Messages returns the current messages for testing
func (m *TranscriptModel) Messages() []Message {
	return m.messages
}
