// Package dashboard implements the interactive terminal session navigator.
package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"golang.org/x/term"
)

// Run owns the terminal until quit, temporarily handing it to the existing CLI
// for attach and creation. All cleanup is deferred, including panic unwinding.
func Run(ctx context.Context, version string, streams remotepkg.IO) error {
	input, inputOK := streams.In.(*os.File)
	output, outputOK := streams.Out.(*os.File)
	if !inputOK || !outputOK || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return errors.New("dashboard requires interactive stdin and stdout")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	remotes, err := config.LoadRemotes(cfg.Dir)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	s := &state{machines: map[string]machine{"local": {}}, defaultAgent: cfg.DefaultAgent, defaultCWD: cwd}
	for name := range remotes {
		s.machines[name] = machine{}
	}
	inv := newInventory(func(ctx context.Context, name string) ([]eggclient.LocalSession, error) {
		if name == "local" {
			return eggclient.DiscoverActiveSessions(ctx, cfg)
		}
		return eggclient.QueryRemoteSessions(version, ctx, name, remotes[name], streams)
	})
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGCONT)
	defer signal.Stop(signals)
	return run(ctx, s, inv, remotes, fileTerminal(input, output), streams, runCommand, signals)
}

func run(ctx context.Context, s *state, inv *inventory, remotes map[string]config.Remote, terminal terminal, streams remotepkg.IO, command commandRunner, signals <-chan os.Signal) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	var actions sync.WaitGroup
	defer func() { cancel(); actions.Wait() }()
	defer func() { err = errors.Join(err, terminal.leave()) }()
	if err := terminal.enter(); err != nil {
		return err
	}
	input, err := readInput(ctx, streams.In)
	if err != nil {
		return err
	}
	defer func() {
		if input != nil {
			err = errors.Join(err, input.stop())
		}
	}()
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	// ESC is both a key and the prefix of arrows; give fragmented sequences a
	// short window to complete without delaying ordinary keys.
	escapeTimer := time.NewTimer(time.Hour)
	escapeTimer.Stop()
	defer escapeTimer.Stop()
	decoder := &keyDecoder{}
	inputEvents := input.events
	suspended := false
	suspend := func() error {
		if err := input.stop(); err != nil {
			return err
		}
		input, inputEvents = nil, nil
		decoder.pending = nil
		escapeTimer.Stop()
		suspended = true
		return terminal.suspend()
	}
	actionDone := make(chan error, 1)
	inv.refresh(ctx, s)
	for {
		if !suspended {
			width, height := terminal.size()
			if _, err := io.WriteString(streams.Out, render(s, width, height, time.Now())); err != nil {
				return err
			}
		}
		var keys []string
		var inputErr error
		select {
		case <-ctx.Done():
			return nil
		case sig := <-signals:
			switch sig {
			case syscall.SIGTSTP:
				if !suspended {
					if err := suspend(); err != nil {
						return err
					}
				}
			case syscall.SIGCONT:
				if suspended && ctx.Err() == nil {
					if err := terminal.enter(); err != nil {
						return err
					}
					input, err = readInput(ctx, streams.In)
					if err != nil {
						return err
					}
					inputEvents, suspended = input.events, false
					inv.refresh(ctx, s)
				}
			}
			continue
		case result := <-inv.results:
			inv.apply(s, result)
			continue
		case <-ticker.C:
			inv.refresh(ctx, s)
			continue
		case actionErr := <-actionDone:
			s.busy = false
			s.message = "Done"
			if actionErr != nil {
				s.message = actionErr.Error()
			}
			inv.refresh(ctx, s)
			continue
		case <-escapeTimer.C:
			keys = decoder.escape()
		case event := <-inputEvents:
			keys, inputErr = decoder.feed(event.data), event.err
			if len(decoder.pending) == 1 && decoder.pending[0] == 0x1b {
				escapeTimer.Reset(40 * time.Millisecond)
			}
		}
		for _, key := range keys {
			if key == "ctrl-z" {
				if err := suspend(); err != nil {
					return err
				}
				inputErr = nil
				break
			}
			act, quit := s.handle(key)
			if quit {
				return nil
			}
			if act == nil {
				continue
			}
			args, actionErr := actionArgs(*act, remotes)
			if actionErr != nil {
				s.message = actionErr.Error()
				continue
			}
			if !act.attach && act.kind != newCWD {
				s.busy, s.message = true, "Running…"
				actions.Add(1)
				go func() {
					defer actions.Done()
					actionCtx, stop := context.WithTimeout(ctx, eggclient.RemoteSessionTimeout)
					defer stop()
					var diagnostic bytes.Buffer
					actionErr := backgroundCommand(actionCtx, command, args, remotepkg.IO{Out: io.Discard, ErrOut: &diagnostic, SSHPath: streams.SSHPath})
					if actionErr != nil && diagnostic.Len() > 0 {
						actionErr = fmt.Errorf("%w: %s", actionErr, diagnostic.String())
					}
					select {
					case actionDone <- actionErr:
					case <-ctx.Done():
					}
				}()
				continue
			}
			if err := input.stop(); err != nil {
				return err
			}
			if err := terminal.leave(); err != nil {
				return err
			}
			actionErr = command(ctx, args, streams)
			// The child may exit or be killed after changing termios. Restore
			// our saved state before re-entering raw mode or returning on SIGTERM.
			if err := terminal.leave(); err != nil {
				return err
			}
			if actionErr != nil {
				s.message = actionErr.Error()
			}
			if ctx.Err() != nil {
				return nil
			}
			if err := terminal.enter(); err != nil {
				return err
			}
			input, err = readInput(ctx, streams.In)
			if err != nil {
				return err
			}
			inputEvents = input.events
			decoder.pending = nil
			escapeTimer.Stop()
			inv.refresh(ctx, s)
			// Bytes read before the handoff belong to the old view.
			inputErr = nil
			break
		}
		if inputErr != nil {
			if errors.Is(inputErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read dashboard input: %w", inputErr)
		}
	}
}
