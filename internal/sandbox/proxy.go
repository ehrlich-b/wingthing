package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultNetworkConnectionLimit = 256
	proxyConnectionHeadroom       = 16
	proxyDialTimeout              = 10 * time.Second
	proxyReadHeaderTimeout        = 10 * time.Second
	proxyIdleTimeout              = 30 * time.Second
	proxyMaxHeaderBytes           = 16 << 10
)

// EgressEvent is one observed outbound connection attempt. The proxy is the only
// component that knows what a sandboxed agent actually contacted, so these are
// evidence, not just logs.
type EgressEvent struct {
	Host    string // as requested, including port
	Matched bool   // matched the domain allowlist
	Blocked bool   // actually refused (false in observe mode even when unmatched)
}

// ProxyOptions configures a DomainProxy.
type ProxyOptions struct {
	Domains []string
	// Observe records what enforce mode would have refused without refusing it.
	// This is the migration path for tightening egress on existing deployments.
	Observe bool
	// MaxConnections bounds simultaneous CONNECT tunnels. Zero uses the safe
	// default; negative values are rejected.
	MaxConnections int
}

// DomainProxy is an HTTP CONNECT proxy that only allows connections to whitelisted domains.
type DomainProxy struct {
	listener    net.Listener
	server      *http.Server
	domains     map[string]bool // exact matches
	wildcards   []string        // wildcard patterns like "*.anthropic.com"
	observe     bool
	connections chan struct{}
	mu          sync.Mutex
	closed      bool
	events      []EgressEvent
	tunnels     map[net.Conn]struct{}
	lookupIP    func(context.Context, string) ([]net.IPAddr, error)
}

// StartProxy starts an enforcing HTTP CONNECT proxy on localhost with the given
// domain allowlist. Supports exact domains ("api.anthropic.com") and wildcards
// ("*.anthropic.com").
func StartProxy(domains []string) (*DomainProxy, error) {
	return StartProxyWithOptions(ProxyOptions{Domains: domains})
}

// StartPolicyProxy starts the host-side HTTP CONNECT endpoint needed by a
// network-isolated sandbox. Local-only policies use explicit loopback port
// forwarding instead. On Linux, NetworkFull keeps the route-less namespace but
// permits any CONNECT destination through the inherited relay. On macOS it
// preserves the existing unrestricted Seatbelt network policy.
func StartPolicyProxy(need NetworkNeed, domains []string) (*DomainProxy, error) {
	return StartPolicyProxyWithMode(need, domains, "")
}

// StartPolicyProxyWithMode applies the resolved domain-policy mode. Observe
// still forces all traffic through the inherited proxy path, but records rather
// than refuses CONNECT destinations outside the declared domain set.
func StartPolicyProxyWithMode(need NetworkNeed, domains []string, mode string) (*DomainProxy, error) {
	if mode != "" && mode != "enforce" && mode != "observe" {
		return nil, fmt.Errorf("unknown network policy mode %q", mode)
	}
	options := ProxyOptions{Domains: domains, Observe: mode == "observe"}
	switch need {
	case NetworkNone, NetworkLocal:
		return nil, nil
	case NetworkHTTPS:
		if len(domains) == 0 {
			return nil, fmt.Errorf("HTTPS network policy has no allowed domains")
		}
		return StartProxyWithOptions(options)
	case NetworkFull:
		if runtime.GOOS != "linux" {
			return nil, nil
		}
		options.Domains = []string{"*"}
		return StartProxyWithOptions(options)
	default:
		return nil, fmt.Errorf("unknown network policy %d", need)
	}
}

