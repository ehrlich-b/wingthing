package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/google/uuid"
)

func claudeControlFixture(t *testing.T) *northstarFixture {
	t.Helper()
	f := northstarNewFixture(t)
	t.Setenv("HOME", f.home)
	t.Setenv("WINGTHING_DIR", f.local)
	t.Setenv("TMPDIR", f.tmp)
	t.Setenv("WT_MCP_CLIENT", "")
	path, err := controlsocket.Path(f.local)
	if err != nil {
		t.Fatal(err)
	}
	northstarSocket(t, path)
	return f
}

func TestClaudeReadyOwnerAdmitsOnceAndReportsAttachFailure(t *testing.T) {
	f := claudeControlFixture(t)
	var calls atomic.Int32
	server, err := controlsocket.Listen(t.Context(), f.local, "fixture-wing", func(h controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		if h.Client != "" {
			t.Errorf("unexpected principal: %q", h.Client)
		}
		return controlsocket.Welcome{}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
			calls.Add(1)
			var launch struct {
				RequestID string   `json:"request_id"`
				ScopedMCP bool     `json:"scoped_mcp"`
				Args      []string `json:"args"`
			}
			if err := json.Unmarshal(req.Arguments, &launch); err != nil || req.Tool != "agent_start" || !launch.ScopedMCP || len(launch.Args) != 2 || launch.Args[1] != "fixture-model" {
				t.Errorf("unexpected launch: %s %v", req.Arguments, err)
			}
			if _, err := uuid.Parse(launch.RequestID); err != nil {
				t.Errorf("invalid original request ID: %v", err)
			}
			return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Result: map[string]any{"session": "admitted-parent"}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cmd := newRootCommand()
	cmd.SetArgs([]string{"claude", "--", "--model", "fixture-model"})
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetOut(new(bytes.Buffer))
	stderr := new(bytes.Buffer)
	cmd.SetErr(stderr)
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("fixture without attachment unexpectedly attached")
	}
	if calls.Load() != 1 || !strings.Contains(stderr.String(), "session admitted-parent admitted; reattach with wt attach admitted-parent") {
		t.Fatalf("lost admitted session: calls=%d stderr=%q", calls.Load(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
		t.Fatalf("ready warm runtime was replaced: %v", err)
	}
}

func TestClaudeUnknownAdmissionNeverRetries(t *testing.T) {
	f := claudeControlFixture(t)
	var calls atomic.Int32
	admitted := make(chan struct{})
	server, err := controlsocket.Listen(t.Context(), f.local, "fixture-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(ctx context.Context, req control.DirectRequest) control.DirectResponse {
			if calls.Add(1) == 1 {
				close(admitted)
			}
			<-ctx.Done() // Admission exists but its acknowledgement never arrives.
			return control.DirectResponse{Version: control.ContractVersion, ID: req.ID}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cmd := newRootCommand()
	cmd.SetArgs([]string{"claude"})
	cmd.SetErr(new(bytes.Buffer))
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(ctx) }()
	select {
	case <-admitted:
		cancel()
	case <-ctx.Done():
		t.Fatal("fixture never received admission")
	}
	if err := <-result; !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("unknown admission retried: calls=%d err=%v", calls.Load(), err)
	}
}

func TestClaudeHandshakeRefusalNeverStartsDaemon(t *testing.T) {
	for _, refusal := range []string{"passkey authentication is required", "MCP client default is not configured", "incompatible local wing control protocol"} {
		t.Run(refusal, func(t *testing.T) {
			f := claudeControlFixture(t)
			server, err := controlsocket.Listen(t.Context(), f.local, "fixture-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
				return controlsocket.Welcome{}, nil, errors.New(refusal)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if client, err := ensureClaudeWing(t.Context(), f.local, ""); err == nil || !strings.Contains(err.Error(), refusal) || client != nil {
				t.Fatalf("handshake refusal lost: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
				t.Fatalf("autostart after authoritative refusal: %v", err)
			}
		})
	}
}

func TestClaudeReadinessRequiresHandshakeAndCancellation(t *testing.T) {
	f := claudeControlFixture(t)
	path, err := controlsocket.Path(f.local)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	northstarWrite(t, filepath.Join(f.local, "wing.status"), `{"state":"local"}`, 0600)
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := ensureClaudeWing(ctx, f.local, ""); result <- err }()
	select {
	case conn := <-accepted:
		defer conn.Close()
		cancel()
	case <-ctx.Done():
		t.Fatal("readiness fixture was never connected")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("status was treated as authenticated readiness: %v", err)
	}
}

func TestClaudeLiveUnreadyDaemonIsNeverRestarted(t *testing.T) {
	f := claudeControlFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Reuse 01's test-executable identity fixture: owned foreground argv,
	// explicit readiness event, no socket. Production wt has no such switch.
	child := exec.CommandContext(t.Context(), exe, "wing", "start", "--foreground", "--local-only")
	child.Env = append(os.Environ(), "WT_NORTHSTAR_PID_FIXTURE=1")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Wait() })
	ready := make(chan string, 1)
	go func() { data := make([]byte, 6); _, _ = io.ReadFull(output, data); ready <- string(data) }()
	select {
	case value := <-ready:
		if value != "ready\n" {
			t.Fatalf("identity fixture: %q", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("identity fixture did not become ready")
	}
	args := []string{"wing", "start", "--foreground", "--local-only"}
	if err := daemonctl.WriteDaemonMetadata(filepath.Join(f.local, "wing.pid"), filepath.Join(f.local, "wing.args"), child.Process.Pid, args); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.local, "wing.status"), `{"state":"local"}`, 0600)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if client, err := ensureClaudeWing(ctx, f.local, ""); client != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live unready daemon accepted or replaced: %v", err)
	}
	if pid, err := daemonctl.ReadPidFrom(filepath.Join(f.local, "wing.pid"), daemonctl.WingDaemon); err != nil || pid != child.Process.Pid || !procinfo.OwnedProcessIsAlive(pid) {
		t.Fatalf("caller cancellation killed or replaced shared daemon: pid=%d err=%v", pid, err)
	}
	got, err := os.ReadFile(filepath.Join(f.local, "wing.args"))
	if err != nil || string(got) != strings.Join(args, "\n") {
		t.Fatalf("live daemon metadata changed: %q %v", got, err)
	}
}

