package sandbox

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"testing"
	"time"
)

func TestProxyStartAndClose(t *testing.T) {
	p, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	port := p.Port()
	if port == 0 {
		t.Fatal("proxy port is 0")
	}
	p.Close()
	// Double close should be safe
	p.Close()
}

func TestPolicyProxyPreservesPlatformFullNetworkSemantics(t *testing.T) {
	p, err := StartPolicyProxy(NetworkFull, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		if p == nil {
			t.Fatal("Linux NetworkFull needs the wildcard relay inside CLONE_NEWNET")
		}
		p.Close()
		return
	}
	if p != nil {
		p.Close()
		t.Fatalf("%s NetworkFull should preserve unrestricted platform networking", runtime.GOOS)
	}
}

func TestPolicyProxyObserveModeIsExplicit(t *testing.T) {
	p, err := StartPolicyProxyWithMode(NetworkHTTPS, []string{"allowed.example"}, "observe")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.Observing() {
		t.Fatal("observe mode created an enforcing proxy")
	}
	if _, err := StartPolicyProxyWithMode(NetworkHTTPS, []string{"allowed.example"}, "unknown"); err == nil {
		t.Fatal("unknown mode was accepted")
	}
}

func TestProxyAllowed(t *testing.T) {
	tests := []struct {
		domains []string
		host    string
		want    bool
	}{
		{[]string{"example.com"}, "example.com", true},
		{[]string{"example.com"}, "example.com:443", true},
		{[]string{"Example.COM"}, "EXAMPLE.com.:443", true},
		{[]string{"example.com"}, "evil.com", false},
		{[]string{"example.com"}, "evil.com:443", false},
		{[]string{"*.anthropic.com"}, "api.anthropic.com", true},
		{[]string{"*.anthropic.com"}, "api.anthropic.com:443", true},
		{[]string{"*.anthropic.com"}, "evil.com", false},
		{[]string{"example.com", "other.com"}, "other.com", true},
		{[]string{"example.com", "other.com"}, "neither.com", false},
	}
	for _, tt := range tests {
		p, err := StartProxy(tt.domains)
		if err != nil {
			t.Fatalf("StartProxy: %v", err)
		}
		got := p.allowed(tt.host)
		p.Close()
		if got != tt.want {
			t.Errorf("allowed(%q) with domains %v = %v, want %v", tt.host, tt.domains, got, tt.want)
		}
	}
}

func TestProxyRejectsNegativeConnectionLimit(t *testing.T) {
	if _, err := StartProxyWithOptions(ProxyOptions{
		Domains:        []string{"example.com"},
		MaxConnections: -1,
	}); err == nil {
		t.Fatal("negative connection limit was accepted")
	}
}

func TestProxyEventBufferDropsExactlyTheOldestEvent(t *testing.T) {
	p := &DomainProxy{}
	for i := 0; i <= maxEgressEvents; i++ {
		p.record(EgressEvent{Host: fmt.Sprintf("host-%d", i)})
	}
	events := p.Events()
	if len(events) != maxEgressEvents {
		t.Fatalf("event count = %d, want %d", len(events), maxEgressEvents)
	}
	if events[0].Host != "host-1" {
		t.Fatalf("oldest retained event = %q, want host-1", events[0].Host)
	}
	if events[len(events)-1].Host != fmt.Sprintf("host-%d", maxEgressEvents) {
		t.Fatalf("newest retained event = %q", events[len(events)-1].Host)
	}
}

