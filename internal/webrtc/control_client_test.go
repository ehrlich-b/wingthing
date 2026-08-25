package webrtc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	pion "github.com/pion/webrtc/v4"
)

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
			payload, _ := json.Marshal(control.DirectResponse{
				Version: control.ContractVersion, ID: request.ID,
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
	defer client.Close()
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
}