// StartProxyWithOptions starts a proxy with explicit options.
func StartProxyWithOptions(opts ProxyOptions) (*DomainProxy, error) {
	if opts.MaxConnections < 0 {
		return nil, fmt.Errorf("proxy max connections must not be negative")
	}
	maxConnections := opts.MaxConnections
	if maxConnections == 0 {
		maxConnections = defaultNetworkConnectionLimit
	}

	// The namespace side deliberately binds only 127.0.0.1. Keep the host side
	// deterministic too: on systems where localhost resolves to ::1 first, a
	// relay that dials 127.0.0.1 must not depend on dual-stack listener quirks.
	baseListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}
	lis := newConnectionLimitListener(baseListener, maxConnections+proxyConnectionHeadroom)

	p := &DomainProxy{
		listener:    lis,
		domains:     make(map[string]bool),
		observe:     opts.Observe,
		connections: make(chan struct{}, maxConnections),
		tunnels:     make(map[net.Conn]struct{}),
		lookupIP:    net.DefaultResolver.LookupIPAddr,
	}
	for _, d := range opts.Domains {
		d = normalizeDomain(d)
		if strings.HasPrefix(d, "*.") {
			p.wildcards = append(p.wildcards, d[1:]) // store ".anthropic.com"
		} else {
			p.domains[d] = true
		}
	}

	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: proxyReadHeaderTimeout,
		IdleTimeout:       proxyIdleTimeout,
		MaxHeaderBytes:    proxyMaxHeaderBytes,
	}
	go func() {
		if err := p.server.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Printf("domain proxy: serve error: %v", err)
		}
	}()

	log.Printf("domain proxy: listening on %s, %d domains, %d wildcards", lis.Addr(), len(p.domains), len(p.wildcards))
	return p, nil
}

// Port returns the port the proxy is listening on.
func (p *DomainProxy) Port() int {
	return p.listener.Addr().(*net.TCPAddr).Port
}

// Close stops the proxy.
func (p *DomainProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	tunnels := make([]net.Conn, 0, len(p.tunnels))
	for connection := range p.tunnels {
		tunnels = append(tunnels, connection)
	}
	p.mu.Unlock()

	_ = p.server.Close()
	for _, connection := range tunnels {
		_ = connection.Close()
	}
}

// record appends an egress event. Bounded so a long-running session with a noisy
// agent cannot grow this without limit.
const maxEgressEvents = 10000

func (p *DomainProxy) record(e EgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) >= maxEgressEvents {
		copy(p.events, p.events[1:])
		p.events[len(p.events)-1] = e
		return
	}
	p.events = append(p.events, e)
}

// Events returns a copy of the observed outbound connection attempts, oldest first.
func (p *DomainProxy) Events() []EgressEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]EgressEvent(nil), p.events...)
}

// Observing reports whether the proxy logs violations instead of refusing them.
func (p *DomainProxy) Observing() bool { return p.observe }

// allowed checks if a domain is in the allowlist.
func (p *DomainProxy) allowed(host string) bool {
	if p.domains["*"] {
		return true
	}
	// Strip port if present
	domain := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		domain = h
	}
	domain = normalizeDomain(domain)

	if p.domains[domain] {
		return true
	}
	for _, w := range p.wildcards {
		if strings.HasSuffix(domain, w) {
			return true
		}
	}
	return false
}

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func (p *DomainProxy) acquireConnection() bool {
	select {
	case p.connections <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *DomainProxy) trackTunnel(client, target net.Conn) (func(), bool) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, false
	}
	p.tunnels[client] = struct{}{}
	p.tunnels[target] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = client.Close()
			_ = target.Close()
			p.mu.Lock()
			delete(p.tunnels, client)
			delete(p.tunnels, target)
			p.mu.Unlock()
			<-p.connections
		})
	}, true
}

