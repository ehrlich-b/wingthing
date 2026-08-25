package sandbox

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
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
}

// DomainProxy is an HTTP CONNECT proxy that only allows connections to whitelisted domains.
type DomainProxy struct {
	listener  net.Listener
	server    *http.Server
	domains   map[string]bool // exact matches
	wildcards []string        // wildcard patterns like "*.anthropic.com"
	observe   bool
	mu        sync.Mutex
	closed    bool
	events    []EgressEvent
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
	switch need {
	case NetworkNone, NetworkLocal:
		return nil, nil
	case NetworkHTTPS:
		if len(domains) == 0 {
			return nil, fmt.Errorf("HTTPS network policy has no allowed domains")
		}
		return StartProxy(domains)
	case NetworkFull:
		if runtime.GOOS != "linux" {
			return nil, nil
		}
		return StartProxy([]string{"*"})
	default:
		return nil, fmt.Errorf("unknown network policy %d", need)
	}
}

// StartProxyWithOptions starts a proxy with explicit options.
func StartProxyWithOptions(opts ProxyOptions) (*DomainProxy, error) {
	// The namespace side deliberately binds only 127.0.0.1. Keep the host side
	// deterministic too: on systems where localhost resolves to ::1 first, a
	// relay that dials 127.0.0.1 must not depend on dual-stack listener quirks.
	lis, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}

	p := &DomainProxy{
		listener: lis,
		domains:  make(map[string]bool),
		observe:  opts.Observe,
	}
	for _, d := range opts.Domains {
		if strings.HasPrefix(d, "*.") {
			p.wildcards = append(p.wildcards, d[1:]) // store ".anthropic.com"
		} else {
			p.domains[d] = true
		}
	}

	p.server = &http.Server{Handler: p}
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
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.server.Close()
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

// ServeHTTP handles HTTP CONNECT requests for the proxy.
func (p *DomainProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
		return
	}

	matched := p.allowed(r.Host)
	blocked := !matched && !p.observe
	p.record(EgressEvent{Host: r.Host, Matched: matched, Blocked: blocked})
	if blocked {
		log.Printf("domain proxy: BLOCKED %s", r.Host)
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
	}
	if !matched {
		log.Printf("domain proxy: OBSERVED (would block) %s", r.Host)
	}

	// Dial the target
	target, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial: %v", err), http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		target.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	client, bufrw, err := hj.Hijack()
	if err != nil {
		target.Close()
		return
	}

	// Send CONNECT 200 response and flush — client waits for this before TLS handshake.
	bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := bufrw.Flush(); err != nil {
		target.Close()
		client.Close()
		return
	}

	// Bidirectional copy — use bufrw.Reader for client reads in case data is already buffered.
	go func() {
		io.Copy(target, bufrw)
		target.Close()
	}()
	go func() {
		io.Copy(client, target)
		client.Close()
	}()
}
