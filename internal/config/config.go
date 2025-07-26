package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	// UI Settings
	Theme       string `json:"theme,omitempty"`
	AutoScroll  bool   `json:"auto_scroll,omitempty"`
	
	// Agent Settings
	MaxTurns    int    `json:"max_turns,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
	
	// Tool Settings
	BashTimeout int    `json:"bash_timeout,omitempty"`
	
	// LLM Settings (for future use)
	Model       string `json:"model,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
}

type Manager struct {
	userConfig    *Config
	projectConfig *Config
	merged        *Config
}

func NewManager() *Manager {
	return &Manager{
		userConfig:    &Config{},
		projectConfig: &Config{},
		merged:        &Config{},
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

func (m *Manager) loadConfig(path string, config *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Config file doesn't exist, use defaults
		}
		return err
	}
	
	return json.Unmarshal(data, config)
}

func (m *Manager) mergeConfigs() {
	m.merged = &Config{
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

func (m *Manager) Get() *Config {
	return m.merged
}

func (m *Manager) SaveUserConfig(userConfigDir string) error {
	configPath := filepath.Join(userConfigDir, "settings.json")
	
	// Ensure directory exists
	if err := os.MkdirAll(userConfigDir, 0755); err != nil {
		return err
	}
	
	data, err := json.MarshalIndent(m.userConfig, "", "  ")
	if err != nil {
		return err
	}
	
	return os.WriteFile(configPath, data, 0644)
}

func (m *Manager) SaveProjectConfig(projectDir string) error {
	wingthingDir := filepath.Join(projectDir, ".wingthing")
	configPath := filepath.Join(wingthingDir, "settings.json")
	
	// Ensure directory exists
	if err := os.MkdirAll(wingthingDir, 0755); err != nil {
		return err
	}
	
	data, err := json.MarshalIndent(m.projectConfig, "", "  ")
	if err != nil {
		return err
	}
	
	return os.WriteFile(configPath, data, 0644)
}