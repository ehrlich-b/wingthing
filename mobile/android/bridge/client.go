// Package bridge is the gomobile boundary. Keystore keys never cross it.
package bridge

import (
	"bytes"
	"compress/flate"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// Wire owns carrier selection. It must never retry an established command.
type Wire interface {
	Exchange(box string, initial bool) (string, error)
}

// Signer returns ASN.1 DER ECDSA-SHA256 from a non-exportable Android key.
type Signer interface {
	Sign(text string) (string, error)
}

func fail(code string) error { return errors.New(code) }
func encode(v any) string    { b, _ := json.Marshal(v); return string(b) }
func unmarshal(s string, v any) error {
	if len(s) > phonepair.MaxPayload+4096 {
		return fail("too_large")
	}
	if json.Unmarshal([]byte(s), v) != nil {
		return fail("bad_proof")
	}
	return nil
}
func LocalAllowed(s string) bool {
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "http" || u.Port() == "" || !clean(u) || strings.Contains(s, "#") {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".ts.net") {
		return true
	}
	ip := net.ParseIP(h)
	v := ip.To4()
	return v != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || (v[0] == 100 && v[1] >= 64 && v[1] <= 127))
}
func clean(u *url.URL) bool {
	return u.Hostname() != "" && u.User == nil && !u.ForceQuery && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}
func RelayAllowed(s string) bool {
	if s == "" {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && clean(u) && !strings.Contains(s, "#")
}
func IsTailnet(s string) bool {
	u, e := url.Parse(s)
	if e != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if strings.HasSuffix(h, ".ts.net") {
		return true
	}
	v := net.ParseIP(h).To4()
	return v != nil && v[0] == 100 && v[1] >= 64 && v[1] <= 127
}
func validate(q phonepair.Invitation, expiry bool) error {
	key, e := base64.StdEncoding.DecodeString(q.Key)
	if e != nil || len(key) != 32 || q.Version != 1 || len(q.WingID) == 0 || len(q.WingID) > 100 || len(q.Name) == 0 || len(q.Name) > 240 || strings.IndexFunc(q.Name, unicode.IsControl) >= 0 || len(q.ID) < 20 || len(q.ID) > 100 || len(q.Secret) > 120 || len(strings.Split(q.Secret, " ")) != 6 || !LocalAllowed(q.Local) || !RelayAllowed(q.Relay) || len(q.Endpoints) > 8 {
		return fail("invalid_qr")
	}
	for _, s := range q.Endpoints {
		if !LocalAllowed(s) {
			return fail("invalid_qr")
		}
	}
	if expiry {
		if !q.ExpiresAt.After(time.Now()) {
			return fail("expired")
		}
		if time.Until(q.ExpiresAt) > 305*time.Second {
			return fail("invalid_qr")
		}
	}
	return nil
}
func parse(link string) (phonepair.Invitation, error) {
	var q phonepair.Invitation
	if len(link) > 4096 {
		return q, fail("invalid_qr")
	}
	u, e := url.Parse(link)
	if e != nil || u.Scheme != "wingthing" || u.Host != "pair" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.User != nil {
		return q, fail("invalid_qr")
	}
	s := u.Fragment
	var data []byte
	if strings.HasPrefix(s, "v1.") || strings.HasPrefix(s, "v1z.") {
		compressed := strings.HasPrefix(s, "v1z.")
		n := 3
		if compressed {
			n = 4
		}
		data, e = base64.RawURLEncoding.DecodeString(s[n:])
		if e != nil {
			return q, fail("invalid_qr")
		}
		if compressed {
			r := flate.NewReader(bytes.NewReader(data))
			data, e = io.ReadAll(io.LimitReader(r, 4097))
			r.Close()
			if e != nil || len(data) > 4096 {
				return q, fail("invalid_qr")
			}
		}
		var fields []json.RawMessage
		if json.Unmarshal(data, &fields) != nil || len(fields) != 9 {
			return q, fail("invalid_qr")
		}
		destinations := []any{&q.Version, &q.WingID, &q.Name, &q.Key, nil, &q.Relay, &q.ID, &q.Secret}
		for i, d := range destinations {
			if d != nil && json.Unmarshal(fields[i], d) != nil {
				return q, fail("invalid_qr")
			}
		}
		if json.Unmarshal(fields[4], &q.Local) != nil {
			var endpoints []string
			if json.Unmarshal(fields[4], &endpoints) != nil || len(endpoints) == 0 {
				return q, fail("invalid_qr")
			}
			q.Local = endpoints[0]
			q.Endpoints = endpoints[1:]
		}
		var expires int64
		if json.Unmarshal(fields[8], &expires) != nil {
			return q, fail("invalid_qr")
		}
		q.ExpiresAt = time.Unix(expires, 0)
	} else {
		data = []byte(s)
		if json.Unmarshal(data, &q) != nil {
			return q, fail("invalid_qr")
		}
	}
	return q, validate(q, true)
}

// ParseInvitation returns public metadata plus the ephemeral secret; do not persist it.
func ParseInvitation(link string) (string, error) {
	q, e := parse(link)
	if e != nil {
		return "", e
	}
	return encode(q), nil
}
func Fingerprint(key string) string    { return phonepair.Fingerprint(key) }
func DiscoveryName(wing string) string { return phonepair.DiscoveryName(wing) }

// Fallback combines the public invitation link and six words. Words alone cannot locate a wing.
func Fallback(link, words string) (string, error) {
	q, e := parse(link)
	if e != nil {
		return "", e
	}
	q.Secret = strings.TrimSpace(words)
	if e = validate(q, true); e != nil {
		return "", e
	}
	return q.Link(), nil
}

func send(w Wire, pin string, r phonepair.Request) (phonepair.Response, error) {
	var out phonepair.Response
	private, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return out, e
	}
	g, e := auth.DeriveSharedKey(private, pin, "wt-phone-carrier-v1")
	if e != nil {
		return out, fail("bad_key")
	}
	payload, e := auth.Encrypt(g, []byte(encode(r)))
	if e != nil {
		return out, e
	}
	box := phonepair.Request{Op: "phone.box", SenderPub: base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()), Payload: payload}
	s, e := w.Exchange(encode(box), r.Op == "pair.start" || r.Op == "device.hello")
	if e != nil {
		return out, e
	}
	var carrier phonepair.Response
	if e = unmarshal(s, &carrier); e != nil {
		return out, e
	}
	data, e := auth.Decrypt(g, carrier.Payload)
	if e != nil {
		return out, fail("bad_proof")
	}
	if e = unmarshal(string(data), &out); e != nil {
		return out, e
	}
	if out.Error != "" {
		return out, fail(out.Error)
	}
	return out, nil
}

