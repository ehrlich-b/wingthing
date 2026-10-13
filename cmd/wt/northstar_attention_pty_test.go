package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

const attentionPrivateContent = "PRIVATE_NATIVE_PROMPT_TRANSCRIPT_TOKEN_ERROR"

type northstarAttentionProvider struct {
	f          *northstarFixture
	commands   map[string]string
	providerID string
}

// Exercise the actual native hook publisher, importer, CLI inventory and TUI.
// Only the owned egg process/transport is a fixture; no account is contacted.
func northstarAttentionHooks(t *testing.T, f *northstarFixture, state, id string, disabled bool) northstarAttentionProvider {
	t.Helper()
	dir := filepath.Join(state, "eggs", id)
	home := filepath.Join(state, "provider-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{state, home, filepath.Join(f.home, "mailbox")} {
		if !strings.HasPrefix(protected, f.home+string(filepath.Separator)) || strings.HasPrefix(protected, f.work+string(filepath.Separator)) {
			t.Fatal("fixture mailbox/state escaped protected HOME")
		}
	}
	northstarWrite(t, filepath.Join(dir, "egg.meta"), "kind=agent\nagent=claude\nprovider_home="+home+"\ncwd="+f.work+"\nisolation=fixture\n", 0600)
	northstarWrite(t, filepath.Join(dir, "lifecycle.jsonl"), "", 0600)
	providerID := "provider-" + id
	args := []string{}
	if disabled {
		args = []string{"--settings", `{"disableAllHooks":true}`}
	}
	args, err := egg.ClaudeLifecycleArgs(args, home, id, providerID)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Disable bool `json:"disableAllHooks"`
		Hooks   map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
		t.Fatal(err)
	}
	p := northstarAttentionProvider{f: f, providerID: providerID, commands: make(map[string]string)}
	for event, entries := range settings.Hooks {
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("unexpected fixture hooks: %s", event)
		}
		p.commands[event] = entries[0].Hooks[0].Command
	}
	if disabled && (!settings.Disable || len(p.commands) != 0) {
		t.Fatal("disabled hooks were silently installed")
	}
	return p
}

func (p northstarAttentionProvider) emit(t *testing.T, event string) {
	t.Helper()
	command, ok := p.commands[event]
	if !ok {
		t.Fatalf("native fixture event not installed: %s", event)
	}
	payload, err := json.Marshal(map[string]any{"session_id": p.providerID, "hook_event_name": event,
		"prompt": attentionPrivateContent, "message": attentionPrivateContent, "token": attentionPrivateContent, "error": attentionPrivateContent})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), "/bin/sh", "-c", command)
	child.Env, child.Dir, child.Stdin = p.f.env, p.f.work, bytes.NewReader(payload)
	if output, err := child.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("native hook publisher: %v %q", err, output)
	}
}

func northstarAttentionBells(h *northstarPTY) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Count(h.transcript.Bytes(), []byte{'\a'})
}

func northstarAttentionRefresh(t *testing.T, h *northstarPTY, label string, wantBells int, wants ...string) string {
	t.Helper()
	text := h.expect(label, h.send("R"), wants...)
	// The screen event is readiness evidence, not a fixed sleep. Every emitted
	// bell precedes the corresponding render in the actual dashboard loop.
	if got := northstarAttentionBells(h); got != wantBells {
		t.Fatalf("%s bells=%d, want=%d", label, got, wantBells)
	}
	return text
}

