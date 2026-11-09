package phoneweb

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

const testOrigin = "https://phone.tunn.invalid"
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T) (*Gateway, *int) {
	t.Helper()
	path := filepath.Join(testsocket.Dir(t), "phone.key")
	if err := os.WriteFile(path, []byte(testKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := New(testOrigin, "http://127.0.0.1:8080", path)
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	g.proxy.(*httputil.ReverseProxy).Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Host != "127.0.0.1:8080" || r.URL.Host != r.Host {
			t.Errorf("wrong upstream: %s %s", r.Host, r.URL)
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://127.0.0.1:8080" {
			t.Errorf("untranslated origin %q", origin)
		}
		if _, err := r.Cookie(cookieName); err == nil {
			t.Error("gateway session leaked to upstream")
		}
		for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Tunn-Authorization"} {
			if r.Header.Get(header) != "" {
				t.Errorf("untrusted header forwarded: %s", header)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"public, max-age=3600"}}, Body: io.NopCloser(strings.NewReader("existing API"))}, nil
	})
	return g, calls
}

func request(method, path string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, testOrigin+path, nil)
	if method != http.MethodGet {
		r.Header.Set("Origin", testOrigin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func signIn(t *testing.T, g *Gateway) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", testOrigin+"/phone/login", strings.NewReader(url.Values{"key": {testKey}}.Encode()))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/app/" {
		t.Fatalf("login %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" || c.Value == testKey {
		t.Fatalf("unsafe session cookie: %#v", c)
	}
	return c
}

func TestGatewayRequiresAuthenticationForEntireControlSurface(t *testing.T) {
	g, calls := fixture(t)
	for _, path := range []string{"/api/app/me", "/api/app/wings", "/ws/relay?wing_id=mac", "/app/manifest.json", "/app/icons/wt-180.png"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request("GET", path, nil))
		if w.Code != 401 && w.Code != 303 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if *calls != 0 {
		t.Fatal("unauthenticated request reached the roost")
	}
	c := signIn(t, g)
	for _, path := range []string{"/api/app/me", "/app/", "/app/manifest.json"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request("GET", path, c))
		if w.Code != 200 || w.Body.String() != "existing API" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("authenticated %s: %d %v", path, w.Code, w.Header())
		}
	}
}

func TestGatewayRejectsRebindingCrossSiteAndForgedCredentials(t *testing.T) {
	g, calls := fixture(t)
	c := signIn(t, g)
	for _, tc := range []struct {
		name, method, host, origin, fetch, upgrade string
		cookie                                     *http.Cookie
		want                                       int
	}{
		{"host", "GET", "attacker.invalid", "", "", "", c, 403},
		{"loopback host", "GET", "127.0.0.1:8081", "", "", "", c, 403},
		{"cross-origin POST", "POST", "", "https://attacker.invalid", "", "", c, 403},
		{"sibling origin", "POST", "", "https://other.tunn.invalid", "same-site", "", c, 403},
		{"missing origin POST", "POST", "", "", "same-origin", "", c, 403},
		{"cross-site", "POST", "", testOrigin, "cross-site", "", c, 403},
		{"cross-origin WS", "GET", "", "https://attacker.invalid", "", "websocket", c, 403},
		{"missing origin WS", "GET", "", "", "", "websocket", c, 403},
		{"unauthenticated WS", "GET", "", testOrigin, "", "websocket", nil, 401},
		{"forged cookie", "GET", "", "", "", "", &http.Cookie{Name: cookieName, Value: strings.Repeat("f", 64)}, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(tc.method, "/api/app/me", tc.cookie)
			if tc.host != "" {
				r.Host = tc.host
			}
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
			r.Header.Set("Upgrade", tc.upgrade)
			r.Header.Set("X-Forwarded-Host", "phone.tunn.invalid")
			r.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
	if *calls != 0 {
		t.Fatal("rejected request reached upstream")
	}
	// Validate accepted POST and upgrade requests with an in-process transport.
	// A real 101 full-duplex upgrade is deliberately a coordinator gate.
	for _, method := range []string{"POST", "GET"} {
		r := request(method, "/ws/relay?wing_id=mac", c)
		r.Header.Set("Origin", testOrigin)
		if method == "GET" {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
		}
		r.Header.Set("Tunn-Authorization", "Bearer fake")
		r.Header.Set("Forwarded", "host=attacker")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("accepted request %d", w.Code)
		}
	}
}

func TestGatewayLogoutExpiryAndRestartRevokeSessions(t *testing.T) {
	g, _ := fixture(t)
	c := signIn(t, g)
	id := shaCookie(c)
	s := g.sessions[id]
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request("POST", "/phone/logout", c))
	if w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	select {
	case <-s.revoked:
	default:
		t.Fatal("logout did not signal active streams")
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, request("GET", "/api/app/me", c))
	if w.Code != 401 {
		t.Fatal("logged-out session still works")
	}
	other, _ := fixture(t)
	w = httptest.NewRecorder()
	other.ServeHTTP(w, request("GET", "/api/app/me", c))
	if w.Code != 401 {
		t.Fatal("session survived gateway restart")
	}
	g.loginNext = time.Time{}
	c = signIn(t, g)
	now := time.Now().Add(sessionLife + time.Minute)
	g.now = func() time.Time { return now }
	w = httptest.NewRecorder()
	g.ServeHTTP(w, request("GET", "/api/app/me", c))
	if w.Code != 401 {
		t.Fatal("expired session still works")
	}
}

func shaCookie(c *http.Cookie) [32]byte { return sha256.Sum256([]byte(c.Value)) }

func TestGatewayLogoutCancelsAnActiveProxyRequest(t *testing.T) {
	g, _ := fixture(t)
	c := signIn(t, g)
	started := make(chan struct{})
	ended := make(chan struct{})
	g.proxy.(*httputil.ReverseProxy).Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, context.Canceled
	})
	go func() { g.ServeHTTP(httptest.NewRecorder(), request("GET", "/ws/relay", c)); close(ended) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not start")
	}
	g.ServeHTTP(httptest.NewRecorder(), request("POST", "/phone/logout", c))
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked proxy request remained active")
	}
}

