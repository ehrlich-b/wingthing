package egg

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func legacyPolicyFixture(t *testing.T, home, policy string) string {
	return legacyPolicyFixtureID(t, home, "old", policy)
}

func legacyPolicyFixtureID(t *testing.T, home, id, policy string) string {
	t.Helper()
	dir := filepath.Join(home, ".wingthing-preview", "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.token": "old-token", "egg.meta": "agent=claude\nprovider_home=" + home + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "old-token", session: &Session{ID: "old", RenderedConfig: policy, StartedAt: time.Now(), replay: newReplayBuffer("claude")}}
	rpc := grpc.NewServer(grpc.UnaryInterceptor(server.authUnary))
	pb.RegisterEggServer(rpc, server)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	return dir
}

func TestLegacyDefaultPolicyAllowsNewEndpointAndRestartRecovery(t *testing.T) {
	home := shortEndpointTempDir(t)
	legacy := legacyPolicyFixture(t, home, "fs: [ro:/, 'deny:~/.gnupg']\n")
	dir := filepath.Join(home, "state", "eggs", "new")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "new-secret", toolCapability: strings.Repeat("a", 64), session: &Session{ID: "new", StartedAt: time.Now(), replay: newReplayBuffer("claude")}}
	// Test the PTY operation separately from macOS environment capabilities.
	server.toolCapability = ""
	listener, err := server.prepareEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer server.closeEndpoint(listener)
	if !HasCurrentControlIsolation(dir) {
		t.Fatal("creation did not persist isolation")
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncols=80\nrows=24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.updateMetaDimensions(100, 40); err != nil {
		t.Fatal(err)
	}
	// A fresh host object has no creation state; recovery uses durable identity.
	restarted := &Server{dir: dir}
	if !HasCurrentControlIsolation(restarted.dir) {
		t.Fatal("metadata rewrite/restart made current egg legacy")
	}
	if HasCurrentControlIsolation(legacy) {
		t.Fatal("legacy accepted")
	}
	controlDir, err := readControlDirectory(dir)
	target := filepath.Join(controlDir, "egg.token")
	if err != nil || !controlPathWithin(target, config.CanonicalProviderPath(filepath.Join(home, ".gnupg"))) {
		t.Fatalf("token exposed outside historical deny: %q %v", target, err)
	}
	if err := legacyDeniesControlDirectory(legacy, target, home); err != nil {
		t.Fatal(err)
	}
	server.toolCapability = strings.Repeat("a", 64)
	rpc := grpc.NewServer(grpc.UnaryInterceptor(server.authUnary))
	pb.RegisterEggServer(rpc, server)
	go func() { _ = rpc.Serve(listener) }()
	defer rpc.Stop()
	// A broker/controller can restart with a different HOME; the durable
	// locator binds recovery to the egg's creation home.
	t.Setenv("HOME", filepath.Join(home, "restarted-home"))
	client, err := Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	t.Setenv("HOME", home) // the surviving egg process keeps its original HOME
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if recovered, err := client.ReclaimToolCapability(ctx); err != nil || recovered != server.toolCapability {
		t.Fatalf("restart capability recovery: %q %v", recovered, err)
	}
}

