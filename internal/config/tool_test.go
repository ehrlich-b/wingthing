package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadToolsDir_Valid(t *testing.T) {
	dir := t.TempDir()
	yaml := `name: echo-test
description: "Echo args back"
params:
  - name: message
    description: Text to echo
    required: true
    examples: [hello]
run: echo "$@"
timeout: 5s
env:
  FOO: bar
`
	os.WriteFile(filepath.Join(dir, "echo.yaml"), []byte(yaml), 0600)
	tools, err := LoadToolsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tc := tools[0]
	if tc.Name != "echo-test" {
		t.Errorf("name = %q, want %q", tc.Name, "echo-test")
	}
	if tc.Description != "Echo args back" {
		t.Errorf("description = %q, want %q", tc.Description, "Echo args back")
	}
	if len(tc.Params) != 1 || tc.Params[0].Name != "message" || tc.Params[0].Type != "string" || !tc.Params[0].Required {
		t.Fatalf("params = %#v", tc.Params)
	}
	if len(tc.Params[0].Examples) != 1 || tc.Params[0].Examples[0] != "hello" {
		t.Fatalf("param examples = %#v", tc.Params[0].Examples)
	}
	if tc.Run != `echo "$@"` {
		t.Errorf("run = %q", tc.Run)
	}
	if tc.Env["FOO"] != "bar" {
		t.Errorf("env FOO = %q, want %q", tc.Env["FOO"], "bar")
	}
	if tc.TimeoutDuration() != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", tc.TimeoutDuration())
	}
}

func TestLoadToolsDir_MissingDir(t *testing.T) {
	tools, err := LoadToolsDir("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error for missing dir: %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil tools for missing dir, got %v", tools)
	}
}

func TestLoadToolsDir_MissingName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("run: echo hi\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadToolsDir_MissingRun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: test\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestLoadToolsDir_SkipsNonYAML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not yaml"), 0600)
	os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte("name: t\nrun: echo\n"), 0600)
	tools, err := LoadToolsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
}

func TestLoadToolsDir_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("name: alpha\nrun: echo a\n"), 0600)
	os.WriteFile(filepath.Join(dir, "b.yml"), []byte("name: beta\nrun: echo b\n"), 0600)
	tools, err := LoadToolsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestLoadToolsDir_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(":::invalid\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadToolsDir_RejectsUnknownFieldsAndInvalidEnvNames(t *testing.T) {
	for name, body := range map[string]string{
		"unknown-field": "name: test\nrun: echo\nparam: []\n",
		"invalid-env":   "name: test\nrun: echo\nenv:\n  BAD-NAME: value\n",
		"second-doc":    "name: test\nrun: echo\n---\nname: other\nrun: echo\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadToolsDir(dir); err == nil {
				t.Fatal("expected invalid tool config to fail")
			}
		})
	}
}

func TestToolConfig_TimeoutDuration(t *testing.T) {
	tc := &ToolConfig{Timeout: ""}
	if tc.TimeoutDuration() != 0 {
		t.Errorf("empty timeout should return 0")
	}
	tc.Timeout = "garbage"
	if tc.TimeoutDuration() != 0 {
		t.Errorf("invalid timeout should return 0")
	}
	tc.Timeout = "30s"
	if tc.TimeoutDuration() != 30*time.Second {
		t.Errorf("30s timeout = %v", tc.TimeoutDuration())
	}
}

func TestLoadToolsDir_PathTraversalName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "evil.yaml"), []byte("name: ../evil\nrun: echo\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for path traversal name")
	}
}

func TestLoadToolsDir_ShellMetacharName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("name: \"foo;rm\"\nrun: echo\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for shell metachar name")
	}
}

func TestLoadToolsDir_ValidNameChars(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("name: slide-db\nrun: echo\n"), 0600)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("name: my_tool2\nrun: echo\n"), 0600)
	tools, err := LoadToolsDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestLoadToolsDir_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("name: echo\nrun: echo a\n"), 0600)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("name: echo\nrun: echo b\n"), 0600)
	_, err := LoadToolsDir(dir)
	if err == nil {
		t.Fatal("expected error for duplicate tool name")
	}
}

func TestLoadToolsDir_ValidatesParams(t *testing.T) {
	for name, params := range map[string]string{
		"invalid-name": "  - name: bad-name\n",
		"duplicate":    "  - name: query\n  - name: query\n",
		"invalid-type": "  - name: query\n    type: secret\n",
		"typed-enum":   "  - name: count\n    type: integer\n    enum: [one]\n",
		"empty-enum":   "  - name: method\n    enum: ['']\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			body := "name: test\nparams:\n" + params + "run: echo\n"
			if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadToolsDir(dir); err == nil {
				t.Fatal("expected invalid params to fail")
			}
		})
	}
}

func TestToolNames(t *testing.T) {
	tools := []*ToolConfig{
		{Name: "a"},
		{Name: "b"},
	}
	names := ToolNames(tools)
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("ToolNames = %v", names)
	}
}
