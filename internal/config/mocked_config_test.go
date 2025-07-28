package config

import (
	"testing"

	"github.com/behrlich/wingthing/internal/agent"
	mocks "github.com/behrlich/wingthing/internal/mocks/interfaces"
	"github.com/stretchr/testify/mock"
)

func TestConfigManagerWithMocks(t *testing.T) {
	// Create a mock filesystem
	mockFS := mocks.NewFileSystem(t)

	// Set up expectations
	userConfig := `{"theme": "dark", "max_turns": 5}`
	projectConfig := `{"max_turns": 10}`

	mockFS.On("ReadFile", mock.AnythingOfType("string")).Return([]byte(userConfig), nil).Once()
	mockFS.On("ReadFile", mock.AnythingOfType("string")).Return([]byte(projectConfig), nil).Once()

	// Create config manager with mock filesystem
	manager := NewManager(mockFS)

	// Test loading configuration
	err := manager.Load("/user/config", "/project")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify configuration merging (project overrides user)
	cfg := manager.Get()
	if cfg.Theme != "dark" {
		t.Errorf("Expected theme 'dark', got '%s'", cfg.Theme)
	}
	if cfg.MaxTurns == nil || *cfg.MaxTurns != 10 {
		if cfg.MaxTurns == nil {
			t.Errorf("Expected max_turns 10, got nil")
		} else {
			t.Errorf("Expected max_turns 10, got %d", *cfg.MaxTurns)
		}
	}

	// Verify all expectations were met
	mockFS.AssertExpectations(t)
}

func TestPermissionEngineWithMocks(t *testing.T) {
	// Create a mock filesystem
	mockFS := mocks.NewFileSystem(t)

	// Set up expectations for file operations
	fileErr := &FileNotExistError{}
	mockFS.On("ReadFile", mock.AnythingOfType("string")).Return(nil, fileErr).Once()
	mockFS.On("IsNotExist", fileErr).Return(true).Once()

	// Create permission engine with mock filesystem
	permEngine := agent.NewPermissionEngine(mockFS)

	// Test loading from non-existent file (should not error)
	err := permEngine.LoadFromFile("/nonexistent/permissions.json")
	if err != nil {
		t.Fatalf("Expected no error for non-existent file, got: %v", err)
	}

	// Verify all expectations were met
	mockFS.AssertExpectations(t)
}

// FileNotExistError implements an error that satisfies os.IsNotExist
type FileNotExistError struct{}

func (e *FileNotExistError) Error() string {
	return "file does not exist"
}
