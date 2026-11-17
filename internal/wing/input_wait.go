package wing

import (
	"context"
	"path/filepath"
	"sync"
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

const inputWaitProbes = 4

// inputWaitProbe is one screen read: ok is false when it couldn't be made.
type inputWaitProbe func(sessionID string) (waiting, ok bool)

func watchInputWaits(ctx context.Context, cfg *config.Config, write ws.PTYWriteFunc) {
	ticker := time.NewTicker(inputWaitSweep)
	defer ticker.Stop()
	raised := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sessions := ListAliveEggSessions(cfg)
		results := probeAll(sessions, func(sessionID string) (bool, bool) {
			return sessionWaitsForInput(ctx, cfg, sessionID)
		})
		sweepInputWaits(sessions, results, write, raised)
	}
}

type probeResult struct{ waiting, ok bool }

func probeAll(sessions []ws.SessionInfo, probe inputWaitProbe) map[string]probeResult {
	results := make(map[string]probeResult, len(sessions))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, inputWaitProbes)
	for _, s := range sessions {
		wg.Add(1)
		slots <- struct{}{}
		go func(sessionID string) {
			defer wg.Done()
			defer func() { <-slots }()
			waiting, ok := probe(sessionID)
			mu.Lock()
			results[sessionID] = probeResult{waiting, ok}
			mu.Unlock()
		}(s.SessionID)
	}
	wg.Wait()
	return results
}

// sweepInputWaits raises attention for each session waiting on a prompt. It
// remembers the episode nonce it raised under and clears only that episode,
// only once a screen read shows the prompt gone: a failed read proves
// nothing, and a newer bell (new nonce) belongs to the browser path. The relay
// deduplicates pushes by nonce, so a long wait notifies once.
func sweepInputWaits(sessions []ws.SessionInfo, results map[string]probeResult, write ws.PTYWriteFunc, raised map[string]string) {
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.SessionID] = true
		r := results[s.SessionID]
		if !r.ok {
			continue
		}
		if r.waiting {
			checkAndSendAttention(s.SessionID, s.Agent, s.CWD, write)
			if nonce, ok := wingAttentionNonce.Load(s.SessionID); ok {
				raised[s.SessionID] = nonce.(string)
			}
			continue
		}
		if mine, ok := raised[s.SessionID]; ok {
			if nonce, ok := wingAttentionNonce.Load(s.SessionID); ok && nonce.(string) == mine {
				clearAttentionCooldown(s.SessionID)
			}
			delete(raised, s.SessionID)
		}
	}
	for sessionID := range raised {
		if !live[sessionID] {
			delete(raised, sessionID)
		}
	}
}

func sessionWaitsForInput(ctx context.Context, cfg *config.Config, sessionID string) (waiting, ok bool) {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return false, false
	}
	statusCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	st, err := ec.Status(statusCtx)
	cancel()
	cmdutil.CloseWithLog("egg input-wait status client", ec)
	if err != nil {
		return false, false
	}
	// An attached viewer sees the prompt and the bell path owns the episode:
	// no evidence either way. A session with recent output is not waiting.
	if st.Readers > 0 {
		return false, false
	}
	if !eggclient.InputWaitCandidate("", st.Readers, time.Duration(st.IdleSeconds)*time.Second) {
		return false, true
	}
	snapshotCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return eggclient.SessionWaitsForInput(snapshotCtx, cfg, sessionID)
}