type Profile struct {
	WingID     string   `json:"wing_id"`
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	Local      string   `json:"local"`
	Endpoints  []string `json:"endpoints"`
	Relay      string   `json:"relay"`
	DeviceID   string   `json:"device_id"`
	Credential string   `json:"credential"`
	Scope      string   `json:"scope"`
}
type Enrollment struct {
	mu                   sync.Mutex
	q                    phonepair.Invitation
	wire                 Wire
	cipher               cipher.AEAD
	emoji                string
	confirmed, cancelled bool
}

func Begin(link, name, publicKey string, w Wire) (*Enrollment, error) {
	q, e := parse(link)
	if e != nil {
		return nil, e
	}
	x, a, e := pake.New(true, q.Secret, phonepair.Identity(q))
	if e != nil {
		return nil, e
	}
	defer x.Close()
	alice := base64.StdEncoding.EncodeToString(a)
	reply, e := send(w, q.Key, phonepair.Request{Op: "pair.start", ID: q.ID, Message: alice})
	if e != nil {
		return nil, e
	}
	if reply.Key != q.Key {
		return nil, fail("identity_mismatch")
	}
	bob, e := base64.StdEncoding.DecodeString(reply.Message)
	if e != nil {
		return nil, fail("bad_proof")
	}
	shared, e := x.Finish(bob)
	if e != nil {
		return nil, e
	}
	g, e := phonepair.PairKey(q, shared, alice, reply.Message)
	if e != nil {
		return nil, e
	}
	en := &Enrollment{q: q, wire: w, cipher: g, emoji: phonepair.EmojiFromShared(q, shared, alice, reply.Message)}
	en.q.Secret = ""
	result, e := en.encrypted("prove", map[string]any{"name": name, "public_key": publicKey, "scope": phonepair.ScopeControl})
	if e != nil {
		return nil, e
	}
	if result.Op != "proved" || result.Emoji != en.emoji || result.Scope != phonepair.ScopeControl {
		_, _ = en.encrypted("cancel", nil)
		return nil, fail("emoji_mismatch")
	}
	return en, nil
}

