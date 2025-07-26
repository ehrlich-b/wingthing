package agent

import (
	"path/filepath"

	"github.com/behrlich/wingthing/internal/interfaces"
)

type Memory struct {
	UserMemory    map[string]string `json:"user_memory"`
	ProjectMemory map[string]string `json:"project_memory"`
	fs            interfaces.FileSystem
}

func NewMemory(fs interfaces.FileSystem) *Memory {
	return &Memory{
		UserMemory:    make(map[string]string),
		ProjectMemory: make(map[string]string),
		fs:            fs,
	}
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
	
	// TODO: Parse CLAUDE.md format
	m.UserMemory["content"] = string(content)
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
	
	// TODO: Parse CLAUDE.md format
	m.ProjectMemory["content"] = string(content)
	return nil
}

func (m *Memory) SaveUserMemory(configDir string) error {
	memoryPath := filepath.Join(configDir, "CLAUDE.md")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	
	// TODO: Format as CLAUDE.md
	content := m.UserMemory["content"]
	return m.fs.WriteFile(memoryPath, []byte(content), 0644)
}

func (m *Memory) SaveProjectMemory(projectDir string) error {
	wingthingDir := filepath.Join(projectDir, ".wingthing")
	memoryPath := filepath.Join(wingthingDir, "CLAUDE.md")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(wingthingDir, 0755); err != nil {
		return err
	}
	
	// TODO: Format as CLAUDE.md
	content := m.ProjectMemory["content"]
	return m.fs.WriteFile(memoryPath, []byte(content), 0644)
}

func (m *Memory) UpdateUserMemory(key, value string) {
	m.UserMemory[key] = value
}

func (m *Memory) UpdateProjectMemory(key, value string) {
	m.ProjectMemory[key] = value
}

func (m *Memory) GetUserMemory(key string) (string, bool) {
	value, exists := m.UserMemory[key]
	return value, exists
}

func (m *Memory) GetProjectMemory(key string) (string, bool) {
	value, exists := m.ProjectMemory[key]
	return value, exists
}
