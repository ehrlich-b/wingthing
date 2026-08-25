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

func grantSet(grants []string) map[string]bool {
	set := make(map[string]bool, len(grants))
	for _, grant := range grants {
		set[grant] = true
	}
	return set
}
