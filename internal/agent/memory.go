package agent

import (
	"bufio"
	"path/filepath"
	"strings"

	"github.com/behrlich/wingthing/internal/interfaces"
)

type Memory struct {
	UserContext    *interfaces.ClaudeContext `json:"user_context"`
	ProjectContext *interfaces.ClaudeContext `json:"project_context"`
	fs             interfaces.FileSystem
}

func NewMemory(fs interfaces.FileSystem) *Memory {
	return &Memory{
		UserContext:    nil,
		ProjectContext: nil,
		fs:             fs,
	}
}

// parseClaude parses CLAUDE.md content into structured format
func parseClaude(content string) *interfaces.ClaudeContext {
	context := &interfaces.ClaudeContext{
		Instructions: make([]string, 0),
		Context:      make(map[string]string),
		RawContent:   content,
	}
	
	scanner := bufio.NewScanner(strings.NewReader(content))
	var currentSection string
	var currentContent strings.Builder
	
	for scanner.Scan() {
		line := scanner.Text()
		
		// Check for section headers (# headings)
		if strings.HasPrefix(line, "#") {
			// Save previous section if any
			if currentSection != "" && currentContent.Len() > 0 {
				sectionContent := strings.TrimSpace(currentContent.String())
				if sectionContent != "" {
					context.Context[currentSection] = sectionContent
				}
			}
			
			// Start new section
			currentSection = strings.TrimSpace(strings.TrimLeft(line, "# "))
			currentContent.Reset()
		} else if strings.TrimSpace(line) != "" {
			// Add content to current section
			if currentContent.Len() > 0 {
				currentContent.WriteString("\n")
			}
			currentContent.WriteString(line)
			
			// If no section yet, treat as instruction
			if currentSection == "" {
				context.Instructions = append(context.Instructions, strings.TrimSpace(line))
			}
		}
	}
	
	// Save last section
	if currentSection != "" && currentContent.Len() > 0 {
		sectionContent := strings.TrimSpace(currentContent.String())
		if sectionContent != "" {
			context.Context[currentSection] = sectionContent
		}
	}
	
	return context
}

func (m *Memory) LoadUserMemory(configDir string) error {
	memoryPath := filepath.Join(configDir, "CLAUDE.md")
	content, err := m.fs.ReadFile(memoryPath)
	if err != nil {
		if m.fs.IsNotExist(err) {
			return nil // No memory file yet
		}
		return err
	}
	
	m.UserContext = parseClaude(string(content))
	return nil
}

func (m *Memory) LoadProjectMemory(projectDir string) error {
	memoryPath := filepath.Join(projectDir, ".wingthing", "CLAUDE.md")
	content, err := m.fs.ReadFile(memoryPath)
	if err != nil {
		if m.fs.IsNotExist(err) {
			return nil // No memory file yet
		}
		return err
	}
	
	m.ProjectContext = parseClaude(string(content))
	return nil
}

func (m *Memory) SaveUserMemory(configDir string) error {
	memoryPath := filepath.Join(configDir, "CLAUDE.md")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	
	var content string
	if m.UserContext != nil {
		content = m.UserContext.RawContent
	}
	return m.fs.WriteFile(memoryPath, []byte(content), 0644)
}

func (m *Memory) SaveProjectMemory(projectDir string) error {
	wingthingDir := filepath.Join(projectDir, ".wingthing")
	memoryPath := filepath.Join(wingthingDir, "CLAUDE.md")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(wingthingDir, 0755); err != nil {
		return err
	}
	
	var content string
	if m.ProjectContext != nil {
		content = m.ProjectContext.RawContent
	}
	return m.fs.WriteFile(memoryPath, []byte(content), 0644)
}

func (m *Memory) UpdateUserContext(context *interfaces.ClaudeContext) {
	m.UserContext = context
}

func (m *Memory) UpdateProjectContext(context *interfaces.ClaudeContext) {
	m.ProjectContext = context
}

func (m *Memory) GetUserContext() *interfaces.ClaudeContext {
	return m.UserContext
}

func (m *Memory) GetProjectContext() *interfaces.ClaudeContext {
	return m.ProjectContext
}

// GetCombinedInstructions returns all instructions from both user and project context
func (m *Memory) GetCombinedInstructions() []string {
	var instructions []string
	
	if m.UserContext != nil {
		instructions = append(instructions, m.UserContext.Instructions...)
	}
	
	if m.ProjectContext != nil {
		instructions = append(instructions, m.ProjectContext.Instructions...)
	}
	
	return instructions
}

// GetCombinedContext returns all context sections from both user and project
func (m *Memory) GetCombinedContext() map[string]string {
	combined := make(map[string]string)
	
	// Add user context first
	if m.UserContext != nil {
		for key, value := range m.UserContext.Context {
			combined[key] = value
		}
	}
	
	// Add project context (overwrites user context for same keys)
	if m.ProjectContext != nil {
		for key, value := range m.ProjectContext.Context {
			combined[key] = value
		}
	}
	
	return combined
}
