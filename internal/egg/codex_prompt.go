package egg

import (
	"encoding/json"
	"fmt"
)

// MaxInitialCodexPromptBytes bounds the actual JSON-encoded prompt content in
// the wing-to-egg argv element, which is at least as large as the literal Codex
// argv prompt. Linux MAX_ARG_STRLEN is 131072 bytes including the final NUL.
// Leave 1024 bytes for that NUL, JSON framing, run ID, deadline and flag prefix.
const MaxInitialCodexPromptBytes = 131072 - 1024

// InitialCodexPromptTooLargeError contains sizes only, never prompt content.
type InitialCodexPromptTooLargeError struct {
	SizeBytes  int
	LimitBytes int
}

func (e *InitialCodexPromptTooLargeError) Error() string {
	return fmt.Sprintf("initial native Codex prompt requires %d encoded argv bytes; limit is %d bytes", e.SizeBytes, e.LimitBytes)
}

// InitialCodexPromptArgBytes measures the actual JSON escaping used by the egg
// argument, excluding the surrounding quotes covered by the metadata margin.
func InitialCodexPromptArgBytes(prompt string) int {
	wire, _ := json.Marshal(prompt) // Marshaling a string cannot fail.
	return len(wire) - 2
}

// ValidateInitialCodexPrompt measures encoded bytes without altering input.
// It applies only to the initial native argv turn, not later prompt transports.
func ValidateInitialCodexPrompt(prompt string) error {
	if size := InitialCodexPromptArgBytes(prompt); size > MaxInitialCodexPromptBytes {
		return &InitialCodexPromptTooLargeError{SizeBytes: size, LimitBytes: MaxInitialCodexPromptBytes}
	}
	return nil
}
