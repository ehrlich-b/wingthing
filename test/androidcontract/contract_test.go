package androidcontract

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestAndroidGoldenVectorUsesActualWingTunnelCrypto(t *testing.T) {
	data, err := os.ReadFile("../../android/contract/tunnel-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector map[string]string
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	keyBytes, err := hex.DecodeString(vector["private_hex"])
	if err != nil {
		t.Fatal(err)
	}
	private, err := ecdh.X25519().NewPrivateKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()); got != vector["public_b64"] {
		t.Fatal("client public key mismatch")
	}
	gcm, err := auth.DeriveSharedKey(private, vector["peer_public_b64"], "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := auth.Decrypt(gcm, vector["encrypted_b64"])
	if err != nil || string(plain) != vector["plaintext"] {
		t.Fatalf("fixture decrypt: %s %v", plain, err)
	}
}

func TestAndroidOperationsMatchExistingTypedSurface(t *testing.T) {
	for _, name := range []string{"conversation_list", "conversation_read", "session_status", "session_read", "session_prompt", "agent_start"} {
		tool, ok := control.Lookup(name)
		if !ok || !tool.Supports(control.SurfaceHTTPMCP) {
			t.Errorf("Android operation %q lacks existing typed handler", name)
		}
	}
	for inner, want := range map[string]string{"session.control": "wing-control", "wing.info": "wing-discovery", "sessions.list": "wing-control"} {
		if got := ws.TunnelPurposeForInnerType(inner); got != want {
			t.Errorf("%s purpose %s != %s", inner, got, want)
		}
	}
}