func TestProxyRejectsNonCONNECT(t *testing.T) {
	p, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/", p.Port()))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestProxyBlocksDeniedDomain(t *testing.T) {
	p, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	// Send a CONNECT to a denied domain
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CONNECT to denied domain status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestProxyBlocksAllowedNameThatRebindsToLoopback(t *testing.T) {
	p, err := StartProxy([]string{"rebind.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprint(conn, "CONNECT rebind.example:443 HTTP/1.1\r\nHost: rebind.example:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound CONNECT status = %d, want forbidden", response.StatusCode)
	}
	events := p.Events()
	if len(events) != 1 || !events[0].Matched || !events[0].Blocked {
		t.Fatalf("rebound egress events = %#v", events)
	}
}

func TestObserveProxyRecordsButAllowsNamedLoopbackTarget(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartProxyWithOptions(ProxyOptions{Domains: []string{"observe.example"}, Observe: true})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}

	if code := connectVia(t, p.Port(), net.JoinHostPort("observe.example", port)); code != http.StatusOK {
		t.Fatalf("observe rebound CONNECT status = %d, want OK", code)
	}
	events := p.Events()
	if len(events) != 1 || !events[0].Matched || events[0].Blocked {
		t.Fatalf("observe rebound egress events = %#v", events)
	}
}

func TestProxyTargetAddressPolicyPreservesExplicitLocalhostCompatibility(t *testing.T) {
	for _, test := range []struct {
		ip                string
		allowUnsafeTarget bool
		want              bool
	}{
		{ip: "127.0.0.1", want: false},
		{ip: "::1", want: false},
		{ip: "169.254.169.254", want: false},
		{ip: "10.0.0.5", want: false},
		{ip: "172.16.0.5", want: false},
		{ip: "192.168.0.5", want: false},
		{ip: "100.64.0.5", want: false},
		{ip: "fd00::5", want: false},
		{ip: "127.0.0.1", allowUnsafeTarget: true, want: true},
		{ip: "169.254.169.254", allowUnsafeTarget: true, want: true},
		{ip: "10.0.0.5", allowUnsafeTarget: true, want: true},
		{ip: "100.64.0.5", allowUnsafeTarget: true, want: true},
		{ip: "203.0.113.5", want: true},
	} {
		if got := proxyTargetIPAllowed(net.ParseIP(test.ip), test.allowUnsafeTarget); got != test.want {
			t.Errorf("proxyTargetIPAllowed(%s, %v) = %v, want %v", test.ip, test.allowUnsafeTarget, got, test.want)
		}
	}
}

// TestProxyCONNECTFlush is the test that would have caught the "http no work" bug.
// The bug was: after hijacking, the 200 response wasn't flushed before starting
// bidirectional copy. Go/Node HTTP clients read the 200 before starting TLS,
// but the response was stuck in the bufio.Writer. curl happened to work because
// it has different timing.
func TestProxyCONNECTFlush(t *testing.T) {
	// Generate a self-signed cert for localhost at runtime
	tlsCert := generateTestCert(t)

	// Start a TLS HTTP server that the proxy will forward to
	tlsLis, err := tls.Listen("tcp", "localhost:0", &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatalf("listen TLS server: %v", err)
	}
	defer func() { _ = tlsLis.Close() }()
	_, echoPort, _ := net.SplitHostPort(tlsLis.Addr().String())

	// Serve a simple HTTP response over TLS
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		_ = http.Serve(tlsLis, mux)
	}()

	// Start proxy allowing localhost
	p, err := StartProxy([]string{"localhost"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	// Use Go's HTTP client with the proxy — this is what Node/Go agents do.
	// This exercises the CONNECT flow exactly as a real agent would.
	proxyURL, _ := url.Parse(fmt.Sprintf("http://localhost:%d", p.Port()))
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("https://localhost:%s/", echoPort))
	if err != nil {
		// The pre-fix code would hang here because the 200 CONNECT response
		// was never flushed, so the Go HTTP client would wait forever.
		t.Fatalf("GET through proxy failed (this is the flush bug): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

// TestProxyCONNECTRawTCP verifies the CONNECT handshake at the TCP level.
// Sends CONNECT, reads the 200 response, then does a plaintext exchange through the tunnel.
func TestProxyCONNECTRawTCP(t *testing.T) {
	// Start a plain TCP echo server
	echoLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = echoLis.Close() }()
	echoAddr := echoLis.Addr().String()

	go func() {
		conn, err := echoLis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(conn, conn)
	}()

	host, _, _ := net.SplitHostPort(echoAddr)
	p, err := StartProxy([]string{host})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	// Send CONNECT
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr); err != nil {
		t.Fatal(err)
	}

	// Read 200 response — the flush bug would cause this to hang
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read CONNECT response (flush bug?): %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	// Now the connection is a raw tunnel — send data through
	testData := "hello through tunnel\n"
	if _, err := fmt.Fprint(conn, testData); err != nil {
		t.Fatal(err)
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if line != testData {
		t.Errorf("tunnel echo = %q, want %q", line, testData)
	}
}

// TestProxyCONNECTBadGateway verifies proxy returns 502 when target is unreachable.
func TestProxyCONNECTBadGateway(t *testing.T) {
	p, err := StartProxy([]string{"localhost"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	// CONNECT to a port nothing is listening on
	if _, err := fmt.Fprintf(conn, "CONNECT localhost:1 HTTP/1.1\r\nHost: localhost:1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("CONNECT to unreachable status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

// TestProxyConcurrent exercises multiple simultaneous CONNECT tunnels.
func TestProxyConcurrent(t *testing.T) {
	echoLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = echoLis.Close() }()
	echoAddr := echoLis.Addr().String()

	// Accept multiple connections
	go func() {
		for {
			conn, err := echoLis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	host, _, _ := net.SplitHostPort(echoAddr)
	p, err := StartProxy([]string{host})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	// Launch 10 concurrent tunnels
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
			if err != nil {
				errs <- fmt.Errorf("conn %d: dial: %v", id, err)
				return
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				errs <- fmt.Errorf("conn %d: deadline: %v", id, err)
				return
			}

			if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr); err != nil {
				errs <- fmt.Errorf("conn %d: CONNECT write: %v", id, err)
				return
			}
			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				errs <- fmt.Errorf("conn %d: read response: %v", id, err)
				return
			}
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("conn %d: status %d", id, resp.StatusCode)
				return
			}

			msg := fmt.Sprintf("msg-%d\n", id)
			if _, err := fmt.Fprint(conn, msg); err != nil {
				errs <- fmt.Errorf("conn %d: echo write: %v", id, err)
				return
			}
			line, err := reader.ReadString('\n')
			if err != nil {
				errs <- fmt.Errorf("conn %d: read echo: %v", id, err)
				return
			}
			if line != msg {
				errs <- fmt.Errorf("conn %d: got %q, want %q", id, line, msg)
				return
			}
			errs <- nil
		}(i)
	}

	for i := 0; i < 10; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestProxyBoundsAndClosesActiveTunnels(t *testing.T) {
	echoLis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = echoLis.Close() }()
	echoAddr := echoLis.Addr().String()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			connection, acceptErr := echoLis.Accept()
			if acceptErr != nil {
				return
			}
			accepted <- connection
		}
	}()

	host, _, _ := net.SplitHostPort(echoAddr)
	p, err := StartProxyWithOptions(ProxyOptions{
		Domains:        []string{host},
		MaxConnections: 1,
	})
	if err != nil {
		t.Fatalf("StartProxyWithOptions: %v", err)
	}
	defer p.Close()

	first, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial first proxy connection: %v", err)
	}
	defer func() { _ = first.Close() }()
	if err := first.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(first, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr); err != nil {
		t.Fatal(err)
	}
	firstResp, err := http.ReadResponse(bufio.NewReader(first), nil)
	if err != nil {
		t.Fatalf("read first CONNECT response: %v", err)
	}
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first CONNECT status = %d, want %d", firstResp.StatusCode, http.StatusOK)
	}
	var upstream net.Conn
	select {
	case upstream = <-accepted:
		defer func() { _ = upstream.Close() }()
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not establish first upstream connection")
	}

	second, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("dial second proxy connection: %v", err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(second, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr); err != nil {
		t.Fatal(err)
	}
	secondResp, err := http.ReadResponse(bufio.NewReader(second), nil)
	if err != nil {
		t.Fatalf("read second CONNECT response: %v", err)
	}
	if secondResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second CONNECT status = %d, want %d", secondResp.StatusCode, http.StatusServiceUnavailable)
	}

	p.Close()
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("active tunnel remained open after proxy close")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := upstream.Read(make([]byte, 1)); err == nil {
		t.Fatal("upstream tunnel remained open after proxy close")
	}
}

