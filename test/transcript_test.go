package test

import (
	"os"
	"testing"

	"github.com/behrlich/wingthing/internal/ui"
)

func TestTranscriptRendering(t *testing.T) {
	// Load golden file for future reference
	_, err := os.ReadFile("golden/transcript_test.golden")
	if err != nil {
		t.Fatalf("Failed to read golden file: %v", err)
	}

	// Create transcript model
	transcript := ui.NewTranscriptModel()
	transcript.SetSize(80, 20)

	// Add test messages
	transcript.AddUserMessage("Hello, how are you?")
	transcript.AddAgentMessage("plan", "I'm planning to respond to your greeting...")
	transcript.AddAgentMessage("final", "Hello! I'm doing well, thank you for asking. How can I help you today?")
	transcript.AddThinkingMessage()
	transcript.AddUserMessage("Can you list the files in this directory?")
	transcript.AddAgentMessage("run_tool", "Running: ls -la")
	transcript.AddAgentMessage("observation", "Command output: total 16\ndrwxr-xr-x  4 user user 4096 Jan 15 10:30 .\ndrwxr-xr-x  3 user user 4096 Jan 15 10:25 ..\n-rw-r--r--  1 user user  123 Jan 15 10:30 example.txt\n-rw-r--r--  1 user user   45 Jan 15 10:28 README.md")
	transcript.AddAgentMessage("final", "Here are the files in the current directory. I can see you have an example.txt file and a README.md file.")

	// Get rendered output (this would need access to the internal content)
	// For now, we'll test that messages were added correctly
	// In a real implementation, you'd need to expose the content or use a test-friendly approach

	// Test that messages were added correctly
	messages := transcript.Messages()
	if len(messages) != 8 {
		t.Errorf("Expected 8 messages, got %d", len(messages))
	}

	// Test first message
	if messages[0].Role != "user" || messages[0].Content != "Hello, how are you?" {
		t.Errorf("First message incorrect: %+v", messages[0])
	}

	// Test last message
	lastMsg := messages[len(messages)-1]
	if lastMsg.Role != "agent" || lastMsg.Type != "final" {
		t.Errorf("Last message incorrect: %+v", lastMsg)
	}
}

// Note: This test demonstrates the concept but would need actual implementation
// of message retrieval from the transcript model for full testing