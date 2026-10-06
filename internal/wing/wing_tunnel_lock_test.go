package wing

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/tunnel"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestTunnelResponseBackpressureDoesNotBlockWingConfigReload(t *testing.T) {
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderPublic := base64.StdEncoding.EncodeToString(senderKey.PublicKey().Bytes())
	senderGCM, err := auth.DeriveSharedKey(senderKey,
		base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes()), "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := auth.Encrypt(senderGCM, []byte(`{"type":"wing.info"}`))
	if err != nil {
		t.Fatal(err)
	}

	enteredWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	done := make(chan struct{})
	wingCfg := &config.WingConfig{Locked: false, Labels: []string{"initial"}}
	allowed := []config.AllowKey(nil)
	wingEggCfg := &egg.EggConfig{}
	var wingEggMu sync.Mutex
	client := &ws.Client{Hostname: "test-wing", Platform: "test", Version: "test"}

	go func() {
		defer close(done)
		tunnel.HandleTunnelRequest(tunnel.References{Version: "dev", WingCfg: wingCfg, WingCfgMu: &wingCfgMu, AllowedKeys: &allowed, WingEggMu: &wingEggMu, WingEggCfg: &wingEggCfg, ListAliveEggSessions: ListAliveEggSessions, ResizeBrowserInput: resizeBrowserInput, KillSessionsViolatingACLs: killSessionsViolatingACLs}, context.Background(), &config.Config{Dir: t.TempDir()}, ws.TunnelRequest{
			RequestID: "request-1", SenderPub: senderPublic, SenderUserID: "owner-1",
			SenderEmail: "owner@example.com", SenderOrgRole: "owner", Payload: payload,
		}, func(any) error {
			close(enteredWrite)
			<-releaseWrite
			return nil
		}, auth.NewAuthCache(), auth.NewChallengeCache(), auth.PasskeyPolicy{}, serverKey, t.TempDir(), false, false, client, nil, &sync.Map{})
	}()

	select {
	case <-enteredWrite:
	case <-time.After(2 * time.Second):
		close(releaseWrite)
		t.Fatal("tunnel handler did not reach the blocked response")
	}

	lockAcquired := make(chan struct{})
	go func() {
		wingCfgMu.Lock()
		wingCfg.Labels = []string{"reloaded"}
		wingCfgMu.Unlock()
		close(lockAcquired)
	}()
	select {
	case <-lockAcquired:
	case <-time.After(500 * time.Millisecond):
		close(releaseWrite)
		t.Fatal("a blocked tunnel response held wingCfgMu and prevented reload")
	}
	close(releaseWrite)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel handler did not finish after response unblocked")
	}
}

