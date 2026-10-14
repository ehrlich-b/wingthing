package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"google.golang.org/protobuf/types/known/structpb"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func permissionState(t *testing.T) *state {
	t.Helper()
	now := time.Now()
	p := &permissions.Request{WingID: "personal", SessionID: "work", ProviderID: "provider", Generation: "generation", RequestID: permissions.ID(), Digest: "", Tool: "Bash", Arguments: map[string]any{"command": "echo allowed"}, Directory: "~/code/wingthing", Machine: "Studio", User: "developer", Scope: "This complete Bash invocation", Deadline: now.Add(time.Minute)}
	p.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: p.Tool, Arguments: p.Arguments, Directory: p.Directory})
	s := &state{machines: map[string]machine{"local": {updated: now, sessions: []eggclient.LocalSession{{ID: "work", Name: "Run DNS tests", Agent: "claude", Status: "blocked", State: "needs_input", StateSource: "claude_hook", StateCursor: 1, CWD: "~/code/wingthing", Permission: p, PermissionRespond: true}}}}, selected: "local:work", selectionTouched: true, theme: terminalTheme{}}
	s.reconcile()
	return s
}
func TestPermissionTUIReviewAndExplicitOnce(t *testing.T) {
	s := permissionState(t)
	act, _ := s.handle("a")
	if act != nil || s.mode != permissionReview {
		t.Fatal("a delivered without review")
	}
	text := strings.Join(s.permissionReviewLines(), "\n")
	for _, want := range []string{"echo allowed", "Studio", "developer", "~/code/wingthing", "Digest:", "Arguments:", "Allow once"} {
		if !strings.Contains(text, want) {
			t.Fatalf("review missing %s", want)
		}
	}
	s.handle("escape")
	if s.mode != navigate {
		t.Fatal("cancel failed")
	}
	s.handle("a")
	act, _ = s.handle("enter")
	if act == nil || act.permission == nil || act.permission.Decision != "allow" || act.permission.AttemptID == "" {
		t.Fatalf("missing exact response: %+v", act)
	}
	if _, ok := s.livePermission(act.row); ok {
		t.Fatal("answered request remains actionable")
	}
	s = permissionState(t)
	act, _ = s.handle("d")
	if act == nil || act.permission.Decision != "deny" {
		t.Fatal("decline did not deny")
	}
	s = permissionState(t)
	s.handle("A")
	if s.mode != agentSetup {
		t.Fatal("agent setup lost")
	}
	s = permissionState(t)
	s.handle("D")
	if s.mode != diagnostics {
		t.Fatal("diagnostics lost")
	}
}
func TestPermissionTUIStaleUnsafeAndReplacedFallback(t *testing.T) {
	for _, mutate := range []func(*permissions.Request){func(p *permissions.Request) { p.Deadline = time.Now().Add(-time.Second) }, func(p *permissions.Request) { p.Tool = "Other" }, func(p *permissions.Request) { p.Arguments["command"] = "echo harmless\x1b]52;bad\a" }, func(p *permissions.Request) { p.Arguments["command"] = "echo api_key=sk-syntheticcredential" }, func(p *permissions.Request) { p.Arguments["command"] = "echo changed" }} {
		s := permissionState(t)
		r, _ := s.selection()
		mutate(r.Permission)
		if _, ok := s.livePermission(r); ok {
			t.Fatal("unsafe request actionable")
		}
		text := render(s, 132, 36, time.Now())
		if !strings.Contains(text, "Answer in terminal") || strings.Contains(text, "a approve") {
			t.Fatal("fallback missing")
		}
	}
	s := permissionState(t)
	s.handle("a")
	r, _ := s.selection()
	r.Permission.RequestID = permissions.ID() // target holds a copy below, not mutable refreshed data
	old := *r.Permission
	s.target.Permission = &old
	r.Permission.RequestID = permissions.ID()
	if act, _ := s.handle("enter"); act != nil {
		t.Fatal("replaced request approved")
	}
	s = permissionState(t)
	s.handle("a")
	s.overlayLimit = 3
	if act, _ := s.handle("enter"); act != nil {
		t.Fatal("unseen clipped review approved")
	}
}
func TestPermissionTUICaptures(t *testing.T) {
	dir := os.Getenv("WT_PERMISSION_CAPTURE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dim := range [][2]int{{132, 36}, {80, 24}} {
		s := permissionState(t)
		now := time.Now()
		wing := eggclient.LocalSession{ID: "coordinator", Name: "wing", Agent: "claude", Status: "idle", State: "idle", StateSource: "claude_hook", StateCursor: 4, CWD: "~/code/wingthing"}
		wing.Role = "coordinator"
		wing.Pinned = true
		m := s.machines["local"]
		m.sessions = append(m.sessions, wing)
		s.machines["local"] = m
		s.reconcile()
		s.previews = map[attentionIdentity]sessionPreview{}
		for _, r := range s.rows() {
			value := sessionPreview{messages: []previewMessage{{speaker: "Agent", text: "DNS tests are ready. I need permission for the command.", timestamp: now.Add(-time.Minute).Format(time.RFC3339)}}, observed: now}
			if r.Pinned {
				value.messages = []previewMessage{{speaker: "Agent", text: "Pairing · The checks passed.", timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)}, {speaker: "Agent", text: "DNS tests · The test command needs your review.", timestamp: now.Add(-time.Minute).Format(time.RFC3339)}}
			}
			s.previews[r.identity] = value
		}
		for _, review := range []bool{false, true} {
			name := "attention"
			if review {
				s.handle("a")
				name = "review"
			}
			suffix := fmt.Sprintf("%dx%d", dim[0], dim[1])
			raw := render(s, dim[0], dim[1], time.Now())
			if err := os.WriteFile(filepath.Join(dir, name+"-"+suffix+".ansi"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			text := strings.Join(renderedLines(t, raw, dim[0], dim[1]), "\n")
			if err := os.WriteFile(filepath.Join(dir, name+"-"+suffix+".txt"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPermissionReviewPreservesSignificantSpaces(t *testing.T) {
	command := "echo 'left                         right'"
	for _, width := range []int{20, 40, 80} {
		if got := strings.Join(permissionWrap(command, width), ""); got != command {
			t.Fatalf("changed shell bytes at width %d: %q", width, got)
		}
	}
	s := permissionState(t)
	s.handle("a")
	for _, line := range s.permissionReviewLines() {
		if strings.ContainsAny(line, "\n\r") {
			t.Fatal("JSON was not split into display lines")
		}
	}
}

func TestPermissionReceiptSurvivesSessionUpdates(t *testing.T) {
	s := permissionState(t)
	s.permissionNotice = "Allow once: delivered. Delivery does not confirm execution."
	s.message = "New reply. Background work may still run."
	text := render(s, 80, 24, time.Now())
	if !strings.Contains(text, s.permissionNotice) {
		t.Fatal("delivery receipt lost to session state")
	}
	for _, notice := range []string{s.permissionNotice, "Decline: delivered. Delivery does not confirm execution.", "Decision not confirmed. Answer in terminal."} {
		if safeNotice(notice) != notice {
			t.Fatal("permission receipt hidden")
		}
	}
}

func TestPermissionLossyRPCNumbersDisableReview(t *testing.T) {
	s := permissionState(t)
	r, _ := s.selection()
	p := r.Permission
	p.Arguments["additional_number"] = json.Number("9007199254740993")
	p.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: p.Tool, Arguments: p.Arguments, Directory: p.Directory})
	// structpb transports JSON numbers as float64. A value not representable by
	// that envelope must fall back instead of reviewing a rounded argument.
	wire, _ := json.Marshal(p)
	var envelope map[string]any
	json.Unmarshal(wire, &envelope)
	message, err := structpb.NewStruct(envelope)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ = json.Marshal(message.AsMap())
	var displayed permissions.Request
	if err = json.Unmarshal(wire, &displayed); err != nil {
		t.Fatal(err)
	}
	r.Permission = &displayed
	if _, ok := s.livePermission(r); ok {
		t.Fatal("rounded RPC arguments remained approvable")
	}
}

func TestPermissionTUIReadOnlyShowsCommandWithoutDecisionKeys(t *testing.T) {
	s := permissionState(t)
	m := s.machines["local"]
	m.sessions[0].PermissionRespond = false
	s.machines["local"] = m
	for _, size := range [][2]int{{132, 36}, {80, 24}} {
		text := render(s, size[0], size[1], time.Now())
		for _, want := range []string{"WILL RUN", "echo allowed", "Studio", "developer", "Answer in terminal"} {
			if !strings.Contains(text, want) {
				t.Fatalf("read-only display missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "a approve") || strings.Contains(text, "d decline") {
			t.Fatal("response keys visible without grant")
		}
	}
	for _, key := range []string{"a", "d"} {
		if act, handled := s.permissionAction(key); act != nil || handled {
			t.Fatal("read-only key answered request")
		}
	}
	s.target, _ = s.selection()
	s.mode = details
	text := strings.Join(overlayLines(s, time.Now()), "\n")
	if !strings.Contains(text, "WILL RUN") || strings.Contains(text, "a approve") {
		t.Fatal("read-only details incorrect")
	}
}

func TestPermissionTUIRevocationRemovesReviewAndHelpKeys(t *testing.T) {
	s := permissionState(t)
	s.handle("a")
	m := s.machines["local"]
	m.sessions[0].PermissionRespond = false
	s.machines["local"] = m
	text := render(s, 132, 36, time.Now())
	if !strings.Contains(text, "WILL RUN") || !strings.Contains(text, "Answer in terminal") || strings.Contains(text, "Enter allow once") || strings.Contains(text, "Enter allows") {
		t.Fatal("revoked review retained approval keys")
	}
	if act, _ := s.handle("enter"); act != nil {
		t.Fatal("revoked review sent a decision")
	}
	s.mode = help
	text = strings.Join(overlayLines(s, time.Now()), "\n")
	if strings.Contains(text, "a approve") || strings.Contains(text, "d decline") {
		t.Fatal("read-only help advertises decision keys")
	}
}

func parallelPermissionState(t *testing.T) *state {
	t.Helper()
	s := permissionState(t)
	m := s.machines["local"]
	first := *m.sessions[0].Permission
	second := first
	second.RequestID = permissions.ID()
	second.Arguments = map[string]any{"command": "echo second"}
	second.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: second.Tool, Arguments: second.Arguments, Directory: second.Directory})
	m.sessions[0].Permission = nil
	m.sessions[0].Permissions = []permissions.Request{first, second}
	s.machines["local"] = m
	return s
}

func TestPermissionTUIParallelNavigationAndBoundReview(t *testing.T) {
	s := parallelPermissionState(t)
	first, _ := s.selection()
	for _, size := range [][2]int{{132, 36}, {80, 24}} {
		text := render(s, size[0], size[1], time.Now())
		for _, want := range []string{"2 pending", "1/2", "[/] requests", "echo allowed"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q at %v: %s", want, size, text)
			}
		}
	}
	s.handle("]")
	second, _ := s.selection()
	if second.Permission.RequestID == first.Permission.RequestID {
		t.Fatal("navigation did not select second request")
	}
	s.handle("[")
	again, _ := s.selection()
	if again.Permission.RequestID != first.Permission.RequestID {
		t.Fatal("previous request did not retain identity")
	}
	s.handle("[") // wraps to second
	s.handle("a")
	if !strings.Contains(strings.Join(s.permissionReviewLines(), "\n"), "echo second") {
		t.Fatal("reviewed wrong command")
	}
	s.handle("]") // cannot change the reviewed request
	m := s.machines["local"]
	m.sessions[0].Permissions[0], m.sessions[0].Permissions[1] = m.sessions[0].Permissions[1], m.sessions[0].Permissions[0]
	s.machines["local"] = m
	selected, _ := s.selection()
	if selected.Permission.RequestID != second.Permission.RequestID {
		t.Fatal("refresh reordered selection by index")
	}
	act, _ := s.handle("enter")
	if act == nil || act.permission.RequestID != second.Permission.RequestID || act.permission.Digest != second.Permission.Digest {
		t.Fatal("answer switched requests")
	}
	remaining, _ := s.selection()
	if remaining.Permission.RequestID != second.Permission.RequestID {
		t.Fatal("answer moved selection without user navigation")
	}
	if act, _ := s.handle("d"); act != nil {
		t.Fatal("repeated decline answered an unseen request")
	}
	s.handle("]")
	act, _ = s.handle("d")
	if act == nil || act.permission.RequestID != first.Permission.RequestID || act.permission.Decision != "deny" {
		t.Fatal("decline switched requests")
	}
	remaining, _ = s.selection()
	if _, live := s.livePermission(remaining); live {
		t.Fatal("an answered parallel request became actionable again")
	}
}

func TestPermissionTUIParallelReviewRemovalAndReadOnly(t *testing.T) {
	s := parallelPermissionState(t)
	s.handle("]")
	s.handle("a")
	reviewed := s.target.Permission.RequestID
	m := s.machines["local"]
	m.sessions[0].Permissions = m.sessions[0].Permissions[:1]
	s.machines["local"] = m
	if s.reviewPermissionLive() {
		t.Fatal("removed review remained live")
	}
	if act, _ := s.handle("enter"); act != nil {
		t.Fatalf("removed %s approved another request", reviewed)
	}
	s = parallelPermissionState(t)
	m = s.machines["local"]
	m.sessions[0].PermissionRespond = false
	s.machines["local"] = m
	s.handle("]")
	r, _ := s.selection()
	if r.Permission.Arguments["command"] != "echo second" {
		t.Fatal("read-only requests cannot be navigated")
	}
	text := render(s, 80, 24, time.Now())
	if !strings.Contains(text, "2 pending") || !strings.Contains(text, "Answer in terminal") || strings.Contains(text, "a approve") {
		t.Fatal("read-only parallel display incorrect")
	}
}

func TestPermissionTUIParallelPendingOverridesSiblingToolStatus(t *testing.T) {
	s := parallelPermissionState(t)
	m := s.machines["local"]
	m.sessions[0].Status, m.sessions[0].State = "running", "working"
	m.sessions[0].StateSource, m.sessions[0].StateCursor = "", 0
	s.machines["local"] = m
	r, _ := s.selection()
	if _, live := s.livePermission(r); !live || r.attention != "blocked" {
		t.Fatal("sibling PostToolUse hid a pending request")
	}
	if text := render(s, 80, 24, time.Now()); !strings.Contains(text, "1 needs you") {
		t.Fatal("header did not count the session with pending broker requests")
	}
	s.handle("]")
	act, _ := s.handle("d")
	if act == nil || act.permission.Decision != "deny" {
		t.Fatal("remaining parallel request could not be answered")
	}
}

func TestPermissionRefreshQueriesActiveClaudeOnSupportedPlatform(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			dir := t.TempDir()
			var mu sync.Mutex
			var calls []string
			connections := 0
			server, err := controlsocket.Listen(t.Context(), dir, "personal", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
				mu.Lock()
				connections++
				mu.Unlock()
				return controlsocket.Welcome{Grants: map[string]bool{"permission.read": true}}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
					var args map[string]string
					if err := json.Unmarshal(r.Arguments, &args); err != nil {
						t.Error(err)
					}
					mu.Lock()
					calls = append(calls, r.Tool+":"+args["session_id"])
					mu.Unlock()
					return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"requests": []permissions.Request{}}}
				}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			sessions := []eggclient.LocalSession{
				{ID: "blocked-claude", Agent: "claude", Status: "blocked"},
				{ID: "running-claude", Agent: "claude", Status: "running"},
				{ID: "idle-claude", Agent: "claude", Status: "idle"},
				{ID: "exited-claude", Agent: "claude", Status: "exited"},
				{ID: "blocked-codex", Agent: "codex", Status: "blocked"},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			for i := 0; i < 3; i++ {
				readPermissionsForPlatform(ctx, dir, sessions, supported)
			}
			mu.Lock()
			defer mu.Unlock()
			want := 0
			if supported {
				want = 9
			}
			wantConnections := 0
			if supported {
				wantConnections = 3
			}
			if len(calls) != want || connections != wantConnections {
				t.Fatalf("three refreshes: calls=%v connections=%d want=%d", calls, connections, want)
			}
			for _, call := range calls {
				if call != "permission_list:blocked-claude" && call != "permission_list:running-claude" && call != "permission_list:idle-claude" {
					t.Fatalf("queried ineligible session: %s", call)
				}
			}
		})
	}
}

