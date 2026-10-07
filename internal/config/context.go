package config

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// ContextConfig enrolls the wing as an impersonation client. Only the wing reads SecretFile.
type ContextConfig struct {
	URL        string   `yaml:"url"`
	ClientID   string   `yaml:"client_id"`
	SecretFile string   `yaml:"secret_file"`
	Scopes     []string `yaml:"scopes,omitempty"`
}

func (c *ContextConfig) Validate() error {
	if c == nil {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("context: url must be an HTTP(S) base URL without credentials, query or fragment")
	}
	// Assertions and bearer tokens must not cross a network in cleartext.
	if host := u.Hostname(); u.Scheme == "http" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("context: url must use https unless it is a loopback address")
	}
	if strings.TrimSpace(c.ClientID) == "" || !filepath.IsAbs(c.SecretFile) || strings.ContainsAny(c.SecretFile, "\x00\r\n") {
		return fmt.Errorf("context: client_id and absolute secret_file are required")
	}
	for _, scope := range c.Scopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
			return fmt.Errorf("context: invalid scope")
		}
	}
	return nil
}

// LoadWingTools validates the cross-file dependency without reading credentials.
func LoadWingTools(dir string, c *ContextConfig) ([]*ToolConfig, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	tools, err := LoadToolsDir(dir)
	if err != nil {
		return nil, err
	}
	for _, tool := range tools {
		if tool.Context != "" && c == nil {
			return nil, fmt.Errorf("tool %s: context requires a wing context block", tool.Name)
		}
	}
	return tools, nil
}

// Context enrollment is process-scoped, including when roost and wing share a
// process. Separate config loads for forks and tool listeners use this snapshot.
var runtimeContexts sync.Map // config directory -> *WingConfig (Context may be nil)

func FreezeContextConfig(dir string, c *ContextConfig) (*ContextConfig, func()) {
	snapshot := &WingConfig{Context: c}
	value, loaded := runtimeContexts.LoadOrStore(filepath.Clean(dir), snapshot)
	return value.(*WingConfig).Context, func() {
		if !loaded {
			runtimeContexts.CompareAndDelete(filepath.Clean(dir), snapshot)
		}
	}
}

func LoadContextConfig(dir string) (*ContextConfig, error) {
	if value, ok := runtimeContexts.Load(filepath.Clean(dir)); ok {
		return value.(*WingConfig).Context, nil
	}
	cfg, err := LoadWingConfig(dir)
	if err != nil {
		return nil, err
	}
	return cfg.Context, nil
}

// RetainContextConfig lets SIGHUP reload other settings without changing the
// credentials or secret masks of surviving sessions.
func RetainContextConfig(next *WingConfig, current *ContextConfig) {
	if !reflect.DeepEqual(next.Context, current) {
		log.Print("context config change requires a restart")
	}
	next.Context = current
}