// ServeHTTP handles HTTP CONNECT requests for the proxy.
func (p *DomainProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
		return
	}

	matched := p.allowed(r.Host)
	blocked := !matched && !p.observe
	if blocked {
		p.record(EgressEvent{Host: r.Host, Matched: matched, Blocked: true})
		log.Printf("domain proxy: BLOCKED %s", r.Host)
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}
	if !matched {
		log.Printf("domain proxy: OBSERVED (would block) %s", r.Host)
	}
	if !p.acquireConnection() {
		p.record(EgressEvent{Host: r.Host, Matched: matched, Blocked: false})
		http.Error(w, "proxy connection limit reached", http.StatusServiceUnavailable)
		return
	}
	releaseConnection := true
	defer func() {
		if releaseConnection {
			<-p.connections
		}
	}()

	// Resolve once, reject implicit host-local destinations in enforce mode, and dial the
	// selected IP literal so DNS cannot change between policy and connect.
	target, err := p.dialTarget(r.Context(), r.Host)
	if errors.Is(err, errUnsafeProxyTarget) {
		p.record(EgressEvent{Host: r.Host, Matched: matched, Blocked: true})
		http.Error(w, "resolved target is not allowed", http.StatusForbidden)
		return
	}
	p.record(EgressEvent{Host: r.Host, Matched: matched, Blocked: false})
	if err != nil {
		http.Error(w, fmt.Sprintf("dial: %v", err), http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = target.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	client, bufrw, err := hj.Hijack()
	if err != nil {
		_ = target.Close()
		return
	}
	cleanup, tracked := p.trackTunnel(client, target)
	if !tracked {
		_ = client.Close()
		_ = target.Close()
		return
	}
	releaseConnection = false

	// Send CONNECT 200 response and flush — client waits for this before TLS handshake.
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		cleanup()
		return
	}
	if err := bufrw.Flush(); err != nil {
		cleanup()
		return
	}

	// Bidirectional copy — use bufrw.Reader for client reads in case data is already buffered.
	go func() {
		_, _ = io.Copy(target, bufrw)
		cleanup()
	}()
	go func() {
		_, _ = io.Copy(client, target)
		cleanup()
	}()
}

var errUnsafeProxyTarget = errors.New("proxy target resolves to a host-local address")

var carrierGradeNAT = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

func (p *DomainProxy) dialTarget(ctx context.Context, target string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid target port %q", port)
	}

	var addresses []net.IPAddr
	if literal := net.ParseIP(host); literal != nil {
		addresses = []net.IPAddr{{IP: literal}}
	} else {
		lookupCtx, cancel := context.WithTimeout(ctx, proxyDialTimeout)
		defer cancel()
		addresses, err = p.lookupIP(lookupCtx, host)
		if err != nil {
			return nil, err
		}
	}
	// Observe mode is deliberately non-enforcing, and wildcard policies retain
	// NetworkFull's historical unrestricted meaning. An IP literal is also an
	// explicit operator choice rather than a DNS-rebinding surprise.
	explicitUnsafeTarget := p.domains[normalizeDomain(host)] &&
		(normalizeDomain(host) == "localhost" || net.ParseIP(host) != nil)
	allowUnsafeTarget := p.observe || p.domains["*"] || explicitUnsafeTarget
	dialer := net.Dialer{Timeout: proxyDialTimeout}
	var lastErr error
	unsafe := false
	for _, address := range addresses {
		if !proxyTargetIPAllowed(address.IP, allowUnsafeTarget) {
			unsafe = true
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if unsafe {
		return nil, errUnsafeProxyTarget
	}
	return nil, fmt.Errorf("target resolved to no addresses")
}

func proxyTargetIPAllowed(ip net.IP, allowUnsafeTarget bool) bool {
	if ip == nil {
		return false
	}
	if allowUnsafeTarget {
		return true
	}
	// An allowed public hostname must not become a route to the host's private
	// networks through DNS rebinding. Operators can still make a deliberate
	// private-network choice by listing an IP literal, using network: "*", or
	// using observe mode; declared host-loopback services use local_ports.
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !carrierGradeNAT.Contains(ip)
}

type connectionLimitListener struct {
	net.Listener
	semaphore chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newConnectionLimitListener(listener net.Listener, limit int) net.Listener {
	return &connectionLimitListener{
		Listener:  listener,
		semaphore: make(chan struct{}, limit),
		done:      make(chan struct{}),
	}
}

func (l *connectionLimitListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	select {
	case l.semaphore <- struct{}{}:
		return &connectionLimitConn{Conn: connection, release: func() { <-l.semaphore }}, nil
	case <-l.done:
		_ = connection.Close()
		return nil, net.ErrClosed
	}
}

func (l *connectionLimitListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

type connectionLimitConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

func (c *connectionLimitConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		closeErr = c.Conn.Close()
		c.release()
	})
	return closeErr
}