func TestPermissionTUIAnswerStaysSelectedAcrossRefreshUntilMovement(t *testing.T) {
	for _, answer := range []string{"allow", "deny"} {
		for _, movement := range []string{"[", "]", "session"} {
			t.Run(answer+"/"+movement, func(t *testing.T) {
				s := parallelPermissionState(t)
				first, _ := s.selection()
				if answer == "allow" {
					s.handle("a")
					s.handle("enter")
				} else {
					s.handle("d")
				}
				m := s.machines["local"]
				m.sessions[0].Permissions = m.sessions[0].Permissions[1:]
				m.sessions = append(m.sessions, eggclient.LocalSession{ID: "other", Agent: "claude", Status: "idle"})
				s.machines["local"] = m
				s.reconcile()
				for _, size := range [][2]int{{132, 36}, {80, 24}} {
					raw := render(s, size[0], size[1], time.Now())
					if !strings.Contains(raw, "1 pending") || !strings.Contains(raw, "[/] requests") || strings.Contains(raw, "echo second") || strings.Contains(raw, "d decline") {
						t.Fatalf("answer exposed another request at %v: %s", size, raw)
					}
				}
				selected, _ := s.selection()
				if selected.Permission == nil || selected.Permission.RequestID != first.Permission.RequestID {
					t.Fatal("refresh moved off the answered request")
				}
				for i := 0; i < 2; i++ {
					if act, _ := s.handle("d"); act != nil {
						t.Fatal("repeated d declined an unseen request")
					}
				}
				if movement == "session" {
					s.handle("j")
					s.handle("k")
				} else {
					s.handle(movement)
				}
				selected, _ = s.selection()
				if selected.Permission == nil || selected.Permission.Arguments["command"] != "echo second" {
					t.Fatal("explicit movement failed to show the remaining request")
				}
				act, _ := s.handle("d")
				if act == nil || act.permission == nil || act.permission.RequestID != selected.Permission.RequestID {
					t.Fatal("could not decline the newly displayed request")
				}
			})
		}
	}
}

