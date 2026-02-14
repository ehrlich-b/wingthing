package ws

import (
	"encoding/json"
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

func TestWingRegisterFields(t *testing.T) {
	reg := WingRegister{
		Type:       TypeWingRegister,
		WingID:  "mac-A1B2",
		Agents:     []string{"claude", "ollama"},
		Skills:     []string{"compress", "scorer"},
		Labels:     []string{"gpu", "home"},
		Identities: []string{"bryan", "team-ml"},
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
}
