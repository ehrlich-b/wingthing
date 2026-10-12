//go:build cgo

package main

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// These supplemental tests exercise the actual CLI, main wing and native PAKE
// with a Go peer. The original phone cards still require physical iOS evidence.
func TestPhoneCLIPrerequisitesOnMainWing(t *testing.T) {
	f, binary := storyboardFixture(t)
	coldCleanup(t, f, binary)
	out, err := storyboardCommand(t, f, binary, "wing", "start", "--local-only", "--paths", f.work)
	if err != nil {
		t.Fatalf("start main wing: %v\n%s", err, out)
	}
	pid, err := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if err != nil || len(bytes.TrimSpace(pid)) == 0 {
		t.Fatal("main wing has no PID receipt", err)
	}
	out, err = storyboardCommand(t, f, binary, "devices")
	if err != nil {
		t.Fatal(err, out)
	}
	storyboardContains(t, out, "NAME", "PAIRED AT", "LAST SEEN")
	first := northstarStartPTY(t, f, binary, 160, 60, "pair", "--show-link")
	first.expect("live invitation on main wing", 0, "Pair a phone", "5:00", "Waiting for scan", "Local network only", "wingthing://pair#")
	q1 := phoneCLIInvitation(t, first)
	first.send("\x03")
	coldPTYExit(t, first, true)
	if got := phoneCLICall(t, q1, phonepair.Request{Op: "pair.start", ID: q1.ID, Message: "invalid"}); got.Error != "cancelled" {
		t.Fatalf("terminal exit left invitation live: %+v", got)
	}
	second := northstarStartPTY(t, f, binary, 160, 60, "pair", "--show-link")
	second.expect("fresh invitation", 0, "Pair a phone", "Waiting for scan", "wingthing://pair#")
	q := phoneCLIInvitation(t, second)
	if q.ID == q1.ID || q.Secret == q1.Secret || q.Key != q1.Key || q.WingID != q1.WingID {
		t.Fatal("fresh invitation replaced identity or reused secret")
	}
	if q.ExpiresAt.Sub(time.Now()) > phonepair.Lifetime || q.ExpiresAt.Sub(time.Now()) < phonepair.Lifetime-10*time.Second {
		t.Fatal("invitation deadline is not five minutes")
	}
	exchange, message, err := pake.New(true, q.Secret, phonepair.Identity(q))
	if err != nil {
		t.Fatal(err)
	}
	defer exchange.Close()
	alice := base64.StdEncoding.EncodeToString(message)
	response := phoneCLICall(t, q, phonepair.Request{Op: "pair.start", ID: q.ID, Message: alice})
	if response.Error != "" || response.Key != q.Key {
		t.Fatalf("scanned identity changed: %+v", response)
	}
	bob, err := base64.StdEncoding.DecodeString(response.Message)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := exchange.Finish(bob)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := phonepair.PairKey(q, shared, alice, response.Message)
	if err != nil {
		t.Fatal(err)
	}
	emoji := phonepair.EmojiFromShared(q, shared, alice, response.Message)
	signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	phoneCLIEncrypted(t, q, pair, "prove", map[string]any{"name": "CLI test phone", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), signing.X, signing.Y))})
	second.expect("terminal confirms native peer", 0, emoji, "CLI test phone wants to pair", "Enter to allow")
	phoneCLIEncrypted(t, q, pair, "confirm", map[string]any{"emoji": emoji})
	out, err = storyboardCommand(t, f, binary, "devices")
	if err != nil || strings.Contains(out, "CLI test phone") {
		t.Fatal("phone approval alone issued a grant", err, out)
	}
	second.expect("both sides confirmed", second.send("\r"), "Paired CLI test phone")
	coldPTYExit(t, second, false)
	result := phoneCLIEncrypted(t, q, pair, "status", nil)
	credential, _ := result["credential"].(string)
	if credential == "" || result["wing_id"] != q.WingID || result["key"] != q.Key {
		t.Fatalf("bad grant: %v", result)
	}
	recovery := phoneCLIEncrypted(t, q, pair, "confirm", map[string]any{"emoji": emoji})
	if recovery["credential"] != credential || recovery["device_id"] != result["device_id"] {
		t.Fatal("recovery minted another grant")
	}
	registry, err := os.ReadFile(filepath.Join(f.local, "phone-devices.json"))
	if err != nil || bytes.Contains(registry, []byte(credential)) || bytes.Contains(registry, []byte(q.Secret)) {
		t.Fatal("raw credential or invitation persisted", err)
	}
	out, err = storyboardCommand(t, f, binary, "devices")
	if err != nil {
		t.Fatal(err, out)
	}
	storyboardContains(t, out, "CLI test phone", result["device_id"].(string))
	out, err = storyboardCommand(t, f, binary, "devices", "revoke", "CLI test phone")
	if err != nil {
		t.Fatal(err, out)
	}
	storyboardContains(t, out, "Revoked CLI test phone", "Live access closed")
	out, err = storyboardCommand(t, f, binary, "devices", "revoke", "CLI test phone")
	if err == nil {
		t.Fatal("second revoke pretended access was active")
	}
	storyboardContains(t, out, "device", "not paired")
	afterPID, err := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if err != nil || !bytes.Equal(pid, afterPID) {
		t.Fatal("pairing started or replaced the main wing")
	}
	entries, err := os.ReadDir(filepath.Join(f.local, "eggs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pairing launched work: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(f.remote, "phone-control.json")); !os.IsNotExist(err) {
		t.Fatal("pairing affected another wing")
	}
}

