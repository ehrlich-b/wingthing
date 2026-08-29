package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWingWebSocketRejectsCrossOriginBrowser(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(nil, ServerConfig{})
	server.SetJWTKey(key)
	token, _, err := IssueWingJWT(key, "wing-user", "public-key", "wing-id")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/wing"
	_, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + token},
			"Origin":        []string{"https://attacker.example"},
		},
	})
	if err == nil {
		t.Fatal("wing endpoint accepted a cross-origin browser")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %#v, err = %v; want 403", response, err)
	}
}

func TestRelayRejectsOversizedBodiesBeforeRouting(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	body := bytes.NewReader(make([]byte, maxRelayRequestBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "http://wingthing.test/not-a-route", body)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", recorder.Code)
	}
}

func TestLocalModeRejectsDNSRebindingHostBeforeRouting(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local"})

	for _, host := range []string{"attacker.example:8080", "localhost.attacker.example:8080", "192.168.1.20:8080"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/health", nil)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("Host %q status = %d, want 403", host, recorder.Code)
		}
	}
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/health", nil)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Errorf("Host %q status = %d, want 200", host, recorder.Code)
		}
	}
}

func TestLocalAppWebSocketRejectsCrossOriginBrowser(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local"})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/app"
	_, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://attacker.example"}},
	})
	if err == nil {
		t.Fatal("local app WebSocket accepted a cross-origin browser")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %#v, err = %v; want 403", response, err)
	}
}

func TestLocalModeRejectsCrossOriginBrowserMutations(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local"})

	for _, test := range []struct {
		name      string
		host      string
		origin    string
		fetchSite string
		tls       bool
		want      int
	}{
		{name: "cross origin", host: "localhost:8080", origin: "https://attacker.example", want: http.StatusForbidden},
		{name: "cross site without origin", host: "localhost:8080", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "scheme mismatch", host: "localhost:8443", origin: "http://localhost:8443", tls: true, want: http.StatusForbidden},
		{name: "same HTTP origin", host: "localhost:8080", origin: "http://localhost:8080", want: http.StatusNotFound},
		{name: "same HTTPS origin", host: "localhost:8443", origin: "https://localhost:8443", tls: true, want: http.StatusNotFound},
		{name: "default HTTP port", host: "localhost", origin: "http://localhost", want: http.StatusNotFound},
		{name: "native client without browser headers", host: "127.0.0.1:8080", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://"+test.host+"/not-a-route", nil)
			req.Host = test.host
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.tls {
				req.TLS = &tls.ConnectionState{}
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestHostedModeRejectsSiblingOriginMutations(t *testing.T) {
	server := NewServer(nil, ServerConfig{
		BaseURL: "https://wingthing.example",
		AppHost: "app.wingthing.example",
	})
	tests := []struct {
		name      string
		origin    string
		fetchSite string
		want      int
	}{
		{name: "app origin", origin: "https://app.wingthing.example", want: http.StatusNotFound},
		{name: "base origin", origin: "https://wingthing.example", want: http.StatusNotFound},
		{name: "sibling origin", origin: "https://attacker.wingthing.example", want: http.StatusForbidden},
		{name: "cross site without origin", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "native client", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://app.wingthing.example/api/not-a-route", nil)
			request.Host = "app.wingthing.example"
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestAppHostRoutingAcceptsConfiguredPortAndHostnameCase(t *testing.T) {
	server := NewServer(nil, ServerConfig{
		BaseURL: "http://login.wingthing.example:8080",
		AppHost: "APP.wingthing.example:8080",
	})
	request := httptest.NewRequest(http.MethodGet, "http://app.wingthing.example:8080/client-side-route", nil)
	request.Host = "app.wingthing.example:8080"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("app route status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(strings.ToLower(recorder.Body.String()), "<!doctype html>") {
		t.Fatalf("app route did not serve the SPA: %q", recorder.Body.String())
	}
}

func TestAppHostRoutingAcceptsBracketedIPv6WithAndWithoutPort(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured string
		request    string
	}{
		{name: "without port", configured: "[::1]", request: "[::1]"},
		{name: "with port", configured: "[::1]:8080", request: "[::1]:8080"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(nil, ServerConfig{AppHost: test.configured})
			request := httptest.NewRequest(http.MethodGet, "http://[::1]/client-side-route", nil)
			request.Host = test.request
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("app route status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if !strings.Contains(strings.ToLower(recorder.Body.String()), "<!doctype html>") {
				t.Fatalf("app route did not serve the SPA: %q", recorder.Body.String())
			}
		})
	}
}

func TestLocalAppWebSocketAcceptsSameOriginBrowser(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	server.LocalMode = true
	server.SetLocalUser(&User{ID: "local"})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/app"
	conn, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{httpServer.URL}},
	})
	if err != nil {
		t.Fatalf("same-origin dial: response=%#v err=%v", response, err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "test complete")
}

func TestHostedBrowserWebSocketOriginsAreExplicit(t *testing.T) {
	hosted := NewServer(nil, ServerConfig{
		BaseURL: "https://wingthing.ai",
		AppHost: "app.wingthing.ai",
	})
	options := hosted.browserWebSocketAcceptOptions()
	if options.InsecureSkipVerify {
		t.Fatal("hosted portal disabled WebSocket Origin verification")
	}
	want := []string{"https://wingthing.ai", "https://app.wingthing.ai"}
	if !reflect.DeepEqual(options.OriginPatterns, want) {
		t.Fatalf("hosted Origin patterns = %#v, want %#v", options.OriginPatterns, want)
	}
	local := NewServer(nil, ServerConfig{})
	local.LocalMode = true
	if local.browserWebSocketAcceptOptions().InsecureSkipVerify {
		t.Fatal("login-bypassing local mode disabled browser Origin checks")
	}
}

func TestHostedAppWebSocketAllowsConfiguredOriginAndRejectsAttacker(t *testing.T) {
	store := testStore(t)
	if err := store.CreateUser("hosted-ws-user"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession("hosted-ws-session", "hosted-ws-user", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, ServerConfig{
		BaseURL: "https://wingthing.example",
		AppHost: "app.wingthing.example",
	})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/app"

	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		t.Helper()
		return websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
			"Cookie": {"wt_session=hosted-ws-session"},
			"Origin": {origin},
		}})
	}

	conn, response, err := dial("https://app.wingthing.example")
	if err != nil {
		t.Fatalf("configured app Origin dial: response=%#v err=%v", response, err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "test complete")

	_, response, err = dial("https://attacker.example")
	if err == nil {
		t.Fatal("hosted app WebSocket accepted an attacker Origin")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("attacker Origin response = %#v, err = %v; want 403", response, err)
	}
}
