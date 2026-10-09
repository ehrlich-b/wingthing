package agent

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type codexOutputPipe struct {
	file    io.ReadCloser
	process *codexProcess
	mu      sync.Mutex
	armed   bool
	exited  bool
	drained bool // only the parser goroutine changes this
	closing atomic.Bool
	changed chan struct{}
	reads   chan codexPipeRead
	stop    chan struct{}
	current codexPipeRead // only the parser goroutine changes this
}

type codexPipeRead struct {
	bytes []byte
	err   error
}

func newCodexOutputPipe(file io.ReadCloser, process *codexProcess) *codexOutputPipe {
	p := &codexOutputPipe{file: file, process: process, changed: make(chan struct{}, 1), reads: make(chan codexPipeRead, 1), stop: make(chan struct{})}
	go func() {
		defer close(p.reads)
		for {
			// Retain only a bounded amount ahead of the parser. A full queue
			// pauses the reader, with no cleanup running behind its consumer.
			buf := make([]byte, 4096)
			n, err := file.Read(buf)
			select {
			case p.reads <- codexPipeRead{bytes: buf[:n], err: err}:
			case <-p.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

func (p *codexOutputPipe) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}

func (p *codexOutputPipe) armQuiet() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = true
	p.notify()
}

func (p *codexOutputPipe) disarmQuiet() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		p.armed = false
		p.notify()
	}
}

func (p *codexOutputPipe) leaderExited() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exited = true
	p.armed = true
	p.notify()
}

func (p *codexOutputPipe) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for {
		if len(p.current.bytes) > 0 || p.current.err != nil {
			n := copy(b, p.current.bytes)
			p.current.bytes = p.current.bytes[n:]
			if len(p.current.bytes) > 0 {
				return n, nil
			}
			err := p.current.err
			p.current = codexPipeRead{}
			if p.closing.Load() && errors.Is(err, os.ErrClosed) {
				err = io.EOF
			}
			return n, err
		}
		// Empty the queue before timing inactivity. Parsing buffered events
		// and waiting for a slow consumer must never trigger cleanup.
		select {
		case result, ok := <-p.reads:
			if !ok {
				return 0, io.EOF
			}
			p.current = result
			continue
		default:
		}
		p.mu.Lock()
		var quiet <-chan time.Time
		var timer *time.Timer
		if p.armed {
			timer = time.NewTimer(codexCompletionQuiet)
			quiet = timer.C
		}
		p.mu.Unlock()
		select {
		case result, ok := <-p.reads:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}
			p.current = result
		case <-p.changed:
			if timer != nil {
				timer.Stop()
			}
		case <-quiet:
			// A byte arrival wins over a simultaneously ready timer.
			select {
			case result, ok := <-p.reads:
				if !ok {
					return 0, io.EOF
				}
				p.current = result
				continue
			default:
			}
			if p.drained {
				return 0, io.EOF // an escaped, quiet pipe holder cannot hang us
			}
			p.process.cleanup(true)
			p.drained = true
			// Killing the group must not close stdout: drain every byte that
			// was already written, with another quiet read for escaped holders.
		}
	}
}

func (p *codexOutputPipe) close() {
	if p.closing.CompareAndSwap(false, true) {
		close(p.stop)
		_ = p.file.Close()
	}
}
