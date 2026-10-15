package bridge

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"strings"
	"testing"
	"time"
)

type testWire struct {
	m      *phonepair.Manager
	tamper bool
}

func (w testWire) Exchange(box string, initial bool) (string, error) {
	var r phonepair.Request
	json.Unmarshal([]byte(box), &r)
	out := w.m.Box(context.Background(), r)
	if w.tamper {
		out.Payload = base64.StdEncoding.EncodeToString(make([]byte, 60))
	}
	return encode(out), nil
}

type testSigner struct{ key *ecdsa.PrivateKey }

func (s testSigner) Sign(text string) (string, error) {
	h := sha256.Sum256([]byte(text))
	b, e := ecdsa.SignASN1(rand.Reader, s.key, h[:])
	return base64.StdEncoding.EncodeToString(b), e
}

type handler struct{}

func (handler) Execute(_ context.Context, _ string, op, _ string, input string) (any, error) {
	return map[string]any{"ok": true, "op": op, "input": input}, nil
}
func (handler) Close(string) {}
func fixture(t *testing.T) (*phonepair.Manager, phonepair.Invitation, testSigner, string) {
	t.Helper()
	private, _ := ecdh.X25519().GenerateKey(rand.Reader)
	m, e := phonepair.New(t.TempDir(), "fixture-wing", "Mac", "http://127.0.0.1:1234", nil, private, handler{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	q, _ := m.Create()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub := base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
	return m, q, testSigner{key}, pub
}
func TestTwoSidedPairingCommandsAndRevocation(t *testing.T) {
	m, q, signer, pub := fixture(t)
	en, e := Begin(q.Link(), "Android fixture", pub, testWire{m: m})
	if e != nil {
		t.Fatal(e)
	}
	status, _ := m.Status(q.ID)
	if status.Emoji != en.Emoji() {
		t.Fatal("emoji differ")
	}
	profile, e := en.Confirm(en.Emoji())
	if e != nil || profile != "" || len(m.Devices()) != 0 {
		t.Fatal("one approval granted access", e)
	}
	if e = m.Allow(q.ID); e != nil {
		t.Fatal(e)
	}
	profile, e = en.Status()
	if e != nil || profile == "" {
		t.Fatal(e)
	}
	c, e := Connect(profile, signer, testWire{m: m})
	if e != nil {
		t.Fatal(e)
	}
	r, e := c.Command("input", "fixture-shell", "hello\r")
	if e != nil || !strings.Contains(r, "hello\\r") {
		t.Fatal(r, e)
	}
	if _, e = c.Command("configure", "", ""); e == nil {
		t.Fatal("configuration allowed")
	}
	if e = m.Revoke("Android fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Command("read", "fixture-shell", ""); e == nil || e.Error() != "revoked" {
		t.Fatal(e)
	}
}
func TestWrongCodeBurnsInvitation(t *testing.T) {
	m, q, _, pub := fixture(t)
	q.Secret = "wrong wrong wrong wrong wrong wrong"
	if _, e := Begin(q.Link(), "Android fixture", pub, testWire{m: m}); e == nil || e.Error() != "bad_proof" {
		t.Fatal(e)
	}
	status, _ := m.Status(q.ID)
	if status.State != "cancelled" || len(m.Devices()) != 0 {
		t.Fatal(status)
	}
}
func TestExpiryAndInputLimits(t *testing.T) {
	_, q, _, _ := fixture(t)
	q.ExpiresAt = time.Now().Add(-time.Second)
	if _, e := ParseInvitation(q.Link()); e == nil || e.Error() != "expired" {
		t.Fatal(e)
	}
	if _, e := ParseInvitation("wingthing://pair#" + strings.Repeat("a", 5000)); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestEmojiMismatchCancels(t *testing.T) {
	m, q, _, pub := fixture(t)
	en, e := Begin(q.Link(), "Android fixture", pub, testWire{m: m})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = en.Confirm("wrong emoji"); e == nil || e.Error() != "emoji_mismatch" {
		t.Fatal(e)
	}
	status, _ := m.Status(q.ID)
	if status.State != "cancelled" {
		t.Fatal(status)
	}
}
func TestCarrierTamperHardStops(t *testing.T) {
	m, q, _, pub := fixture(t)
	if _, e := Begin(q.Link(), "Android fixture", pub, testWire{m: m, tamper: true}); e == nil || e.Error() != "bad_proof" {
		t.Fatal(e)
	}
}
func TestOrigins(t *testing.T) {
	for _, s := range []string{"http://192.168.1.3:1234", "http://100.64.0.1:1234", "http://MAC.LOCAL:1234", "http://MAC.TS.NET:1234", "http://127.0.0.1:1234"} {
		if !LocalAllowed(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"http://example.com:1234", "http://8.8.8.8:1234", "http://user@10.0.0.1:1234", "http://10.0.0.1:1234/path", "http://10.0.0.1:1234?secret=yes"} {
		if LocalAllowed(s) {
			t.Fatal(s)
		}
	}
	if !IsTailnet("http://MAC.TS.NET:1234") || IsTailnet("http://10.0.0.1:1234") {
		t.Fatal("tailnet classification")
	}
}

type pinWire struct {
	private *ecdh.PrivateKey
	manager *phonepair.Manager
	calls   int
}

func (w *pinWire) Exchange(box string, initial bool) (string, error) {
	w.calls++
	var r phonepair.Request
	json.Unmarshal([]byte(box), &r)
	g, e := auth.DeriveSharedKey(w.private, r.SenderPub, "wt-phone-carrier-v1")
	if e != nil {
		return "", e
	}
	b, e := auth.Decrypt(g, r.Payload)
	if e != nil {
		return "", e
	}
	var inner phonepair.Request
	json.Unmarshal(b, &inner)
	reply := w.manager.Handle(context.Background(), inner)
	reply.Key = base64.StdEncoding.EncodeToString(make([]byte, 32))
	p, e := auth.Encrypt(g, []byte(encode(reply)))
	return encode(phonepair.Response{Payload: p}), e
}
func TestAuthenticatedPinMismatchStopsBeforeProof(t *testing.T) {
	private, _ := ecdh.X25519().GenerateKey(rand.Reader)
	m, e := phonepair.New(t.TempDir(), "fixture-wing", "Mac", "http://127.0.0.1:1234", nil, private, handler{})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	q, _ := m.Create()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub := base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
	wire := &pinWire{private: private, manager: m}
	if _, e = Begin(q.Link(), "Android fixture", pub, wire); e == nil || e.Error() != "identity_mismatch" {
		t.Fatal(e)
	}
	if wire.calls != 1 || len(m.Devices()) != 0 {
		t.Fatal("continued after identity mismatch")
	}
}
