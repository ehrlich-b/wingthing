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
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	return run(ctx, s, inv, remotes, fileTerminal(input, output), streams, runCommand, resize)
}

func run(ctx context.Context, s *state, inv *inventory, remotes map[string]config.Remote, terminal terminal, streams remotepkg.IO, command commandRunner, resize <-chan os.Signal) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	actionDone := make(chan error, 1)
	inv.refresh(ctx, s)
	for {
		width, height := terminal.size()
		if _, err := io.WriteString(streams.Out, render(s, width, height, time.Now())); err != nil {
			return err
		}
		var keys []string
		var inputErr error
		select {
		case <-ctx.Done():
			return nil
		case <-resize:
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
		case event := <-input.events:
			keys, inputErr = decoder.feed(event.data), event.err
			if len(decoder.pending) == 1 && decoder.pending[0] == 0x1b {
				escapeTimer.Reset(40 * time.Millisecond)
			}
		}
		for _, key := range keys {
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
				go func() {
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
