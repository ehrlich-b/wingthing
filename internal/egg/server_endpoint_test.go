package egg

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareEndpointCreatesPrivateCompleteEndpoint(t *testing.T) {
	dir := shortEndpointTempDir(t)
	server := &Server{dir: dir, token: "test-token"}

	listener, err := server.prepareEndpoint()
	if err != nil {
		t.Fatalf("prepareEndpoint: %v", err)
	}
	defer server.closeEndpoint(listener)

	for name, wantMode := range map[string]os.FileMode{
		"egg.sock":  0o600,
		"egg.token": 0o600,
		"egg.pid":   0o644,
	} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil {
			t.Fatalf("stat %s: %v", name, statErr)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Errorf("%s mode = %o, want %o", name, got, wantMode)
		}
	}
	token, err := os.ReadFile(filepath.Join(dir, "egg.token"))
	if err != nil || string(token) != "test-token" {
		t.Fatalf("token = %q, err=%v", token, err)
	}
	connection, err := net.Dial("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		t.Fatalf("dial prepared socket: %v", err)
	}
	_ = connection.Close()
}

func TestPrepareEndpointRollsBackOnCredentialFailure(t *testing.T) {
	for _, blocked := range []string{"egg.token", "egg.pid"} {
		t.Run(blocked, func(t *testing.T) {
			dir := shortEndpointTempDir(t)
			// A non-empty directory cannot be replaced by WriteFile or removed by
			// rollback, providing deterministic failure injection at each step.
			blockedPath := filepath.Join(dir, blocked)
			if err := os.Mkdir(blockedPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(blockedPath, "keep"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			server := &Server{dir: dir, token: "test-token"}
			listener, err := server.prepareEndpoint()
			if err == nil {
				server.closeEndpoint(listener)
				t.Fatal("prepareEndpoint succeeded with a blocked credential path")
			}
			if !strings.Contains(err.Error(), strings.TrimSuffix(blocked, "egg.")) &&
				!strings.Contains(err.Error(), strings.TrimPrefix(blocked, "egg.")) {
				t.Fatalf("error %q does not name blocked step %q", err, blocked)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "egg.sock")); !os.IsNotExist(statErr) {
				t.Fatalf("socket survived rollback: %v", statErr)
			}
			if blocked == "egg.pid" {
				if _, statErr := os.Stat(filepath.Join(dir, "egg.token")); !os.IsNotExist(statErr) {
					t.Fatalf("token survived PID rollback: %v", statErr)
				}
			}
		})
	}
}

func TestPrepareEndpointRefusesUnremovableStaleSocket(t *testing.T) {
	dir := shortEndpointTempDir(t)
	stale := filepath.Join(dir, "egg.sock")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := &Server{dir: dir, token: "test-token"}
	if listener, err := server.prepareEndpoint(); err == nil {
		server.closeEndpoint(listener)
		t.Fatal("prepareEndpoint replaced an unremovable stale socket path")
	}
	for _, name := range []string{"egg.token", "egg.pid"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was created after socket preflight failed: %v", name, err)
		}
	}
}

func shortEndpointTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wt-egg-endpoint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
