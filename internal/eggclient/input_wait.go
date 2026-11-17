package eggclient

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// InputWaitIdle is how long a terminal must be quiet before its screen is read
// for an input prompt. A working agent redraws its spinner far more often.
const InputWaitIdle = time.Minute

// footerMarkers end full-screen dialogs (Claude Code, Codex) that are cleared
// once answered, so they count anywhere near the bottom of the screen.
var footerMarkers = []string{
	"enter to confirm",
	"do you trust",
	"do you want to proceed",
}

// lineMarkers are plain-shell questions. An answered one stays visible in
// the scrollback, so they count only on the last line, where input goes.
var lineMarkers = []string{
	"(y/n)",
	"[y/n]",
	"press enter to continue",
}

const inputWaitTailLines = 12

// maxInputWaitRender bounds the bytes rendered when a snapshot has no grid
// repaint to start from (a canonical replay fallback).
const maxInputWaitRender = 256 << 10

// gridRepaint starts the grid section of egg.VTerm.Snapshot.
var gridRepaint = []byte("\x1b[m\x1b[H")

// ScreenWaitsForInput reports whether the bottom of a rendered screen shows a
// prompt that blocks until someone answers it.
func ScreenWaitsForInput(screen string) bool {
	lines := strings.Split(screen, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < inputWaitTailLines; i-- {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if line == "" {
			continue
		}
		markers := footerMarkers
		if seen == 0 {
			markers = append(markers[:len(markers):len(markers)], lineMarkers...)
		}
		seen++
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

// RenderSnapshot replays the screen part of a session snapshot into a
// terminal of the session's size and returns the visible text. Scrollback is
// skipped, so the cost is one screen, not the whole replay buffer.
func RenderSnapshot(snapshot []byte, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 40
	}
	if i := bytes.LastIndex(snapshot, gridRepaint); i >= 0 {
		snapshot = snapshot[i:]
	} else if len(snapshot) > maxInputWaitRender {
		snapshot = snapshot[len(snapshot)-maxInputWaitRender:]
	}
	vt := egg.NewVTerm(cols, rows)
	defer vt.Close()
	_, _ = vt.Write(snapshot)
	return vt.ScreenText()
}

// SessionWaitsForInput reads a session's screen and reports whether it is
// sitting on an input prompt. ok is false when the screen couldn't be read,
// which is not evidence either way.
func SessionWaitsForInput(ctx context.Context, cfg *config.Config, sessionID string) (waiting, ok bool) {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return false, false
	}
	defer cmdutil.CloseWithLog("egg input-wait client", ec)
	snapshot, err := readEggSnapshot(ctx, ec, sessionID)
	if err != nil || ctx.Err() != nil {
		return false, false
	}
	meta := ReadEggMetaValues(dir)
	cols, _ := strconv.Atoi(meta["cols"])
	rows, _ := strconv.Atoi(meta["rows"])
	return ScreenWaitsForInput(RenderSnapshot(snapshot, cols, rows)), true
}

// InputWaitCandidate reports whether a session is worth a screen read: quiet
// for InputWaitIdle, nobody attached (an attached viewer sees the prompt and
// the browser bell path covers it), and not already explained by native
// lifecycle evidence.
func InputWaitCandidate(state string, readers int32, idle time.Duration) bool {
	switch state {
	case "needs_input", "completed", "failed", "stopped":
		return false
	}
	return readers == 0 && idle >= InputWaitIdle
}

// probeInputWaits runs after every row has its status, on whatever is left of
// the inventory budget, so a slow screen read can't cost another row its
// status.
func probeInputWaits(ctx context.Context, cfg *config.Config, sessions []LocalSession) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, localInventoryWorkers)
	for i := range sessions {
		s := &sessions[i]
		if !InputWaitCandidate(s.State, s.Readers, time.Duration(s.IdleSecs)*time.Second) {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			probeCtx, cancel := context.WithTimeout(ctx, localStatusTimeout)
			defer cancel()
			if waiting, ok := SessionWaitsForInput(probeCtx, cfg, s.ID); ok && waiting {
				projectInputWait(s)
			}
		}()
	}
	wg.Wait()
}

// projectInputWait marks a session blocked on a terminal prompt. The inventory
// stays content-free: the reason is fixed text, never the screen.
func projectInputWait(session *LocalSession) {
	session.Status = "blocked"
	session.State, session.StateSource = "needs_input", "terminal_prompt"
	session.StateReason = "The terminal shows an input prompt and has been quiet for over a minute."
}
