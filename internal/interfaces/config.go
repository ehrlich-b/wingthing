package interfaces

// Config represents application configuration
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

// ConfigManager handles configuration loading and saving
type ConfigManager interface {
	Load(userConfigDir, projectDir string) error
	Get() *Config
	SaveUserConfig(userConfigDir string) error
	SaveProjectConfig(projectDir string) error
}
