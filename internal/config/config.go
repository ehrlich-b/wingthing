package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	Discord  DiscordConfig  `yaml:"discord"`
	LLM      LLMConfig      `yaml:"llm"`
	Dreams   DreamsConfig   `yaml:"dreams"`
	Database DatabaseConfig `yaml:"database"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type DiscordConfig struct {
	Token  string `yaml:"token"`
	UserID string `yaml:"user_id"`
}

type LLMConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
}

type DreamsConfig struct {
	ScheduleTime string `yaml:"schedule_time"`
	Timezone     string `yaml:"timezone"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

// Load reads configuration from a file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Override with environment variables if present
	if token := os.Getenv("DISCORD_TOKEN"); token != "" {
		cfg.Discord.Token = token
	}
	if apiKey := os.Getenv("LLM_API_KEY"); apiKey != "" {
		cfg.LLM.APIKey = apiKey
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Discord.Token == "" {
		return fmt.Errorf("discord.token is required")
	}
	if c.Discord.UserID == "" {
		return fmt.Errorf("discord.user_id is required")
	}
	if c.LLM.Provider == "" {
		return fmt.Errorf("llm.provider is required")
	}
	if c.LLM.Provider != "anthropic" && c.LLM.Provider != "openai" {
		return fmt.Errorf("llm.provider must be 'anthropic' or 'openai'")
	}
	if c.LLM.APIKey == "" {
		return fmt.Errorf("llm.api_key is required")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	return nil
}
