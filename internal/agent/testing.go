package agent

import "context"

// NewTestStream creates a Stream pre-loaded with the given text, for testing.
func NewTestStream(text string) *Stream {
	s := newStream(context.Background())
	if text != "" {
		s.send(Chunk{Text: text})
	}
	s.close(nil)
	return s
}
