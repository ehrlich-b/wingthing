package llm

import (
	"context"
	"testing"
	"time"

	"github.com/behrlich/wingthing/internal/interfaces"
)

func TestDummyProvider_Chat_Greeting(t *testing.T) {
	provider := NewDummyProvider(10 * time.Millisecond)
	
	messages := []interfaces.Message{
		{Role: "user", Content: "hello"},
	}
	
	resp, err := provider.Chat(context.Background(), messages)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	if resp.Content == "" {
		t.Fatal("Expected non-empty response content")
	}
	
	if !resp.Finished {
		t.Fatal("Expected response to be finished for greeting")
	}
	
	if len(resp.ToolCalls) != 0 {
		t.Fatal("Expected no tool calls for greeting")
	}
}

func TestDummyProvider_Chat_ListFiles(t *testing.T) {
	provider := NewDummyProvider(10 * time.Millisecond)
	
	messages := []interfaces.Message{
		{Role: "user", Content: "list files"},
	}
	
	resp, err := provider.Chat(context.Background(), messages)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	if resp.Content == "" {
		t.Fatal("Expected non-empty response content")
	}
	
	if resp.Finished {
		t.Fatal("Expected response to not be finished when tool calls are made")
	}
	
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	
	toolCall := resp.ToolCalls[0]
	if toolCall.Function.Name != "bash" {
		t.Fatalf("Expected bash tool call, got %s", toolCall.Function.Name)
	}
	
	command, ok := toolCall.Function.Arguments["command"]
	if !ok {
		t.Fatal("Expected command argument in tool call")
	}
	
	if command != "ls -la" {
		t.Fatalf("Expected 'ls -la' command, got %s", command)
	}
}

func TestDummyProvider_Chat_Help(t *testing.T) {
	provider := NewDummyProvider(10 * time.Millisecond)
	
	messages := []interfaces.Message{
		{Role: "user", Content: "help"},
	}
	
	resp, err := provider.Chat(context.Background(), messages)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	if resp.Content == "" {
		t.Fatal("Expected non-empty response content")
	}
	
	if !resp.Finished {
		t.Fatal("Expected response to be finished for help")
	}
	
	if len(resp.ToolCalls) != 0 {
		t.Fatal("Expected no tool calls for help")
	}
}