package dashboard

import (
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"google.golang.org/protobuf/types/known/structpb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func permissionState(t *testing.T) *state {
	t.Helper()
	now := time.Now()
	p := &permissions.Request{WingID: "personal", SessionID: "work", ProviderID: "provider", Generation: "generation", RequestID: permissions.ID(), Digest: "", Tool: "Bash", Arguments: map[string]any{"command": "echo allowed"}, Directory: "~/code/wingthing", Machine: "Studio", User: "developer", Scope: "This complete Bash invocation", Deadline: now.Add(time.Minute)}
	p.Digest = permissionbridge.Digest(permissionbridge.Input{Tool: p.Tool, Arguments: p.Arguments, Directory: p.Directory})
	s := &state{machines: map[string]machine{"local": {updated: now, sessions: []eggclient.LocalSession{{ID: "work", Name: "Run DNS tests", Agent: "claude", Status: "blocked", State: "needs_input", StateSource: "claude_hook", StateCursor: 1, CWD: "~/code/wingthing", Permission: p}}}}, selected: "local:work", selectionTouched: true, theme: terminalTheme{}}
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
		if !strings.Contains(text, "Answer in terminal") || strings.Contains(text, "a review") {
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
