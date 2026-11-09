//go:build cgo

package phonepair

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

type fixtureHandler struct {
	calls  int
	closed []string
}

func (h *fixtureHandler) Execute(context.Context, string, string, string, string) (any, error) {
	h.calls++
	return map[string]bool{"ok": true}, nil
}
func (h *fixtureHandler) Close(id string) { h.closed = append(h.closed, id) }

type phoneFixture struct {
	m                  *Manager
	q                  Invitation
	pair               cipher.AEAD
	emoji              string
	signing            *ecdsa.PrivateKey
	handler            *fixtureHandler
	device, credential string
}

func fixture(t *testing.T) *phoneFixture {
	t.Helper()
	private, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	handler := &fixtureHandler{}
	m, e := New(testsocket.Dir(t), "main-wing", "my-mac", "http://192.168.1.40:6078", nil, private, handler)
	if e != nil {
		t.Fatal(e)
	}
	q, e := m.Create()
	if e != nil {
		t.Fatal(e)
	}
	signing, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &phoneFixture{m: m, q: q, signing: signing, handler: handler}
}
func (f *phoneFixture) start(t *testing.T, secret string) {
	f.startOnTransport(t, secret, false)
}
func (f *phoneFixture) startOnTransport(t *testing.T, secret string, inventoryOnly bool) {
	t.Helper()
	a, am, e := pake.New(true, secret, Identity(f.q))
	if e != nil {
		t.Fatal(e)
	}
	as := base64.StdEncoding.EncodeToString(am)
	response := f.m.Handle(context.Background(), Request{Op: "pair.start", ID: f.q.ID, Message: as, InventoryOnly: inventoryOnly})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	bm, _ := base64.StdEncoding.DecodeString(response.Message)
	shared, e := a.Finish(bm)
	if e != nil {
		t.Fatal(e)
	}
	f.pair, e = PairKey(f.q, shared, as, response.Message)
	if e != nil {
		t.Fatal(e)
	}
	f.emoji = EmojiFromShared(f.q, shared, as, response.Message)
}
func (f *phoneFixture) send(t *testing.T, op string, fields map[string]any) Response {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["op"] = op
	fields["id"] = f.q.ID
	payload, e := auth.Encrypt(f.pair, encode(fields))
	if e != nil {
		t.Fatal(e)
	}
	return f.m.Handle(context.Background(), Request{Op: "pair." + op, ID: f.q.ID, Payload: payload})
}
func (f *phoneFixture) prove(t *testing.T) {
	t.Helper()
	response := f.send(t, "prove", map[string]any{"name": "My iPhone", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), f.signing.X, f.signing.Y))})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	b, e := auth.Decrypt(f.pair, response.Payload)
	if e != nil {
		t.Fatal(e)
	}
	var reply map[string]any
	_ = json.Unmarshal(b, &reply)
	if reply["emoji"] != f.emoji {
		t.Fatal("emoji not equal")
	}
}
func (f *phoneFixture) pairPhone(t *testing.T) {
	t.Helper()
	f.start(t, f.q.Secret)
	f.prove(t)
	if e := f.m.Allow(f.q.ID); e != nil {
		t.Fatal(e)
	}
	r := f.send(t, "confirm", map[string]any{"emoji": f.emoji})
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	b, e := auth.Decrypt(f.pair, r.Payload)
	if e != nil {
		t.Fatal(e)
	}
	var reply struct {
		DeviceID   string `json:"device_id"`
		Credential string `json:"credential"`
	}
	_ = json.Unmarshal(b, &reply)
	f.device, f.credential = reply.DeviceID, reply.Credential
	if f.device == "" {
		t.Fatal("missing device")
	}
}
func TestTwoSidedConfirmationAndCredentialRecovery(t *testing.T) {
	for _, phoneFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "phone-first", false: "terminal-first"}[phoneFirst], func(t *testing.T) {
			f := fixture(t)
			f.start(t, f.q.Secret)
			f.prove(t)
			if phoneFirst {
				r := f.send(t, "confirm", map[string]any{"emoji": f.emoji})
				if r.Error != "" {
					t.Fatal(r.Error)
				}
			} else {
				if e := f.m.Allow(f.q.ID); e != nil {
					t.Fatal(e)
				}
			}
			if len(f.m.Devices()) != 0 {
				t.Fatal("single approval granted access")
			}
			if phoneFirst {
				if e := f.m.Allow(f.q.ID); e != nil {
					t.Fatal(e)
				}
			} else {
				r := f.send(t, "confirm", map[string]any{"emoji": f.emoji})
				if r.Error != "" {
					t.Fatal(r.Error)
				}
			}
			if len(f.m.Devices()) != 1 {
				t.Fatal("both approvals didn't grant exactly one device")
			}
			r1, r2 := f.send(t, "status", nil), f.send(t, "confirm", map[string]any{"emoji": f.emoji})
			b1, _ := auth.Decrypt(f.pair, r1.Payload)
			b2, _ := auth.Decrypt(f.pair, r2.Payload)
			if string(b1) != string(b2) {
				t.Fatal("recovery minted a different credential")
			}
			b, _ := os.ReadFile(filepath.Join(f.m.dir, "phone-devices.json"))
			var reply struct {
				Credential string `json:"credential"`
			}
			_ = json.Unmarshal(b1, &reply)
			if reply.Credential == "" || contains(b, reply.Credential) {
				t.Fatal("credential stored in plaintext")
			}
			info, _ := os.Stat(filepath.Join(f.m.dir, "phone-devices.json"))
			if info.Mode().Perm() != 0600 {
				t.Fatal("insecure registry permissions")
			}
		})
	}
}
func contains(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
func TestExpiredUsedCancelledEmojiAndWrongPassword(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		f := fixture(t)
		f.m.now = func() time.Time { return f.q.ExpiresAt }
		r := f.m.Handle(context.Background(), Request{Op: "pair.start", ID: f.q.ID})
		if r.Error != "expired" {
			t.Fatal(r)
		}
		if e := f.m.Allow(f.q.ID); e == nil {
			t.Fatal("expired approval")
		}
		fresh, e := f.m.Create()
		if e != nil || fresh.Secret == f.q.Secret || fresh.ID == f.q.ID {
			t.Fatal("fresh invitation reused secret")
		}
	})
	t.Run("used", func(t *testing.T) {
		f := fixture(t)
		f.start(t, f.q.Secret)
		r := f.m.Handle(context.Background(), Request{Op: "pair.start", ID: f.q.ID})
		if r.Error != "used" {
			t.Fatal(r)
		}
	})
	for _, op := range []string{"terminal-cancel", "phone-cancel", "emoji-mismatch", "wrong-password"} {
		t.Run(op, func(t *testing.T) {
			f := fixture(t)
			secret := f.q.Secret
			if op == "wrong-password" {
				secret = "wrong six word code for test"
			}
			f.start(t, secret)
			if op == "wrong-password" {
				r := f.send(t, "prove", map[string]any{})
				if r.Error != "bad_proof" {
					t.Fatal(r)
				}
			} else {
				f.prove(t)
				switch op {
				case "terminal-cancel":
					f.m.Cancel(f.q.ID)
				case "phone-cancel":
					f.send(t, "cancel", nil)
				case "emoji-mismatch":
					r := f.send(t, "confirm", map[string]any{"emoji": "🌲 🐙 🚲 🌙"})
					if r.Error != "emoji_mismatch" {
						t.Fatal(r)
					}
				}
			}
			if e := f.m.Allow(f.q.ID); e == nil {
				t.Fatal("cancelled pairing allowed")
			}
			if len(f.m.Devices()) != 0 {
				t.Fatal("failed pairing granted access")
			}
		})
	}
}

