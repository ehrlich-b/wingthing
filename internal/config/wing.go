package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WingConfig holds wing-specific settings persisted in ~/.wingthing/wing.yaml.
type WingConfig struct {
	WingID    string     `yaml:"wing_id"`
	Roost     string     `yaml:"roost,omitempty"`
	Org       string     `yaml:"org,omitempty"`
	Root      string     `yaml:"root,omitempty"`
	Labels    []string   `yaml:"labels,omitempty"`
	EggConfig string     `yaml:"egg_config,omitempty"`
	Conv      string     `yaml:"conv,omitempty"`
	Audit     bool       `yaml:"audit,omitempty"`
	Debug     bool       `yaml:"debug,omitempty"`
	Pinned    bool       `yaml:"pinned,omitempty"`     // explicit pin mode toggle
	AuthTTL   string     `yaml:"auth_ttl,omitempty"`   // passkey auth token duration (default "1h")
	AllowKeys []AllowKey `yaml:"allow_keys,omitempty"`
}

// AllowKey is a pinned user for wing access control.
type AllowKey struct {
	Key    string `yaml:"key,omitempty"`     // base64 raw P-256 public key (optional)
	UserID string `yaml:"user_id,omitempty"` // relay user ID
	Email  string `yaml:"email,omitempty"`   // auto-set from relay, for display
}

// LoadWingConfig reads wing.yaml from dir. If the file doesn't exist,
// it returns a zero-value config (no error). If a legacy wing-id file
// exists, the wing_id is seeded from it.
func LoadWingConfig(dir string) (*WingConfig, error) {
	cfg := &WingConfig{}
	path := filepath.Join(dir, "wing.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Migrate from legacy wing-id file
			if idData, idErr := os.ReadFile(filepath.Join(dir, "wing-id")); idErr == nil {
				cfg.WingID = strings.TrimSpace(string(idData))
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveWingConfig writes wing.yaml to dir.
func SaveWingConfig(dir string, cfg *WingConfig) error {
	os.MkdirAll(dir, 0755)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "wing.yaml"), data, 0644)
}
