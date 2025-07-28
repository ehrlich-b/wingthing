package ui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

var spinnerStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("11")). // Yellow
	Bold(true)

// LiveKind represents the type of live block being displayed
type LiveKind int

const (
	LiveThinking LiveKind = iota
	LiveTool
)

// LiveBlock represents an animated block that shows real-time progress
type LiveBlock struct {
	ID        string
	Kind      LiveKind
	Title     string // e.g. "Bash(make build)" or "Thinking…"
	StartedAt time.Time
	Spinner   spinner.Model
	Lines     []string // streamed tool/LLM output (optional)
}

// NewThinkingBlock creates a new thinking live block
func NewThinkingBlock() *LiveBlock {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	return &LiveBlock{
		ID:        "thinking",
		Kind:      LiveThinking,
		Title:     "Thinking…",
		StartedAt: time.Now(),
		Spinner:   s,
		Lines:     []string{},
	}
}

// NewToolBlock creates a new tool execution live block  
func NewToolBlock(title string) *LiveBlock {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	return &LiveBlock{
		ID:        "tool_" + title,
		Kind:      LiveTool,
		Title:     title,
		StartedAt: time.Now(),
		Spinner:   s,
		Lines:     []string{},
	}
}

// AppendLine adds a new line to the live block's output
func (lb *LiveBlock) AppendLine(line string) {
	lb.Lines = append(lb.Lines, line)
}