func TestLegacyCustomPolicyStrictRefusesSecretExposingOperation(t *testing.T) {
	home := shortEndpointTempDir(t)
	legacy := legacyPolicyFixture(t, home, "fs: [ro:/]\n")
	dir := filepath.Join(home, "state", "eggs", "new")
	if err := config.SaveWingConfig(filepath.Join(home, "state"), &config.WingConfig{LegacyIsolation: config.LegacyIsolationStrict}); err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "new-secret"}
	if listener, err := server.prepareEndpoint(); err == nil || !strings.Contains(err.Error(), "legacy sessions: old") {
		server.closeEndpoint(listener)
		t.Fatalf("exposed token admitted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "egg.token")); !os.IsNotExist(err) {
		t.Fatal("refusal published secret")
	}
	if err := os.WriteFile(filepath.Join(legacy, "egg.pid"), []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireLegacySecretProtection(dir, false); err != nil {
		t.Fatalf("dead legacy blocks launch: %v", err)
	}
}

func TestCompatibilityTokenSupportsOldControllerAndMarksLegacyExposure(t *testing.T) {
	home := shortEndpointTempDir(t)
	legacy := legacyPolicyFixture(t, home, "fs: [ro:/, 'deny:~/.gnupg']\n")
	dir := filepath.Join(home, "state", "eggs", "new")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "compatible-secret", session: &Session{ID: "new", StartedAt: time.Now(), replay: newReplayBuffer("claude")}}
	listener, err := server.prepareEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer server.closeEndpoint(listener)
	isolation := ReadLegacyIsolation(dir)
	if isolation == nil || !strings.Contains(isolation.Reason, "compatibility controller token") || strings.Join(isolation.LegacySessions, ",") != "old" {
		t.Fatalf("legacy compatibility exposure not reported: %+v", isolation)
	}
	token, err := os.ReadFile(filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatalf("rollback token missing: %v", err)
	}
	rpc := grpc.NewServer(grpc.UnaryInterceptor(server.authUnary))
	pb.RegisterEggServer(rpc, server)
	go func() { _ = rpc.Serve(listener) }()
	defer rpc.Stop()
	// Reproduce the v0.147.0 controller: read egg.token and authenticate
	// directly, without consulting egg.control or the new isolation marker.
	conn, err := grpc.NewClient("unix://"+filepath.Join(dir, "egg.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", string(token))
	if state, err := pb.NewEggClient(conn).Status(ctx, &pb.StatusRequest{}); err != nil || state.SessionId != "new" {
		t.Fatalf("old controller cannot attach after rollback: %v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "egg.sock")); err != nil {
		t.Fatalf("surviving legacy PTY removed: %v", err)
	}
}

func TestLegacyPolicyDenyingBothTokensKeepsFullIsolation(t *testing.T) {
	home := shortEndpointTempDir(t)
	dir := filepath.Join(home, "state", "eggs", "new")
	legacyPolicyFixture(t, home, "fs: [ro:/, 'deny:~/.gnupg', 'deny:"+filepath.Dir(dir)+"']\n")
	if isolation := InspectLegacyIsolation(dir, false); isolation != nil {
		t.Fatalf("legacy denies both token locations but admission degraded: %+v", isolation)
	}
}

func TestStrictLegacyModeRefusesCompatibilityTokenExposure(t *testing.T) {
	home := shortEndpointTempDir(t)
	legacyPolicyFixture(t, home, "fs: [ro:/, 'deny:~/.gnupg']\n")
	dir := filepath.Join(home, "state", "eggs", "new")
	if err := config.SaveWingConfig(filepath.Join(home, "state"), &config.WingConfig{LegacyIsolation: config.LegacyIsolationStrict}); err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "secret"}
	if listener, err := server.prepareEndpoint(); err == nil || !strings.Contains(err.Error(), "compatibility controller token") {
		server.closeEndpoint(listener)
		t.Fatalf("strict mode admitted exposed rollback token: %v", err)
	}
	for _, path := range []string{filepath.Join(dir, "egg.token"), filepath.Join(controlDirectory(dir), "egg.token")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("strict refusal published token %s: %v", path, err)
		}
	}
}

func TestLegacyCustomPoliciesAdmitDegradedEndpointByDefault(t *testing.T) {
	home := shortEndpointTempDir(t)
	for _, id := range []string{"old-b", "old-a"} {
		legacyPolicyFixtureID(t, home, id, "fs: [ro:/]\n")
	}
	dir := filepath.Join(home, "state", "eggs", "new")
	server := &Server{dir: dir, token: "new-secret"}
	listener, err := server.prepareEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer server.closeEndpoint(listener)
	if !HasCurrentControlIsolation(dir) {
		t.Fatal("new degraded endpoint lost its current controller identity")
	}
	isolation := ReadLegacyIsolation(dir)
	if isolation == nil || strings.Join(isolation.LegacySessions, ",") != "old-a,old-b" || !strings.Contains(isolation.Reason, "permits reading the controller directory") {
		t.Fatalf("degraded report = %+v", isolation)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("isolation=wingthing-sandbox\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if ReadLegacyIsolation(dir) == nil {
		t.Fatal("metadata rewrite lost admission warning")
	}
}

func TestLegacyCapabilityReasonWrittenOnceAndPTYRetained(t *testing.T) {
	home := shortEndpointTempDir(t)
	legacy := legacyPolicyFixture(t, home, "fs: [ro:/, 'deny:~/.gnupg']\n")
	MarkLegacyEggForReplacement(legacy)
	info, err := os.Stat(filepath.Join(legacy, "replacement-required"))
	if err != nil {
		t.Fatal(err)
	}
	MarkLegacyEggForReplacement(legacy)
	again, err := os.Stat(filepath.Join(legacy, "replacement-required"))
	if err != nil || !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("legacy warning repeated")
	}
	if _, err := os.Stat(filepath.Join(legacy, "egg.sock")); err != nil {
		t.Fatalf("legacy PTY endpoint lost: %v", err)
	}
	if runtime.GOOS == "darwin" {
		dir := filepath.Join(home, "state", "eggs", "new")
		if err := RequireLegacySecretProtection(dir, true); err != nil {
			t.Fatalf("default mode blocked new tools session: %v", err)
		}
		if isolation := ReadLegacyIsolation(dir); isolation == nil || !strings.Contains(isolation.Reason, "capability environments") {
			t.Fatalf("environment capability exposure was not reported: %+v", isolation)
		}
	}
}