func TestTunnelRejectsPathShapedSessionIDBeforeFilesystemAccess(t *testing.T) {
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderPublic := base64.StdEncoding.EncodeToString(senderKey.PublicKey().Bytes())
	senderGCM, err := auth.DeriveSharedKey(senderKey,
		base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes()), "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := auth.Encrypt(senderGCM, []byte(`{"type":"pty.kill","session_id":"../../victim"}`))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	cfg := &config.Config{Dir: filepath.Join(root, ".wingthing")}
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(victim, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := []config.AllowKey(nil)
	wingCfg := &config.WingConfig{}
	wingEggCfg := &egg.EggConfig{}
	var wingEggMu sync.Mutex
	var responses int
	tunnel.HandleTunnelRequest(tunnel.References{Version: "dev", WingCfg: wingCfg, WingCfgMu: &wingCfgMu, AllowedKeys: &allowed, WingEggMu: &wingEggMu, WingEggCfg: &wingEggCfg, ListAliveEggSessions: ListAliveEggSessions, ResizeBrowserInput: resizeBrowserInput, KillSessionsViolatingACLs: killSessionsViolatingACLs}, context.Background(), cfg, ws.TunnelRequest{
		RequestID: "traversal", SenderPub: senderPublic, SenderUserID: "owner-1", SenderOrgRole: "owner", Payload: payload,
	}, func(any) error {
		responses++
		return nil
	}, auth.NewAuthCache(), auth.NewChallengeCache(), auth.PasskeyPolicy{}, serverKey, root, false, false, &ws.Client{}, nil, &sync.Map{})

	if responses != 1 {
		t.Fatalf("responses = %d, want one rejection", responses)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("path-shaped session ID touched victim: data=%q err=%v", data, err)
	}
}

func TestTunnelRejectsMissingCoordinatorIdentity(t *testing.T) {
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPublic := base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes())
	senderPublic := base64.StdEncoding.EncodeToString(senderKey.PublicKey().Bytes())
	senderGCM, err := auth.DeriveSharedKey(senderKey, serverPublic, "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := auth.Encrypt(senderGCM, []byte(`{"type":"wing.info"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response ws.TunnelResponse
	wingCfg := &config.WingConfig{}
	allowed := []config.AllowKey(nil)
	wingEggCfg := &egg.EggConfig{}
	var wingEggMu sync.Mutex
	tunnel.HandleTunnelRequest(tunnel.References{Version: "dev", WingCfg: wingCfg, WingCfgMu: &wingCfgMu, AllowedKeys: &allowed, WingEggMu: &wingEggMu, WingEggCfg: &wingEggCfg, ListAliveEggSessions: ListAliveEggSessions, ResizeBrowserInput: resizeBrowserInput, KillSessionsViolatingACLs: killSessionsViolatingACLs}, context.Background(), &config.Config{Dir: t.TempDir()}, ws.TunnelRequest{
		RequestID: "anonymous", SenderPub: senderPublic, Payload: payload,
	}, func(message any) error {
		var ok bool
		response, ok = message.(ws.TunnelResponse)
		if !ok {
			t.Fatalf("response type = %T", message)
		}
		return nil
	}, auth.NewAuthCache(), auth.NewChallengeCache(), auth.PasskeyPolicy{}, serverKey, t.TempDir(), false, false, &ws.Client{}, nil, &sync.Map{})
	plaintext, err := auth.Decrypt(senderGCM, response.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plaintext), "authenticated user identity required") {
		t.Fatalf("anonymous tunnel response = %s", plaintext)
	}
}

func TestFileDownloadOwnerDenialUsesTerminalStreamError(t *testing.T) {
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	senderPublic := base64.StdEncoding.EncodeToString(senderKey.PublicKey().Bytes())
	senderGCM, err := auth.DeriveSharedKey(senderKey, base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes()), "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := auth.Encrypt(senderGCM, []byte(`{"type":"file.download","session_id":"missing-session","path":"result.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	allowed := []config.AllowKey(nil)
	wingCfg := &config.WingConfig{Paths: config.PathList{{Path: t.TempDir()}}}
	wingEggCfg := &egg.EggConfig{}
	var wingEggMu sync.Mutex
	var response ws.TunnelStream
	tunnel.HandleTunnelRequest(tunnel.References{Version: "dev", WingCfg: wingCfg, WingCfgMu: &wingCfgMu, AllowedKeys: &allowed, WingEggMu: &wingEggMu, WingEggCfg: &wingEggCfg, ListAliveEggSessions: ListAliveEggSessions, ResizeBrowserInput: resizeBrowserInput, KillSessionsViolatingACLs: killSessionsViolatingACLs}, context.Background(), &config.Config{Dir: t.TempDir()}, ws.TunnelRequest{
		RequestID: "download-denied", SenderPub: senderPublic, SenderUserID: "alice", SenderEmail: "alice@example.com", SenderOrgRole: "member", Payload: payload,
	}, func(message any) error {
		var ok bool
		response, ok = message.(ws.TunnelStream)
		if !ok {
			t.Fatalf("response type = %T, want ws.TunnelStream", message)
		}
		return nil
	}, auth.NewAuthCache(), auth.NewChallengeCache(), auth.PasskeyPolicy{}, serverKey, t.TempDir(), false, false, &ws.Client{}, nil, &sync.Map{})
	if !response.Done || response.RequestID != "download-denied" {
		t.Fatalf("stream response = %#v", response)
	}
	plaintext, err := auth.Decrypt(senderGCM, response.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plaintext), "session not found or not owned by caller") {
		t.Fatalf("download denial = %s", plaintext)
	}
}