func TestPermissionTUISequentialRequestReplacesAnsweredRequest(t *testing.T) {
	for _, answer := range []string{"allow", "deny"} {
		for _, gap := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gap-%t", answer, gap), func(t *testing.T) {
				s := permissionState(t)
				first, _ := s.selection()
				if answer == "allow" {
					s.handle("a")
					s.handle("enter")
				} else {
					s.handle("d")
				}
				m := s.machines["local"]
				m.sessions[0].Permission = nil
				if gap {
					m.sessions[0].Status, m.sessions[0].State = "running", "working"
					s.machines["local"] = m
					s.reconcile()
				}
				next := *first.Permission
				next.RequestID = permissions.ID()
				next.Arguments = map[string]any{"command": "echo next"}
				next.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: next.Tool, Arguments: next.Arguments, Directory: next.Directory})
				m.sessions[0].Permissions = []permissions.Request{next}
				m.sessions[0].Status, m.sessions[0].State = "blocked", "needs_input"
				s.machines["local"] = m
				s.reconcile()
				selected, _ := s.selection()
				if p, live := s.livePermission(selected); !live || p.RequestID != next.RequestID {
					t.Fatal("answered request hid the next command")
				}
				for _, size := range [][2]int{{132, 36}, {80, 24}} {
					if text := render(s, size[0], size[1], time.Now()); !strings.Contains(text, "echo next") {
						t.Fatalf("next command missing at %v: %s", size, text)
					}
				}
				s.handle("a")
				act, _ := s.handle("enter")
				if act == nil || act.permission == nil || act.permission.RequestID != next.RequestID {
					t.Fatal("next command could not be reviewed and answered")
				}
			})
		}
	}
}

