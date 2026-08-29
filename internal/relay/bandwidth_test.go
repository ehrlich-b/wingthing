package relay

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestCoordinationRateLimitDoesNotConsumeOrConsultRelayQuota(t *testing.T) {
	meter := NewBandwidthMeter(1<<20, 1<<20, nil)
	userID := "direct-only-user"
	meter.counter(userID).Store(freeMonthlyCap)

	if err := meter.Wait(context.Background(), userID, 1); err == nil {
		t.Fatal("ordinary hosted payload ignored the exhausted monthly quota")
	}
	if err := meter.WaitRate(context.Background(), userID, 1024); err != nil {
		t.Fatalf("bounded direct-tier coordination was denied by hosted payload quota: %v", err)
	}
	if got := meter.MonthlyUsage(userID); got != freeMonthlyCap {
		t.Fatalf("coordination changed hosted payload usage to %d", got)
	}
}

func TestRateLimiterIsBoundedAndEvictsOldestIP(t *testing.T) {
	limiter := NewRateLimiter(1, 1)
	for index := 0; index < maxRateLimiterEntries; index++ {
		limiter.getLimiter(fmt.Sprintf("192.0.2.%d", index))
	}
	limiter.getLimiter("198.51.100.1")

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if got := len(limiter.limiters); got != maxRateLimiterEntries {
		t.Fatalf("limiter entries = %d, want %d", got, maxRateLimiterEntries)
	}
	if got := len(limiter.order); got != maxRateLimiterEntries {
		t.Fatalf("limiter order = %d, want %d", got, maxRateLimiterEntries)
	}
	if _, exists := limiter.limiters["192.0.2.0"]; exists {
		t.Fatal("oldest IP was not evicted")
	}
	if _, exists := limiter.limiters["198.51.100.1"]; !exists {
		t.Fatal("new IP was not retained")
	}
}

func TestClientIPUsesOnlyValidForwardedAddresses(t *testing.T) {
	tests := []struct {
		name        string
		remoteAddr  string
		flyClientIP string
		forwarded   string
		want        string
	}{
		{name: "trusted proxy fly", remoteAddr: "10.0.0.9:1234", flyClientIP: " 2001:db8::1 ", forwarded: "198.51.100.2", want: "2001:db8::1"},
		{name: "trusted proxy forwarded", remoteAddr: "[fd00::9]:1234", forwarded: " 198.51.100.2, 10.0.0.1", want: "198.51.100.2"},
		{name: "invalid fly falls through", remoteAddr: "127.0.0.1:1234", flyClientIP: "not-an-ip", forwarded: "198.51.100.3", want: "198.51.100.3"},
		{name: "invalid forwarded falls back", remoteAddr: "10.0.0.9:1234", forwarded: "attacker-controlled", want: "10.0.0.9"},
		{name: "public peer cannot spoof fly", remoteAddr: "203.0.113.9:1234", flyClientIP: "198.51.100.7", want: "203.0.113.9"},
		{name: "public peer cannot spoof forwarded", remoteAddr: "203.0.113.9:1234", forwarded: "198.51.100.8", want: "203.0.113.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://wingthing.test/", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Fly-Client-IP", test.flyClientIP)
			request.Header.Set("X-Forwarded-For", test.forwarded)
			if got := clientIP(request); got != test.want {
				t.Fatalf("clientIP() = %q, want %q", got, test.want)
			}
		})
	}
}
