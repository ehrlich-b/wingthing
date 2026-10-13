package main

import (
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// Only fixture-created state is accepted by callers. Credentials remain in
// memory and never appear in logs, captured commands or screenshot artifacts.
type builtFleetPhone struct {
	t           *testing.T
	http        *http.Client
	origin, pub string
	carrier     cipher.AEAD
	session     cipher.AEAD
	id          string
	sequence    uint64
}

func (p *builtFleetPhone) post(path string, value, result any, token string) {
	p.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		p.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(p.t.Context(), "POST", p.origin+path, bytes.NewReader(data))
	if err != nil {
		p.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := p.http.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		p.t.Fatalf("fixture phone HTTP status=%d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		p.t.Fatal(err)
	}
}
func (p *builtFleetPhone) wire(request phonepair.Request) phonepair.Response {
	p.t.Helper()
	raw, _ := json.Marshal(request)
	encrypted, err := auth.Encrypt(p.carrier, raw)
	if err != nil {
		p.t.Fatal(err)
	}
	var outer phonepair.Response
	p.post("/phone", phonepair.Request{Op: "phone.box", SenderPub: p.pub, Payload: encrypted}, &outer, "")
	data, err := auth.Decrypt(p.carrier, outer.Payload)
	if err != nil {
		p.t.Fatal("fixture carrier decryption failed")
	}
	var response phonepair.Response
	if err := json.Unmarshal(data, &response); err != nil {
		p.t.Fatal(err)
	}
	if response.Error != "" {
		p.t.Fatalf("fixture protocol error=%s", response.Error)
	}
	return response
}
func (p *builtFleetPhone) encrypted(op string, fields map[string]any, key cipher.AEAD) map[string]any {
	p.t.Helper()
	data, _ := json.Marshal(fields)
	payload, err := auth.Encrypt(key, data)
	if err != nil {
		p.t.Fatal(err)
	}
	response := p.wire(phonepair.Request{Op: op, ID: stringField(fields, "id"), ConnectionID: stringField(fields, "connection_id"), Payload: payload})
	decoded, err := auth.Decrypt(key, response.Payload)
	if err != nil {
		p.t.Fatal("fixture encrypted response failed")
	}
	result := map[string]any{}
	if err := json.Unmarshal(decoded, &result); err != nil {
		p.t.Fatal(err)
	}
	return result
}
func stringField(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}
func pairBuiltFleetPhone(t *testing.T, state string) *builtFleetPhone {
	t.Helper()
	if !pake.Available {
		t.Skip("PAKE requires CGO")
	}
	data, err := os.ReadFile(filepath.Join(state, "phone-control.json"))
	if err != nil {
		t.Fatal(err)
	}
	var control phonepair.Control
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	p := &builtFleetPhone{t: t, http: client, origin: fmt.Sprintf("http://127.0.0.1:%d", control.Port)}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.pub = base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	p.carrier, err = auth.DeriveSharedKey(key, control.Key, "wt-phone-carrier-v1")
	if err != nil {
		t.Fatal(err)
	}
	var q phonepair.Invitation
	p.post("/admin", map[string]string{"Op": "create"}, &q, control.Token)
	exchange, first, err := pake.New(true, q.Secret, phonepair.Identity(q))
	if err != nil {
		t.Fatal(err)
	}
	alice := base64.StdEncoding.EncodeToString(first)
	response := p.wire(phonepair.Request{Op: "pair.start", ID: q.ID, Message: alice})
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
	signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.encrypted("pair.prove", map[string]any{"op": "prove", "id": q.ID, "name": "fixture-phone", "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), signing.X, signing.Y))}, pair)
	var allowed map[string]any
	p.post("/admin", map[string]string{"Op": "allow", "ID": q.ID}, &allowed, control.Token)
	paired := p.encrypted("pair.confirm", map[string]any{"op": "confirm", "id": q.ID, "emoji": phonepair.EmojiFromShared(q, shared, alice, response.Message)}, pair)
	p.session, err = auth.DeriveSharedKey(key, control.Key, "wt-phone-v1")
	if err != nil {
		t.Fatal(err)
	}
	hello := p.wire(phonepair.Request{Op: "device.hello", SenderPub: p.pub})
	raw, err := auth.Decrypt(p.session, hello.Payload)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	p.id = stringField(fields, "connection_id")
	digest := sha256.Sum256([]byte("wt-device-v1\n" + q.WingID + "\n" + p.id + "\n" + stringField(fields, "challenge") + "\n" + p.pub))
	signature, err := ecdsa.SignASN1(rand.Reader, signing, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	authenticated := p.encrypted("device.auth", map[string]any{"connection_id": p.id, "device_id": paired["device_id"], "credential": paired["credential"], "signature": base64.StdEncoding.EncodeToString(signature)}, p.session)
	if authenticated["ok"] != true {
		t.Fatal("fixture device authentication failed")
	}
	return p
}
func (p *builtFleetPhone) command(op string, input any) map[string]any {
	p.t.Helper()
	p.sequence++
	raw, _ := json.Marshal(input)
	return p.encrypted("device.command", map[string]any{"connection_id": p.id, "sequence": p.sequence, "op": op, "input": string(raw)}, p.session)
}
