package skill

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Agent       string   `yaml:"agent"`
	Isolation   string   `yaml:"isolation"`
	Mounts      []string `yaml:"mounts"`
	Timeout     string   `yaml:"timeout"`
	Memory      []string `yaml:"memory"`
	MemoryWrite bool     `yaml:"memory_write"`
	Schedule    string   `yaml:"schedule"`
	Tags        []string `yaml:"tags"`
	Thread      bool     `yaml:"thread"`
	Body        string   `yaml:"-"`
}

func Load(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill file: %w", err)
	}
	return Parse(string(data))
}

func Parse(content string) (*Skill, error) {
	front, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	var s Skill
	if err := yaml.Unmarshal([]byte(front), &s); err != nil {
		return nil, fmt.Errorf("parse skill frontmatter: %w", err)
	}
	s.Body = body
	return &s, nil
}

func splitFrontmatter(content string) (front, body string, err error) {
	const fence = "---"
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, fence) {
		return "", "", fmt.Errorf("skill file must start with ---")
	}

	// Find closing fence after the opening one
	rest := trimmed[len(fence):]
	idx := strings.Index(rest, "\n"+fence)
	if idx < 0 {
		return "", "", fmt.Errorf("no closing --- found in skill frontmatter")
	}

	front = strings.TrimSpace(rest[:idx])
	// Body starts after the closing fence line
	afterClose := rest[idx+1+len(fence):]
	// Skip the rest of the closing fence line (could have trailing newline)
	if nl := strings.IndexByte(afterClose, '\n'); nl >= 0 {
		body = afterClose[nl+1:]
	} else {
		body = ""
	}
	return front, body, nil
}
