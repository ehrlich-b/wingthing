//go:build cgo

package phonepair

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/control"
)

func TestBrowserPermissionGrantsCannotWidenInventoryScope(t *testing.T) {
	f := fixture(t)
	f.startOnTransport(t, f.q.Secret, true)
	if r := f.send(t, "prove", map[string]any{"name": "Browser", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y))}); r.Error != "" {
		t.Fatal(r)
	}
	if r := f.send(t, "confirm", map[string]any{"emoji": f.emoji}); r.Error != "" {
		t.Fatal(r)
	}
	if err := f.m.Allow(f.q.ID); err != nil {
		t.Fatal(err)
	}
	r := f.send(t, "commit", nil)
	b, err := auth.Decrypt(f.pair, r.Payload)
	if r.Error != "" || err != nil {
		t.Fatal(r, err)
	}
	var paired struct {
		DeviceID   string `json:"device_id"`
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(b, &paired); err != nil {
		t.Fatal(err)
	}
	f.device, f.credential = paired.DeviceID, paired.Credential
	grants := map[string]bool{}
	for _, name := range []string{"permission_list", "permission_respond"} {
		tool, ok := control.Lookup(name)
		if !ok || !tool.Supports(control.SurfaceHuman) {
			t.Fatal("missing human permission contract", name)
		}
		grants[tool.Grant] = true
	}
	input, err := json.Marshal(map[string]any{"scope": ScopeControl, "grants": grants, "operation": "permission_respond", "decision": "allow"})
	if err != nil {
		t.Fatal(err)
	}
	for _, reconnect := range []bool{false, true} {
		if reconnect {
			m, err := New(f.m.dir, f.m.wingID, f.m.name, f.m.local, nil, f.m.private, f.handler)
			if err != nil {
				t.Fatal(err)
			}
			f.m = m
		}
		c := f.connect(t)
		for i, op := range []string{"permission_list", "permission_respond", "input", "action", "launch", "read", "fleet_input"} {
			req := c.request(uint64(i+1), op)
			plain, err := auth.Decrypt(c.cipher, req.Payload)
			if err != nil {
				t.Fatal(err)
			}
			var cmd map[string]any
			if err := json.Unmarshal(plain, &cmd); err != nil {
				t.Fatal(err)
			}
			cmd["input"], cmd["scope"], cmd["grants"] = string(input), ScopeControl, grants
			req.Payload, err = auth.Encrypt(c.cipher, encode(cmd))
			if err != nil {
				t.Fatal(err)
			}
			if r := f.m.Handle(context.Background(), req); r.Error != "denied" {
				t.Fatal("permission grants enlarged device authority", op, r)
			}
		}
		if f.handler.calls != 0 || len(f.m.Devices()) != 1 || f.m.Devices()[0].Scope != ScopeInventory {
			t.Fatal("permission grant changed durable inventory scope or reached handler")
		}
		if r := f.m.Handle(context.Background(), c.request(8, "sessions")); r.Error != "" {
			t.Fatal("inventory access lost", r)
		}
		f.handler.calls = 0
	}
}
