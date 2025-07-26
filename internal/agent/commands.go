package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

type SlashCommand struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Args        []string          `yaml:"args"`
	Body        string            `yaml:"-"` // The template body after frontmatter
}

type CommandLoader struct {
	commands map[string]SlashCommand
}

func NewCommandLoader() *CommandLoader {
	return &CommandLoader{
		commands: make(map[string]SlashCommand),
	}
}

func (cl *CommandLoader) LoadCommands(configDir, projectDir string) error {
	// Load user commands
	userCommandsDir := filepath.Join(configDir, "commands")
	if err := cl.loadCommandsFromDir(userCommandsDir); err != nil {
		return fmt.Errorf("loading user commands: %w", err)
	}
	
	// Load project commands (override user commands)
	projectCommandsDir := filepath.Join(projectDir, ".wingthing", "commands")
	if err := cl.loadCommandsFromDir(projectCommandsDir); err != nil {
		return fmt.Errorf("loading project commands: %w", err)
	}
	
	return nil
}

func (cl *CommandLoader) loadCommandsFromDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // Directory doesn't exist, skip
	}
	
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		
		filePath := filepath.Join(dir, entry.Name())
		if err := cl.loadCommand(filePath); err != nil {
			return fmt.Errorf("loading command %s: %w", entry.Name(), err)
		}
	}
	
	return nil
}

func (cl *CommandLoader) loadCommand(filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	
	// Parse frontmatter and body
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		return fmt.Errorf("invalid command file format: missing frontmatter")
	}
	
	var cmd SlashCommand
	if err := yaml.Unmarshal([]byte(parts[1]), &cmd); err != nil {
		return fmt.Errorf("parsing frontmatter: %w", err)
	}
	
	cmd.Body = strings.TrimSpace(parts[2])
	cl.commands[cmd.Name] = cmd
	
	return nil
}

func (cl *CommandLoader) GetCommand(name string) (SlashCommand, bool) {
	cmd, exists := cl.commands[name]
	return cmd, exists
}

func (cl *CommandLoader) ListCommands() []string {
	var names []string
	for name := range cl.commands {
		names = append(names, name)
	}
	return names
}

func (cl *CommandLoader) ExecuteCommand(name string, args []string, env map[string]string) (string, error) {
	cmd, exists := cl.commands[name]
	if !exists {
		return "", fmt.Errorf("command not found: %s", name)
	}
	
	// Prepare template data
	data := map[string]any{
		"ARGS": args,
		"CWD":  env["PWD"],
	}
	
	// Add environment variables
	for key, value := range env {
		data[key] = value
	}
	
	// Execute template
	tmpl, err := template.New(name).Parse(cmd.Body)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	
	var result strings.Builder
	if err := tmpl.Execute(&result, data); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	
	return result.String(), nil
}
