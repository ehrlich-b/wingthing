package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalRoutesRequireSecretOnStandaloneRoost(t *testing.T) {
	server := &Server{Config: ServerConfig{InternalSecret: "shared-secret", JWTKey: "must-not-authorize-internal-apis"}}
	handler := server.withInternalAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("standalone loopback status = %d, want forbidden", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	request.Header.Set(internalSecretHeader, "shared-secret")
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("standalone shared-secret status = %d", response.Code)
	}
}

func TestInternalRoutesDistinguishClusterTrafficFromForwardedPublicTraffic(t *testing.T) {
	server := &Server{Config: ServerConfig{NodeRole: "edge", FlyAppName: "wingthing", FlyMachineID: "edge-1"}}
	handler := server.withInternalAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, test := range []struct {
		name       string
		remoteAddr string
		flyClient  string
		xff        string
		want       int
	}{
		{name: "direct 6PN", remoteAddr: "[fdaa::1234]:443", want: http.StatusNoContent},
		{name: "private Fly caller", remoteAddr: "[fdaa::1]:443", flyClient: "fdaa::2", want: http.StatusNoContent},
		{name: "public through Fly proxy", remoteAddr: "[fdaa::1]:443", flyClient: "203.0.113.20", want: http.StatusForbidden},
		{name: "spoofed XFF", remoteAddr: "203.0.113.30:443", xff: "127.0.0.1", want: http.StatusForbidden},
		{name: "spoofed Fly header without private hop", remoteAddr: "203.0.113.30:443", flyClient: "127.0.0.1", want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Fly-Client-IP", test.flyClient)
			request.Header.Set("X-Forwarded-For", test.xff)
			response := httptest.NewRecorder()
			handler(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestInternalRoutesDoNotTreatJWTSigningKeyAsHTTPSecret(t *testing.T) {
	server := &Server{Config: ServerConfig{JWTKey: "signing-key"}}
	handler := server.withInternalAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	request.Header.Set(internalSecretHeader, "signing-key")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("JWT signing key authorized internal API: status=%d", response.Code)
	}
}

func TestInternalRoutesRequireFlyIdentityForPrivateNetworkBypass(t *testing.T) {
	server := &Server{Config: ServerConfig{NodeRole: "edge"}}
	handler := server.withInternalAuth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	request.RemoteAddr = "10.0.0.5:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("generic RFC1918 caller authorized as Fly cluster traffic: status=%d", response.Code)
	}
}
