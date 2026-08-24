package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"gopkg.in/yaml.v3"
)

type localMCPClientBounds struct {
	MaxSessions      int `yaml:"max_sessions"`
	MaxSpawnsPerHour int `yaml:"max_spawns_per_hour"`
}

type localMCPClientConfig struct {
	Owner  string               `yaml:"owner"`
	Grants []string             `yaml:"grants"`
	Bounds localMCPClientBounds `yaml:"bounds"`
}

type localMCPClientsConfig struct {
	RequireClient bool                            `yaml:"require_client"`
	Clients       map[string]localMCPClientConfig `yaml:"clients"`
}

func loadLocalMCPClientsConfig(cfg *config.Config) (localMCPClientsConfig, error) {
	path := filepath.Join(cfg.Dir, "clients.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return localMCPClientsConfig{}, nil
	}
	if err != nil {
		return localMCPClientsConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0077 != 0 {
		return localMCPClientsConfig{}, fmt.Errorf("%s must not be readable or writable by group/others (run chmod 600 %s)", path, path)
	}
	var clients localMCPClientsConfig
	if err := yaml.Unmarshal(data, &clients); err != nil {
		return localMCPClientsConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return clients, nil
}

var localMCPToolGrants = map[string]string{
	"wingthing_capabilities": "capabilities.read",
	"sandbox_explain":        "sandbox.read",
	"terminal_list":          "terminal.read",
	"terminal_read":          "terminal.read",
	"terminal_wait":          "terminal.read",
	"terminal_send":          "terminal.send",
	"terminal_start":         "terminal.start",
	"agent_start":            "terminal.start",
	"agent_run":              "agent.run",
	"agent_status":           "agent.read",
	"agent_wait":             "agent.read",
	"agent_result":           "agent.read",
	"agent_events":           "agent.read",
	"agent_steer":            "agent.run",
	"agent_stop":             "agent.stop",
	"message_list":           "message.read",
	"message_wait":           "message.read",
	"message_send":           "message.send",
	"terminal_rename":        "terminal.rename",
	"terminal_stop":          "terminal.stop",
	"prompt_list":            "prompt.read",
	"prompt_get":             "prompt.read",
	"task_get":               "prompt.read",
	"prompt_save":            "prompt.save",
	"prompt_run":             "prompt.run",
	"prompt_loop":            "prompt.run",
	"swarm_run":              "prompt.run",
}

func grantSet(grants []string) map[string]bool {
	set := make(map[string]bool, len(grants))
	for _, grant := range grants {
		set[grant] = true
	}
	return set
}
