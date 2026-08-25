package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

func TestResolveDirectMCPPolicyCompatibilityMatrix(t *testing.T) {
	home := t.TempDir()
	openPath := filepath.Join(home, "open")
	memberPath := filepath.Join(home, "member")
	otherPath := filepath.Join(home, "other")
	wingCfg := &config.WingConfig{
		Org: "test-org",
		Paths: config.PathList{
			{Path: openPath},
			{Path: memberPath, Members: []string{"member@example.com"}},
			{Path: otherPath, Members: []string{"other@example.com"}},
		},
		Admins: []string{"wing-admin@example.com"},
	}

	tests := []struct {
		name             string
		identity         webrtcpkg.PeerIdentity
		sharedHost       bool
		wantRole         string
		wantPaths        []string
		wantEnforcePaths bool
		wantSharedHost   bool
		wantSealedFS     bool
		wantErr          string
	}{
		{
			name:     "personal or org owner keeps administrative path visibility",
			identity: webrtcpkg.PeerIdentity{UserID: "owner", Email: "owner@example.com", OrgRole: "owner"},
			wantRole: "owner", wantPaths: []string{openPath, memberPath, otherPath},
		},
		{
			name:     "ordinary org member is owner and path scoped",
			identity: webrtcpkg.PeerIdentity{UserID: "member", Email: "member@example.com", OrgRole: "member"},
			wantRole: "member", wantPaths: []string{openPath, memberPath}, wantEnforcePaths: true,
			wantSharedHost: true, wantSealedFS: true,
		},
		{
			name:     "wing admin override preserves existing owner visibility",
			identity: webrtcpkg.PeerIdentity{UserID: "admin", Email: "wing-admin@example.com", OrgRole: "member"},
			wantRole: "admin", wantPaths: []string{openPath, memberPath, otherPath},
		},
		{
			name:       "shared roost legacy empty role remains a sealed member",
			identity:   webrtcpkg.PeerIdentity{UserID: "shared-user", Email: "member@example.com"},
			sharedHost: true, wantRole: "member", wantPaths: []string{openPath, memberPath},
			wantEnforcePaths: true, wantSharedHost: true, wantSealedFS: true,
		},
		{
			name:     "empty role outside a shared roost fails closed",
			identity: webrtcpkg.PeerIdentity{UserID: "unknown", Email: "unknown@example.com"},
			wantErr:  "missing authenticated organization role",
		},
		{
			name:     "unknown role fails closed",
			identity: webrtcpkg.PeerIdentity{UserID: "outsider", Email: "outsider@example.com", OrgRole: "outsider"},
			wantErr:  "unsupported authenticated organization role",
		},
		{
			name:     "missing user fails closed",
			identity: webrtcpkg.PeerIdentity{Email: "owner@example.com", OrgRole: "owner"},
			wantErr:  "authenticated user identity is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := resolveDirectMCPPolicy(wingCfg, home, tt.sharedHost, tt.identity)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolve error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy.role != tt.wantRole {
				t.Errorf("role = %q, want %q", policy.role, tt.wantRole)
			}
			if strings.Join(policy.allowedPaths, "\x00") != strings.Join(tt.wantPaths, "\x00") {
				t.Errorf("allowed paths = %#v, want %#v", policy.allowedPaths, tt.wantPaths)
			}
			if policy.enforcePathBounds != tt.wantEnforcePaths {
				t.Errorf("enforce paths = %v, want %v", policy.enforcePathBounds, tt.wantEnforcePaths)
			}
			if policy.identity.SharedHost != tt.wantSharedHost || policy.identity.SealedFS != tt.wantSealedFS {
				t.Errorf("identity boundary = %#v, want shared=%v sealed=%v", policy.identity, tt.wantSharedHost, tt.wantSealedFS)
			}
			if policy.maxSessions <= 0 || policy.maxSpawnsPerHour <= 0 {
				t.Errorf("direct policy must be bounded: %#v", policy)
			}
			if policy.grants == nil {
				t.Fatal("direct grants must be explicit, not nil/full-access sentinel")
			}
			for _, tool := range control.ToolsForAuthority(control.SurfaceDirectMCP, control.AuthorityWing) {
				if !policy.grants[tool.Grant] {
					t.Errorf("default policy omitted direct grant %q for %s", tool.Grant, tool.Name)
				}
			}
		})
	}
}

