package egg

import "fmt"

// MaxInitialCodexPromptBytes bounds the initial native Codex argv prompt.
// The wing first passes it to the egg as JSON in one argument (up to sixfold
// escaping), then the egg passes it to Codex literally. 16 KiB leaves room
// below the Linux 128 KiB per-argument ceiling at both process boundaries.
const MaxInitialCodexPromptBytes = 16 * 1024

// InitialCodexPromptTooLargeError contains sizes only, never prompt content.
type InitialCodexPromptTooLargeError struct {
	SizeBytes  int
	LimitBytes int
}

func (e *InitialCodexPromptTooLargeError) Error() string {
	return fmt.Sprintf("initial native Codex prompt is %d bytes; limit is %d bytes", e.SizeBytes, e.LimitBytes)
}

// ValidateInitialCodexPrompt counts bytes without trimming or altering input.
// It applies only to the initial native argv turn, not later prompt transports.
func ValidateInitialCodexPrompt(prompt string) error {
	if len(prompt) > MaxInitialCodexPromptBytes {
		return &InitialCodexPromptTooLargeError{SizeBytes: len(prompt), LimitBytes: MaxInitialCodexPromptBytes}
	}
	return nil
}
