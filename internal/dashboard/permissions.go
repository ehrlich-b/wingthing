package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mattn/go-runewidth"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"github.com/ehrlich-b/wingthing/internal/sessionsearch"
)

func readPermissions(ctx context.Context, dir string, sessions []eggclient.LocalSession) {
	readPermissionsForPlatform(ctx, dir, sessions, permissions.NativeHookSupported())
}

func readPermissionsForPlatform(ctx context.Context, dir string, sessions []eggclient.LocalSession, supported bool) {
	if !supported {
		return
	}
	c, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: "tui", Human: "tui"})
	if err != nil {
		return
	}
	defer c.Close()
	if !c.Welcome.Grants["permission.read"] {
		return
	}
	for i := range sessions {
		r := &sessions[i]
		if r.Agent != "claude" || r.Status != "blocked" {
			continue
		}
		args, _ := json.Marshal(map[string]string{"wing_id": c.Welcome.WingID, "session_id": r.ID})
		result, isError, err := c.Call(ctx, "permission_list", args)
		if err != nil || isError {
			continue
		}
		wire, _ := json.Marshal(result["requests"])
		var requests []permissions.Request
		if json.Unmarshal(wire, &requests) != nil || len(requests) == 0 {
			continue
		}
		for _, p := range requests {
			if p.WingID == c.Welcome.WingID && p.SessionID == r.ID {
				r.Permissions = append(r.Permissions, p)
			}
		}
		r.PermissionRespond = c.Welcome.Grants["permission.respond"]
	}
}
func (s *state) livePermission(r row) (*permissions.Request, bool) {
	p := r.Permission
	if p == nil || s.permissionWasAnswered(p.RequestID) || r.machine != "local" || r.Agent != "claude" || r.Status != "blocked" || s.stale(r.machine) || p.SessionID != r.ID || !time.Now().Before(p.Deadline) || p.Tool != "Bash" || p.WingID == "" || p.RequestID == "" || p.Digest == "" || p.ProviderID == "" || p.Generation == "" || p.Machine == "" || p.User == "" || p.Directory == "" {
		return nil, false
	}
	if permissionbridge.Digest(permissionbridge.Input{Tool: p.Tool, Arguments: p.Arguments, Directory: p.Directory}) != p.Digest {
		return nil, false
	}
	command, ok := p.Arguments["command"].(string)
	if !ok || command == "" {
		return nil, false
	}
	// Text that would require escaping or redaction cannot be fully reviewed.
	// Keep that request on the provider's native surface instead of approving a
	// cleaned-up representation of a different command.
	wire, _ := json.Marshal(p.Arguments)
	for _, value := range []string{command, string(wire), p.Machine, p.User, p.Directory, p.Scope} {
		if clean(value) != value || sessionsearch.Redact(value) != value {
			return nil, false
		}
	}
	return p, true
}
func permissionWhere(p *permissions.Request) string {
	return "on " + p.Machine + ", as " + p.User + ", in " + p.Directory
}
func (s *state) permissionAction(key string) (*action, bool) {
	r, ok := s.selection()
	if !ok {
		return nil, false
	}
	p, ok := s.livePermission(r)
	if !ok || !r.PermissionRespond {
		return nil, false
	}
	if key == "a" {
		s.permissionNotice = ""
		s.target, s.mode, s.overlayOffset = r, permissionReview, 0
		return nil, true
	}
	if key == "d" {
		s.permissionNotice = ""
		response := permissionResponse(p, "deny")
		s.markPermissionAnswered(r, p)
		return &action{kind: permissionReview, row: r, permission: &response}, true
	}
	return nil, false
}
func permissionResponse(p *permissions.Request, decision string) permissions.Response {
	return permissions.Response{WingID: p.WingID, SessionID: p.SessionID, RequestID: p.RequestID, Digest: p.Digest, AttemptID: permissions.ID(), Decision: decision}
}
func sendPermission(ctx context.Context, dir string, r permissions.Response) (string, error) {
	args, _ := json.Marshal(r)
	result, err := localmcp.CallHumanPermission(ctx, dir, "tui", "tui", "permission_respond", args)
	if err != nil {
		return "", err
	}
	wire, _ := json.Marshal(result["outcome"])
	var out permissions.Outcome
	if err = json.Unmarshal(wire, &out); err != nil {
		return "", err
	}
	label := "Allow once"
	if r.Decision == "deny" {
		label = "Decline"
	}
	if out.State != "delivered" {
		return label + ": unconfirmed. Answer in terminal; this decision will not be resent.", nil
	}
	return fmt.Sprintf("%s: delivered. Delivery does not confirm execution.", label), nil
}
func (s *state) permissionReviewLines() []string {
	p := s.target.Permission
	if p == nil {
		return []string{"Answer in terminal"}
	}
	title, confirm, cancel := "Allow once", "Enter allows this complete Bash invocation once.", "Esc cancels. No permission rule is saved."
	if !s.reviewPermissionLive() {
		title, confirm, cancel = "Answer in terminal", "Answer in terminal", "Esc returns to the session list."
	}
	wire, _ := json.MarshalIndent(p.Arguments, "", "  ")
	lines := []string{title, s.target.name(), "", "WILL RUN", fmt.Sprint(p.Arguments["command"]), permissionWhere(p), "", "Tool: " + p.Tool, "Arguments:"}
	lines = append(lines, strings.Split(string(wire), "\n")...)
	return append(lines, "Scope: "+p.Scope, "Deadline: "+p.Deadline.Local().Format("15:04:05"), "Request: "+p.RequestID, "Digest: "+p.Digest, "", confirm, cancel)
}

