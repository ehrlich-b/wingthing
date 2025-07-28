package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// DummyProvider is a mock LLM provider for testing
type DummyProvider struct {
	delay  time.Duration
	logger *slog.Logger
}

// NewDummyProvider creates a new dummy LLM provider
func NewDummyProvider(delay time.Duration) *DummyProvider {
	// Set up debug logging
	debugFile, err := os.OpenFile("/tmp/wingthing-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(debugFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	
	return &DummyProvider{
		delay:  delay,
		logger: logger,
	}
}

// Chat implements the LLMProvider interface with hardcoded responses
func (d *DummyProvider) Chat(ctx context.Context, messages []interfaces.Message) (*interfaces.LLMResponse, error) {
	// Simulate processing time
	time.Sleep(d.delay)

	// Get the last user message
	var lastUserMessage string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMessage = strings.ToLower(messages[i].Content)
			break
		}
	}

	// Special case: prompt "tool" triggers bash tool call
	if strings.TrimSpace(lastUserMessage) == "tool" {
		return &interfaces.LLMResponse{
			Content: "I'll run a sample bash command for you.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_tool",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "cli",
						Arguments: map[string]any{
							"command": "echo 'Hello from bash tool!'",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	// Special case: prompt "diff" shows large multiline diff viewer
	if strings.TrimSpace(lastUserMessage) == "diff" {
		diffOutput := `--- a/src/main.go
+++ b/src/main.go
@@ -1,10 +1,15 @@
 package main
 
 import (
 	"fmt"
+	"log"
+	"os"
 )
 
 func main() {
-	fmt.Println("Hello World")
+	if len(os.Args) < 2 {
+		log.Fatal("Usage: program <name>")
+	}
+	name := os.Args[1]
+	fmt.Printf("Hello %s!\n", name)
 }`
		return &interfaces.LLMResponse{
			Content:  fmt.Sprintf("Here's a sample diff showing code changes:\n\n```diff\n%s\n```", diffOutput),
			Finished: true,
		}, nil
	}

	// Check for tool call requests
	if strings.Contains(lastUserMessage, "list files") || strings.Contains(lastUserMessage, "ls") {
		return &interfaces.LLMResponse{
			Content: "I'll help you list the files in the current directory.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "cli",
						Arguments: map[string]any{
							"command": "ls -la",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "read") && strings.Contains(lastUserMessage, "file") {
		return &interfaces.LLMResponse{
			Content: "I'll read the file for you.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_2",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "read_file",
						Arguments: map[string]any{
							"file_path": "README.md",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "write") && strings.Contains(lastUserMessage, "file") {
		return &interfaces.LLMResponse{
			Content: "I'll create a test file for you.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_3",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "write_file",
						Arguments: map[string]any{
							"file_path": "test.txt",
							"content":   "Hello from Wingthing!\nThis is a test file created by the AI.",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "edit") && strings.Contains(lastUserMessage, "file") {
		return &interfaces.LLMResponse{
			Content: "I'll edit the test file for you.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_4",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "edit_file",
						Arguments: map[string]any{
							"file_path": "test.txt",
							"old_text":  "Hello from Wingthing!",
							"new_text":  "Hello from Wingthing (edited)!",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	// Special case: prompt "file" demonstrates read then edit workflow
	if strings.TrimSpace(lastUserMessage) == "file" {
		return &interfaces.LLMResponse{
			Content: "I'll first read a file, then edit it to demonstrate the permission system.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_read_demo",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "read_file",
						Arguments: map[string]any{
							"file_path": "README.md",
						},
					},
				},
				{
					ID:   "call_edit_demo",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "edit_file",
						Arguments: map[string]any{
							"file_path": "README.md",
							"old_text":  "# wingthing",
							"new_text":  "# wingthing (Demo Edit)",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "help") {
		return &interfaces.LLMResponse{
			Content: `I'm a dummy LLM provider for testing Wingthing. I can help with:
- Listing files (try "list files" or "ls")
- Reading files (try "read file")
- Writing files (try "write file")
- Editing files (try "edit file")
- File demo with permissions (try "file")
- Running bash commands (try "tool")
- Showing diffs (try "diff")
- General conversation

This is a mock implementation to test the agent system.`,
			Finished: true,
		}, nil
	}

	if strings.Contains(lastUserMessage, "hello") || strings.Contains(lastUserMessage, "hi") {
		return &interfaces.LLMResponse{
			Content:  "Hello! I'm a dummy AI assistant built into Wingthing. How can I help you today?",
			Finished: true,
		}, nil
	}

	// Handle tool results (when the user message contains tool output)
	if len(messages) >= 2 {
		d.logger.Debug("LLM processing message", "lastUserMessage", lastUserMessage)
		// Check if this looks like a tool result - note: message is lowercased above
		if strings.Contains(lastUserMessage, "tool cli executed successfully") {
			d.logger.Debug("LLM detected tool result, returning Finished=true")
			return &interfaces.LLMResponse{
				Content: fmt.Sprintf("I can see the command executed successfully. The output shows: %s",
					summarizeOutput(lastUserMessage)),
				Finished: true,
			}, nil
		}
	}

	// Default response with multi-line lorem ipsum
	defaultResponse := `Hi, I'm your fake AI assistant! Here's some of the things I can do:

• List files and directories (try typing "list files" or "ls")
• Read files (try "read file")
• Write files (try "write file")
• Edit files (try "edit file")
• File demo with permissions (type "file" for read+edit demo)
• Execute bash commands (type "tool" for a demo)
• Show diffs and code changes (type "diff" for a sample)
• Help with various tasks (type "help" for more info)

Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor 
incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis 
nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat.

Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu 
fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in 
culpa qui officia deserunt mollit anim id est laborum.

Sed ut perspiciatis unde omnis iste natus error sit voluptatem accusantium 
doloremque laudantium, totam rem aperiam, eaque ipsa quae ab illo inventore 
veritatis et quasi architecto beatae vitae dicta sunt explicabo.`

	return &interfaces.LLMResponse{
		Content:  defaultResponse,
		Finished: true,
	}, nil
}

// summarizeOutput creates a brief summary of command output
func summarizeOutput(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 3 {
		return output
	}

	// Return first few lines with indication of more content
	summary := strings.Join(lines[:3], "\n")
	return fmt.Sprintf("%s\n... and %d more lines", summary, len(lines)-3)
}
