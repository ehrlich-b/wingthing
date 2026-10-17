package phonepair

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackPhoneListenerDoesNotAdvertiseLAN(t *testing.T) {
	dir := t.TempDir()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server, err := Listen(t.Context(), dir, "fixture-loopback-wing", "Fixture", nil, private, &fixtureHandler{}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if server == nil {
		t.Skip("PAKE unavailable on this build")
	}
	defer server.Close()
	if server.discovery != nil {
		t.Fatal("loopback listener started LAN discovery")
	}
	q, err := server.Manager.Create()
	if err != nil {
		t.Fatal(err)
	}
	if q.Local != "http://"+server.Address() || len(q.Endpoints) != 0 {
		t.Fatalf("loopback invitation advertised unbound endpoints: %+v", q)
	}
	data, err := os.ReadFile(filepath.Join(dir, "phone-control.json"))
	if err != nil {
		t.Fatal(err)
	}
	var control Control
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		remote, token string
		status        int
	}{
		{"127.0.0.1:40000", control.Token, http.StatusOK},
		{"127.0.0.1:40000", "wrong-token", http.StatusForbidden},
		{"203.0.113.7:40000", control.Token, http.StatusForbidden},
	} {
		req := httptest.NewRequest("POST", "/admin", strings.NewReader(`{"Op":"devices"}`))
		req.RemoteAddr = tt.remote
		req.Header.Set("Authorization", "Bearer "+tt.token)
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, req)
		if response.Code != tt.status {
			t.Fatalf("admin peer=%s status=%d want=%d", tt.remote, response.Code, tt.status)
		}
	}
}

func TestPhoneControlAdminURL(t *testing.T) {
	for _, tt := range []struct{ address, want string }{
		{"", "http://127.0.0.1:1234/admin"},
		{"0.0.0.0:1234", "http://127.0.0.1:1234/admin"},
		{"127.0.0.2:1234", "http://127.0.0.2:1234/admin"},
		{"[::1]:1234", "http://[::1]:1234/admin"},
		{"[::]:1234", "http://[::1]:1234/admin"},
		{"203.0.113.7:1234", "http://203.0.113.7:1234/admin"},
	} {
		if got := (Control{Port: 1234, Address: tt.address}).AdminURL(); got != tt.want {
			t.Fatalf("admin address %q: %s want %s", tt.address, got, tt.want)
		}
	}
}
