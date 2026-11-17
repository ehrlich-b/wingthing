package eggclient

import (
	"strings"
	"testing"
	"time"
)

func TestScreenWaitsForInput(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"claude folder trust", "Do you trust this folder?\n❯ No, exit\n  Yes, add it\nEnter to confirm · Esc to cancel\n\n\n", true},
		{"codex approval", "Allow command?\n  Press enter to confirm or esc to cancel\n", true},
		{"claude permission", "Bash command\n  rm -rf build\nDo you want to proceed?\n❯ 1. Yes\n  2. No\n", true},
		{"shell y/n", "Overwrite config.yaml? (y/N) ", true},
		{"answered shell y/n", "Overwrite config.yaml? (y/N) y\nwrote config.yaml\n$ ", false},
		{"press enter, waiting", "Build done.\nPress Enter to continue...\n\n", true},
		{"working agent", "✻ Bootstrapping… (12s · esc to interrupt)\n⏵⏵ auto mode on\n", false},
		{"idle input box", "────\n❯ Try \"edit <filepath> to…\"\n────\n⏵⏵ auto mode on\n", false},
		{"plain output", "PASS\nok  github.com/x 0.2s\n$ ", false},
		{"prompt scrolled away", "Enter to confirm · Esc to cancel\n" + strings.Repeat("log line\n", inputWaitTailLines), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScreenWaitsForInput(tc.screen); got != tc.want {
				t.Fatalf("ScreenWaitsForInput = %v, want %v for %q", got, tc.want, tc.screen)
			}
		})
	}
}

// A TUI draws by moving the cursor, not by printing spaces, so the prompt is
// only readable once the snapshot is rendered into a terminal grid.
func TestRenderSnapshotReadsCursorDrawnPrompt(t *testing.T) {
	snapshot := "\x1b[2J\x1b[H" +
		"\x1b[3;1HDo\x1b[3;4Hyou\x1b[3;8Htrust\x1b[3;14Hthis\x1b[3;19Hfolder?" +
		"\x1b[5;1H❯ No, exit\x1b[6;3HYes, add it" +
		"\x1b[8;1HEnter\x1b[8;7Hto\x1b[8;10Hconfirm\x1b[8;18H·\x1b[8;20HEsc\x1b[8;24Hto\x1b[8;27Hcancel"
	if strings.Contains(snapshot, "Enter to confirm") {
		t.Fatal("fixture must not contain the marker as plain text")
	}
	screen := RenderSnapshot([]byte(snapshot), 80, 24)
	if !ScreenWaitsForInput(screen) {
		t.Fatalf("rendered screen should wait for input:\n%s", screen)
	}
	if ScreenWaitsForInput(RenderSnapshot([]byte(snapshot+"\x1b[2J\x1b[H✻ Working… (esc to interrupt)"), 80, 24)) {
		t.Fatal("a cleared screen must not still read as waiting")
	}
}

// An egg snapshot is scrollback, then a grid repaint. Only the repaint is
// rendered, so a prompt left in the scrollback doesn't count and a huge
// scrollback costs nothing.
func TestRenderSnapshotRendersOnlyTheGrid(t *testing.T) {
	scrollback := strings.Repeat("old log line\r\n", 50000) + "Enter to confirm · Esc to cancel\r\n"
	grid := "\x1b[m\x1b[H$ make test\r\nok\r\n$ "
	screen := RenderSnapshot([]byte(scrollback+strings.Repeat("\n", 23)+grid), 80, 24)
	if strings.Contains(screen, "old log line") || ScreenWaitsForInput(screen) {
		t.Fatalf("rendered more than the grid:\n%s", screen)
	}
	if !strings.Contains(screen, "$ make test") {
		t.Fatalf("grid missing:\n%s", screen)
	}
}

func TestInputWaitCandidateAndProjection(t *testing.T) {
	for _, tc := range []struct {
		state   string
		readers int32
		idle    time.Duration
		want    bool
	}{
		{"unknown", 0, InputWaitIdle, true},
		{"", 0, InputWaitIdle, true},
		{"idle", 0, InputWaitIdle, true},
		{"working", 0, InputWaitIdle, true},
		{"unknown", 0, InputWaitIdle - time.Second, false},
		{"unknown", 1, InputWaitIdle, false},
		{"needs_input", 0, InputWaitIdle, false},
		{"completed", 0, InputWaitIdle, false},
		{"failed", 0, InputWaitIdle, false},
		{"stopped", 0, InputWaitIdle, false},
	} {
		if got := InputWaitCandidate(tc.state, tc.readers, tc.idle); got != tc.want {
			t.Errorf("state %q readers %d idle %s: candidate = %v, want %v", tc.state, tc.readers, tc.idle, got, tc.want)
		}
	}
	session := LocalSession{Status: "unknown", State: "unknown", StateSource: "unsupported"}
	projectInputWait(&session)
	if session.Status != "blocked" || session.State != "needs_input" || session.StateSource != "terminal_prompt" || session.StateReason == "" {
		t.Fatalf("projection = %+v", session)
	}
}