// ConfirmationLabels renders the current server vocabulary without UI glyphs.
func (e *Enrollment) ConfirmationLabels() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return phonepair.EmojiLabels(e.emoji)
}

func (e *Enrollment) Emoji() string { e.mu.Lock(); defer e.mu.Unlock(); return e.emoji }

type pairResult struct {
	Op, ID, State, Emoji, Scope string
	WingID                      string `json:"wing_id"`
	Key                         string
	DeviceID                    string `json:"device_id"`
	Credential                  string
}

func (e *Enrollment) encrypted(op string, extra map[string]any) (pairResult, error) {
	var result pairResult
	if e.cipher == nil || e.cancelled {
		return result, fail("cancelled")
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["op"] = op
	extra["id"] = e.q.ID
	p, err := auth.Encrypt(e.cipher, []byte(encode(extra)))
	if err != nil {
		return result, err
	}
	r, err := send(e.wire, e.q.Key, phonepair.Request{Op: "pair." + op, ID: e.q.ID, Payload: p})
	if err != nil {
		return result, err
	}
	b, err := auth.Decrypt(e.cipher, r.Payload)
	if err != nil {
		return result, fail("bad_proof")
	}
	if err = unmarshal(string(b), &result); err != nil {
		return result, err
	}
	if result.ID != e.q.ID {
		return result, fail("bad_proof")
	}
	return result, nil
}
func (e *Enrollment) complete(r pairResult, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if r.State == "cancelled" {
		return "", fail("cancelled")
	}
	if r.Op != "paired" {
		if r.State != "confirming" {
			return "", fail("bad_proof")
		}
		return "", nil
	}
	if r.Scope != phonepair.ScopeControl {
		return "", fail("denied")
	}
	if r.WingID != e.q.WingID || r.Key != e.q.Key || r.DeviceID == "" || len(r.Credential) < 40 {
		return "", fail("identity_mismatch")
	}
	return encode(Profile{WingID: r.WingID, Name: e.q.Name, Key: r.Key, Local: e.q.Local, Endpoints: e.q.Endpoints, Relay: e.q.Relay, DeviceID: r.DeviceID, Credential: r.Credential, Scope: r.Scope}), nil
}
func (e *Enrollment) Confirm(emoji string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if emoji != e.emoji {
		_, _ = e.encrypted("cancel", nil)
		e.cancelled = true
		e.cipher = nil
		return "", fail("emoji_mismatch")
	}
	e.confirmed = true
	return e.complete(e.encrypted("confirm", map[string]any{"emoji": emoji}))
}
func (e *Enrollment) Status() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.confirmed {
		return "", fail("denied")
	}
	return e.complete(e.encrypted("status", nil))
}
func (e *Enrollment) Cancel() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.encrypted("cancel", nil)
	e.cancelled = true
	e.cipher = nil
	return err
}

type Connection struct {
	mu       sync.Mutex
	profile  Profile
	wire     Wire
	cipher   cipher.AEAD
	id       string
	sequence uint64
}