func TestResolveDirectMCPPolicyHonorsAdditiveWingRestrictions(t *testing.T) {
	wingCfg := &config.WingConfig{DirectMCP: &config.DirectMCPConfig{
		AllowGrants:      []string{"capabilities.read"},
		MaxSessions:      2,
		MaxSpawnsPerHour: 3,
	}}
	policy, err := resolveDirectMCPPolicy(wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{
		UserID: "owner", Email: "owner@example.com", OrgRole: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.grants["capabilities.read"] || policy.grants["terminal.read"] {
		t.Fatalf("restricted grants = %#v", policy.grants)
	}
	if policy.maxSessions != 2 || policy.maxSpawnsPerHour != 3 {
		t.Fatalf("restricted bounds = sessions:%d spawns:%d", policy.maxSessions, policy.maxSpawnsPerHour)
	}
	server := &localMCPServer{
		cfg: &config.Config{Dir: t.TempDir()}, principal: "owner", actor: "codex",
		surface: control.SurfaceDirectMCP, grants: policy.grants,
	}
	result, isError, protocolErr := server.callTool(context.Background(), "terminal_list", json.RawMessage(`{}`))
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), "lacks grant") {
		t.Fatalf("denied direct tool = result %#v, isError %v, protocolErr %v", result, isError, protocolErr)
	}
}

