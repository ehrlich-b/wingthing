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
	"github.com/ehrlich-b/wingthing/internal/onboarding"
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
	registryErr := err
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	s := &state{machines: map[string]machine{"local": {}}, defaultAgent: cfg.DefaultAgent, defaultCWD: cwd, home: home, localDir: cfg.Dir, agents: onboarding.Discover(), persistAttention: true, theme: detectTheme()}
	s.loadReadCursors()
	s.previewReader = readSessionPreview
	inv := newInventory(nil)
	inv.reload(s, remotes, registryErr)
	inv.queryProfile = func(ctx context.Context, name string, profile config.Remote) ([]eggclient.LocalSession, error) {
		if name == "local" {
			sessions, err := eggclient.DiscoverActiveSessions(ctx, cfg)
			if err == nil {
				readPermissions(ctx, cfg.Dir, sessions)
			}
			return sessions, err
		}
		return eggclient.QueryRemoteSessions(version, ctx, name, profile, streams)
	}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGCONT)
	defer signal.Stop(signals)
	return run(ctx, s, inv, remotes, fileTerminal(input, output), streams, runCommand, signals)
}

func run(ctx context.Context, s *state, inv *inventory, remotes map[string]config.Remote, terminal terminal, streams remotepkg.IO, command commandRunner, signals <-chan os.Signal) (err error) {
	if s.remotes == nil {
		s.remotes = remotes
	}
	ctx, cancel := context.WithCancel(ctx)
	var actions sync.WaitGroup
	history := s.history()
	previews := newPreviews()
	reads := machineReads{}
	inv.readGates, previews.readGates = reads, reads
	defer func() { previews.stop(); previews.workers.Wait() }()
	defer func() { cancel(); actions.Wait(); inv.workers.Wait(); history.workers.Wait() }()
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
	var activeAction row
	var actionCancel context.CancelFunc
	reload := func() {
		if inv.queryProfile != nil {
			profiles, err := config.LoadRemotes(s.localDir)
			inv.reload(s, profiles, err)
			remotes = s.remotes
			if actionCancel != nil && activeAction.generation != s.machines[activeAction.machine].generation {
				actionCancel()
			}
		}
	}
	type actionResult struct {
		message string
		row     row
		err     error
	}
	actionDone := make(chan actionResult, 1)
	type diagnosticReply struct {
		sequence uint64
		lines    []string
	}
	diagnosticDone := make(chan diagnosticReply, 1)
	inv.refresh(ctx, s)
	for {
		if !suspended {
			if bells := s.takeBells(); bells != "" {
				if _, err := io.WriteString(streams.Out, bells); err != nil {
					return err
				}
			}
			width, height := terminal.size()
			if s.previewReader != nil && !suspended {
				previews.request(ctx, s, width >= 120 && height >= 30, time.Now(), s.visibleReports(width, height)...)
			}
			if s.isOverlay() {
				lineCount := 0
				for _, line := range overlayLines(s, time.Now()) {
					lineCount += len(s.overlayWrap(line, max(1, width)))
				}
				s.overlayLimit = max(0, lineCount-max(0, height-6))
				s.overlayOffset = min(s.overlayOffset, s.overlayLimit)
			}
			if _, err := io.WriteString(streams.Out, render(s, width, height, time.Now())); err != nil {
				return err
			}
		}
		var keys []string
		var inputErr error
		select {
		case <-ctx.Done():
			return nil
		case result := <-previews.results:
			previews.apply(s, result)
			continue
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
		case result := <-history.results:
			if result.target != nil && s.mode != runResult {
				history.loading = false
				history.content = nil
			} else {
				history.apply(result)
			}
			if history.requested && s.recent && s.mode == navigate {
				history.request(ctx, s.localDir, remotes, nil)
			}
			continue
		case result := <-inv.results:
			if result.err != nil && accessDenied(result.err.Error()) && actionCancel != nil && activeAction.machine == result.name && activeAction.generation == result.generation && s.machines[result.name].generation == result.generation {
				actionCancel()
			}
			if inv.apply(s, result) {
				inv.start(ctx, s, result.name)
			}
			continue
		case <-ticker.C:
			reload()
			inv.refresh(ctx, s)
			continue
		case result := <-actionDone:
			s.busy = false
			actionCancel = nil
			if result.row.generation != s.machines[result.row.machine].generation {
				continue
			}
			actionErr := result.err
			s.message = "Done"
			if result.message != "" {
				s.message = result.message
				s.permissionNotice = result.message
			}
			if actionErr != nil {
				s.message = actionErr.Error()
			}
			inv.refresh(ctx, s)
			continue
		case result := <-diagnosticDone:
			if result.sequence == s.diagnostic.sequence {
				s.diagnostic.lines, s.diagnostic.loading = result.lines, false
			}
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
			if key == "R" && s.mode == navigate {
				for identity, read := range previews.reads {
					read.next = time.Time{}
					previews.reads[identity] = read
				}
				reload()
			}
			act, quit := s.handle(key)
			if s.refresh {
				s.refresh = false
				reload()
				inv.forceRefresh(ctx, s)
			}
			if history.requested && !history.loading {
				history.request(ctx, s.localDir, remotes, nil)
			}
			if quit {
				return nil
			}
			if act == nil {
				continue
			}
			if act.kind == setupInvoke {
				if err := input.stop(); err != nil {
					return err
				}
				if err := terminal.leave(); err != nil {
					return err
				}
				setupErr := setupCommand(ctx, s.setupAgent, act.setupStage, streams)
				s.setupStage = act.setupStage
				if setupErr == nil {
					s.setupCompleted = act.setupStage
				}
				s.agents = onboarding.Discover()
				for _, a := range s.agents {
					if a.ID == s.setupAgent.ID {
						s.setupAgent = a
					}
				}
				if setupErr != nil {
					s.mode, s.setupError = setupFailure, setupErr.Error()
				} else if s.setupAgent.Path == "" {
					s.mode, s.setupStage, s.setupError = setupFailure, "PATH", "Binary absent on PATH. Check ~/.local/bin, then rescan; do not reinstall by default."
				} else {
					s.mode, s.setupError = providerReady, ""
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
				inputErr = nil
				break
			}
			if act.kind == diagnosticReport {
				sequence := s.diagnostic.sequence
				args := append([]string{}, act.args...)
				actions.Add(1)
				go func() {
					defer actions.Done()
					reportCtx, stop := context.WithTimeout(ctx, 30*time.Second)
					defer stop()
					var output diagnosticBuffer
					err := validateDiagnosticArgs(args)
					if err == nil {
						err = backgroundCommand(reportCtx, command, args, remotepkg.IO{Out: &output, ErrOut: &output})
					}
					select {
					case diagnosticDone <- diagnosticReply{sequence, diagnosticResult(&output, err)}:
					case <-ctx.Done():
					}
				}()
				continue
			}
			if act.kind == runResult {
				r := act.run
				history.request(ctx, s.localDir, remotes, &r)
				continue
			}
			if act.kind == runAttach {
				if err := verifyRunAttach(ctx, s.localDir, act.run); err != nil {
					s.mode, s.message, history.target = runDetails, err.Error(), act.run
					history.target.live = false
					history.pages[act.run.WingID] = staleRunPage(history.pages[act.run.WingID], err)
					continue
				}
			}
			// Recheck the disk registry at the last event-loop boundary before
			// dispatch; actionArgs then pins the accepted profile in literal argv.
			reload()
			if act.kind != runAttach && act.kind != runResult && act.row.machine != "local" && s.registryError != "" {
				s.message = "cannot validate remote session: registry error: " + s.registryError
				continue
			}
			if act.kind != runAttach && act.row.generation != s.machines[act.row.machine].generation {
				s.message = "Cancelled: saved profile " + act.row.machine + " changed or was removed; select the current machine before acting"
				continue
			}
			if act.permission != nil {
				s.busy, s.message = true, "Sending decision…"
				actionCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				activeAction, actionCancel = act.row, stop
				target, response := act.row, *act.permission
				actions.Add(1)
				go func() {
					defer actions.Done()
					defer stop()
					message, err := sendPermission(actionCtx, s.localDir, response)
					if err != nil {
						message = "Decision not confirmed. Answer in terminal."
					}
					select {
					case actionDone <- actionResult{row: target, message: message, err: err}:
					case <-ctx.Done():
					}
				}()
				continue
			}
			args, actionErr := actionArgs(*act, remotes)
			if actionErr != nil {
				s.message = actionErr.Error()
				continue
			}
			if !act.attach && act.kind != newCWD {
				s.busy, s.message = true, "Running…"
				actionCtx, stop := context.WithTimeout(ctx, eggclient.RemoteSessionTimeout)
				activeAction, actionCancel = act.row, stop
				target := act.row
				actions.Add(1)
				go func() {
					defer actions.Done()
					defer stop()
					var diagnostic bytes.Buffer
					actionErr := backgroundCommand(actionCtx, command, args, remotepkg.IO{Out: io.Discard, ErrOut: &diagnostic, SSHPath: streams.SSHPath})
					if actionErr != nil && diagnostic.Len() > 0 {
						actionErr = fmt.Errorf("%w: %s", actionErr, diagnostic.String())
					}
					select {
					case actionDone <- actionResult{row: target, err: actionErr}:
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
			if act.attach && act.kind != runAttach {
				s.attached = &act.row.identity
			}
			actionErr = command(ctx, args, streams)
			if act.attach && act.kind != runAttach && actionErr == nil {
				s.acknowledge(act.row)
			}
			s.attached = nil
			// The child may exit or be killed after changing termios. Restore
			// our saved state before re-entering raw mode or returning on SIGTERM.
			if err := terminal.leave(); err != nil {
				return err
			}
			if actionErr != nil {
				s.message = qualifyLaunchAction(actionErr, act.row.machine).Error()
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
			reload()
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
