package main

// The client mode is a disposable phone stand-in, using the same public wire
// protocol as iOS. Credentials and signing keys stay in this process's memory.
import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

type phoneClient struct {
	q                                       phonepair.Invitation
	endpoint                                string
	signing                                 *ecdsa.PrivateKey
	pair, device                            cipher.AEAD
	emoji, deviceID, credential, connection string
	sequence                                uint64
	http                                    *http.Client
}

func marshal(v any) []byte { b, _ := json.Marshal(v); return b }
func (p *phoneClient) wire(r phonepair.Request) (phonepair.Response, error) {
	var out phonepair.Response
	key, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return out, e
	}
	g, e := auth.DeriveSharedKey(key, p.q.Key, "wt-phone-carrier-v1")
	if e != nil {
		return out, e
	}
	payload, e := auth.Encrypt(g, marshal(r))
	if e != nil {
		return out, e
	}
	box := phonepair.Request{Op: "phone.box", SenderPub: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Payload: payload}
	resp, e := p.http.Post(p.endpoint+"/phone", "application/json", bytes.NewReader(marshal(box)))
	if e != nil {
		return out, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("carrier status %d", resp.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, phonepair.MaxPayload+4096)).Decode(&out); e != nil {
		return out, e
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	b, e := auth.Decrypt(g, out.Payload)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return out, e
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}
func (p *phoneClient) enrollment(op string, fields map[string]any) (map[string]any, error) {
	fields["op"], fields["id"] = op, p.q.ID
	payload, e := auth.Encrypt(p.pair, marshal(fields))
	if e != nil {
		return nil, e
	}
	r, e := p.wire(phonepair.Request{Op: "pair." + op, ID: p.q.ID, Payload: payload})
	if e != nil {
		return nil, e
	}
	b, e := auth.Decrypt(p.pair, r.Payload)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	e = json.Unmarshal(b, &result)
	if e != nil {
		return nil, e
	}
	if result["id"] != p.q.ID {
		return nil, errors.New("wrong invitation")
	}
	if credential, ok := result["credential"].(string); ok {
		id, _ := result["device_id"].(string)
		if result["wing_id"] != p.q.WingID || result["key"] != p.q.Key || id == "" || credential == "" {
			return nil, errors.New("identity_mismatch")
		}
		if p.credential != "" && (p.credential != credential || p.deviceID != id) {
			return nil, errors.New("duplicate grant")
		}
		p.credential, p.deviceID = credential, id
		delete(result, "credential")
	}
	return result, nil
}
func (p *phoneClient) start() (any, error) {
	a, am, e := pake.New(true, p.q.Secret, phonepair.Identity(p.q))
	if e != nil {
		return nil, e
	}
	defer a.Close()
	as := base64.StdEncoding.EncodeToString(am)
	r, e := p.wire(phonepair.Request{Op: "pair.start", ID: p.q.ID, Message: as})
	if e != nil {
		return nil, e
	}
	if r.Key != p.q.Key {
		return nil, errors.New("identity_mismatch")
	}
	bm, e := base64.StdEncoding.DecodeString(r.Message)
	if e != nil {
		return nil, e
	}
	shared, e := a.Finish(bm)
	if e != nil {
		return nil, e
	}
	p.pair, e = phonepair.PairKey(p.q, shared, as, r.Message)
	if e != nil {
		return nil, e
	}
	p.emoji = phonepair.EmojiFromShared(p.q, shared, as, r.Message)
	return map[string]any{"ok": true}, nil
}
func (p *phoneClient) connect() (any, error) {
	key, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	pub := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	p.device, e = auth.DeriveSharedKey(key, p.q.Key, "wt-phone-v1")
	if e != nil {
		return nil, e
	}
	r, e := p.wire(phonepair.Request{Op: "device.hello", SenderPub: pub})
	if e != nil {
		return nil, e
	}
	b, e := auth.Decrypt(p.device, r.Payload)
	if e != nil {
		return nil, e
	}
	var hello struct {
		Connection string `json:"connection_id"`
		Challenge  string `json:"challenge"`
		WingID     string `json:"wing_id"`
		Key        string `json:"key"`
	}
	if e = json.Unmarshal(b, &hello); e != nil {
		return nil, e
	}
	if hello.WingID != p.q.WingID || hello.Key != p.q.Key {
		return nil, errors.New("identity_mismatch")
	}
	p.connection = hello.Connection
	p.sequence = 0
	digest := sha256.Sum256([]byte("wt-device-v1\n" + p.q.WingID + "\n" + p.connection + "\n" + hello.Challenge + "\n" + pub))
	sig, e := ecdsa.SignASN1(rand.Reader, p.signing, digest[:])
	if e != nil {
		return nil, e
	}
	payload, e := auth.Encrypt(p.device, marshal(map[string]any{"connection_id": p.connection, "device_id": p.deviceID, "credential": p.credential, "signature": base64.StdEncoding.EncodeToString(sig)}))
	if e != nil {
		return nil, e
	}
	r, e = p.wire(phonepair.Request{Op: "device.auth", ConnectionID: p.connection, Payload: payload})
	if e != nil {
		return nil, e
	}
	return p.deviceReply(r, 0)
}
func (p *phoneClient) deviceReply(r phonepair.Response, sequence uint64) (map[string]any, error) {
	b, e := auth.Decrypt(p.device, r.Payload)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if e = json.Unmarshal(b, &result); e != nil {
		return nil, e
	}
	if result["connection_id"] != p.connection || result["sequence"] != float64(sequence) {
		return nil, errors.New("response mismatch")
	}
	if code, ok := result["error"].(string); ok {
		return nil, errors.New(code)
	}
	return result, nil
}
func (p *phoneClient) rpc(op, id, input string) (any, error) {
	p.sequence++
	payload, e := auth.Encrypt(p.device, marshal(map[string]any{"op": op, "connection_id": p.connection, "sequence": p.sequence, "session_id": id, "input": input}))
	if e != nil {
		return nil, e
	}
	r, e := p.wire(phonepair.Request{Op: "device.command", ConnectionID: p.connection, Payload: payload})
	if e != nil {
		return nil, e
	}
	return p.deviceReply(r, p.sequence)
}
func runClient() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), phonepair.MaxPayload)
	p := &phoneClient{http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	defer p.http.CloseIdleConnections()
	for scanner.Scan() {
		var cmd struct {
			Op, Endpoint, Name, SessionID, Input string
			Invitation                           phonepair.Invitation
		}
		var result any
		var e error
		if e = json.Unmarshal(scanner.Bytes(), &cmd); e == nil {
			switch cmd.Op {
			case "init":
				*p = phoneClient{q: cmd.Invitation, endpoint: cmd.Endpoint, http: p.http}
				p.signing, e = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				result = map[string]bool{"ok": e == nil}
			case "start":
				result, e = p.start()
			case "prove":
				result, e = p.enrollment("prove", map[string]any{"name": cmd.Name, "public_key": base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), p.signing.X, p.signing.Y))})
			case "confirm":
				result, e = p.enrollment("confirm", map[string]any{"emoji": p.emoji})
			case "status", "cancel":
				result, e = p.enrollment(cmd.Op, map[string]any{})
			case "connect":
				result, e = p.connect()
			default:
				result, e = p.rpc(cmd.Op, cmd.SessionID, cmd.Input)
			}
		}
		if e != nil {
			result = map[string]string{"error": e.Error()}
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
}
