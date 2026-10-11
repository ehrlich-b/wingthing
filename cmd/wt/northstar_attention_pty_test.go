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
		h.expect("working and disabled native evidence / no bell", 0, "wt | 3 sessions", "W local-agent", "W forge-agent", "? hooks-off", "U:0", "bell:off")
		if northstarAttentionBells(h) != 0 {
			t.Fatal("bell was enabled by default")
		}
		h.expect("instance bell help", h.send("?"), "b: bell off", "unread", "disabled hooks")
		h.expect("enable instance bell", h.send("\x1bb"), "Bell on for this dashboard", "bell:on")
		h.expect("select exact local provider", h.send("/local-agent\r"), "> W local-agent")
		started := time.Now()
		local.emit(t, "PermissionRequest")
		northstarAttentionRefresh(t, h, "native permission needs input", 1, "B local-agent", "Native provider requested input.")
		permissionVisible := time.Since(started)
		t.Logf("PermissionRequest to visible native blocked: %s", permissionVisible)
		northstarAttentionRefresh(t, h, "repeated permission cursor", 1, "B local-agent")
		h.expect("view blocked exact cursor", h.send("i"), "Attention: blocked", "claude_hook", "State cursor: 3", "acknowledgement does not clear blocking")
		h.expect("blocked survives viewing", h.send("\x1b"), "B local-agent")
		local.emit(t, "Stop")
		northstarAttentionRefresh(t, h, "foreground completed / alive provider / unread", 2, "I* local-agent", "U:1", "D:0", "descendants may continue")
		if _, alive := eggclient.ReadAliveEggPID(localEgg.dir); !alive {
			t.Fatal("foreground completion exited the provider")
		}
		northstarAttentionRefresh(t, h, "repeated completion cursor", 2, "I* local-agent", "U:1")
		h.expect("acknowledge local completion", h.send("i"), "Details: local:same", "State cursor: 4", "Foreground response completed", "U:0")
		h.expect("return after exact acknowledgement", h.send("\x1b"), "I local-agent", "U:0")
		h.expect("filter disabled hooks", h.send("/\x15hooks-off\r"), "> ? hooks-off")
		h.expect("unknown evidence explains itself", h.send("i"), "Attention: unknown", "unsupported", "missing, unknown or hooks are disabled", "no screen inference")
		h.expect("clear disabled filter", h.send("\x1b/\x15\r"), "I local-agent", "W forge-agent")
		// Identical session IDs belong to separate machines and hook spools.
		local.emit(t, "UserPromptSubmit")
		local.emit(t, "Stop")
		remote.emit(t, "Stop")
		northstarAttentionRefresh(t, h, "two same IDs / two unread results", 4, "I* local-agent", "I* forge-agent", "U:2")
		h.expect("select only local result", h.send("/local-agent\r"), "> I* local-agent")
		h.expect("ack only local result", h.send("i"), "Details: local:same", "U:1")
		h.expect("select forge after local acknowledgement", h.send("\x1b/\x15forge-agent\r"), "> I* forge-agent", "U:1")
		h.expect("attach exact remote result", h.send("\r"), "ATTENTION_REMOTE_ATTACH_READY")
		select {
		case id := <-remoteEgg.attached:
			if id != "same" {
				t.Fatal(id)
			}
		case <-time.After(time.Second):
			t.Fatal("remote exact-ID attachment did not execute")
		}
		h.expect("remote detach acknowledges captured completion", h.send("\x02q"), "I forge-agent", "U:0")
		if northstarAttentionBells(h) != 4 {
			t.Fatal("attach/detach replayed a completion alert")
		}
		h.expect("restore all rows", h.send("/\x15\r"), "I local-agent", "I forge-agent")
		remote.emit(t, "UserPromptSubmit")
		remote.emit(t, "Stop")
		northstarAttentionRefresh(t, h, "new remote completion", 5, "I* forge-agent", "U:1")
		northstarWrite(t, filepath.Join(f.home, "forge-offline"), "fixture offline", 0600)
		northstarAttentionRefresh(t, h, "stale machine retains visual unread", 5, "cached rows stale", "I* forge-agent", "U:1")
		if err := os.Remove(filepath.Join(f.home, "forge-offline")); err != nil {
			t.Fatal(err)
		}
		h.waitScreen("reconnect without repeated cursor bell", h.send("R"), func(screen string) bool {
			return strings.Contains(screen, "I* forge-agent") && strings.Contains(screen, "U:1") && !strings.Contains(screen, "cached rows stale")
		})
		if northstarAttentionBells(h) != 5 {
			t.Fatal("reconnect replayed an alert")
		}
		h.expect("disable bell keeps visual attention", h.send("b"), "bell:off")
		local.emit(t, "PermissionRequest")
		northstarAttentionRefresh(t, h, "disabled bell still shows blocked", 5, "B local-agent", "U:1")
		h.expect("attention filter excludes unknown", h.send("!"), "B local-agent", "I* forge-agent", "! attention")
		if screen, _ := h.screen(); strings.Contains(screen, "hooks-off") {
			t.Fatal("attention filter treated unknown as completion")
		}
		h.exit("q")
		fresh := northstarStartPTY(t, f, binary, 120, 32)
		fresh.expect("fresh instance / historical completion baseline / known blocked", 0, "B local-agent", "I forge-agent", "U:0", "bell:off")
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