type connectedFixture struct {
	id     string
	cipher cipher.AEAD
}

func (f *phoneFixture) connect(t *testing.T) *connectedFixture {
	t.Helper()
	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	g, e := auth.DeriveSharedKey(key, f.q.Key, "wt-phone-v1")
	if e != nil {
		t.Fatal(e)
	}
	r := f.m.Handle(context.Background(), Request{Op: "device.hello", SenderPub: pub})
	b, e := auth.Decrypt(g, r.Payload)
	if e != nil {
		t.Fatal(e)
	}
	var hello struct {
		ConnectionID string `json:"connection_id"`
		Challenge    string `json:"challenge"`
	}
	_ = json.Unmarshal(b, &hello)
	digest := sha256.Sum256([]byte("wt-device-v1\n" + f.q.WingID + "\n" + hello.ConnectionID + "\n" + hello.Challenge + "\n" + pub))
	sig, _ := ecdsa.SignASN1(rand.Reader, f.signing, digest[:])
	payload, _ := auth.Encrypt(g, encode(command{ConnectionID: hello.ConnectionID, DeviceID: f.device, Credential: f.credential, Signature: base64.StdEncoding.EncodeToString(sig)}))
	r = f.m.Handle(context.Background(), Request{Op: "device.auth", ConnectionID: hello.ConnectionID, Payload: payload})
	if r.Error != "" {
		t.Fatal(r)
	}
	return &connectedFixture{hello.ConnectionID, g}
}
func (c *connectedFixture) request(seq uint64, op string) Request {
	p, _ := auth.Encrypt(c.cipher, encode(command{ConnectionID: c.id, Sequence: seq, Op: op}))
	return Request{Op: "device.command", ConnectionID: c.id, Payload: p}
}
func TestDeviceScopeReplayRevocationAndLiveClosure(t *testing.T) {
	f := fixture(t)
	f.pairPhone(t)
	c := f.connect(t)
	ctx := context.Background()
	r := f.m.Handle(ctx, c.request(1, "sessions"))
	if r.Error != "" || f.handler.calls != 1 {
		t.Fatal(r)
	}
	r = f.m.Handle(ctx, c.request(1, "input"))
	if r.Error != "replay" || f.handler.calls != 1 {
		t.Fatal("replayed input admitted")
	}
	r = f.m.Handle(ctx, c.request(2, "config"))
	if r.Error != "denied" || f.handler.calls != 1 {
		t.Fatal("configuration allowed")
	}
	r = f.m.Handle(ctx, c.request(3, "attach|read"))
	if r.Error != "denied" || f.handler.calls != 1 {
		t.Fatal("combined operation bypassed exact session scope")
	}
	if e := f.m.Revoke("My iPhone"); e != nil {
		t.Fatal(e)
	}
	if len(f.handler.closed) != 1 || f.handler.closed[0] != c.id {
		t.Fatal("live connection survived revoke")
	}
	r = f.m.Handle(ctx, c.request(3, "input"))
	if r.Error != "revoked" {
		t.Fatal(r)
	}
	loaded, e := New(f.m.dir, f.q.WingID, f.q.Name, f.q.Local, nil, f.m.private, nil)
	if e != nil || len(loaded.Devices()) != 0 {
		t.Fatal("revocation didn't persist")
	}
	other := fixture(t)
	if r = other.m.Handle(ctx, c.request(3, "sessions")); r.Error != "revoked" {
		t.Fatal("credential reached other wing")
	}
}
func TestConcurrentReplayAndExpiredConnection(t *testing.T) {
	f := fixture(t)
	f.pairPhone(t)
	c := f.connect(t)
	req := c.request(1, "input")
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() { f.m.Handle(context.Background(), req) })
	}
	group.Wait()
	if f.handler.calls != 1 {
		t.Fatal("concurrent replay ran input more than once")
	}
	f.m.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	r := f.m.Handle(context.Background(), c.request(2, "input"))
	if r.Error != "revoked" || len(f.handler.closed) != 1 {
		t.Fatal("idle connection remained usable")
	}
}
func TestTranscriptPinAndOpaqueCarrier(t *testing.T) {
	f := fixture(t)
	f.start(t, f.q.Secret)
	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	g, e := auth.DeriveSharedKey(key, f.q.Key, "wt-phone-carrier-v1")
	if e != nil {
		t.Fatal(e)
	}
	payload, _ := auth.Encrypt(g, encode(Request{Op: "pair.status", ID: f.q.ID, Payload: "not valid"}))
	box := f.m.Box(context.Background(), Request{Op: "phone.box", SenderPub: pub, Payload: payload})
	if box.Error != "" || box.Payload == "" || contains(encode(box), "bad_proof") {
		t.Fatal("carrier leaked error")
	}
	b, e := auth.Decrypt(g, box.Payload)
	if e != nil || !contains(b, "bad_proof") {
		t.Fatal("carrier didn't authenticate error")
	}
	other := fixture(t)
	if other.m.Box(context.Background(), Request{Op: "phone.box", SenderPub: pub, Payload: payload}).Error != "bad_proof" {
		t.Fatal("box accepted by wrong wing")
	}
}

