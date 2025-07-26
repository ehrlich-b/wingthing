package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/behrlich/wingthing/internal/llm"
	"github.com/behrlich/wingthing/internal/tools"
)

// MockFileSystem for testing
type MockFileSystem struct{}

func (fs *MockFileSystem) ReadFile(filename string) ([]byte, error) {
	return []byte{}, nil
}

func (fs *MockFileSystem) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return nil
}

func (fs *MockFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return nil
}

func (fs *MockFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	return []os.DirEntry{}, nil
}

func (fs *MockFileSystem) Remove(name string) error {
	return nil
}

func (fs *MockFileSystem) IsNotExist(err error) bool {
	return false
}

func TestOrchestrator_Integration_SimpleConversation(t *testing.T) {
	// Create event channel
	events := make(chan Event, 100)
	
	// Create mock filesystem
	fs := &MockFileSystem{}
	
	// Create tool runner
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("bash", tools.NewBashRunner())
	
	// Create components
	memoryManager := NewMemory(fs)
	permissionChecker := NewPermissionEngine(fs)
	llmProvider := llm.NewDummyProvider(10 * time.Millisecond)
	
	// Pre-grant permission for bash command
	permissionChecker.GrantPermission("bash", "execute", map[string]any{"command": "ls -la"}, AlwaysAllow)
	
	// Create orchestrator
	orchestrator := NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
	)
	
	// Start processing in background
	ctx := context.Background()
	go orchestrator.ProcessPrompt(ctx, "list files")
	
	// Collect events
	var receivedEvents []Event
	timeout := time.After(5 * time.Second)
	
	for {
		select {
		case event := <-events:
			receivedEvents = append(receivedEvents, event)
			
			// Check if we got the final event
			if event.Type == string(EventTypeFinal) {
				goto done
			}
			
		case <-timeout:
			t.Fatal("Timeout waiting for events")
		}
	}
	
done:
	// Verify we got the expected sequence of events
	if len(receivedEvents) < 5 {
		t.Fatalf("Expected at least 5 events, got %d", len(receivedEvents))
	}
	
	// Should have: Plan -> RunTool -> Observation -> Plan (for tool results) -> Final
	expectedTypes := []string{
		string(EventTypePlan),
		string(EventTypeRunTool),
		string(EventTypeObservation),
		string(EventTypePlan), // LLM processes tool results
		string(EventTypeFinal),
	}
	
	// Check that we have at least the minimum expected events
	for i, expectedType := range expectedTypes {
		if i >= len(receivedEvents) {
			t.Fatalf("Missing event at position %d, expected %s", i, expectedType)
		}
		
		if receivedEvents[i].Type != expectedType {
			t.Fatalf("Event %d: expected type %s, got %s", i, expectedType, receivedEvents[i].Type)
		}
	}
	
	// Verify we got a final event
	finalEvent := receivedEvents[len(receivedEvents)-1]
	if finalEvent.Type != string(EventTypeFinal) {
		t.Fatalf("Last event should be final, got %s", finalEvent.Type)
	}
}

func TestOrchestrator_Integration_PermissionRequest(t *testing.T) {
	// Create event channel
	events := make(chan Event, 100)
	
	// Create mock filesystem
	fs := &MockFileSystem{}
	
	// Create tool runner
	toolRunner := tools.NewMultiRunner()
	toolRunner.RegisterRunner("bash", tools.NewBashRunner())
	
	// Create components
	memoryManager := NewMemory(fs)
	permissionChecker := NewPermissionEngine(fs)
	llmProvider := llm.NewDummyProvider(10 * time.Millisecond)
	
	// Don't pre-grant permissions - should trigger permission request
	
	// Create orchestrator
	orchestrator := NewOrchestrator(
		toolRunner,
		events,
		memoryManager,
		permissionChecker,
		llmProvider,
	)
	
	// Start processing in background
	ctx := context.Background()
	go orchestrator.ProcessPrompt(ctx, "list files")
	
	// Collect events
	var receivedEvents []Event
	timeout := time.After(5 * time.Second)
	
	for {
		select {
		case event := <-events:
			receivedEvents = append(receivedEvents, event)
			
			// Check if we got a permission request
			if event.Type == string(EventTypePermissionRequest) {
				goto done
			}
			
		case <-timeout:
			t.Fatal("Timeout waiting for permission request")
		}
	}
	
done:
	// Verify we got a permission request
	found := false
	for _, event := range receivedEvents {
		if event.Type == string(EventTypePermissionRequest) {
			found = true
			break
		}
	}
	
	if !found {
		t.Fatal("Expected permission request event")
	}
}