func TestNorthstarPTYAttention(t *testing.T) {
	t.Run("symlinked-TMPDIR-native-hooks", func(t *testing.T) {
		f := northstarNewFixture(t)
		binary := northstarBuild(t, f, "stable")
		if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
			t.Fatal("built wt bypassed symlinked TMPDIR")
		}
		if info, err := os.Lstat(f.tmp); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("TMPDIR parent was not a test-created symlink")
		}
		localEgg := northstarStandaloneEgg(t, f, f.local, "same", "local-agent", "working", "ATTENTION_LOCAL_ATTACH_READY")
		remoteEgg := northstarStandaloneEgg(t, f, f.remote, "same", "forge-agent", "working", "ATTENTION_REMOTE_ATTACH_READY")
		northstarStandaloneEgg(t, f, f.local, "nohooks", "hooks-off", "working", "Allow private text? [Y/n]")
		local := northstarAttentionHooks(t, f, f.local, "same", false)
		remote := northstarAttentionHooks(t, f, f.remote, "same", false)
		northstarAttentionHooks(t, f, f.local, "nohooks", true)
		for _, provider := range []northstarAttentionProvider{local, remote} {
			provider.emit(t, "SessionStart")
			provider.emit(t, "UserPromptSubmit")
		}
		h := northstarStartPTY(t, f, binary, 120, 32)
		initial := h.expect("working and disabled native evidence / no bell", 0, "3 known sessions", "local-agent", "forge-agent", "hooks-off", "0 unread")
		// Names also appear in LAST LINE while the table is still painting.
		// Wait for the exact native states on all three inventory rows.
		initial = h.waitScreen("native states on exact rows", 0, func(screen string) bool {
			return northstarNamedState(screen, "local-agent", "* Working") && northstarNamedState(screen, "forge-agent", "* Working") && northstarNamedState(screen, "hooks-off", "? Unknown")
		})
		if !northstarNamedState(initial, "local-agent", "* Working") || !northstarNamedState(initial, "forge-agent", "* Working") || !northstarNamedState(initial, "hooks-off", "? Unknown") {
			t.Fatal("native lifecycle state was not shown on its exact row")
		}
		if northstarAttentionBells(h) != 0 {
			t.Fatal("bell was enabled by default")
		}
		h.expect("instance bell help", h.send("?"), "b bell off", "unread")
		h.expect("enable instance bell", h.send("\x1bb"), "Bell on for this dashboard")
		h.expect("select exact local provider", h.send("/local-agent\r"), "> local-agent")
		started := time.Now()
		local.emit(t, "PermissionRequest")
		permission := northstarAttentionRefresh(t, h, "native permission needs input", 1, "local-agent", "Waiting for your answer.")
		permission = h.waitScreen("permission on exact native row", 0, func(screen string) bool {
			return northstarNamedState(screen, "local-agent", "! Needs you")
		})
		if !northstarNamedState(permission, "local-agent", "! Needs you") {
			t.Fatal("native permission state lost")
		}
		permissionVisible := time.Since(started)
		t.Logf("PermissionRequest to visible native blocked: %s", permissionVisible)
		northstarAttentionRefresh(t, h, "repeated permission cursor", 1, "local-agent")
		h.expect("view blocked exact cursor", h.send("i"), "State: ! Needs you", "Machine: This machine", "is waiting for your answer")
		h.expect("blocked survives viewing", h.send("\x1b"), "local-agent")
		local.emit(t, "Stop")
		completion := northstarAttentionRefresh(t, h, "foreground completed / alive provider / unread", 2, "local-agent *", "1 unread", "Background work may still run")
		completion = h.waitScreen("completion on exact native row", 0, func(screen string) bool {
			return northstarNamedState(screen, "local-agent *", "o Idle") && strings.Contains(screen, "1 unread")
		})
		if !northstarNamedState(completion, "local-agent *", "o Idle") || strings.Contains(completion, "+ Done") {
			t.Fatal("foreground response was shown as an ended session")
		}
		if _, alive := eggclient.ReadAliveEggPID(localEgg.dir); !alive {
			t.Fatal("foreground completion exited the provider")
		}
		northstarAttentionRefresh(t, h, "repeated completion cursor", 2, "local-agent *", "1 unread")
		h.waitScreen("exact native completion before details", 0, func(screen string) bool {
			return northstarNamedState(screen, "local-agent *", "o Idle") && strings.Contains(screen, "1 unread")
		})
		h.expect("acknowledge local completion", h.send("i"), "Machine: This machine", "Unread response", "replied. Background work may still run.", "0 unread")
		h.expect("return after exact acknowledgement", h.send("\x1b"), "local-agent", "0 unread")
		h.expect("filter disabled hooks", h.send("/\x15hooks-off\r"), "> hooks-off")
		h.expect("unknown evidence explains itself", h.send("i"), "State: ? Unknown", "does not report whether it is working")
		h.expect("clear disabled filter", h.send("\x1b/\x15\r"), "local-agent", "forge-agent")
		// Identical session IDs belong to separate machines and hook spools.
		local.emit(t, "UserPromptSubmit")
		local.emit(t, "Stop")
		remote.emit(t, "Stop")
		northstarAttentionRefresh(t, h, "two same IDs / two unread results", 4, "local-agent *", "forge-agent *", "2 unread")
		h.expect("select only local result", h.send("/local-agent\r"), "> local-agent")
		h.expect("ack only local result", h.send("i"), "Machine: This machine", "1 unread")
		h.expect("select forge after local acknowledgement", h.send("\x1b/\x15forge-agent\r"), "> forge-agent", "1 unread")
		h.expect("attach exact remote result", h.send("\r"), "ATTENTION_REMOTE_ATTACH_READY")
		select {
		case id := <-remoteEgg.attached:
			if id != "same" {
				t.Fatal(id)
			}
		case <-time.After(time.Second):
			t.Fatal("remote exact-ID attachment did not execute")
		}
		h.expect("remote detach acknowledges captured completion", h.send("\x02q"), "forge-agent", "0 unread")
		if northstarAttentionBells(h) != 4 {
			t.Fatal("attach/detach replayed a completion alert")
		}
		h.expect("restore all rows", h.send("/\x15\r"), "local-agent", "forge-agent")
		remote.emit(t, "UserPromptSubmit")
		remote.emit(t, "Stop")
		northstarAttentionRefresh(t, h, "new remote completion", 5, "forge-agent *", "1 unread")
		northstarWrite(t, filepath.Join(f.home, "forge-offline"), "fixture offline", 0600)
		northstarAttentionRefresh(t, h, "stale machine retains visual unread", 5, "forge unreachable", "forge-agent *", "1 unread")
		if err := os.Remove(filepath.Join(f.home, "forge-offline")); err != nil {
			t.Fatal(err)
		}
		h.waitScreen("reconnect without repeated cursor bell", h.send("R"), func(screen string) bool {
			return northstarContainsRow(screen, "forge-agent *") && strings.Contains(screen, "1 unread") && !strings.Contains(screen, "forge unreachable")
		})
		if northstarAttentionBells(h) != 5 {
			t.Fatal("reconnect replayed an alert")
		}
		h.expect("disable bell keeps visual attention", h.send("b"))
		local.emit(t, "PermissionRequest")
		northstarAttentionRefresh(t, h, "disabled bell still shows blocked", 5, "local-agent", "1 unread")
		// The footer advertises ! attention even before the filter is applied.
		// Inventory refreshes can redraw that old view after send, so wait for
		// the filtered header and complete rows in the same rendered snapshot.
		h.waitScreen("attention filter excludes unknown", h.send("!"), func(screen string) bool {
			return strings.Contains(screen, "All machines · needs you") &&
				strings.Contains(screen, "local-agent") && northstarContainsRow(screen, "forge-agent *") &&
				!strings.Contains(screen, "hooks-off")
		})
		h.exit("q")
		fresh := northstarStartPTY(t, f, binary, 120, 32)
		fresh.expect("fresh instance / durable unread cursor / known blocked", 0, "local-agent", "forge-agent *", "1 unread")
		fresh.exit("q")
		for _, pty := range []*northstarPTY{h, fresh} {
			pty.mu.Lock()
			private := bytes.Contains(pty.transcript.Bytes(), []byte(attentionPrivateContent))
			pty.mu.Unlock()
			if private {
				t.Fatal("alert/list/details exposed hook payload or transcript content")
			}
		}
		northstarWrite(t, filepath.Join(f.evidence, "attention-timing.txt"), fmt.Sprintf("permission_to_visible=%s; final_bells=5; two_machine_same_id=true\n", permissionVisible), 0600)
	})
}

func northstarNamedState(screen, name, state string) bool {
	unread := strings.HasSuffix(name, " *")
	name = strings.TrimSuffix(name, " *")
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimLeft(line, "│|")
		line = strings.SplitN(strings.SplitN(line, "|", 2)[0], "│", 2)[0]
		line = strings.TrimPrefix(strings.Join(strings.Fields(line), " "), "> ")
		if !strings.HasPrefix(line, state) {
			continue
		}
		// State is the first column; unread is separate immediately before Title.
		if unread {
			if strings.Contains(line, " * > "+name+" ") || strings.Contains(line, " * "+name+" ") {
				return true
			}
		} else if strings.Contains(line, " "+name+" ") {
			return true
		}
	}
	return false
}
