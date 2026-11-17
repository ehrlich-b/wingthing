package eggclient

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// InputWaitIdle is how long a terminal must be quiet before its screen is read
// for an input prompt. A working agent redraws its spinner far more often.
const InputWaitIdle = time.Minute

// inputWaitMarkers are footers of prompts that block until a person answers,
// matched case-insensitively near the bottom of the rendered screen.
var inputWaitMarkers = []string{
	"enter to confirm",
	"press enter to continue",
	"do you trust",
	"do you want to proceed",
	"(y/n)",
	"[y/n]",
}

const inputWaitTailLines = 12

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
		seen++
		for _, marker := range inputWaitMarkers {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

// RenderSnapshot replays a session snapshot into a terminal of the session's
// size and returns the visible screen.
func RenderSnapshot(snapshot []byte, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 40
	}
	vt := egg.NewVTerm(cols, rows)
	defer vt.Close()
	_, _ = vt.Write(snapshot)
	return vt.ScreenText()
}

// SessionWaitsForInput reads a session's screen once it has been quiet for
// InputWaitIdle and reports whether it is sitting on an input prompt. Headless
// sessions have no browser stream to carry a bell, so this is the only signal
// that one is blocked.
func SessionWaitsForInput(ctx context.Context, cfg *config.Config, sessionID string, idle time.Duration) bool {
	if idle < InputWaitIdle {
		return false
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return false
	}
	defer cmdutil.CloseWithLog("egg input-wait client", ec)
	snapshot, err := readEggSnapshot(ctx, ec, sessionID)
	if err != nil {
		return false
	}
	meta := ReadEggMetaValues(dir)
	cols, _ := strconv.Atoi(meta["cols"])
	rows, _ := strconv.Atoi(meta["rows"])
	return ScreenWaitsForInput(RenderSnapshot(snapshot, cols, rows))
}

// inputWaitCandidate skips sessions that native evidence already explains.
func inputWaitCandidate(session *LocalSession) bool {
	switch session.State {
	case "needs_input", "completed", "failed", "stopped":
		return false
	}
	return time.Duration(session.IdleSecs)*time.Second >= InputWaitIdle
}

// projectInputWait marks a session blocked on a terminal prompt. The inventory
// stays content-free: the reason is fixed text, never the screen.
func projectInputWait(session *LocalSession) {
	session.Status = "blocked"
	session.State, session.StateSource = "needs_input", "terminal_prompt"
	session.StateReason = "The terminal shows an input prompt and has been quiet for over a minute."
}
