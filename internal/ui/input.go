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
	ta.SetHeight(1)  // Start with single line
	ta.ShowLineNumbers = false
	
	// Configure for auto-expansion
	ta.MaxHeight = 10  // Maximum 10 lines
	ta.CharLimit = 0   // No character limit
	
	// Disable built-in newline handling so we can control it
	ta.KeyMap.InsertNewline.SetEnabled(false)
	
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
		case "ctrl+j", "ctrl+enter":
			// Handle ctrl+j (which is ctrl+enter in most terminals) to insert newline
			// Pre-calculate if we need to expand height BEFORE inserting the newline
			// Calculate current lines including the new one we're about to add
			expectedLines := m.textarea.LineCount() + 1
			
			// If we need to expand, do it BEFORE inserting the newline
			if expectedLines > m.textarea.Height() && expectedLines <= m.textarea.MaxHeight {
				m.textarea.SetHeight(expectedLines)
				m.logger.Info("Pre-expanded height before newline insertion", 
					"new_height", expectedLines)
			}
			
			// Now insert the newline
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune{'\n'},
			})
		case "enter":
			// Block plain enter - let parent handle it for sending
			return m, nil
		case "backspace", "delete":
			// Height will be adjusted after the update
		}
		
		// For character input, check for newlines that aren't from Ctrl+Enter
		if len(msg.Runes) > 0 {
			// Check if this contains newlines (likely from paste)
			for i, r := range msg.Runes {
				if r == '\n' || r == '\r' {
					// This is a newline NOT from Ctrl+Enter (which we handle above)
					// Replace with space to prevent accidental send
					msg.Runes[i] = ' '
					m.logger.Info("Replaced newline with space in character input")
				}
			}
			
			// Debug log text length vs width
			m.logger.Info("Character input", 
				"current_len", len(m.textarea.Value()),
				"textarea_width", m.textarea.Width(),
				"runes", string(msg.Runes))
		}
	}
	
	// Let textarea handle all other keys
	m.textarea, cmd = m.textarea.Update(msg)
	
	// After update, check if we need to adjust height for wrapped text
	currentValue := m.textarea.Value()
	if currentValue != "" {
		// Use LineCount after the value has been updated
		lines := m.textarea.LineCount()
		
		// Also manually calculate expected lines based on wrapping
		textWidth := m.textarea.Width()
		if textWidth <= 0 {
			textWidth = 80 // fallback
		}
		
		// Simple wrap calculation - count how many lines we need
		manualLines := 1
		currentLineLength := 0
		for _, r := range currentValue {
			if r == '\n' {
				manualLines++
				currentLineLength = 0
			} else {
				currentLineLength++
				if currentLineLength >= textWidth {
					manualLines++
					currentLineLength = 0
				}
			}
		}
		
		// Use the larger of the two calculations
		if manualLines > lines {
			lines = manualLines
		}
		
		// Always log the current state for debugging
		m.logger.Info("Current textarea state", 
			"value_length", len(currentValue),
			"line_count", lines,
			"manual_lines", manualLines,
			"current_height", m.textarea.Height(),
			"width", m.textarea.Width())
		
		// Always set height to match line count (force update)
		currentHeight := m.textarea.Height()
		targetHeight := lines
		if targetHeight > m.textarea.MaxHeight {
			targetHeight = m.textarea.MaxHeight
		}
		if targetHeight < 1 {
			targetHeight = 1
		}
		
		// Always set the height, even if it seems unchanged
		// This ensures the textarea properly renders all lines
		m.textarea.SetHeight(targetHeight)
		
		// If height changed, we need to handle viewport
		if targetHeight != currentHeight {
			m.logger.Info("Height adjustment made", 
				"old_height", currentHeight,
				"new_height", targetHeight)
			
			// When height increases and we're not at max height yet,
			// log for debugging (viewport should already be correct from pre-expansion)
			if targetHeight > currentHeight && targetHeight < m.textarea.MaxHeight {
				m.logger.Info("Height increased (should have been pre-expanded for newlines)")
			}
			
			// Log cursor position
			lineInfo := m.textarea.LineInfo()
			m.logger.Info("Cursor position after height change",
				"cursor_line", lineInfo.RowOffset,
				"cursor_col", lineInfo.ColumnOffset,
				"height", targetHeight)
		}
	} else {
		// Empty textarea, reset to single line
		if m.textarea.Height() != 1 {
			m.textarea.SetHeight(1)
			m.logger.Info("Reset height to 1 for empty textarea")
		}
	}
	
	return m, cmd
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
		"content_lines", strings.Count(content, "\n") + 1,
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
