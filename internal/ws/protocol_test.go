package ws

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeRouting(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string
	}{
		{"register", WingRegister{Type: TypeWingRegister, WingID: "m1"}, TypeWingRegister},
		{"heartbeat", WingHeartbeat{Type: TypeWingHeartbeat, WingID: "m1"}, TypeWingHeartbeat},
		{"tunnel_req", TunnelRequest{Type: TypeTunnelRequest, WingID: "w1", RequestID: "r1"}, TypeTunnelRequest},
		{"tunnel_res", TunnelResponse{Type: TypeTunnelResponse, RequestID: "r1"}, TypeTunnelResponse},
		{"tunnel_stream", TunnelStream{Type: TypeTunnelStream, RequestID: "r1"}, TypeTunnelStream},
	}

	for _, tt := range tests {
		data, err := json.Marshal(tt.msg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tt.name, err)
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("%s: unmarshal envelope: %v", tt.name, err)
		}
		if env.Type != tt.want {
			t.Errorf("%s: type = %q, want %q", tt.name, env.Type, tt.want)
		}
	}
}

func TestTunnelPurposeIsBoundToInnerType(t *testing.T) {
	for innerType, want := range map[string]string{
		"webrtc.offer":        TunnelPurposeSignal,
		"wing.info":           TunnelPurposeDiscovery,
		"passkey.auth.begin":  TunnelPurposePasskey,
		"passkey.auth.finish": TunnelPurposePasskey,
		"sessions.list":       TunnelPurposeControl,
	} {
		if got := TunnelPurposeForInnerType(innerType); got != want {
			t.Errorf("purpose for %s = %q, want %q", innerType, got, want)
		}
		if !TunnelPurposeMatches(want, innerType) {
			t.Errorf("purpose %q did not match %s", want, innerType)
		}
	}
	if TunnelPurposeMatches(TunnelPurposeSignal, "audit.request") {
		t.Fatal("signaling declaration matched a control payload")
	}
}

func TestWingRegisterFields(t *testing.T) {
	reg := WingRegister{
		Type:           TypeWingRegister,
		WingID:         "mac-A1B2",
		Agents:         []string{"claude", "ollama"},
		Skills:         []string{"compress", "scorer"},
		Labels:         []string{"gpu", "home"},
		Identities:     []string{"bryan", "team-ml"},
		PurposeBinding: true,
		DirectMCP:      true,
		HostedRelay:    HostedRelayDeny,
	}

	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded WingRegister
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Agents) != 2 || decoded.Agents[0] != "claude" {
		t.Errorf("agents = %v, want [claude, ollama]", decoded.Agents)
	}
	if len(decoded.Labels) != 2 || decoded.Labels[1] != "home" {
		t.Errorf("labels = %v, want [gpu, home]", decoded.Labels)
	}
	if len(decoded.Identities) != 2 || decoded.Identities[0] != "bryan" {
		t.Errorf("identities = %v, want [bryan, team-ml]", decoded.Identities)
	}
	if !decoded.PurposeBinding {
		t.Error("purpose binding capability was lost in registration")
	}
	if !decoded.DirectMCP {
		t.Error("direct MCP capability was lost in registration")
	}
	if decoded.HostedRelay != HostedRelayDeny || HostedRelayAllowed(decoded.HostedRelay) {
		t.Fatalf("hosted relay policy = %q allowed=%v", decoded.HostedRelay, HostedRelayAllowed(decoded.HostedRelay))
	}
}

func TestRegisteredMessageCarriesAdditivePasskeyPolicy(t *testing.T) {
	message := RegisteredMsg{
		Type:           TypeRegistered,
		WingID:         "connection-1",
		PasskeyRPID:    "roost.example.test",
		PasskeyOrigins: []string{"https://app.roost.example.test"},
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RegisteredMsg
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PasskeyRPID != message.PasskeyRPID || len(decoded.PasskeyOrigins) != 1 || decoded.PasskeyOrigins[0] != message.PasskeyOrigins[0] {
		t.Fatalf("registered passkey policy = %#v", decoded)
	}
}

func TestHostedRelayWireCompatibility(t *testing.T) {
	for policy, want := range map[string]bool{
		"":               true, // N-1 wings omit the additive field.
		HostedRelayAllow: true,
		HostedRelayDeny:  false,
		"future-value":   false, // unknown explicit policies fail closed.
	} {
		if got := HostedRelayAllowed(policy); got != want {
			t.Errorf("HostedRelayAllowed(%q) = %v, want %v", policy, got, want)
		}
	}
}

func TestValidSessionIDIsSingleBoundedPathComponent(t *testing.T) {
	for _, valid := range []string{"deadbeef", "session-1", "agent_one", "a.b"} {
		if !ValidSessionID(valid) {
			t.Errorf("ValidSessionID(%q) = false", valid)
		}
	}
	for _, invalid := range []string{
		"", ".", "..", "../egg", "a/b", `a\b`, "two words", "x\ncommand",
		strings.Repeat("a", 129),
	} {
		if ValidSessionID(invalid) {
			t.Errorf("ValidSessionID(%q) = true", invalid)
		}
	}
}
