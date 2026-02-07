package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Dir          string            `yaml:"-"`
	DefaultAgent string            `yaml:"default_agent"`
	MachineID    string            `yaml:"machine_id"`
	PollInterval string            `yaml:"poll_interval"`
	Vars         map[string]string `yaml:"vars"`
}

func Load() (*Config, error) {
	dir := os.Getenv("WINGTHING_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		dir = filepath.Join(home, ".wingthing")
	}

	cfg := &Config{
		Dir:          dir,
		DefaultAgent: "claude",
		PollInterval: "1s",
		Vars:         make(map[string]string),
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			cfg.setStandardVars()
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Dir = dir
	if cfg.Vars == nil {
		cfg.Vars = make(map[string]string)
	}
	cfg.setStandardVars()
	return cfg, nil
}

func (c *Config) setStandardVars() {
	home, _ := os.UserHomeDir()
	c.Vars["HOME"] = home
	c.Vars["WINGTHING_DIR"] = c.Dir
	if cwd, err := os.Getwd(); err == nil {
		c.Vars["PROJECT_ROOT"] = cwd
	}
}

func (c *Config) ResolveVars(s string) string {
	for k, v := range c.Vars {
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}

func (c *Config) DBPath() string {
	return filepath.Join(c.Dir, "wt.db")
}

func (c *Config) MemoryDir() string {
	return filepath.Join(c.Dir, "memory")
}

func (c *Config) SkillsDir() string {
	return filepath.Join(c.Dir, "skills")
}

func (c *Config) SocketPath() string {
	return filepath.Join(c.Dir, "wt.sock")
}
