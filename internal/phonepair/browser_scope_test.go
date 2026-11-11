package phonepair

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowserTransportForcesInventoryScope(t *testing.T) {
	for _, startRestricted := range []bool{false, true} {
		for _, scope := range []string{"", ScopeInventory, ScopeControl, "admin", "*"} {
			t.Run(scope+map[bool]string{false: "/prove-route", true: "/start-route"}[startRestricted], func(t *testing.T) {
				f := fixture(t)
				f.startOnTransport(t, f.q.Secret, startRestricted)
				payload, err := auth.Encrypt(f.pair, encode(map[string]any{
					"op": "prove", "id": f.q.ID, "name": "Browser", "scope": scope,
					"public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y)),
				}))
				if err != nil {
					t.Fatal(err)
				}
				// Starting on the browser route binds the restriction even if
				// the subsequent proof arrives through the native transport.
				r := f.m.Handle(context.Background(), Request{Op: "pair.prove", ID: f.q.ID, Payload: payload, InventoryOnly: !startRestricted})
				if scope != "" && scope != ScopeInventory {
					if r.Error != "denied" {
						t.Fatal(r)
					}
					if err := f.m.Allow(f.q.ID); err == nil {
						t.Fatal("denied scope reached terminal approval")
					}
				} else {
					status, err := f.m.Status(f.q.ID)
					if r.Error != "" || err != nil || status.Scope != ScopeInventory {
						t.Fatal(r, status, err)
					}
				}
				if len(f.m.Devices()) != 0 {
					t.Fatal("scope request granted a device")
				}
			})
		}
	}
}

func TestBrowserPolicyRequiresAuthenticatedPairingPayload(t *testing.T) {
	for _, fault := range []string{"ciphertext", "invitation", "operation"} {
		t.Run(fault, func(t *testing.T) {
			f := fixture(t)
			f.start(t, f.q.Secret)
			if r := f.send(t, "prove", map[string]any{"name": "Native", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y)), "scope": ScopeControl}); r.Error != "" {
				t.Fatal(r)
			}
			c := map[string]any{"op": "status", "id": f.q.ID}
			if fault == "invitation" {
				c["id"] = "another-invitation"
			} else if fault == "operation" {
				c["op"] = "confirm"
			}
			payload, err := auth.Encrypt(f.pair, encode(c))
			if err != nil {
				t.Fatal(err)
			}
			if fault == "ciphertext" {
				payload = "invalid"
			}
			r := f.m.Handle(context.Background(), Request{Op: "pair.status", ID: f.q.ID, Payload: payload, InventoryOnly: true})
			if r.Error != "bad_proof" || f.m.pending[f.q.ID].inventoryOnly {
				t.Fatal("unauthenticated request changed native policy", r)
			}
		})
	}
}

func TestTerminalApprovedInventoryScopePersistsAndCannotAct(t *testing.T) {
	f := fixture(t)
	f.start(t, f.q.Secret)
	r := f.send(t, "prove", map[string]any{"name": "Browser", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y)), "scope": ScopeInventory})
	if r.Error != "" {
		t.Fatal(r)
	}
	status, err := f.m.Status(f.q.ID)
	if err != nil || status.Scope != ScopeInventory || status.Fingerprint != Fingerprint(f.q.Key) {
		t.Fatal(status, err)
	}
	if r = f.send(t, "confirm", map[string]any{"emoji": f.emoji}); r.Error != "" {
		t.Fatal(r)
	}
	if len(f.m.Devices()) != 0 {
		t.Fatal("browser confirmation alone granted access")
	}
	if err = f.m.Allow(f.q.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.m.Devices()) != 0 {
		t.Fatal("grant active before browser storage commit")
	}
	if f.m.device(f.m.pending[f.q.ID].device) != nil {
		t.Fatal("prepared credential can authenticate")
	}
	r = f.send(t, "commit", nil)
	b, _ := auth.Decrypt(f.pair, r.Payload)
	var reply struct {
		DeviceID   string `json:"device_id"`
		Credential string `json:"credential"`
	}
	if json.Unmarshal(b, &reply) != nil {
		t.Fatal("paired response")
	}
	f.device, f.credential = reply.DeviceID, reply.Credential
	// Restart the manager from the durable registry, preserving no pairing state.
	restarted, err := New(f.m.dir, f.m.wingID, f.m.name, f.m.local, nil, f.m.private, f.handler)
	if err != nil || restarted.Devices()[0].Scope != ScopeInventory {
		t.Fatal("scope not persisted", err)
	}
	f.m = restarted
	c := f.connect(t)
	for i, op := range []string{"sessions", "inventory", "launch", "input", "action", "attach", "read", "stop", "search", "fleet_input", "fleet_attach", "permission_list", "permission_respond"} {
		r = f.m.Handle(context.Background(), c.request(uint64(i+1), op))
		if i < 2 {
			if r.Error != "" {
				t.Fatal(op, r)
			}
		} else if r.Error != "denied" {
			t.Fatal(op, r)
		}
	}
	if f.handler.calls != 2 {
		t.Fatal("an action reached the handler")
	}
	replay := c.request(14, "sessions")
	if r = f.m.Handle(context.Background(), replay); r.Error != "" {
		t.Fatal(r)
	}
	if r = f.m.Handle(context.Background(), replay); r.Error != "replay" {
		t.Fatal(r)
	}
	if f.handler.calls != 3 {
		t.Fatal("replay executed")
	}
	if err = f.m.Revoke(f.device); err != nil {
		t.Fatal(err)
	}
	if r = f.m.Handle(context.Background(), c.request(15, "sessions")); r.Error != "revoked" {
		t.Fatal(r)
	}
}

