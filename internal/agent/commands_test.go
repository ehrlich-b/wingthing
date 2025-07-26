package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlashCommandTemplateExpansion(t *testing.T) {
	// Create temporary directory for test commands
	tempDir := t.TempDir()
	commandsDir := filepath.Join(tempDir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatalf("Failed to create commands directory: %v", err)
	}

	// Create a test command file
	commandContent := `---
name: "test"
description: "A test command"
args: ["arg1", "arg2"]
---
Hello {{index .ARGS 0}}, your working directory is {{.CWD}}.
Environment variable PATH is {{.PATH}}.`

	commandFile := filepath.Join(commandsDir, "test.md")
	if err := os.WriteFile(commandFile, []byte(commandContent), 0644); err != nil {
		t.Fatalf("Failed to write command file: %v", err)
	}

	// Load commands
	loader := NewCommandLoader()
	if err := loader.LoadCommands(tempDir, ""); err != nil {
		t.Fatalf("Failed to load commands: %v", err)
	}

	// Test command existence
	cmd, exists := loader.GetCommand("test")
	if !exists {
		t.Fatal("Expected 'test' command to exist")
	}

	if cmd.Name != "test" {
		t.Errorf("Expected command name 'test', got '%s'", cmd.Name)
	}

	if cmd.Description != "A test command" {
		t.Errorf("Expected description 'A test command', got '%s'", cmd.Description)
	}

	if len(cmd.Args) != 2 || cmd.Args[0] != "arg1" || cmd.Args[1] != "arg2" {
		t.Errorf("Expected args [arg1, arg2], got %v", cmd.Args)
	}

	// Test template execution
	env := map[string]string{
		"PWD":  "/test/directory",
		"PATH": "/usr/bin:/bin",
	}
	args := []string{"World", "unused"}

	result, err := loader.ExecuteCommand("test", args, env)
	if err != nil {
		t.Fatalf("Failed to execute command: %v", err)
	}

	expected := "Hello World, your working directory is /test/directory.\nEnvironment variable PATH is /usr/bin:/bin."
	if result != expected {
		t.Errorf("Expected result:\n%s\nGot:\n%s", expected, result)
	}
}

func TestSlashCommandNotFound(t *testing.T) {
	loader := NewCommandLoader()

	_, exists := loader.GetCommand("nonexistent")
	if exists {
		t.Error("Expected 'nonexistent' command to not exist")
	}

	_, err := loader.ExecuteCommand("nonexistent", []string{}, map[string]string{})
	if err == nil {
		t.Error("Expected error when executing nonexistent command")
	}
}
