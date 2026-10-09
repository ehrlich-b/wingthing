package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

const codexCompletionQuiet = 2 * time.Second

// Exit observation never reaps. The leader pins our PGID until every group
// signal has finished; only finish may hand ownership to exec.Cmd.Wait.
type codexProcess struct {
	mu              sync.Mutex
	cmd             *exec.Cmd
	signal          func() error
	beforeTerminate func(*os.Process)
	exited          chan struct{}
	exitErr         error // published by closing exited
	waiting         bool
	cleaned         bool
	killed          bool
	cleanupErr      error
}

func (p *codexProcess) cancel() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting {
		return p.cmd.Process.Kill()
	}
	// stdout EOF does not transfer ownership: cancellation must still kill
	// descendants while the leader is alive or is an unreaped zombie.
	return p.signal()
}

func (p *codexProcess) cleanup(terminate bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cleaned || p.waiting {
		return
	}
	p.cleaned = true
	exited, err := processExited(p.cmd.Process)
	if terminate && err == nil && !exited {
		if p.beforeTerminate != nil {
			p.beforeTerminate(p.cmd.Process)
		}
		// An alive probe followed by SIGKILL cannot attribute the exit: the
		// provider might kill itself between them. Confirm SIGSTOP first so
		// it cannot execute its own exit/signal before our direct leader kill.
		stopped, stopErr := stopProcessForCleanup(p.cmd.Process)
		if stopped && stopErr == nil {
			p.killed = p.cmd.Process.Kill() == nil
			if p.killed {
				// Darwin can reject a group signal while the killed leader is
				// transitioning into a zombie. Observe exit before the last
				// group signal, without releasing the PID through Wait.
				<-p.exited
			}
		}
	}
	// Always clean the owned group, even if the leader exited on its own.
	// Its status is preserved unless we proved our direct kill above.
	if err := p.signal(); err != nil && !processGroupSignalGone(p.cmd.Process, err) {
		p.cleanupErr = fmt.Errorf("group signal: %w", err)
	}
}

func (p *codexProcess) finish(ctx context.Context, terminal, readFailed bool) error {
	if !p.cleaned {
		if terminal || readFailed {
			// EOF is quiet too, but a live leader still gets a quiet period.
			delay := codexCompletionQuiet
			if readFailed {
				delay = 0
			}
			timer := time.NewTimer(delay)
			select {
			case <-p.exited:
				if p.exitErr != nil {
					select {
					case <-ctx.Done():
					case <-timer.C:
					}
				}
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
		} else {
			select {
			case <-p.exited:
			case <-ctx.Done():
			}
		}
		p.cleanup(terminal || readFailed)
	}
	// The independent observer must finish before Wait can release the PID.
	<-p.exited
	p.mu.Lock()
	p.waiting = true
	p.mu.Unlock()
	err := p.cmd.Wait()
	if p.exitErr != nil && !errors.Is(p.exitErr, errors.ErrUnsupported) {
		p.cleanupErr = errors.Join(p.cleanupErr, fmt.Errorf("exit observation: %w", p.exitErr))
	}
	return err
}
