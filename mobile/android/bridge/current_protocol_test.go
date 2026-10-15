package bridge

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// Exercise the Android bridge against the current manager through opaque HTTP
// boxes, with a persistent session and separate per-connection attachments.
type sessionFixture struct {
	mu       sync.Mutex
	attached map[string]string
	inputs   []string
}

func (h *sessionFixture) Execute(_ context.Context, connection, op, session, input string) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch op {
	case "sessions":
		return []map[string]string{{"id": "existing-session", "name": "Existing work"}}, nil
	case "attach":
		if session != "existing-session" {
			return nil, fmt.Errorf("unknown session")
		}
		h.attached[connection] = session
	case "detach":
		delete(h.attached, connection)
	case "input", "read":
		if h.attached[connection] != session {
			return nil, fmt.Errorf("not attached")
		}
		if op == "input" {
			h.inputs = append(h.inputs, input)
		}
		if op == "read" {
			return map[string]any{"output": strings.Join(h.inputs, ""), "ended": false}, nil
		}
	}
	return map[string]bool{"ok": true}, nil
}
func (h *sessionFixture) Close(connection string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.attached, connection)
}

type httpWire struct{ url string }

func (w httpWire) Exchange(box string, _ bool) (string, error) {
	response, err := http.Post(w.url, "application/json", strings.NewReader(box))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, phonepair.MaxPayload+4097))
	return string(b), err
}
func serveManager(t *testing.T, m *phonepair.Manager, restricted bool) httpWire {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var box phonepair.Request
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, phonepair.MaxPayload+4096)).Decode(&box) != nil {
			w.WriteHeader(400)
			return
		}
		box.InventoryOnly = restricted // Authority belongs to the trusted carrier.
		if err := json.NewEncoder(w).Encode(m.Box(r.Context(), box)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return httpWire{url: server.URL}
}
func TestCurrentProtocolSameSessionReconnectAndRevoke(t *testing.T) {
	h := &sessionFixture{attached: map[string]string{}}
	m, q, signer, pub := statefulFixture(t, h)
	wire := serveManager(t, m, false)
	en, err := Begin(q.Link(), "Reconnect phone", pub, wire)
	if err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(q.ID)
	if err != nil || status.Scope != phonepair.ScopeControl {
		t.Fatal(status, err)
	}
	if en.ConfirmationLabels() != phonepair.EmojiLabels(status.Emoji) {
		t.Fatal("confirmation vocabulary differs from current terminal")
	}
	if _, err = en.Confirm(en.Emoji()); err != nil {
		t.Fatal(err)
	}
	if err = m.Allow(q.ID); err != nil {
		t.Fatal(err)
	}
	profile, err := en.Status()
	if err != nil {
		t.Fatal(err)
	}
	var p Profile
	if json.Unmarshal([]byte(profile), &p) != nil || p.Scope != phonepair.ScopeControl {
		t.Fatal("grant scope missing")
	}
	first, err := Connect(profile, signer, wire)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.id
	for _, op := range []string{"sessions", "attach", "input", "read", "close"} {
		result, err := first.Command(op, "existing-session", "sent once\r")
		if err != nil {
			t.Fatal(op, err)
		}
		if op == "sessions" && !strings.Contains(result, "existing-session") {
			t.Fatal(result)
		}
	}
	second, err := Connect(profile, signer, wire)
	if err != nil {
		t.Fatal(err)
	}
	if second.id == firstID {
		t.Fatal("reused connection challenge")
	}
	if _, err = second.Command("attach", "existing-session", ""); err != nil {
		t.Fatal(err)
	}
	result, err := second.Command("read", "existing-session", "")
	if err != nil || !strings.Contains(result, "sent once") {
		t.Fatal("session lost", result, err)
	}
	h.mu.Lock()
	inputCount := len(h.inputs)
	h.mu.Unlock()
	if inputCount != 1 {
		t.Fatal("input replayed", inputCount)
	}
	if err = m.Revoke("Reconnect phone"); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Command("input", "existing-session", "must not arrive"); err == nil || err.Error() != "revoked" {
		t.Fatal("live revoke", err)
	}
	if _, err = Connect(profile, signer, wire); err == nil || err.Error() != "revoked" {
		t.Fatal("revoked credential reconnected", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.attached) != 0 || len(h.inputs) != 1 {
		t.Fatal("revocation retained attachment or delivered input")
	}
}
func TestCurrentProtocolRestrictedCarrierCannotGrantNativeControl(t *testing.T) {
	m, q, _, pub := fixture(t)
	if _, err := Begin(q.Link(), "Restricted phone", pub, serveManager(t, m, true)); err == nil || err.Error() != "denied" {
		t.Fatal(err)
	}
	if err := m.Allow(q.ID); err == nil {
		t.Fatal("restricted carrier granted native control")
	}
	if len(m.Devices()) != 0 {
		t.Fatal("restricted carrier persisted a grant")
	}
}
func TestCurrentProtocolServerExpiryRejectsExtendedClientClaim(t *testing.T) {
	t.Parallel()
	m, q, _, pub := fixture(t)
	wire := serveManager(t, m, false)
	// Wait for the real current server's five-minute deadline. Changing only the
	// phone's claim must never extend terminal authority.
	time.Sleep(time.Until(q.ExpiresAt) + time.Second)
	q.ExpiresAt = time.Now().Add(phonepair.Lifetime)
	if _, err := Begin(q.Link(), "Expired phone", pub, wire); err == nil || err.Error() != "expired" {
		t.Fatal(err)
	}
	if len(m.Devices()) != 0 {
		t.Fatal("expired invitation granted access")
	}
}

func statefulFixture(t *testing.T, h *sessionFixture) (*phonepair.Manager, phonepair.Invitation, testSigner, string) {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := phonepair.New(t.TempDir(), "fixture-wing", "Mac", "http://127.0.0.1:1234", nil, private, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	q, err := m.Create()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y))
	return m, q, testSigner{key}, pub
}
