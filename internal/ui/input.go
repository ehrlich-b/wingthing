package ui

import (
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type InputModel struct {
	textarea textarea.Model
	logger   *slog.Logger
}

func NewInputModel() InputModel {
	// Set up debug logging
	debugFile, err := os.OpenFile("/tmp/wingthing-input-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(debugFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ta := textarea.New()
	ta.Placeholder = "Type your message... (Ctrl+Enter for new line, Enter to send)"
	ta.Focus()
	ta.SetHeight(1) // Start with single line
	ta.ShowLineNumbers = false

	// Configure for auto-expansion
	ta.MaxHeight = 10 // Maximum 10 lines
	ta.CharLimit = 0  // No character limit

	// Disable built-in newline handling so we can control it
	ta.KeyMap.InsertNewline.SetEnabled(false)

	// Remove ALL enter key bindings from the textarea
	ta.KeyMap.InsertNewline.SetKeys() // Remove all keys for newline insertion

	logger.Info("InputModel created", "initial_width", ta.Width())

	return InputModel{
		textarea: ta,
		logger:   logger,
	}
}

func (m InputModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m *InputModel) Update(msg tea.Msg) (*InputModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Block plain enter - let parent handle it for sending
			return m, nil
		case "ctrl+j", "ctrl+enter":
			// Real newline: just stuff it through as a rune
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\n'}}
			fallthrough
		default:
			// 1) Let textarea update first
			m.textarea, cmd = m.textarea.Update(msg)

			// 2) Re-size AFTER update using what it actually renders
			m.FixDynamicHeight()
		}
	default:
		m.textarea, cmd = m.textarea.Update(msg)
		m.FixDynamicHeight()
	}

	return m, cmd
}

// FixDynamicHeight sets textarea height to the amount it *renders*.
// It also nudges the internal viewport so the first line doesn't get hidden.
func (m *InputModel) FixDynamicHeight() {
	currentValue := m.textarea.Value()
	if currentValue == "" {
		if m.textarea.Height() != 1 {
			m.textarea.SetHeight(1)
		}
		return
	}

	// Calculate wrapped lines manually since View() gives us styled output
	width := m.textarea.Width()
	if width <= 0 {
		width = 80
	}

	lines := 1
	currentLineLength := 0
	for _, r := range currentValue {
		if r == '\n' {
			lines++
			currentLineLength = 0
		} else {
			currentLineLength++
			if currentLineLength >= width {
				lines++
				currentLineLength = 0
			}
		}
	}

	if lines > m.textarea.MaxHeight {
		lines = m.textarea.MaxHeight
	}

	m.logger.Info("FixDynamicHeight debug",
		"value_length", len(currentValue),
		"calculated_lines", lines,
		"current_height", m.textarea.Height(),
		"width", width)

	if lines != m.textarea.Height() {
		oldHeight := m.textarea.Height()
		m.textarea.SetHeight(lines)
		// nudge viewport to recompute & reset offsets
		m.textarea.SetValue(currentValue)
		m.textarea.CursorEnd() // keep cursor visible

		m.logger.Info("Fixed dynamic height",
			"old_height", oldHeight,
			"new_height", lines)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// updateHeightIfNeeded adjusts textarea height based on content
func (m *InputModel) updateHeightIfNeeded() {
	currentValue := m.textarea.Value()
	if currentValue == "" {
		if m.textarea.Height() != 1 {
			m.textarea.SetHeight(1)
		}
		return
	}

	// Use LineCount after the value has been updated
	lines := m.textarea.LineCount()

	// Also manually calculate wrapped lines since LineCount() might lag
	width := m.textarea.Width()
	if width <= 0 {
		width = 80 // fallback
	}

	// Calculate lines needed including word wrapping
	wrappedLines := 1
	currentLineLength := 0
	for _, r := range currentValue {
		if r == '\n' {
			wrappedLines++
			currentLineLength = 0
		} else {
			currentLineLength++
			if currentLineLength >= width {
				wrappedLines++
				currentLineLength = 0
			}
		}
	}

	// Use the larger of the two calculations
	if wrappedLines > lines {
		lines = wrappedLines
	}

	// Set height to match content, respecting max height
	targetHeight := lines
	if targetHeight > m.textarea.MaxHeight {
		targetHeight = m.textarea.MaxHeight
	}
	if targetHeight < 1 {
		targetHeight = 1
	}

	currentHeight := m.textarea.Height()
	if targetHeight != currentHeight {
		m.textarea.SetHeight(targetHeight)
		m.logger.Info("Height adjustment made",
			"old_height", currentHeight,
			"new_height", targetHeight,
			"line_count", lines,
			"wrapped_lines", wrappedLines)
	}
}

// setHeightForValue adjusts height based on the provided value, accounting for word wrapping
func (m *InputModel) setHeightForValue(value string) {
	if value == "" {
		m.textarea.SetHeight(1)
		return
	}

	// Store current value to restore later
	oldValue := m.textarea.Value()

	// Temporarily set the value to calculate proper height including wrapped lines
	m.textarea.SetValue(value)

	// Use built-in line counting that accounts for wrapping
	lines := m.textarea.LineCount()

	// Restore the original value
	m.textarea.SetValue(oldValue)

	// Set height to match content, respecting max height
	if lines > m.textarea.MaxHeight {
		lines = m.textarea.MaxHeight
	}
	if lines < 1 {
		lines = 1
	}

	m.textarea.SetHeight(lines)

	m.logger.Info("Height calculation",
		"value_length", len(value),
		"calculated_lines", lines,
		"textarea_width", m.textarea.Width())
}

func (m InputModel) View() string {
	// Get the textarea view
	content := m.textarea.View()

	// Log what we're about to render
	m.logger.Info("Rendering textarea view",
		"content_lines", strings.Count(content, "\n")+1,
		"textarea_height", m.textarea.Height(),
		"line_count", m.textarea.LineCount())

	// Create subtle border style
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")). // Subtle gray
		Padding(0, 1).                         // Add horizontal padding inside border
		MarginBottom(0)

	return borderStyle.Render(content)
}

func (m InputModel) Value() string {
	return m.textarea.Value()
}

func (m *InputModel) Reset() {
	m.textarea.Reset()
	m.textarea.SetHeight(1) // Reset to single line
	// Ensure the display is properly refreshed
	m.FixDynamicHeight()
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

func (m *InputModel) SetWidth(width int) {
	// Account for border (2) + padding (2) = 4 total overhead
	targetWidth := width - 4

	// Ensure minimum reasonable width but don't cap at 60!
	if targetWidth < 20 {
		targetWidth = 20
	}

	m.logger.Info("SetWidth called",
		"terminal_width", width,
		"target_width", targetWidth,
		"before_textarea_width", m.textarea.Width())

	m.textarea.SetWidth(targetWidth)

	// Important: Recalculate height after width change because text reflows
	currentValue := m.textarea.Value()
	if currentValue != "" {
		m.setHeightForValue(currentValue)
	}

	m.logger.Info("SetWidth completed",
		"after_textarea_width", m.textarea.Width())
}

func (m *InputModel) Focus() tea.Cmd {
	return m.textarea.Focus()
}

func (m *InputModel) Blur() {
	m.textarea.Blur()
}
