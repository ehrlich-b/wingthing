package wing

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestQueuedOldControllerInputIsRefused(t *testing.T) {
	oldChannel := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	for _, channel := range []string{"stable", "preview"} {
		for _, mode := range []string{"web", "reclaimed"} {
			t.Run(channel+"/"+mode, func(t *testing.T) {
				config.ReleaseChannel = channel
				f := newSessionTransportFixture(t)
				oldKey := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
				id := "queued-old-input"
				if mode == "web" {
					f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, ControllerID: "old", Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: oldKey})
					f.next(ws.TypePTYStarted)
				} else {
					result := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
					id = result["session"].(string)
					f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "old", UserID: "alice", OrgRole: "owner", PublicKey: oldKey, Takeover: true})
					f.next(ws.TypePTYStarted)
				}
				replacement, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				newKey, err := auth.DeriveSharedKey(replacement, base64.StdEncoding.EncodeToString(f.wingKey.PublicKey().Bytes()), "wt-pty")
				if err != nil {
					t.Fatal(err)
				}
				oldInput, err := auth.Encrypt(f.ptyKey, []byte("old"))
				if err != nil {
					t.Fatal(err)
				}
				newInput, err := auth.Encrypt(newKey, []byte("replacement"))
				if err != nil {
					t.Fatal(err)
				}
				// Queue old-controller frames after attach, before waiting for its reply.
				// The relay can still be forwarding these while authorization is pending.
				f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "new", UserID: "alice", OrgRole: "owner", PublicKey: base64.StdEncoding.EncodeToString(replacement.PublicKey().Bytes()), Takeover: true})
				frames := []struct{ controller, request, data, code string }{
					{"old", "queued-old", oldInput, "controller_replaced"},
					{"", "legacy-old", oldInput, "input_decrypt_failed"},
					{"old", "same-key-old", newInput, "controller_replaced"},
					{"new", "invalid-key", oldInput, "input_decrypt_failed"},
				}
				for _, frame := range frames {
					f.send(map[string]string{"type": ws.TypePTYInput, "session_id": id, "controller_id": frame.controller, "request_id": frame.request, "data": frame.data})
				}
				f.send(map[string]string{"type": ws.TypePTYInput, "session_id": id, "controller_id": "new", "data": newInput})
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				for _, frame := range frames {
					for {
						select {
						case data := <-f.replies:
							var reply ws.ErrorMsg
							if err := json.Unmarshal(data, &reply); err != nil {
								t.Fatal(err)
							}
							if reply.Type != ws.TypeError {
								continue
							}
							if reply.SessionID != id || reply.ControllerID != frame.controller || reply.RequestID != frame.request || reply.Operation != ws.TypePTYInput || reply.Code != frame.code {
								t.Fatalf("misrouted refusal: %+v, want %+v", reply, frame)
							}
						case <-deadline.C:
							t.Fatalf("no explicit refusal for %s", frame.request)
						}
						break
					}
				}
				select {
				case input := <-f.egg(id).input:
					if string(input) != "replacement" {
						t.Fatalf("stale input reached egg: %q", input)
					}
				case <-deadline.C:
					t.Fatal("replacement input was not delivered")
				}
				t.Log("queued stale identities and old keys refused to their sender; replacement input delivered")
			})
		}
	}
}