func TestPhoneCLIRequiresRunningMainWing(t *testing.T) {
	f, binary := storyboardFixture(t)
	t.Cleanup(func() { storyboardNoWork(t, f) })
	for _, args := range [][]string{{"pair"}, {"devices"}, {"devices", "revoke", "unpaired phone"}} {
		out, err := storyboardCommand(t, f, binary, args...)
		if err == nil || strings.Contains(out, "unknown command") {
			t.Fatalf("missing wing was not refused: %v %s", err, out)
		}
		storyboardContains(t, out, "main wing is not ready for pairing", "wt wing start")
	}
}

func phoneCLIInvitation(t *testing.T, h *northstarPTY) phonepair.Invitation {
	t.Helper()
	h.mu.Lock()
	link := regexp.MustCompile(`wingthing://pair#v1z\.[A-Za-z0-9_-]+`).FindString(h.transcript.String())
	h.mu.Unlock()
	packed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "wingthing://pair#v1z."))
	if err != nil {
		t.Fatal(err)
	}
	reader := flate.NewReader(bytes.NewReader(packed))
	defer reader.Close()
	var fields []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(reader, 4097)).Decode(&fields); err != nil || len(fields) != 9 {
		t.Fatalf("invalid live QR: %v", err)
	}
	var q phonepair.Invitation
	for i, target := range map[int]any{0: &q.Version, 1: &q.WingID, 2: &q.Name, 3: &q.Key, 5: &q.Relay, 6: &q.ID, 7: &q.Secret} {
		if err := json.Unmarshal(fields[i], target); err != nil {
			t.Fatal(err)
		}
	}
	if json.Unmarshal(fields[4], &q.Local) != nil {
		var endpoints []string
		if err := json.Unmarshal(fields[4], &endpoints); err != nil || len(endpoints) == 0 {
			t.Fatal("invalid QR endpoints")
		}
		q.Local, q.Endpoints = endpoints[0], endpoints[1:]
	}
	var expiry int64
	if err := json.Unmarshal(fields[8], &expiry); err != nil {
		t.Fatal(err)
	}
	q.ExpiresAt = time.Unix(expiry, 0)
	return q
}

func phoneCLICall(t *testing.T, q phonepair.Invitation, inner phonepair.Request) phonepair.Response {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := auth.DeriveSharedKey(private, q.Key, "wt-phone-carrier-v1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := auth.Encrypt(carrier, data)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(phonepair.Request{Op: "phone.box", SenderPub: base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()), Payload: payload})
	req, err := http.NewRequestWithContext(context.Background(), "POST", q.Local+"/phone", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var response phonepair.Response
	if err := json.NewDecoder(io.LimitReader(r.Body, phonepair.MaxPayload+4096)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	plaintext, err := auth.Decrypt(carrier, response.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plaintext, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func phoneCLIEncrypted(t *testing.T, q phonepair.Invitation, pair cipher.AEAD, op string, fields map[string]any) map[string]any {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["op"], fields["id"] = op, q.ID
	data, _ := json.Marshal(fields)
	payload, err := auth.Encrypt(pair, data)
	if err != nil {
		t.Fatal(err)
	}
	response := phoneCLICall(t, q, phonepair.Request{Op: "pair." + op, ID: q.ID, Payload: payload})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	data, err = auth.Decrypt(pair, response.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
