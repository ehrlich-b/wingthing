package webrtc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	pion "github.com/pion/webrtc/v4"
)

func TestRandomControlIDFromFailsClosedOnShortRandomness(t *testing.T) {
	if id, err := randomControlIDFrom(strings.NewReader("short")); err == nil || id != "" {
		t.Fatalf("id = %q, err = %v; want empty ID and error", id, err)
	}
	id, err := randomControlIDFrom(strings.NewReader(strings.Repeat("x", 16)))
	if err != nil || len(id) != 32 {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func TestControlClientRoundTrip(t *testing.T) {
	manager := NewPeerManager(nil)
	defer manager.Close()
	manager.OnDC(func(_ string, _ string, _ PeerIdentity, dc *pion.DataChannel) {
		if dc.Label() != control.DirectChannelPrefix+"codex" {
			t.Errorf("channel label = %q", dc.Label())
			return
		}
		dc.OnMessage(func(message pion.DataChannelMessage) {
			var request control.DirectRequest
			if err := json.Unmarshal(message.Data, &request); err != nil {
				t.Error(err)
				return
			}
			responseVersion := control.ContractVersion
			if request.Tool == "future-version" {
				responseVersion = "control.v999"
			}
			payload, _ := json.Marshal(control.DirectResponse{
				Version: responseVersion, ID: request.ID,
				Result: map[string]any{"tool": request.Tool},
			})
			if err := dc.Send(payload); err != nil {
				t.Error(err)
			}
		})
	})
	client, err := NewControlClient("codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	offer, err := client.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := manager.HandleOffer("sender-public-key", "user-1", "u@example.com", "member", nil, offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AcceptAnswer(answer); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	result, isError, err := client.Call(ctx, "terminal_list", json.RawMessage(`{}`))
	if err != nil || isError || result["tool"] != "terminal_list" {
		t.Fatalf("result = %#v, isError = %v, err = %v", result, isError, err)
	}
	if _, _, err := client.Call(ctx, "future-version", json.RawMessage(`{}`)); err == nil || err.Error() != `direct control: unsupported response contract version "control.v999"` {
		t.Fatalf("future response version err = %v", err)
	}
}

func TestControlClientRejectsOversizedRequestBeforeTransport(t *testing.T) {
	client := &ControlClient{
		done: make(chan struct{}), pending: make(map[string]chan control.DirectResponse),
	}
	arguments := json.RawMessage(`{"value":"` + strings.Repeat("x", maxControlMessageBytes) + `"}`)
	if _, _, err := client.Call(context.Background(), "terminal_send", arguments); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized request error = %v", err)
	}
	if len(client.pending) != 0 {
		t.Fatalf("pending requests leaked: %d", len(client.pending))
	}
}
