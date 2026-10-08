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
	pending      string
	final        *Chunk
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
	if c.Text == "" && s.pending == "" {
		s.sendReady(c)
		return
	}
	c.Text, s.pending = s.redactor.streamText(s.pending + c.Text)
	if c.Text != "" {
		s.sendReady(c)
	}
}

func (s *Stream) sendReady(c Chunk) {
	select {
	case s.ch <- c:
	case <-s.ctx.Done():
	}
}

func (s *Stream) close(err error) {
	s.mu.Lock()
	if s.pending != "" {
		// At EOF an incomplete credential prefix is ordinary partial output;
		// fully matched credentials still pass through the shared redactor.
		// Reserve the final chunk outside the channel so cancellation cannot
		// drop it and a full queue cannot block close.
		s.final = &Chunk{Text: s.redactor.Text(s.pending)}
		s.pending = ""
	}
	s.err = s.redactor.Error(err)
	s.done = true
	s.mu.Unlock()
	close(s.ch)
}

func (s *Stream) Next() (Chunk, bool) {
	c, ok := <-s.ch
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok && s.final != nil {
		c, ok = *s.final, true
		s.final = nil
	}
	if ok {
		s.chunks = append(s.chunks, c)
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
