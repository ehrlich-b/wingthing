package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
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
