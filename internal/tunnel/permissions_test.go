package tunnel

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestPermissionTunnelRespondRefused(t *testing.T) {
	wing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sender := base64.StdEncoding.EncodeToString(browser.PublicKey().Bytes())
	gcm, err := auth.DeriveSharedKey(wing, sender, "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	keys := []config.AllowKey{}
	refs := References{WingCfg: &config.WingConfig{}, WingCfgMu: &sync.Mutex{}, AllowedKeys: &keys}
	for _, operation := range []string{"permission_respond", "permission_list"} {
		plain, _ := json.Marshal(map[string]any{"type": "session.control", "operation": operation, "arguments": map[string]string{"decision": "allow"}})
		payload, err := auth.Encrypt(gcm, plain)
		if err != nil {
			t.Fatal(err)
		}
		var response ws.TunnelResponse
		HandleTunnelRequest(refs, context.Background(), &config.Config{Dir: t.TempDir()}, ws.TunnelRequest{SenderPub: sender, SenderUserID: "owner", SenderOrgRole: "owner", RequestID: "permission", Payload: payload}, func(v any) error { response = v.(ws.TunnelResponse); return nil }, nil, nil, auth.PasskeyPolicy{}, wing, t.TempDir(), false, false, nil, nil, nil)
		data, err := auth.Decrypt(gcm, response.Payload)
		if err != nil || !strings.Contains(string(data), "permission operations are local only") {
			t.Fatalf("tunnel permission admitted: %s %v", data, err)
		}
	}
}
