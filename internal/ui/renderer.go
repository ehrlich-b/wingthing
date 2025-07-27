package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Renderer handles ANSI-styled rendering of messages for immediate output to scrollback
type Renderer struct {
	theme Theme
}

// NewRenderer creates a new ANSI renderer with the given theme
func NewRenderer(theme Theme) *Renderer {
	return &Renderer{theme: theme}
}

// printToScrollback prints to scrollback using tea.Println for proper handling
func printToScrollback(s string) tea.Cmd {
	// Strip the trailing newlines since tea.Println adds its own
	content := strings.TrimRight(s, "\n")
	return tea.Println(content)
}

// User renders a user message with ANSI styling
func (r *Renderer) User(content string) string {
	prefix := r.theme.UserMessage.Render("You:")
	message := r.theme.UserMessageContent.Render(content)
	return prefix + " " + message + "\n\n"
}

// AgentRun renders an agent tool execution message
func (r *Renderer) AgentRun(content string) string {
	header := "Assistant (Running):"
	styled := r.theme.AgentMessage.Render(header + " " + content)
	return styled + "\n\n"
}

// AgentObservation renders an agent observation message
func (r *Renderer) AgentObservation(content string) string {
	header := "Assistant (Observation):"
	styled := r.theme.AgentMessage.Render(header + " " + content)
	return styled + "\n\n"
}

// AgentFinal renders an agent final response message
func (r *Renderer) AgentFinal(content string) string {
	header := "Assistant (Result):"
	styled := r.theme.AgentMessage.Render(header + " " + content)
	return styled + "\n\n"
}

// AgentError renders an agent error message
func (r *Renderer) AgentError(content string) string {
	header := "Assistant (Error):"
	styled := r.theme.ErrorMessage.Render("❌ " + header + " " + content)
	return styled + "\n\n"
}

// System renders a system message
func (r *Renderer) System(content string) string {
	styled := r.theme.SystemMessage.Render(content)
	return styled + "\n\n"
}

// Welcome renders the welcome message
func (r *Renderer) Welcome() string {
	styled := r.theme.SystemMessage.Render("Welcome to Wingthing! Type a message to get started.")
	return styled + "\n\n"
}

// TODO: Add diff highlighting and code block highlighting methods
// DiffBlock renders a diff with syntax highlighting
func (r *Renderer) DiffBlock(content string) string {
	lines := strings.Split(content, "\n")
	var styledLines []string
	
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			// Hunk header - bold and dim
			styledLines = append(styledLines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("8")).Render(line))
		} else if strings.HasPrefix(line, "+") {
			// Addition - green
			styledLines = append(styledLines, lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(line))
		} else if strings.HasPrefix(line, "-") {
			// Deletion - red
			styledLines = append(styledLines, lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(line))
		} else {
			// Context lines - default
			styledLines = append(styledLines, line)
		}
	}
	
	return strings.Join(styledLines, "\n") + "\n\n"
}

// CodeBlock renders a code block with basic highlighting
func (r *Renderer) CodeBlock(language, content string) string {
	// Basic code block styling - could be enhanced with chroma later
	codeStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("252")).
		Padding(1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	
	header := ""
	if language != "" {
		header = lipgloss.NewStyle().
			Foreground(lipgloss.Color("6")).
			Render(language) + "\n"
	}
	
	return header + codeStyle.Render(content) + "\n\n"
}