func TestGatewayKeyAndConfigurationFailClosed(t *testing.T) {
	path := filepath.Join(testsocket.Dir(t), "phone.key")
	if err := os.WriteFile(path, []byte(testKey), 0600); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://phone.invalid", "https://phone.invalid/", "https://x:y@phone.invalid", "https://phone.invalid?key=x", "https://phone.invalid?", "https://phone.invalid#", "https://:443", ""} {
		if _, err := New(origin, "http://127.0.0.1:8080", path); err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
	for _, upstream := range []string{"http://localhost:8080", "http://192.168.1.2", "https://127.0.0.1", "http://127.0.0.1/api", "http://x:y@127.0.0.1"} {
		if _, err := New(testOrigin, upstream, path); err == nil {
			t.Errorf("accepted upstream %q", upstream)
		}
	}
	for _, addr := range []string{"0.0.0.0:8081", "localhost:8081", ":8081", "192.168.1.2:8081"} {
		if ValidateListenAddr(addr) == nil {
			t.Errorf("accepted bind %q", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8081", "[::1]:8081"} {
		if err := ValidateListenAddr(addr); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(testOrigin, "http://127.0.0.1:8080", path); err == nil {
		t.Fatal("accepted public key file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := path + ".link"
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := New(testOrigin, "http://127.0.0.1:8080", alias); err == nil {
		t.Fatal("accepted key symlink")
	}
	if err := os.WriteFile(path, []byte("weak"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(testOrigin, "http://127.0.0.1:8080", path); err == nil || strings.Contains(err.Error(), "weak") {
		t.Fatal("weak key accepted or logged")
	}
}

func TestGatewayLoginRequiresPostedKeyAndLimitsFailures(t *testing.T) {
	g, calls := fixture(t)
	for _, tc := range []struct {
		origin, body, query string
		want                int
	}{
		{"https://attacker.invalid", "key=" + testKey, "", 403},
		{testOrigin, "key=wrong", "", 401},
		{testOrigin, "key=" + testKey, "", 429},
	} {
		r := httptest.NewRequest("POST", testOrigin+"/phone/login"+tc.query, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != tc.want || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), testKey) {
			t.Fatalf("login failure response: %d %s", w.Code, w.Body.String())
		}
	}
	g.loginNext = time.Time{}
	r := httptest.NewRequest("POST", testOrigin+"/phone/login?key="+testKey, strings.NewReader(""))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("query-string credential accepted")
	}
	if *calls != 0 {
		t.Fatal("login attempts reached upstream")
	}
}