func TestPermissionRefreshShowsParallelRequestAfterSiblingPostToolUse(t *testing.T) {
	s := parallelPermissionState(t)
	m := s.machines["local"]
	remaining := m.sessions[0].Permissions[1]
	m.sessions[0].Permissions = nil
	m.sessions[0].Status, m.sessions[0].State = "running", "working"
	m.sessions[0].StateCursor++
	dir := t.TempDir()
	server, err := controlsocket.Listen(t.Context(), dir, "personal", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{WingID: "personal", Grants: map[string]bool{"permission.read": true, "permission.respond": true}}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"requests": []permissions.Request{remaining}}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	readPermissionsForPlatform(ctx, dir, m.sessions, true)
	s.machines["local"] = m
	s.reconcile()
	r, _ := s.selection()
	if p, live := s.livePermission(r); !live || p.RequestID != remaining.RequestID || r.attention != "blocked" {
		t.Fatal("production refresh hid the remaining parallel request")
	}
	s.handle("a")
	act, _ := s.handle("enter")
	if act == nil || act.permission == nil || act.permission.RequestID != remaining.RequestID {
		t.Fatal("refreshed parallel request could not be reviewed and answered")
	}
}

func TestPermissionPlaceSplitsAtCommasInsteadOfInsideTheDirectory(t *testing.T) {
	s := permissionState(t)
	r, _ := s.selection()
	r.Permission.Directory = "/home/developer/projects/wingthing"
	r.Permission.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: r.Permission.Tool, Arguments: r.Permission.Arguments, Directory: r.Permission.Directory})
	text := strings.Join(renderedLines(t, render(s, 132, 36, time.Now()), 132, 36), "\n")
	for _, want := range []string{"on Studio, as developer", "in /home/developer/projects/wingthing", "a approve   d decline"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}
