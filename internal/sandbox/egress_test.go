package sandbox

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// echoListener starts a throwaway TCP listener so egress tests never touch the
// real network. Returns its host:port.
func echoListener(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return fmt.Sprintf("localhost:%d", lis.Addr().(*net.TCPAddr).Port)
}

func connectVia(t *testing.T, proxyPort int, target string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", proxyPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestProxyRecordsEgressEvents — the proxy already sees every CONNECT and
// currently throws that away, logging only refusals to stderr. It is the only
// place that knows what an agent actually contacted, so it must be evidence.
func TestProxyRecordsEgressEvents(t *testing.T) {
	target := echoListener(t)
	p, err := StartProxy([]string{"localhost"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	if code := connectVia(t, p.Port(), target); code != http.StatusOK {
		t.Fatalf("allowed CONNECT status = %d, want 200", code)
	}
	if code := connectVia(t, p.Port(), "blocked.example:443"); code != http.StatusForbidden {
		t.Fatalf("denied CONNECT status = %d, want 403", code)
	}

	events := p.Events()
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2: %+v", len(events), events)
	}
	if !events[0].Matched || events[0].Blocked {
		t.Errorf("allowed event = %+v, want matched and not blocked", events[0])
	}
	if events[1].Matched || !events[1].Blocked {
		t.Errorf("denied event = %+v, want unmatched and blocked", events[1])
	}
	if events[1].Host != "blocked.example:443" {
		t.Errorf("denied event host = %q", events[1].Host)
	}
}

// TestProxyObserveModeDoesNotBlock — observe mode is the migration path for the
// Linux egress tightening. It must record exactly what enforce mode would have
// refused, while letting the traffic through so real workloads keep running.
func TestProxyObserveModeDoesNotBlock(t *testing.T) {
	target := echoListener(t)
	p, err := StartProxyWithOptions(ProxyOptions{Domains: []string{"nothing-matches.example"}, Observe: true})
	if err != nil {
		t.Fatalf("StartProxyWithOptions: %v", err)
	}
	defer p.Close()

	if code := connectVia(t, p.Port(), target); code != http.StatusOK {
		t.Fatalf("observe-mode CONNECT status = %d, want 200 (must not block)", code)
	}

	events := p.Events()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	if events[0].Matched {
		t.Errorf("event should not match the allowlist: %+v", events[0])
	}
	if events[0].Blocked {
		t.Errorf("observe mode must not block: %+v", events[0])
	}
}

// TestStartProxyDefaultsToEnforce pins that the existing constructor keeps its
// current meaning; observe mode is opt-in only.
func TestStartProxyDefaultsToEnforce(t *testing.T) {
	p, err := StartProxy([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer p.Close()

	if code := connectVia(t, p.Port(), "blocked.example:443"); code != http.StatusForbidden {
		t.Errorf("StartProxy status = %d, want 403 (enforce by default)", code)
	}
}
