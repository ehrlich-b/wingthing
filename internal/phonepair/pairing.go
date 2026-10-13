// Package phonepair implements wing-scoped, two-sided phone enrollment and control.
package phonepair

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/pake"
)

const Lifetime = 5 * time.Minute
const MaxPayload = 256 << 10

type Invitation struct {
	Version   int       `json:"v"`
	WingID    string    `json:"wing_id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	Local     string    `json:"local"`
	Endpoints []string  `json:"endpoints,omitempty"`
	Relay     string    `json:"relay"`
	ID        string    `json:"id"`
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (q Invitation) Link() string {
	var direct any = q.Local
	if len(q.Endpoints) > 0 {
		direct = append([]string{q.Local}, q.Endpoints...)
	}
	data := encode([]any{q.Version, q.WingID, q.Name, q.Key, direct, q.Relay, q.ID, q.Secret, q.ExpiresAt.Unix()})
	var packed bytes.Buffer
	writer, _ := flate.NewWriter(&packed, flate.BestCompression)
	_, _ = writer.Write(data)
	_ = writer.Close()
	return "wingthing://pair#v1z." + base64.RawURLEncoding.EncodeToString(packed.Bytes())
}

type Request struct {
	Op           string `json:"op"`
	ID           string `json:"id,omitempty"`
	Message      string `json:"message,omitempty"`
	SenderPub    string `json:"sender_pub,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Payload      string `json:"payload,omitempty"`
}
type Response struct {
	Error        string    `json:"error,omitempty"`
	Message      string    `json:"message,omitempty"`
	Key          string    `json:"key,omitempty"`
	Name         string    `json:"name,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	Payload      string    `json:"payload,omitempty"`
}
type Device struct {
	ID             string    `json:"device_id"`
	Name           string    `json:"name"`
	PublicKey      string    `json:"public_key"`
	CredentialHash string    `json:"credential_hash"`
	PairedAt       time.Time `json:"paired_at"`
	LastSeen       time.Time `json:"last_seen"`
	Revoked        bool      `json:"revoked"`
}
type PairStatus struct {
	State     string    `json:"state"`
	Name      string    `json:"name,omitempty"`
	Emoji     string    `json:"emoji,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}
type pending struct {
	q                      Invitation
	state                  string
	cipher                 cipher.AEAD
	name, publicKey, emoji string
	device                 string
	phone, terminal        bool
	result                 json.RawMessage
}
type command struct {
	Op           string `json:"op"`
	ID           string `json:"id,omitempty"`
	Emoji        string `json:"emoji,omitempty"`
	Name         string `json:"name,omitempty"`
	PublicKey    string `json:"public_key,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	Credential   string `json:"credential,omitempty"`
	Signature    string `json:"signature,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Sequence     uint64 `json:"sequence,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	OriginWingID string `json:"origin_wing_id,omitempty"`
	Input        string `json:"input,omitempty"`
}
type Handler interface {
	Execute(context.Context, string, string, string, string) (any, error)
	Close(string)
}

// OriginHandler must authorize and resolve a qualified resource before acting.
// The distinct operation prefix makes old wings fail closed on remote actions.
type OriginHandler interface {
	ExecuteOrigin(context.Context, string, string, string, string, string) (any, error)
}
type connection struct {
	id, challenge, sender, device string
	cipher                        cipher.AEAD
	deadline                      time.Time
	sequence                      uint64
	authenticated                 bool
	inFlight                      bool
	ctx                           context.Context
	cancel                        context.CancelFunc
}
type Manager struct {
	mu                            sync.Mutex
	wingID, name, key, dir, local string
	relay                         func() string
	endpoints                     func() []string
	private                       *ecdh.PrivateKey
	now                           func() time.Time
	pending                       map[string]*pending
	devices                       []Device
	connections                   map[string]*connection
	handler                       Handler
}

func New(dir, wingID, name, local string, relay func() string, private *ecdh.PrivateKey, handler Handler) (*Manager, error) {
	m := &Manager{wingID: wingID, name: name, key: base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()), dir: dir, local: local, relay: relay, private: private, now: time.Now, pending: map[string]*pending{}, connections: map[string]*connection{}, handler: handler}
	b, e := os.ReadFile(filepath.Join(dir, "phone-devices.json"))
	if e == nil {
		e = json.Unmarshal(b, &m.devices)
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	return m, nil
}
func (m *Manager) save() error {
	b := encode(m.devices)
	p := filepath.Join(m.dir, "phone-devices.json")
	f, e := os.CreateTemp(m.dir, ".phone-devices-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(f.Name(), p)
	}
	return e
}
func (m *Manager) clean() {
	now := m.now()
	for id, p := range m.pending {
		if !now.Before(p.q.ExpiresAt) {
			delete(m.pending, id)
		}
	}
	for id, c := range m.connections {
		if !now.Before(c.deadline) {
			m.closeConnection(id)
		}
	}
}
func (m *Manager) Create() (Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clean()
	if len(m.pending) >= 8 {
		return Invitation{}, failure("busy")
	}
	relay := ""
	if m.relay != nil {
		relay = m.relay()
	}
	q := Invitation{Version: 1, WingID: m.wingID, Name: m.name, Key: m.key, Local: m.local, Relay: relay, ID: randomID(), Secret: randomWords(), ExpiresAt: m.now().Add(Lifetime)}
	if m.endpoints != nil {
		direct := m.endpoints()
		if len(direct) > 0 {
			q.Local = direct[0]
			q.Endpoints = direct[1:]
		}
	}
	m.pending[q.ID] = &pending{q: q, state: "waiting"}
	return q, nil
}
func (m *Manager) Status(id string) (PairStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, e := m.invitation(id)
	if e != nil {
		return PairStatus{}, e
	}
	return PairStatus{p.state, p.name, p.emoji, p.q.ExpiresAt}, nil
}
func (m *Manager) invitation(id string) (*pending, error) {
	p := m.pending[id]
	if p == nil || !m.now().Before(p.q.ExpiresAt) {
		return nil, failure("expired")
	}
	return p, nil
}
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.pending[id]; p != nil {
		if err := m.cancelPending(p); err != nil {
			return err
		}
		p.cipher = nil
	}
	return nil
}
func (m *Manager) cancelPending(p *pending) error {
	if p.device != "" && m.device(p.device) != nil {
		if err := m.revokeDevice(p.device); err != nil {
			return err
		}
	}
	p.state = "cancelled"
	p.q.Secret = ""
	p.result = nil
	return nil
}
func (m *Manager) Allow(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, e := m.invitation(id)
	if e != nil {
		return e
	}
	if p.state != "confirming" {
		return failure("denied")
	}
	p.terminal = true
	return m.complete(p)
}
func (m *Manager) complete(p *pending) error {
	if !p.phone || !p.terminal {
		return nil
	}
	if p.result != nil {
		return nil
	}
	for _, d := range m.devices {
		if !d.Revoked && d.Name == p.name {
			return failure("name_in_use")
		}
	}
	credential := randomID()
	hash := sha256.Sum256([]byte(credential))
	d := Device{ID: randomID(), Name: p.name, PublicKey: p.publicKey, CredentialHash: base64.StdEncoding.EncodeToString(hash[:]), PairedAt: m.now(), LastSeen: m.now()}
	m.devices = append(m.devices, d)
	if e := m.save(); e != nil {
		m.devices = m.devices[:len(m.devices)-1]
		return e
	}
	p.device = d.ID
	p.result = encode(map[string]any{"op": "paired", "id": p.q.ID, "wing_id": m.wingID, "key": m.key, "device_id": d.ID, "credential": credential})
	p.state = "paired"
	return nil
}
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, d := range m.devices {
		if !d.Revoked {
			d.CredentialHash = ""
			out = append(out, d)
		}
	}
	return out
}
func (m *Manager) Revoke(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := -1
	for i, d := range m.devices {
		if !d.Revoked && (d.Name == name || d.ID == name) {
			if index != -1 {
				return failure("ambiguous_name")
			}
			index = i
		}
	}
	if index < 0 {
		return failure("unknown_device")
	}
	return m.revokeDevice(m.devices[index].ID)
}
func (m *Manager) revokeDevice(id string) error {
	index := -1
	for i := range m.devices {
		if m.devices[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return failure("unknown_device")
	}
	old := m.devices[index]
	m.devices[index].Revoked = true
	m.devices[index].CredentialHash = ""
	if err := m.save(); err != nil {
		m.devices[index] = old
		return err
	}
	for connectionID, c := range m.connections {
		if c.device == old.ID {
			m.closeConnection(connectionID)
		}
	}
	return nil
}

func (m *Manager) closeConnection(id string) {
	if c := m.connections[id]; c != nil {
		c.cancel()
		if m.handler != nil {
			m.handler.Close(id)
		}
		delete(m.connections, id)
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.connections {
		m.closeConnection(id)
	}
	clear(m.pending)
}
func (m *Manager) Handle(ctx context.Context, r Request) Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clean()
	if len(r.Payload) > MaxPayload || len(r.Message) > 100 || len(r.ID) > 100 || len(r.ConnectionID) > 100 {
		return Response{Error: "too_large"}
	}
	response, e := m.handle(ctx, r)
	if e != nil {
		return Response{Error: e.Error()}
	}
	return response
}
func (m *Manager) handle(ctx context.Context, r Request) (Response, error) {
	switch r.Op {
	case "pair.start":
		p, e := m.invitation(r.ID)
		if e != nil {
			return Response{}, e
		}
		if p.state != "waiting" {
			return Response{}, failure("used")
		}
		p.state = "used"
		peer, e := base64.StdEncoding.DecodeString(r.Message)
		if e != nil {
			return Response{}, failure("bad_proof")
		}
		x, b, e := pake.New(false, p.q.Secret, Identity(p.q))
		p.q.Secret = ""
		if e != nil {
			return Response{}, failure("bad_proof")
		}
		defer x.Close()
		shared, e := x.Finish(peer)
		if e != nil {
			return Response{}, failure("bad_proof")
		}
		bob := base64.StdEncoding.EncodeToString(b)
		p.cipher, e = PairKey(p.q, shared, r.Message, bob)
		if e != nil {
			return Response{}, e
		}
		p.emoji = EmojiFromShared(p.q, shared, r.Message, bob)
		p.state = "proving"
		return Response{Message: bob, Key: m.key, Name: m.name, ExpiresAt: p.q.ExpiresAt}, nil
	case "pair.prove", "pair.confirm", "pair.status", "pair.cancel":
		p, e := m.invitation(r.ID)
		if e != nil {
			return Response{}, e
		}
		if p.cipher == nil || p.state == "cancelled" {
			return Response{}, failure("cancelled")
		}
		data, e := auth.Decrypt(p.cipher, r.Payload)
		if e != nil {
			if err := m.cancelPending(p); err != nil {
				return Response{}, err
			}
			p.cipher = nil
			return Response{}, failure("bad_proof")
		}
		var c command
		if json.Unmarshal(data, &c) != nil || c.ID != r.ID || "pair."+c.Op != r.Op {
			return Response{}, failure("bad_proof")
		}
		switch r.Op {
		case "pair.prove":
			if p.state != "proving" {
				return Response{}, failure("used")
			}
			pub, e := base64.StdEncoding.DecodeString(c.PublicKey)
			if e != nil {
				return Response{}, failure("bad_key")
			}
			x, _ := elliptic.Unmarshal(elliptic.P256(), pub)
			if x == nil || !validName(c.Name) {
				p.state = "cancelled"
				return Response{}, failure("bad_key")
			}
			p.publicKey = c.PublicKey
			p.name = c.Name
			p.state = "confirming"
		case "pair.confirm":
			if p.state != "confirming" && p.state != "paired" {
				return Response{}, failure("cancelled")
			}
			if c.Emoji != p.emoji {
				if err := m.cancelPending(p); err != nil {
					return Response{}, err
				}
				p.cipher = nil
				return Response{}, failure("emoji_mismatch")
			}
			p.phone = true
			if e = m.complete(p); e != nil {
				return Response{}, e
			}
		case "pair.cancel":
			if e := m.cancelPending(p); e != nil {
				return Response{}, e
			}
		}
		result := p.result
		if result == nil {
			result = encode(map[string]any{"op": map[string]string{"pair.prove": "proved"}[r.Op], "id": p.q.ID, "state": p.state, "emoji": p.emoji})
		}
		payload, e := auth.Encrypt(p.cipher, result)
		return Response{Payload: payload}, e
	case "device.hello":
		if len(m.connections) >= 64 {
			return Response{}, failure("busy")
		}
		g, e := auth.DeriveSharedKey(m.private, r.SenderPub, "wt-phone-v1")
		if e != nil {
			return Response{}, failure("bad_key")
		}
		id := randomID()
		cctx, cancel := context.WithCancel(context.Background())
		c := &connection{id: id, challenge: randomID(), sender: r.SenderPub, cipher: g, deadline: m.now().Add(30 * time.Second), ctx: cctx, cancel: cancel}
		m.connections[id] = c
		direct := []string{m.local}
		if m.endpoints != nil {
			direct = m.endpoints()
		}
		relay := ""
		if m.relay != nil {
			relay = m.relay()
		}
		payload, e := auth.Encrypt(g, encode(map[string]any{"connection_id": id, "challenge": c.challenge, "wing_id": m.wingID, "key": m.key, "endpoints": direct, "relay": relay}))
		return Response{ConnectionID: id, Payload: payload}, e
	case "device.auth", "device.command":
		c := m.connections[r.ConnectionID]
		if c == nil {
			return Response{}, failure("revoked")
		}
		data, e := auth.Decrypt(c.cipher, r.Payload)
		if e != nil {
			m.closeConnection(c.id)
			return Response{}, failure("bad_proof")
		}
		var cmd command
		if json.Unmarshal(data, &cmd) != nil || cmd.ConnectionID != c.id {
			return Response{}, failure("bad_proof")
		}
		if r.Op == "device.auth" {
			if c.authenticated {
				return Response{}, failure("replay")
			}
			d := m.device(cmd.DeviceID)
			if d == nil {
				return Response{}, failure("revoked")
			}
			h := sha256.Sum256([]byte(cmd.Credential))
			expected, _ := base64.StdEncoding.DecodeString(d.CredentialHash)
			pub, _ := base64.StdEncoding.DecodeString(d.PublicKey)
			x, y := elliptic.Unmarshal(elliptic.P256(), pub)
			sig, _ := base64.StdEncoding.DecodeString(cmd.Signature)
			digest := sha256.Sum256([]byte("wt-device-v1\n" + m.wingID + "\n" + c.id + "\n" + c.challenge + "\n" + c.sender))
			if x == nil || subtle.ConstantTimeCompare(h[:], expected) != 1 || !ecdsa.VerifyASN1(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], sig) {
				m.closeConnection(c.id)
				return Response{}, failure("bad_proof")
			}
			c.device = d.ID
			c.authenticated = true
			c.challenge = ""
			c.deadline = m.now().Add(5 * time.Minute)
			d.LastSeen = m.now()
			_ = m.save()
			payload, e := auth.Encrypt(c.cipher, encode(map[string]any{"connection_id": c.id, "sequence": 0, "ok": true}))
			return Response{Payload: payload}, e
		}
		if !c.authenticated || m.device(c.device) == nil {
			return Response{}, failure("revoked")
		}
		if c.inFlight {
			return Response{}, failure("busy")
		}
		if cmd.Sequence != c.sequence+1 {
			return Response{}, failure("replay")
		}
		c.sequence = cmd.Sequence
		c.deadline = m.now().Add(5 * time.Minute)
		if d := m.device(c.device); d != nil && m.now().Sub(d.LastSeen) >= time.Minute {
			d.LastSeen = m.now()
			_ = m.save()
		}
		switch cmd.Op {
		case "sessions", "attach", "read", "thread", "input", "stop", "detach", "close":
			if cmd.OriginWingID != "" && cmd.OriginWingID != m.wingID {
				return Response{}, failure("denied")
			}
		case "origin.attach", "origin.read", "origin.thread", "origin.input", "origin.stop", "origin.detach":
			if cmd.OriginWingID == "" {
				return Response{}, failure("denied")
			}
			if _, ok := m.handler.(OriginHandler); !ok {
				return Response{}, failure("denied")
			}
		default:
			return Response{}, failure("denied")
		}
		var result any = map[string]any{"ok": true}
		if cmd.Op == "close" {
			m.closeConnection(c.id)
		} else if m.handler != nil {
			c.inFlight = true
			m.mu.Unlock()
			if strings.HasPrefix(cmd.Op, "origin.") {
				result, e = m.handler.(OriginHandler).ExecuteOrigin(c.ctx, c.id, strings.TrimPrefix(cmd.Op, "origin."), cmd.OriginWingID, cmd.SessionID, cmd.Input)
			} else {
				result, e = m.handler.Execute(c.ctx, c.id, cmd.Op, cmd.SessionID, cmd.Input)
			}
			m.mu.Lock()
			c.inFlight = false
			if m.connections[c.id] != c || m.device(c.device) == nil || c.ctx.Err() != nil {
				m.handler.Close(c.id)
				return Response{}, failure("revoked")
			}
		}
		reply := map[string]any{"connection_id": c.id, "sequence": cmd.Sequence, "result": result}
		if e != nil {
			reply["error"] = "session_unavailable"
		}
		payload, e := auth.Encrypt(c.cipher, encode(reply))
		return Response{Payload: payload}, e
	}
	return Response{}, failure("denied")
}
func (m *Manager) device(id string) *Device {
	for i := range m.devices {
		d := &m.devices[i]
		if d.ID == id && !d.Revoked {
			return d
		}
	}
	return nil
}
func validName(name string) bool {
	if len(name) == 0 || len(name) > 80 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}