func TestCancelAfterBothApprovalsRevokesUnclaimedGrant(t *testing.T) {
	f := fixture(t)
	f.pairPhone(t)
	c := f.connect(t)
	if r := f.send(t, "cancel", nil); r.Error != "" {
		t.Fatal(r.Error)
	}
	if len(f.m.Devices()) != 0 || len(f.handler.closed) != 1 {
		t.Fatal("cancel after approval left phone access")
	}
	if r := f.m.Handle(context.Background(), c.request(1, "input")); r.Error != "revoked" {
		t.Fatal(r)
	}
}

type blockedHandler struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (h *blockedHandler) Execute(ctx context.Context, _, _, _, _ string) (any, error) {
	close(h.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *blockedHandler) Close(string) { h.once.Do(func() { close(h.closed) }) }
func TestRevocationInterruptsInFlightSessionWithoutWaiting(t *testing.T) {
	f := fixture(t)
	f.pairPhone(t)
	c := f.connect(t)
	h := &blockedHandler{entered: make(chan struct{}), closed: make(chan struct{})}
	f.m.handler = h
	finished := make(chan Response, 1)
	go func() { finished <- f.m.Handle(context.Background(), c.request(1, "read")) }()
	<-h.entered
	if e := f.m.Revoke("My iPhone"); e != nil {
		t.Fatal(e)
	}
	select {
	case reply := <-finished:
		if reply.Error != "revoked" {
			t.Fatal("revoked reply delivered content", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation blocked behind session work")
	}
	select {
	case <-h.closed:
	default:
		t.Fatal("revocation didn't close live access")
	}
}

func TestPhoneFleetOperationsRetainEncryptedReplayAndRevocationGates(t *testing.T) {
	f := fixture(t)
	f.pairPhone(t)
	c := f.connect(t)
	for i, op := range []string{"inventory", "search", "action", "launch", "fleet_attach", "fleet_read", "fleet_input", "fleet_detach"} {
		sequence := uint64(i + 1)
		response := f.m.Handle(t.Context(), c.request(sequence, op))
		if response.Error != "" || f.handler.calls != i+1 {
			t.Fatalf("qualified operation not dispatched: %s %+v", op, response)
		}
		replay := f.m.Handle(t.Context(), c.request(sequence, op))
		if replay.Error != "replay" || f.handler.calls != i+1 {
			t.Fatalf("qualified operation replayed: %s %+v", op, replay)
		}
	}
	if e := f.m.Revoke("My iPhone"); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"inventory", "search", "action", "launch", "fleet_attach", "fleet_read", "fleet_input", "fleet_detach"} {
		if response := f.m.Handle(t.Context(), c.request(9, op)); response.Error != "revoked" {
			t.Fatalf("revoked device used %s: %+v", op, response)
		}
	}
	if f.handler.calls != 8 {
		t.Fatal("revocation dispatched fleet content")
	}
}
