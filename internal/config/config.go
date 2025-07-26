package config

import (
	"encoding/json"
	"path/filepath"

	"github.com/behrlich/wingthing/internal/interfaces"
)

type Manager struct {
	userConfig    *interfaces.Config
	projectConfig *interfaces.Config
	merged        *interfaces.Config
	fs            interfaces.FileSystem
}

func NewManager(fs interfaces.FileSystem) *Manager {
	return &Manager{
		userConfig:    &interfaces.Config{},
		projectConfig: &interfaces.Config{},
		merged:        &interfaces.Config{},
		fs:            fs,
	}
}

func (m *Manager) Load(userConfigDir, projectDir string) error {
	// Load user config
	userConfigPath := filepath.Join(userConfigDir, "settings.json")
	if err := m.loadConfig(userConfigPath, m.userConfig); err != nil {
		return err
	}
	
	// Load project config
	projectConfigPath := filepath.Join(projectDir, ".wingthing", "settings.json")
	if err := m.loadConfig(projectConfigPath, m.projectConfig); err != nil {
		return err
	}
	
	// Merge configs (project overrides user)
	m.mergeConfigs()
	
	return nil
}

func (m *Manager) loadConfig(path string, config *interfaces.Config) error {
	data, err := m.fs.ReadFile(path)
	if err != nil {
		if m.fs.IsNotExist(err) {
			return nil // Config file doesn't exist, use defaults
		}
		return err
	}
	
	return json.Unmarshal(data, config)
}

func (m *Manager) mergeConfigs() {
	m.merged = &interfaces.Config{
		Theme:       m.getStringValue(m.userConfig.Theme, m.projectConfig.Theme, "default"),
		AutoScroll:  m.getBoolValue(m.userConfig.AutoScroll, m.projectConfig.AutoScroll, true),
		MaxTurns:    m.getIntValue(m.userConfig.MaxTurns, m.projectConfig.MaxTurns, 10),
		Timeout:     m.getIntValue(m.userConfig.Timeout, m.projectConfig.Timeout, 300),
		BashTimeout: m.getIntValue(m.userConfig.BashTimeout, m.projectConfig.BashTimeout, 30),
		Model:       m.getStringValue(m.userConfig.Model, m.projectConfig.Model, ""),
		APIKey:      m.getStringValue(m.userConfig.APIKey, m.projectConfig.APIKey, ""),
		BaseURL:     m.getStringValue(m.userConfig.BaseURL, m.projectConfig.BaseURL, ""),
	}
}

func (m *Manager) getStringValue(user, project, defaultValue string) string {
	if project != "" {
		return project
	}
	if user != "" {
		return user
	}
	return defaultValue
}

func (m *Manager) getBoolValue(user, project, defaultValue bool) bool {
	if project {
		return project
	}
	if user {
		return user
	}
	return defaultValue
}

func (m *Manager) getIntValue(user, project, defaultValue int) int {
	if project != 0 {
		return project
	}
	if user != 0 {
		return user
	}
	return defaultValue
}

func (m *Manager) Get() *interfaces.Config {
	return m.merged
}

func (m *Manager) SaveUserConfig(userConfigDir string) error {
	configPath := filepath.Join(userConfigDir, "settings.json")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(userConfigDir, 0755); err != nil {
		return err
	}
	
	data, err := json.MarshalIndent(m.userConfig, "", "  ")
	if err != nil {
		return err
	}
	
	return m.fs.WriteFile(configPath, data, 0644)
}

func (m *Manager) SaveProjectConfig(projectDir string) error {
	wingthingDir := filepath.Join(projectDir, ".wingthing")
	configPath := filepath.Join(wingthingDir, "settings.json")
	
	// Ensure directory exists
	if err := m.fs.MkdirAll(wingthingDir, 0755); err != nil {
		return err
	}
	
	data, err := json.MarshalIndent(m.projectConfig, "", "  ")
	if err != nil {
		return err
	}
	
	return m.fs.WriteFile(configPath, data, 0644)
}