func Connect(profile string, signer Signer, w Wire) (*Connection, error) {
	var p Profile
	if unmarshal(profile, &p) != nil || p.DeviceID == "" || p.Credential == "" || p.Scope != phonepair.ScopeControl {
		return nil, fail("bad_key")
	}
	q := phonepair.Invitation{Version: 1, WingID: p.WingID, Name: p.Name, Key: p.Key, Local: p.Local, Endpoints: p.Endpoints, Relay: p.Relay, ID: strings.Repeat("x", 20), Secret: "a b c d e f"}
	if e := validate(q, false); e != nil {
		return nil, e
	}
	private, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	pub := base64.StdEncoding.EncodeToString(private.PublicKey().Bytes())
	g, e := auth.DeriveSharedKey(private, p.Key, "wt-phone-v1")
	if e != nil {
		return nil, e
	}
	r, e := send(w, p.Key, phonepair.Request{Op: "device.hello", SenderPub: pub})
	if e != nil {
		return nil, e
	}
	b, e := auth.Decrypt(g, r.Payload)
	if e != nil {
		return nil, fail("bad_proof")
	}
	var hello struct {
		ConnectionID string `json:"connection_id"`
		Challenge    string
		WingID       string `json:"wing_id"`
		Key          string
		Endpoints    []string
		Relay        string
	}
	if unmarshal(string(b), &hello) != nil {
		return nil, fail("bad_proof")
	}
	if hello.WingID != p.WingID || hello.Key != p.Key || hello.ConnectionID == "" || hello.Challenge == "" {
		return nil, fail("identity_mismatch")
	}
	signature, e := signer.Sign("wt-device-v1\n" + p.WingID + "\n" + hello.ConnectionID + "\n" + hello.Challenge + "\n" + pub)
	if e != nil {
		return nil, e
	}
	payload, e := auth.Encrypt(g, []byte(encode(map[string]any{"connection_id": hello.ConnectionID, "device_id": p.DeviceID, "credential": p.Credential, "signature": signature})))
	if e != nil {
		return nil, e
	}
	r, e = send(w, p.Key, phonepair.Request{Op: "device.auth", ConnectionID: hello.ConnectionID, Payload: payload})
	if e != nil {
		return nil, e
	}
	b, e = auth.Decrypt(g, r.Payload)
	if e != nil {
		return nil, fail("bad_proof")
	}
	var verified struct {
		ConnectionID string `json:"connection_id"`
		Sequence     *uint64
		OK           bool
	}
	if unmarshal(string(b), &verified) != nil || verified.ConnectionID != hello.ConnectionID || !verified.OK || verified.Sequence == nil || *verified.Sequence != 0 {
		return nil, fail("bad_proof")
	}
	if len(hello.Endpoints) == 0 || len(hello.Endpoints) > 9 || !RelayAllowed(hello.Relay) {
		return nil, fail("bad_proof")
	}
	for _, s := range hello.Endpoints {
		if !LocalAllowed(s) {
			return nil, fail("bad_proof")
		}
	}
	p.Local = hello.Endpoints[0]
	p.Endpoints = hello.Endpoints[1:]
	p.Relay = hello.Relay
	return &Connection{profile: p, wire: w, cipher: g, id: hello.ConnectionID}, nil
}
func (c *Connection) Profile() string { c.mu.Lock(); defer c.mu.Unlock(); return encode(c.profile) }
func (c *Connection) Command(op, session, input string) (result string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cipher == nil {
		return "", fail("revoked")
	}
	switch op {
	case "sessions", "attach", "read", "input", "stop", "detach", "close":
	default:
		return "", fail("denied")
	}
	defer func() {
		if err != nil || op == "close" {
			c.cipher = nil
			c.id = ""
		}
	}()
	c.sequence++
	payload, err := auth.Encrypt(c.cipher, []byte(encode(map[string]any{"op": op, "connection_id": c.id, "sequence": c.sequence, "session_id": session, "input": input})))
	if err != nil {
		return "", err
	}
	r, err := send(c.wire, c.profile.Key, phonepair.Request{Op: "device.command", ConnectionID: c.id, Payload: payload})
	if err != nil {
		return "", err
	}
	b, err := auth.Decrypt(c.cipher, r.Payload)
	if err != nil {
		return "", fail("bad_proof")
	}
	var response struct {
		ConnectionID string `json:"connection_id"`
		Sequence     uint64
		Result       json.RawMessage
		Error        string
	}
	if unmarshal(string(b), &response) != nil || response.ConnectionID != c.id || response.Sequence != c.sequence {
		return "", fail("bad_proof")
	}
	if response.Error != "" {
		return "", fail(response.Error)
	}
	return string(response.Result), nil
}