func TestIsolationMarkerCannotBeForgedInSessionMetadata(t *testing.T) {
	dir := shortEndpointTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("control_isolation="+ControlIsolationVersion+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if HasCurrentControlIsolation(dir) {
		t.Fatal("metadata spoof admitted tool recovery")
	}
	fake := controlDirectoryUnderHome(dir, filepath.Join(dir, "workspace"))
	if err := os.MkdirAll(fake, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, "isolation"), []byte(ControlIsolationVersion+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.control"), []byte(fake+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if HasCurrentControlIsolation(dir) {
		t.Fatal("workspace locator spoof admitted tool recovery")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".gnupg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, ".gnupg", "wingthing-control")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareControlDirectory(dir); err == nil {
		t.Fatal("aliased runtime-owned controller child accepted")
	}
}

func TestLegacyToolRecoveryRefusedWithoutRemovingPTYStatus(t *testing.T) {
	dir := shortEndpointTempDir(t)
	server := &Server{dir: dir, toolCapability: strings.Repeat("a", 64), session: &Session{ID: "old", StartedAt: time.Now(), replay: newReplayBuffer("claude")}}
	if _, err := server.Status(context.Background(), &pb.StatusRequest{ReclaimTools: true}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy tool authority recovered: %v", err)
	}
	if result, err := server.Status(context.Background(), &pb.StatusRequest{}); err != nil || result.SessionId != "old" || result.ToolCapability != "" {
		t.Fatalf("legacy terminal status lost: %v %v", result, err)
	}
}

func TestLegacySandboxCannotReadFutureControlToken(t *testing.T) {
	if runtime.GOOS == "darwin" && os.Getenv("WT_TEST_SEATBELT_ENFORCEMENT") != "1" {
		t.Skip("set WT_TEST_SEATBELT_ENFORCEMENT=1 on an unsandboxed Mac")
	}
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("sandbox unavailable: %s", reason)
	}
	home := shortEndpointTempDir(t)
	// Compile only the historical policy, without any new controller denies.
	legacy, err := sandbox.New(sandbox.Config{
		Mounts: []sandbox.Mount{{Source: home, Target: home, ReadOnly: true}},
		Deny:   []string{filepath.Join(home, ".gnupg")}, NetworkNeed: sandbox.NetworkNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Destroy()
	dir := filepath.Join(home, "state", "eggs", "future")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	server := &Server{dir: dir, token: "future-secret"}
	listener, err := server.prepareEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer server.closeEndpoint(listener)
	cmd, err := legacy.Exec(context.Background(), "/bin/sh", []string{"-c", `if cat "$1" >/dev/null 2>&1; then echo stolen; exit 1; fi; echo denied`, "legacy", config.CanonicalProviderPath(filepath.Join(controlDirectory(dir), "egg.token"))})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "denied" {
		t.Fatalf("legacy read future credential: %q %v", output, err)
	}
}

func TestLinuxCurrentEggStartsWithLegacyCredentialDirectoryDenied(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux mount masks")
	}
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	home := shortEndpointTempDir(t)
	workspace := filepath.Join(home, "work")
	legacyPolicyFixture(t, home, "fs: [ro:/]\n")
	dir := filepath.Join(home, "state", "eggs", "current")
	for _, path := range []string{workspace, dir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	server, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Include guarded host setup, the race-instrumented wrapper, and shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = server.RunSession(ctx, RunConfig{
		Command: []string{"/bin/sh", "-c", "printf fixture-ready"}, CWD: workspace,
		FS:  []string{"ro:/", "rw:" + workspace, "deny:" + filepath.Join(home, ".gnupg")},
		Env: map[string]string{"HOME": home, "PATH": "/usr/bin:/bin"}, Rows: 24, Cols: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.session == nil || !strings.Contains(string(server.session.replay.Bytes()), "fixture-ready") {
		logData, _ := os.ReadFile(filepath.Join(home, "state", "logs", "current.deny_init.log"))
		t.Fatalf("nested credential deny mask prevented the agent from starting: %s", logData)
	}
	output := string(server.session.replay.Bytes())
	if strings.Count(output, "Warning: isolation: degraded") != 1 || !strings.Contains(output, "legacy sessions: old") {
		t.Fatalf("missing or repeated user-facing warning: %q", output)
	}
}
