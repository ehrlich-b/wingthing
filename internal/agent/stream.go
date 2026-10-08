package agent

import (
	"context"
	"os"
	"os/exec"
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
	redactor     *Redactor
}

func newStream(ctx context.Context) *Stream {
	return &Stream{
		ctx:      ctx,
		ch:       make(chan Chunk, 64),
		redactor: NewRedactor(os.Environ()),
	}
}

func newCommandStream(ctx context.Context, cmd *exec.Cmd, opts RunOpts) *Stream {
	s := newStream(ctx)
	// Include ambient helper credentials even if the sandbox strips them from
	// the command's environment and exposes them through a private helper file.
	s.redactor = NewRedactor(append(os.Environ(), cmd.Environ()...), opts.Credentials...)
	return s
}

func (s *Stream) send(c Chunk) {
	c.Text = s.redactor.Text(c.Text)
	select {
	case s.ch <- c:
	case <-s.ctx.Done():
	}
}

func (s *Stream) close(err error) {
	s.mu.Lock()
	s.err = s.redactor.Error(err)
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
	return s.redactor.Text(b.String())
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