func TestResolveDirectMCPPolicyRejectsDisabledAndUnknownGrant(t *testing.T) {
	identity := webrtcpkg.PeerIdentity{UserID: "owner", Email: "owner@example.com", OrgRole: "owner"}
	for name, testCase := range map[string]struct {
		direct *config.DirectMCPConfig
		want   string
	}{
		"disabled": {direct: &config.DirectMCPConfig{Disabled: true}, want: "disabled by this wing"},
		"unknown allow grant": {
			direct: &config.DirectMCPConfig{AllowGrants: []string{"host.root"}},
			want:   `unknown direct grant "host.root"`,
		},
		"unknown deny grant": {
			direct: &config.DirectMCPConfig{DenyGrants: []string{"host.root"}},
			want:   `unknown direct grant "host.root"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveDirectMCPPolicy(&config.WingConfig{DirectMCP: testCase.direct}, t.TempDir(), false, identity)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("resolve error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestDirectMCPSpawnRateSurvivesClientReconnect(t *testing.T) {
	admission := newMCPAdmissionState()
	one := &localMCPServer{principal: "same-owner", maxSpawnsPerHour: 1, admission: admission}
	two := &localMCPServer{principal: "same-owner", maxSpawnsPerHour: 1, admission: admission}
	other := &localMCPServer{principal: "other-owner", maxSpawnsPerHour: 1, admission: admission}

	if err := one.admitSpawn(func() error { return nil }); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if err := two.admitSpawn(func() error { return nil }); err == nil || !strings.Contains(err.Error(), "max_spawns_per_hour=1") {
		t.Fatalf("reconnected owner spawn error = %v", err)
	}
	if err := other.admitSpawn(func() error { return nil }); err != nil {
		t.Fatalf("other owner should have an independent bound: %v", err)
	}
}

func TestDirectMCPMaxSessionsIsSharedAcrossConnections(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "eggs", "existing")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("kind=command\ncwd="+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionPrincipal(sessionDir, "same-owner"); err != nil {
		t.Fatal(err)
	}

	admission := newMCPAdmissionState()
	sameOwner := &localMCPServer{
		cfg: &config.Config{Dir: dir}, principal: "same-owner", maxSessions: 1, admission: admission,
	}
	otherOwner := &localMCPServer{
		cfg: &config.Config{Dir: dir}, principal: "other-owner", maxSessions: 1, admission: admission,
	}
	if err := sameOwner.admitSpawn(func() error { return nil }); err == nil || !strings.Contains(err.Error(), "max_sessions=1") {
		t.Fatalf("same owner session bound error = %v", err)
	}
	if err := otherOwner.admitSpawn(func() error { return nil }); err != nil {
		t.Fatalf("other owner should have an independent session bound: %v", err)
	}
}

func connectDirectMCPTestClient(t *testing.T, cfg *config.Config, wingCfg *config.WingConfig, home string, sharedHost bool, identity webrtcpkg.PeerIdentity) (*webrtcpkg.ControlClient, context.Context) {
	t.Helper()
	manager := webrtcpkg.NewPeerManager(nil)
	t.Cleanup(manager.Close)
	admission := newMCPAdmissionState()
	manager.OnDC(func(_ string, _ string, authenticated webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
		serveDirectMCPChannel(cfg, wingCfg, home, sharedHost, nil, admission, authenticated, dc)
	})
	client, err := webrtcpkg.NewControlClient("codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	offer, err := client.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := manager.HandleOffer("native-client-public-key", identity.UserID, identity.Email, identity.OrgRole, nil, offer)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AcceptAnswer(answer); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return client, ctx
}

func TestDirectMCPTransportAppliesConfiguredGrants(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}
	wingCfg := &config.WingConfig{DirectMCP: &config.DirectMCPConfig{
		AllowGrants: []string{"capabilities.read"},
	}}
	client, ctx := connectDirectMCPTestClient(t, cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{
		UserID: "grant-owner", Email: "owner@example.com", OrgRole: "owner",
	})
	result, isError, err := client.Call(ctx, "terminal_list", json.RawMessage(`{}`))
	if err != nil || !isError {
		t.Fatalf("restricted direct call error = %v, isError = %v, result = %#v", err, isError, result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, `lacks grant "terminal.read"`) {
		t.Fatalf("restricted direct result = %#v", result)
	}
}

func TestDirectMCPTransportPreservesOrgPathBoundary(t *testing.T) {
	home := t.TempDir()
	memberPath := filepath.Join(home, "member")
	ownerPath := filepath.Join(home, "owner-visible")
	for _, path := range []string{memberPath, ownerPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	wingCfg := &config.WingConfig{Org: "test-org", Paths: config.PathList{
		{Path: memberPath, Members: []string{"member@example.com"}},
		{Path: ownerPath, Members: []string{"someone-else@example.com"}},
	}}
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}

	member, memberCtx := connectDirectMCPTestClient(t, cfg, wingCfg, home, false, webrtcpkg.PeerIdentity{
		UserID: "member", Email: "member@example.com", OrgRole: "member",
	})
	result, isError, err := member.Call(memberCtx, "sandbox_explain", json.RawMessage(`{"cwd":`+strconv.Quote(ownerPath)+`}`))
	if err != nil || !isError {
		t.Fatalf("member outside-path call error = %v, isError = %v, result = %#v", err, isError, result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "outside this user's roost paths") {
		t.Fatalf("member outside-path result = %#v", result)
	}

	owner, ownerCtx := connectDirectMCPTestClient(t, cfg, wingCfg, home, false, webrtcpkg.PeerIdentity{
		UserID: "owner", Email: "owner@example.com", OrgRole: "owner",
	})
	result, isError, err = owner.Call(ownerCtx, "sandbox_explain", json.RawMessage(`{"cwd":`+strconv.Quote(ownerPath)+`}`))
	if err != nil || isError {
		t.Fatalf("owner outside-path compatibility call error = %v, isError = %v, result = %#v", err, isError, result)
	}
}

func TestDirectMCPExecutesOnAuthenticatedWing(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}
	wingCfg := &config.WingConfig{}
	client, ctx := connectDirectMCPTestClient(t, cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{
		UserID: "user-direct", Email: "owner@example.com", OrgRole: "owner",
	})
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
	client, ctx := connectDirectMCPTestClient(t, cfg, wingCfg, t.TempDir(), false, webrtcpkg.PeerIdentity{
		UserID: "user-direct", Email: "owner@example.com", OrgRole: "owner",
	})
	_, _, err := client.Call(ctx, "wingthing_capabilities", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "local lock policy") {
		t.Fatalf("locked direct call error = %v", err)
	}
}