// TestProxySNIFiltering simulates SNI-based domain filtering through the proxy.
// The proxy allows/denies based on the CONNECT host, but this test verifies
// the TLS SNI the server sees matches what the client requested — proving the
// domain filtering proxy could inspect SNI if extended.
// All CPU-local: fake TLS servers with generated certs, no real DNS.
func TestProxySNIFiltering(t *testing.T) {
	// TLS server that records the SNI ServerName from each handshake
	type sniResult struct {
		serverName string
	}
	sniCh := make(chan sniResult, 10)

	cert := generateTestCert(t)
	tlsLis, err := tls.Listen("tcp", "localhost:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			sniCh <- sniResult{serverName: hello.ServerName}
			return nil, nil // use default config
		},
	})
	if err != nil {
		t.Fatalf("listen TLS: %v", err)
	}
	defer func() { _ = tlsLis.Close() }()
	_, tlsPort, _ := net.SplitHostPort(tlsLis.Addr().String())

	// Accept connections — must complete TLS handshake before closing
	go func() {
		for {
			conn, err := tlsLis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				if tc, ok := c.(*tls.Conn); ok {
					_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
					_ = tc.Handshake()
				}
			}(conn)
		}
	}()

	// Proxy allows api.anthropic.com, blocks evil.com
	p, err := StartProxy([]string{"api.anthropic.com", "localhost"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse(fmt.Sprintf("http://localhost:%d", p.Port()))

	// Test 1: Allowed domain through CONNECT — proxy lets it through,
	// TLS handshake happens, server sees SNI
	t.Run("allowed_domain_SNI", func(t *testing.T) {
		// CONNECT to api.anthropic.com:PORT but actually connect to localhost
		// (since DNS doesn't resolve, we CONNECT to localhost but set SNI)
		conn, err := net.DialTimeout("tcp", proxyURL.Host, 2*time.Second)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// CONNECT to localhost (the actual port) with the allowed domain
		target := fmt.Sprintf("localhost:%s", tlsPort)
		if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("CONNECT response: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("CONNECT status = %d", resp.StatusCode)
		}

		// Now do TLS handshake with a specific ServerName
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         "api.anthropic.com",
			InsecureSkipVerify: true,
		})
		defer func() { _ = tlsConn.Close() }()
		if err := tlsConn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		err = tlsConn.Handshake()
		if err != nil {
			t.Fatalf("TLS handshake: %v", err)
		}

		// Server should have seen the SNI
		select {
		case result := <-sniCh:
			if result.serverName != "api.anthropic.com" {
				t.Errorf("SNI = %q, want %q", result.serverName, "api.anthropic.com")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for SNI result")
		}
	})

	// Test 2: Blocked domain — proxy rejects the CONNECT
	t.Run("blocked_domain_rejected", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", proxyURL.Host, 2*time.Second)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}

		if _, err := fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("CONNECT response: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("blocked CONNECT status = %d, want 403", resp.StatusCode)
		}
	})

	// Test 3: Wildcard domain matching
	t.Run("wildcard_allowed", func(t *testing.T) {
		p2, err := StartProxy([]string{"*.anthropic.com"})
		if err != nil {
			t.Fatalf("StartProxy: %v", err)
		}
		defer p2.Close()

		// api.anthropic.com should match *.anthropic.com
		if !p2.allowed("api.anthropic.com:443") {
			t.Error("api.anthropic.com:443 should match *.anthropic.com")
		}
		if !p2.allowed("sentry.anthropic.com:443") {
			t.Error("sentry.anthropic.com should match *.anthropic.com")
		}
		if p2.allowed("anthropic.com:443") {
			t.Error("anthropic.com should NOT match *.anthropic.com (must be subdomain)")
		}
		if p2.allowed("evil.anthropic.com.evil.com:443") {
			t.Error("evil.anthropic.com.evil.com should NOT match *.anthropic.com")
		}
	})

	// Test 4: Full TLS through proxy with Go HTTP client (the real agent path)
	t.Run("go_http_client_through_proxy", func(t *testing.T) {
		// Start a proper HTTPS server
		httpsCert := generateTestCert(t)
		httpsSrv := &http.Server{
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{httpsCert},
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("hello-from-" + r.Host))
			}),
		}
		httpsLis, err := tls.Listen("tcp", "localhost:0", httpsSrv.TLSConfig)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = httpsLis.Close() }()
		go func() { _ = httpsSrv.Serve(httpsLis) }()
		_, httpsPort, _ := net.SplitHostPort(httpsLis.Addr().String())

		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
			Timeout: 5 * time.Second,
		}

		resp, err := client.Get(fmt.Sprintf("https://localhost:%s/", httpsPort))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		got := string(body)
		want := "hello-from-localhost:" + httpsPort
		if got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}

// generateTestCert creates a self-signed TLS certificate for localhost.
func generateTestCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"test"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}
