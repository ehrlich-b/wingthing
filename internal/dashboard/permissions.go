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
	c, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: "tui", Human: "tui"})
	if err != nil {
		return
	}
	defer c.Close()
	if !c.Welcome.Grants["permission.read"] || !c.Welcome.Grants["permission.respond"] {
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
		if json.Unmarshal(wire, &requests) != nil || len(requests) != 1 {
			continue
		}
		p := requests[0]
		if p.WingID != c.Welcome.WingID || p.SessionID != r.ID {
			continue
		}
		r.Permission = &p
	}
}
func (s *state) livePermission(r row) (*permissions.Request, bool) {
	p := r.Permission
	if p == nil || p.RequestID == s.permissionAnswered || r.machine != "local" || r.Agent != "claude" || r.Status != "blocked" || s.stale(r.machine) || p.SessionID != r.ID || !time.Now().Before(p.Deadline) || p.Tool != "Bash" || p.WingID == "" || p.RequestID == "" || p.Digest == "" || p.ProviderID == "" || p.Generation == "" || p.Machine == "" || p.User == "" || p.Directory == "" {
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
	if !ok {
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
		s.permissionAnswered = p.RequestID
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
	wire, _ := json.MarshalIndent(p.Arguments, "", "  ")
	lines := []string{"Allow once", s.target.name(), "", "WILL RUN", fmt.Sprint(p.Arguments["command"]), permissionWhere(p), "", "Tool: " + p.Tool, "Arguments:"}
	lines = append(lines, strings.Split(string(wire), "\n")...)
	return append(lines, "Scope: "+p.Scope, "Deadline: "+p.Deadline.Local().Format("15:04:05"), "Request: "+p.RequestID, "Digest: "+p.Digest, "", "Enter allows this complete Bash invocation once.", "Esc cancels. No permission rule is saved.")
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
