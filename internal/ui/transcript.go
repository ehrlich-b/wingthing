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
	vp.SetContent("Welcome to Wingthing! Type a message to get started.")
	
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
	return m.viewport.View()
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
			lines = append(lines, m.theme.AgentMessage.Render(header+" "+msg.Content))
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
	m.viewport.GotoBottom()
}

// Messages returns the current messages for testing
func (m *TranscriptModel) Messages() []Message {
	return m.messages
}