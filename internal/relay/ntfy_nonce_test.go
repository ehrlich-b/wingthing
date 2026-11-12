package relay

import (
	"fmt"
	"testing"
)

func TestNtfyNonceDedupIsPerUserAndBounded(t *testing.T) {
	server := NewServer(nil, ServerConfig{})
	if !server.markNtfyNonce("alice", "same") {
		t.Fatal("first nonce was treated as a duplicate")
	}
	if server.markNtfyNonce("alice", "same") {
		t.Fatal("same user's duplicate nonce was accepted")
	}
	if !server.markNtfyNonce("bob", "same") {
		t.Fatal("one user's nonce suppressed another user's notification")
	}

	for index := 0; index < maxNtfyDedupNonces; index++ {
		if !server.markNtfyNonce("alice", fmt.Sprintf("nonce-%d", index)) {
			t.Fatalf("new nonce %d was rejected", index)
		}
	}
	server.ntfyNonceMu.Lock()
	seenCount := len(server.ntfyNonceSeen)
	orderCount := len(server.ntfyNonceOrder)
	server.ntfyNonceMu.Unlock()
	if seenCount != maxNtfyDedupNonces || orderCount != maxNtfyDedupNonces {
		t.Fatalf("nonce cache sizes = %d/%d, want %d", seenCount, orderCount, maxNtfyDedupNonces)
	}
	if !server.markNtfyNonce("alice", "same") {
		t.Fatal("oldest evicted nonce was not admitted again")
	}
}

func TestNtfyClickURLKeepsExecutionWing(t *testing.T) {
	server := NewServer(nil, ServerConfig{BaseURL: "https://wingthing.ai", AppHost: "app.wingthing.ai"})
	for _, test := range []struct{ wing, session, want string }{
		{"studio", "redirect", "https://app.wingthing.ai/#session/studio/redirect"},
		{"studio two", "a/b", "https://app.wingthing.ai/#session/studio%20two/a%2Fb"},
	} {
		if got := server.ntfyClickURL(test.wing, test.session); got != test.want {
			t.Fatalf("ntfyClickURL(%q, %q) = %q; want %q", test.wing, test.session, got, test.want)
		}
	}
}
