package eggclient

import (
	"context"

	"errors"

	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

// Lifecycle reads include archived sessions. Terminal input/attachment retains
// its existing live-only resolution. IDs and labels use the same exact-first
// ambiguity rules, without constructing a path from a caller-supplied string.
func ResolveLifecycleSession(cfg *config.Config, ref string) (LocalSession, error) {
	if ref == "" {
		return LocalSession{}, errors.New("session is required")
	}
	if err := ValidateSessionName(ref); err != nil {
		return LocalSession{}, err
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
	if err != nil {
		return LocalSession{}, err
	}
	var candidates []LocalSession
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(cfg.Dir, "eggs", entry.Name())
		meta := ReadEggMetaValues(dir)
		pid, _ := ReadAliveEggPID(dir)
		session := LocalSession{ID: entry.Name(), Name: ReadSessionName(dir), Principal: ReadSessionPrincipal(dir), Agent: meta["agent"], Kind: meta["kind"], CWD: meta["cwd"], PID: pid}
		if session.ID == ref {
			return session, nil
		}
		if session.Name == ref || strings.HasPrefix(session.ID, ref) {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		return LocalSession{}, errors.New("session reference is ambiguous; use its full ID")
	}
	return LocalSession{}, errors.New("session not found")
}

func LifecycleViewForSession(cfg *config.Config, session LocalSession, after int64, limit int) (egg.SessionView, error) {
	return readLifecycleViewForSession(cfg, session, after, limit, false)
}

func tryLifecycleViewForSession(cfg *config.Config, session LocalSession) (egg.SessionView, error) {
	return readLifecycleViewForSession(cfg, session, 0, 1, true)
}

func readLifecycleViewForSession(cfg *config.Config, session LocalSession, after int64, limit int, nonBlocking bool) (egg.SessionView, error) {
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	meta := ReadEggMetaValues(dir)
	home, err := LifecycleProviderHome(cfg, meta["provider_home"])
	if err != nil {
		return egg.SessionView{}, err
	}
	_, alive := ReadAliveEggPID(dir)
	read := egg.ReadSessionLifecycle
	if nonBlocking {
		read = egg.TryReadSessionLifecycle
	}
	return read(dir, session.Agent, session.CWD, home, meta["provider_session_id"], alive, after, limit)
}

func LifecycleProviderHome(cfg *config.Config, recorded string) (string, error) {
	if config.Channel() == "preview" {
		home := EffectiveSessionHome(cfg, EggIdentity{})
		if recorded != "" && wingpolicy.CanonicalSessionPath(recorded) != wingpolicy.CanonicalSessionPath(home) {
			return "", errors.New("preview session provider home does not belong to this preview state")
		}
		return home, nil
	}
	if recorded != "" {
		return recorded, nil
	}
	return os.UserHomeDir()
}

func ReadSessionLifecycleView(ctx context.Context, cfg *config.Config, ref string, after int64, limit int) (egg.SessionView, error) {
	if err := ctx.Err(); err != nil {
		return egg.SessionView{}, err
	}
	session, err := ResolveLifecycleSession(cfg, ref)
	if err != nil {
		return egg.SessionView{}, err
	}
	return LifecycleViewForSession(cfg, session, after, limit)
}

func ValidateLifecycleWait(state string) error {
	switch state {
	case "", "ready", "starting", "working", "idle", "completed", "needs_input", "failed", "unknown":
		return nil
	}
	return errors.New("state must be ready, starting, working, idle, completed, needs_input, failed, or unknown")
}

func lifecycleWaitMatched(view egg.SessionView, after int64, state string) bool {
	if state == "" {
		return view.HeadCursor > after
	}
	if state == "ready" {
		return view.Ready
	}
	if view.State != state {
		return false
	}
	if after == 0 {
		return true
	}
	return view.StateCursor > after
}

func WaitSessionLifecycle(ctx context.Context, cfg *config.Config, session LocalSession, after int64, state string) (egg.SessionView, bool, error) {
	if err := ValidateLifecycleWait(state); err != nil {
		return egg.SessionView{}, false, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var view egg.SessionView
	for {
		var err error
		view, err = LifecycleViewForSession(cfg, session, after, 200)
		if err != nil {
			return view, false, err
		}
		if lifecycleWaitMatched(view, after, state) {
			return view, true, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return view, false, nil
			}
			return view, false, ctx.Err()
		case <-ticker.C:
		}
	}
}
