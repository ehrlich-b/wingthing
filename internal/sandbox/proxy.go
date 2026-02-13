package sandbox

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// DomainProxy is an HTTP CONNECT proxy that only allows connections to whitelisted domains.
type DomainProxy struct {
	listener net.Listener
	server   *http.Server
	domains  map[string]bool // exact matches
	wildcards []string       // wildcard patterns like "*.anthropic.com"
	mu       sync.Mutex
	closed   bool
}

// StartProxy starts an HTTP CONNECT proxy on localhost with the given domain allowlist.
// Supports exact domains ("api.anthropic.com") and wildcards ("*.anthropic.com").
func StartProxy(domains []string) (*DomainProxy, error) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}

	p := &DomainProxy{
		listener: lis,
		domains:  make(map[string]bool),
	}
	for _, d := range domains {
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

// allowed checks if a domain is in the allowlist.
func (p *DomainProxy) allowed(host string) bool {
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

	if !p.allowed(r.Host) {
		log.Printf("domain proxy: BLOCKED %s", r.Host)
		http.Error(w, "domain not allowed", http.StatusForbidden)
		return
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

	w.WriteHeader(http.StatusOK)
	client, _, err := hj.Hijack()
	if err != nil {
		target.Close()
		return
	}

	// Bidirectional copy
	go func() {
		io.Copy(target, client)
		target.Close()
	}()
	go func() {
		io.Copy(client, target)
		client.Close()
	}()
}
