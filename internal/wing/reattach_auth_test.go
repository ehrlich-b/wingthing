package wing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestReattachAuthorizationRepliesKeepAttempt(t *testing.T) {
	for _, mode := range []string{"web", "reclaimed"} {
		t.Run(mode, func(t *testing.T) {
			f := newSessionTransportFixture(t)
			// A nonempty local key exercises the challenge path; assertions are
			// deliberately invalid, so no credential or external authenticator is used.
			keys := []config.AllowKey{{UserID: "bob", Key: "invalid-fixture-key"}}
			policy := f.sessions.Policy
			f.sessions.Policy = func() wingsession.Policy { p := policy(); p.Keys = keys; return p }
			f.client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
				handlePTYSession("test", ctx, f.cfg, f.wc.Clone(), start, write, input, egg.DefaultEggConfig(), false, false, &keys, f.sessions.AuthCache, auth.PasskeyPolicy{}, 0, 0, nil, nil, nil, false, f.sessions)
			}
			key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
			id := "auth-attempt"
			if mode == "web" {
				f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: key})
				f.next(ws.TypePTYStarted)
			} else {
				result := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
				id = result["session"].(string)
			}
			for _, tc := range []struct{ user, role, key, message string }{
				{"charlie", "member", key, "session not found or not owned by caller"},
				{"alice", "owner", "", "client encryption key required"},
				{"alice", "owner", "invalid", "client encryption setup failed"},
			} {
				f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "attempt", UserID: tc.user, OrgRole: tc.role, PublicKey: tc.key})
				var refusal ws.ErrorMsg
				if err := json.Unmarshal(f.next(ws.TypeError), &refusal); err != nil {
					t.Fatal(err)
				}
				if refusal.ControllerID != "attempt" || refusal.Message != tc.message {
					t.Fatalf("unscoped refusal: %+v", refusal)
				}
			}
			for _, attempt := range []string{"expired", "replacement"} {
				f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: attempt, UserID: "bob", OrgRole: "admin", PublicKey: key})
				var challenge ws.PasskeyChallenge
				if err := json.Unmarshal(f.next(ws.TypePasskeyChallenge), &challenge); err != nil {
					t.Fatal(err)
				}
				if challenge.ControllerID != attempt {
					t.Fatalf("unscoped challenge: %+v", challenge)
				}
			}
			f.send(ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: id, ControllerID: "expired"})
			f.send(ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: id, ControllerID: "replacement"})
			var refusal ws.ErrorMsg
			if err := json.Unmarshal(f.next(ws.TypeError), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.ControllerID != "replacement" || refusal.Message != "invalid passkey" {
				t.Fatalf("stale response consumed replacement: %+v", refusal)
			}
		})
	}
}