func TestClaudeInvalidSelectedConfigurationFailsClosed(t *testing.T) {
	for _, file := range []string{"config.yaml", "wing.yaml", "egg.yaml"} {
		t.Run(file, func(t *testing.T) {
			f := claudeControlFixture(t)
			northstarWrite(t, filepath.Join(f.local, file), "invalid: [", 0600)
			if client, err := ensureClaudeWing(t.Context(), f.local, ""); err == nil || client != nil {
				t.Fatalf("invalid selected config accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
				t.Fatalf("invalid config started a daemon: %v", err)
			}
		})
	}
}

func TestClaudeStartupBoundsIncompleteHandshake(t *testing.T) {
	f := claudeControlFixture(t)
	path, err := controlsocket.Path(f.local)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	started := time.Now()
	client, err := ensureClaudeWing(t.Context(), f.local, "")
	if conn := <-accepted; conn != nil {
		_ = conn.Close()
	}
	if client != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > claudeWingStartupTimeout+2*time.Second {
		t.Fatalf("incomplete handshake exceeded startup bound: client=%v err=%v elapsed=%s", client, err, time.Since(started))
	}
}

type claudeProgressRecorder struct {
	bytes.Buffer
	written chan struct{}
}

func (w *claudeProgressRecorder) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return n, err
}

func TestClaudeDelayedProgressClearsBeforeProviderOutput(t *testing.T) {
	fast := &claudeProgressRecorder{written: make(chan struct{}, 1)}
	claudeStartupProgress(fast)()
	if fast.Len() != 0 {
		t.Fatalf("fast launch emitted progress: %q", fast.String())
	}
	slow := &claudeProgressRecorder{written: make(chan struct{}, 1)}
	started := time.Now()
	stop := claudeStartupProgress(slow)
	select {
	case <-slow.written:
		if time.Since(started) < 250*time.Millisecond {
			stop()
			t.Fatal("progress appeared before 250ms")
		}
	case <-time.After(3 * time.Second):
		stop()
		t.Fatal("delayed progress did not appear")
	}
	stop()
	_, _ = io.WriteString(slow, "provider output\n")
	if got := slow.String(); got != "\rStarting local Claude...\r\x1b[2Kprovider output\n" {
		t.Fatalf("progress not cleared before output: %q", got)
	}
}

func TestClaudePreviewBindingFailureNeverFallsBackToStable(t *testing.T) {
	f := claudeControlFixture(t)
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	t.Setenv("WINGTHING_PREVIEW_DIR", f.local)
	northstarWrite(t, filepath.Join(f.local, ".release-channel"), "preview\n", 0600)
	northstarWrite(t, filepath.Join(f.local, config.ProviderHomeBinding), "relative-provider-home\n", 0600)
	if client, err := ensureClaudeWing(t.Context(), f.local, ""); client != nil || err == nil || !strings.Contains(err.Error(), "provider-home binding") {
		t.Fatalf("invalid preview binding did not fail closed: %v", err)
	}
	for _, path := range []string{filepath.Join(f.local, "wing.pid"), filepath.Join(f.home, ".wingthing"), filepath.Join(f.home, ".wingthing-preview")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preview startup adopted state %s: %v", path, err)
		}
	}
}
