package agent

import (
	"context"
	"strings"
	"sync"
)

type Stream struct {
	ctx          context.Context
	ch           chan Chunk
	err          error
	mu           sync.Mutex
	chunks       []Chunk
	done         bool
	inputTokens  int
	outputTokens int
}

func newStream(ctx context.Context) *Stream {
	return &Stream{
		ctx: ctx,
		ch:  make(chan Chunk, 64),
	}
}

func (s *Stream) send(c Chunk) {
	select {
	case s.ch <- c:
	case <-s.ctx.Done():
	}
}

func (s *Stream) close(err error) {
	s.mu.Lock()
	s.err = err
	s.done = true
	s.mu.Unlock()
	close(s.ch)
}

func (s *Stream) Next() (Chunk, bool) {
	c, ok := <-s.ch
	if ok {
		s.mu.Lock()
		s.chunks = append(s.chunks, c)
		s.mu.Unlock()
	}
	return c, ok
}

func (s *Stream) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, c := range s.chunks {
		b.WriteString(c.Text)
	}
	return b.String()
}

func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Stream) SetTokens(input, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputTokens = input
	s.outputTokens = output
}

func (s *Stream) Tokens() (input, output int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputTokens, s.outputTokens
}
