package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func TestDirectMCPExecutesOnAuthenticatedWing(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}
	wingCfg := &config.WingConfig{}
	manager := webrtcpkg.NewPeerManager(nil)
	defer manager.Close()
	manager.OnDC(func(_ string, _ string, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
		serveDirectMCPChannel(cfg, wingCfg, t.TempDir(), nil, identity, dc)
	})
	client, err := webrtcpkg.NewControlClient("codex", nil)
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
	answer, err := manager.HandleOffer("native-client-public-key", "user-direct", "owner@example.com", "owner", nil, offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AcceptAnswer(answer); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	result, isError, err := client.Call(ctx, "wingthing_capabilities", json.RawMessage(`{}`))
	if err != nil || isError {
		t.Fatalf("capabilities error = %v, isError = %v, result = %#v", err, isError, result)
	}
	if result["actor"] != "codex" || result["principal"] != roostSessionPrincipal("user-direct") {
		t.Fatalf("authenticated control identity = %#v", result)
	}
}

func TestDirectMCPFailsClosedForLockedWing(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}
	wingCfg := &config.WingConfig{Locked: true}
	manager := webrtcpkg.NewPeerManager(nil)
	defer manager.Close()
	manager.OnDC(func(_ string, _ string, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
		serveDirectMCPChannel(cfg, wingCfg, t.TempDir(), nil, identity, dc)
	})
	client, err := webrtcpkg.NewControlClient("codex", nil)
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
	answer, err := manager.HandleOffer("native-client-public-key", "user-direct", "owner@example.com", "owner", nil, offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AcceptAnswer(answer); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Call(ctx, "wingthing_capabilities", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "local lock policy") {
		t.Fatalf("locked direct call error = %v", err)
	}
}