func TestBrowserStorageCommitIsRequiredAndIdempotent(t *testing.T) {
	for _, mode := range []string{"commit", "cancel", "expiry", "restart", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			f.start(t, f.q.Secret)
			r := f.send(t, "prove", map[string]any{"name": "Browser", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y)), "scope": ScopeInventory})
			if r.Error != "" {
				t.Fatal(r)
			}
			if r = f.send(t, "commit", nil); r.Error != "denied" {
				t.Fatal("commit before approval", r)
			}
			if r = f.send(t, "confirm", map[string]any{"emoji": f.emoji}); r.Error != "" {
				t.Fatal(r)
			}
			if err := f.m.Allow(f.q.ID); err != nil {
				t.Fatal(err)
			}
			p := f.m.pending[f.q.ID]
			if p.state != "saving" || len(f.m.Devices()) != 0 || f.m.device(p.device) != nil {
				t.Fatal("storage not acknowledged but credential admitted")
			}
			switch mode {
			case "cancel":
				if r = f.send(t, "cancel", nil); r.Error != "" {
					t.Fatal(r)
				}
				if r = f.send(t, "commit", nil); r.Error != "cancelled" {
					t.Fatal(r)
				}
			case "expiry":
				f.m.now = func() time.Time { return f.q.ExpiresAt }
				if r = f.send(t, "commit", nil); r.Error != "expired" {
					t.Fatal(r)
				}
			case "restart":
				m, err := New(f.m.dir, f.m.wingID, f.m.name, f.m.local, nil, f.m.private, f.handler)
				if err != nil || len(m.Devices()) != 0 {
					t.Fatal("restart admitted prepared grant", err)
				}
			case "save-failure":
				dir := f.m.dir
				f.m.dir = filepath.Join(dir, "missing")
				if r = f.send(t, "commit", nil); r.Error == "" {
					t.Fatal("failed persistence activated device")
				}
				if len(f.m.Devices()) != 0 || f.m.device(p.device) != nil {
					t.Fatal("save failure admitted device")
				}
				f.m.dir = dir
			case "commit":
				r = f.send(t, "commit", nil)
				if r.Error != "" || len(f.m.Devices()) != 1 {
					t.Fatal(r)
				}
				first, _ := auth.Decrypt(f.pair, r.Payload)
				r = f.send(t, "commit", nil)
				second, _ := auth.Decrypt(f.pair, r.Payload)
				if r.Error != "" || string(first) != string(second) || len(f.m.Devices()) != 1 {
					t.Fatal("commit replay enlarged grant")
				}
				if err := f.m.Revoke(p.device); err != nil {
					t.Fatal(err)
				}
				if r = f.send(t, "commit", nil); r.Error != "revoked" {
					t.Fatal("revoked commit resurrected grant", r)
				}
			}
			if mode != "commit" && len(f.m.Devices()) != 0 {
				t.Fatal("failure granted access")
			}
		})
	}
}