// Permission review preserves every space: normal prose wrapping would change
// shell quoted strings or JSON argument values at line boundaries.
func permissionWrap(line string, width int) []string {
	width = max(1, width)
	line = clean(line)
	var lines []string
	for runewidth.StringWidth(line) > width {
		prefix := runewidth.Truncate(line, width, "")
		if prefix == "" {
			prefix = string([]rune(line)[0])
		}
		lines = append(lines, prefix)
		line = strings.TrimPrefix(line, prefix)
	}
	return append(lines, line)
}
func (s *state) overlayWrap(line string, width int) []string {
	if s.mode == permissionReview {
		return permissionWrap(line, width)
	}
	return wrap(line, width)
}

func (s *state) permissionKeys(r row) string {
	label := s.permissionPosition(r)
	if r.PermissionRespond {
		return label + "a review   d decline   Enter open"
	}
	return label + "Answer in terminal   Enter open"
}

func (s *state) permissionPosition(r row) string {
	requests := s.pendingPermissions(r)
	if r.Permission != nil && s.permissionWasAnswered(r.Permission.RequestID) && len(requests) > 0 {
		return fmt.Sprintf("%d pending · [/] requests   ", len(requests))
	}
	if len(requests) == 1 {
		return "1 pending   "
	}
	if len(requests) > 1 {
		index := 0
		for i := range requests {
			if r.Permission != nil && requests[i].RequestID == r.Permission.RequestID {
				index = i
			}
		}
		return fmt.Sprintf("%d pending · %d/%d · [/] requests   ", len(requests), index+1, len(requests))
	}
	return ""
}

func (s *state) reviewPermissionLive() bool {
	for _, r := range s.rows() {
		if r.identity != s.target.identity {
			continue
		}
		p, live := s.reviewedPermission(r)
		return live && r.PermissionRespond && s.target.Permission != nil && p.RequestID == s.target.Permission.RequestID && p.Digest == s.target.Permission.Digest
	}
	return false
}

func (s *state) permissionWasAnswered(id string) bool {
	return id == s.permissionAnswered || s.permissionAnswers[id]
}

func (s *state) markPermissionAnswered(r row, p *permissions.Request) {
	id := p.RequestID
	if s.permissionRetained == nil {
		s.permissionRetained = map[string]permissions.Request{}
	}
	// Keep this exact request visible through refreshes until explicit movement.
	s.permissionRetained[r.key()] = *p
	if s.permissionAnswers == nil {
		s.permissionAnswers = map[string]bool{}
	}
	s.permissionAnswers[id] = true
	s.permissionAnswered = id
}

func (s *state) pendingPermissions(r row) []permissions.Request {
	requests := r.Permissions
	if len(requests) == 0 && r.Permission != nil {
		requests = []permissions.Request{*r.Permission}
	}
	var pending []permissions.Request
	for _, p := range requests {
		if p.SessionID == r.ID && !s.permissionWasAnswered(p.RequestID) && time.Now().Before(p.Deadline) {
			pending = append(pending, p)
		}
	}
	return pending
}

func (s *state) selectPermission(r *row) {
	if retained, ok := s.permissionRetained[r.key()]; ok {
		r.Permission = &retained
		if len(s.pendingPermissions(*r)) > 0 {
			r.Status, r.State = "blocked", "needs_input"
		}
		return
	}
	if len(r.Permissions) == 0 {
		return
	}
	r.Permission = nil
	requests := s.pendingPermissions(*r)
	if len(requests) == 0 {
		return
	}
	// A sibling tool can publish PostToolUse while another hook is waiting.
	// The authenticated pending list still requires an answer in that case.
	r.Status, r.State = "blocked", "needs_input"
	selected := s.permissionSelected[r.key()]
	p := requests[0]
	for _, candidate := range requests {
		if candidate.RequestID == selected {
			p = candidate
			break
		}
	}
	r.Permission = &p
	if s.permissionSelected == nil {
		s.permissionSelected = map[string]string{}
	}
	s.permissionSelected[r.key()] = p.RequestID
}

func (s *state) stepPermission(key string) bool {
	r, ok := s.selection()
	if !ok || s.stale(r.machine) || r.Status != "blocked" {
		return false
	}
	requests := s.pendingPermissions(r)
	if len(requests) == 0 {
		return false
	}
	if _, retained := s.permissionRetained[r.key()]; retained {
		index := 0
		if key == "[" {
			index = len(requests) - 1
		}
		delete(s.permissionRetained, r.key())
		if s.permissionSelected == nil {
			s.permissionSelected = map[string]string{}
		}
		s.permissionSelected[r.key()] = requests[index].RequestID
		if s.mode == details {
			s.target, _ = s.selection()
			s.overlayOffset = 0
		}
		return true
	}
	if len(requests) < 2 {
		return false
	}
	for i, p := range requests {
		if r.Permission != nil && p.RequestID == r.Permission.RequestID {
			delta := 1
			if key == "[" {
				delta = -1
			}
			if s.permissionSelected == nil {
				s.permissionSelected = map[string]string{}
			}
			s.permissionSelected[r.key()] = requests[(i+delta+len(requests))%len(requests)].RequestID
			if s.mode == details {
				s.target, _ = s.selection()
				s.overlayOffset = 0
			}
			return true
		}
	}
	return false
}

func (s *state) reviewedPermission(r row) (*permissions.Request, bool) {
	if s.target.Permission == nil || r.identity != s.target.identity {
		return nil, false
	}
	for _, p := range s.pendingPermissions(r) {
		if p.RequestID == s.target.Permission.RequestID && p.Digest == s.target.Permission.Digest {
			r.Permission = &p
			return s.livePermission(r)
		}
	}
	return nil, false
}
