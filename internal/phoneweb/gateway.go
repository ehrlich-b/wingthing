// Package phoneweb authenticates a phone browser before forwarding the existing
// local roost API. It adds no wing-control protocol and never trusts proxy headers.
package phoneweb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const cookieName = "__Host-wingthing-phone"
const sessionLife = 12 * time.Hour

type session struct {
	expires time.Time
	revoked chan struct{}
}

type Gateway struct {
	origin    string
	host      string
	key       [32]byte
	proxy     http.Handler
	mu        sync.Mutex
	sessions  map[[32]byte]session
	loginNext time.Time
	now       func() time.Time
}

// ValidateListenAddr rejects DNS names and wildcard/LAN binds.
func ValidateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return errors.New("phone gateway requires a literal loopback listen address")
	}
	return nil
}

func New(origin, upstream, keyFile string) (*Gateway, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") || u.Opaque != "" {
		return nil, errors.New("phone origin must be one exact HTTPS origin, without path or credentials")
	}
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.ForceQuery || strings.Contains(upstream, "#") || target.Path != "" || target.Opaque != "" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() {
		return nil, errors.New("phone upstream must be an HTTP origin on a literal loopback address")
	}
	info, err := os.Lstat(keyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128 {
		return nil, errors.New("phone key must be a private regular file (mode 0600), containing 64 random hex characters")
	}
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, errors.New("cannot read phone key file")
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("phone key must contain 64 random hex characters")
	}
	g := &Gateway{origin: origin, host: u.Host, key: sha256.Sum256(key), sessions: make(map[[32]byte]session), now: time.Now}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = target.Host
			// Only requests already authenticated and checked below reach here.
			if p.In.Header.Get("Origin") != "" {
				p.Out.Header.Set("Origin", upstream)
			}
			for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Tunn-Authorization"} {
				p.Out.Header.Del(name)
			}
			cookies := p.Out.Cookies()
			p.Out.Header.Del("Cookie")
			for _, c := range cookies {
				if c.Name != cookieName {
					p.Out.AddCookie(c)
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Local wing unavailable. Check the roost, then reconnect; messages are not resent.", http.StatusBadGateway)
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Set("Cache-Control", "private, no-store")
			r.Header.Set("X-Wingthing-Phone-Gateway", "1")
			return nil
		},
	}
	return g, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; form-action 'self'")
	if !strings.EqualFold(r.Host, g.host) {
		http.Error(w, "Incorrect phone host", http.StatusForbidden)
		return
	}
	upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
	if upgrade || unsafe {
		if r.Header.Get("Origin") != g.origin || strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			http.Error(w, "Phone requests require the configured origin", http.StatusForbidden)
			return
		}
	}
	if r.URL.Path == "/phone/login" {
		g.login(w, r)
		return
	}
	s, ok := g.lookup(r)
	if !ok {
		if r.Method == http.MethodGet && !upgrade && (strings.HasPrefix(r.URL.Path, "/app") || r.URL.Path == "/" || r.URL.Path == "/login") {
			http.Redirect(w, r, "/phone/login", http.StatusSeeOther)
		} else {
			http.Error(w, "Phone access expired. Open /phone/login to reconnect.", http.StatusUnauthorized)
		}
		return
	}
	if r.URL.Path == "/phone/logout" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		g.mu.Lock()
		if c, err := r.Cookie(cookieName); err == nil {
			id := sha256.Sum256([]byte(c.Value))
			if old, found := g.sessions[id]; found {
				close(old.revoked)
				delete(g.sessions, id)
			}
		}
		g.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		http.Redirect(w, r, "/phone/login", http.StatusSeeOther)
		return
	}
	// Expiry and sign-out also end an already upgraded WebSocket. The server's
	// BaseContext is canceled on shutdown, closing every proxy stream.
	ctx, cancel := context.WithDeadline(r.Context(), s.expires)
	defer cancel()
	go func() {
		select {
		case <-s.revoked:
			cancel()
		case <-ctx.Done():
		}
	}()
	g.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (g *Gateway) lookup(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) != 64 {
		return session{}, false
	}
	id := sha256.Sum256([]byte(c.Value))
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.sessions[id]
	if ok && !g.now().Before(s.expires) {
		close(s.revoked)
		delete(g.sessions, id)
		ok = false
	}
	return s, ok
}

func (g *Gateway) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, signedIn := g.lookup(r)
		form := `<form method="post"><label>Phone access key<input name="key" type="password" required autocomplete="current-password" autocapitalize="none" spellcheck="false"></label><button>Connect</button></form>`
		if signedIn {
			form = `<p>Phone access is connected for up to 12 hours.</p><p><a href="/app/">Open Wingthing</a></p><form action="/phone/logout" method="post"><button>Sign out this phone</button></form>`
		}
		fmt.Fprint(w, `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"><meta name="color-scheme" content="dark"><title>Wingthing phone access</title><style>body{font:18px system-ui;max-width:30rem;margin:12vh auto;padding:24px;background:#121b32;color:#fff}input,button{box-sizing:border-box;display:block;width:100%;font:inherit;min-height:48px;margin:12px 0}a{color:#b9d6ff}</style><h1>Wingthing</h1>`, form, `<p>Use the private key from your Mac. Keep it out of links and messages.</p></html>`)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	g.mu.Lock()
	if g.now().Before(g.loginNext) {
		g.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Try again in one second", http.StatusTooManyRequests)
		return
	}
	g.loginNext = g.now().Add(time.Second)
	g.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid login form", http.StatusBadRequest)
		return
	}
	key, err := hex.DecodeString(r.PostForm.Get("key"))
	digest := sha256.Sum256(key)
	if err != nil || len(key) != 32 || subtle.ConstantTimeCompare(digest[:], g.key[:]) != 1 {
		http.Error(w, "Invalid phone key", http.StatusUnauthorized)
		return
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		http.Error(w, "Cannot create phone session", http.StatusInternalServerError)
		return
	}
	value := hex.EncodeToString(token[:])
	id := sha256.Sum256([]byte(value))
	expires := g.now().Add(sessionLife)
	g.mu.Lock()
	for oldID, old := range g.sessions {
		if !g.now().Before(old.expires) {
			close(old.revoked)
			delete(g.sessions, oldID)
		}
	}
	if len(g.sessions) >= 32 {
		g.mu.Unlock()
		http.Error(w, "Too many phone sessions; sign out or restart the gateway", http.StatusServiceUnavailable)
		return
	}
	g.sessions[id] = session{expires: expires, revoked: make(chan struct{})}
	g.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLife.Seconds()), Expires: expires})
	http.Redirect(w, r, "/app/", http.StatusSeeOther)
}
