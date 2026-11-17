package wing

import (
	"context"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// inputWaitSweep is how often the wing reads quiet sessions' screens. A bell
// only raises attention while a browser streams the session, so a headless
// session stuck on a prompt (a trust dialog, a y/n) would otherwise sit
// unnoticed until someone looks.
const inputWaitSweep = 30 * time.Second

func watchInputWaits(ctx context.Context, cfg *config.Config, write ws.PTYWriteFunc) {
	ticker := time.NewTicker(inputWaitSweep)
	defer ticker.Stop()
	flagged := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sweepInputWaits(ListAliveEggSessions(cfg), func(sessionID string) bool {
			return sessionWaitsForInput(ctx, cfg, sessionID)
		}, write, flagged)
	}
}

// sweepInputWaits raises attention for each session waiting on a prompt and
// clears what it raised once the prompt is gone. The relay deduplicates
// pushes by the episode nonce, so a long wait notifies once.
func sweepInputWaits(sessions []ws.SessionInfo, waiting func(sessionID string) bool, write ws.PTYWriteFunc, flagged map[string]bool) {
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.SessionID] = true
		if waiting(s.SessionID) {
			checkAndSendAttention(s.SessionID, s.Agent, s.CWD, write)
			flagged[s.SessionID] = true
		} else if flagged[s.SessionID] {
			clearAttentionCooldown(s.SessionID)
			delete(flagged, s.SessionID)
		}
	}
	for sessionID := range flagged {
		if !live[sessionID] {
			delete(flagged, sessionID)
		}
	}
}

func sessionWaitsForInput(ctx context.Context, cfg *config.Config, sessionID string) bool {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return false
	}
	statusCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	st, err := ec.Status(statusCtx)
	cancel()
	cmdutil.CloseWithLog("egg input-wait status client", ec)
	if err != nil {
		return false
	}
	snapshotCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return eggclient.SessionWaitsForInput(snapshotCtx, cfg, sessionID, time.Duration(st.IdleSeconds)*time.Second)
}
