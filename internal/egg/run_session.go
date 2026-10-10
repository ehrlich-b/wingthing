package egg

import (
	"context"
	"errors"
	"time"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

func nativeRunSupported(kind, agent string, command []string, codexRun bool) bool {
	return kind == "agent" && len(command) == 0 && (agent == "claude" || (agent == "codex" && codexRun))
}

func (s *Server) sessionRunTurns(sess *Session, home, providerID string) *runTurnRuntime {
	tree := sess.processTree
	if tree == nil {
		tree = &runProcessTree{root: sess.PID}
		sess.processTree = tree
	}
	setRoot := func() {
		tree.mu.Lock()
		tree.root = sess.PID
		tree.mu.Unlock()
	}
	read := func(ctx context.Context, after int64, limit int) (SessionView, error) {
		if err := ctx.Err(); err != nil {
			return SessionView{}, err
		}
		alive := true
		select {
		case <-sess.done:
			alive = false
		default:
		}
		return ReadSessionLifecycle(s.dir, sess.Agent, sess.CWD, home, providerID, alive, after, limit)
	}
	backend := runTurnBackend{Agent: sess.Agent, Read: read, Done: sess.done, Kill: func() ([]RunDescendant, error) {
		setRoot()
		return tree.kill(sess)
	}, StartupDiagnostic: func() string { return codexStartupDiagnostic(sess.vterm.ScreenText()) }}
	if !nativeRunSupported(sess.Kind, sess.Agent, sess.Command, sess.codexRun) {
		backend.Agent = ""
	}
	backend.Prepare = func(prompt, exactID string) (func() (turnEvidence, error), error) {
		var scan func() (turnEvidence, error)
		var err error
		if sess.Agent == "codex" {
			scan, err = codexRunScanner(home, sess.ID, exactID, prompt, read, readRunFile)
		} else {
			scan, err = claudeRunScanner(home, sess.CWD, exactID, prompt, read, readRunFile)
		}
		if err != nil {
			return nil, err
		}
		last := time.Time{}
		return func() (turnEvidence, error) {
			setRoot()
			if time.Since(last) >= time.Second {
				if snapshot, err := processSnapshot(); err == nil {
					tree.observe(snapshot)
				}
				last = time.Now()
			}
			return scan()
		}, nil
	}
	backend.Send = func(ctx context.Context, input string) (PromptDelivery, error) {
		delivery := PromptDelivery{NoInputAttempted: true}
		attachment := s.inputLease.register(&pb.AttachOptions{Owner: "egg-run"})
		if err := s.inputLease.claim(attachment, false); err != nil {
			return delivery, err
		}
		delivery.Release = func() { s.inputLease.release(attachment) }
		err := s.inputLease.mutation(ctx, attachment, func(ctx context.Context) error {
			s.inputMu.Lock()
			defer s.inputMu.Unlock()
			view, err := read(ctx, 0, 1)
			if err != nil {
				return err
			}
			if !NativePromptReady(view) {
				return errors.New("native foreground readiness changed before input")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			frame := []byte("\x1b[200~" + input + "\x1b[201~")
			delivery.NoInputAttempted = false
			if err = writePTYInput(ctx, sess.ptmx, frame); err != nil {
				return err
			}
			delivery.BytesEnqueued = len(frame)
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
			if err = writePTYInput(ctx, sess.ptmx, []byte{'\r'}); err != nil {
				return err
			}
			delivery.BytesEnqueued++
			sess.mu.Lock()
			sess.lastInput = time.Now()
			sess.mu.Unlock()
			if sess.auditor != nil {
				sess.auditor.Process(append(frame, '\r'))
			}
			return nil
		})
		return delivery, err
	}
	return newRunTurnRuntime(s.dir, backend)
}

func (rt *runTurnRuntime) awaitProcessExit() {
	rt.mu.Lock()
	var runs []*ownedRunTurn
	for _, run := range rt.runs {
		runs = append(runs, run)
	}
	rt.mu.Unlock()
	for _, run := range runs {
		<-run.done
		<-run.workerDone
	}
}

func (rt *runTurnRuntime) stopActive() {
	rt.mu.Lock()
	var ids []string
	for id := range rt.runs {
		ids = append(ids, id)
	}
	rt.mu.Unlock()
	for _, id := range ids {
		_, _ = rt.stop(id)
	}
	rt.awaitProcessExit()
}
