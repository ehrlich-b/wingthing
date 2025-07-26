package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type EditRunner struct{}

func NewEditRunner() *EditRunner {
	return &EditRunner{}
}

func (er *EditRunner) Run(ctx context.Context, tool string, params map[string]any) (*Result, error) {
	switch tool {
	case "read_file":
		return er.readFile(params)
	case "write_file":
		return er.writeFile(params)
	case "edit_file":
		return er.editFile(params)
	default:
		return &Result{Error: "unsupported tool: " + tool}, nil
	}
}

func (er *EditRunner) SupportedTools() []string {
	return []string{"read_file", "write_file", "edit_file"}
}

func (er *EditRunner) readFile(params map[string]any) (*Result, error) {
	filePath, ok := params["file_path"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'file_path' parameter"}, nil
	}
	
	content, err := os.ReadFile(filePath)
	if err != nil {
		return &Result{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	
	return &Result{Output: string(content)}, nil
}

func (er *EditRunner) writeFile(params map[string]any) (*Result, error) {
	filePath, ok := params["file_path"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'file_path' parameter"}, nil
	}
	
	content, ok := params["content"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'content' parameter"}, nil
	}
	
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &Result{Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}
	
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return &Result{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
	
	return &Result{Output: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), filePath)}, nil
}

func (er *EditRunner) editFile(params map[string]any) (*Result, error) {
	filePath, ok := params["file_path"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'file_path' parameter"}, nil
	}
	
	oldText, ok := params["old_text"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'old_text' parameter"}, nil
	}
	
	newText, ok := params["new_text"].(string)
	if !ok {
		return &Result{Error: "missing or invalid 'new_text' parameter"}, nil
	}
	
	// Read current content
	content, err := os.ReadFile(filePath)
	if err != nil {
		return &Result{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	
	contentStr := string(content)
	
	// Check if old text exists
	if !strings.Contains(contentStr, oldText) {
		return &Result{Error: "old_text not found in file"}, nil
	}
	
	// Replace text
	newContent := strings.Replace(contentStr, oldText, newText, -1)
	
	// Write back to file
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return &Result{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
	
	return &Result{Output: fmt.Sprintf("Successfully replaced text in %s", filePath)}, nil
}