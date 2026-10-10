//go:build darwin || linux

package controlsocket

import (
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/control"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketServesLongStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("state-", 30))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(t.Context(), dir, "long-wing", func(Hello) (Welcome, Handler, error) {
		return Welcome{}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"ok": true}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path, relocated, err := socketPath(dir)
	if err != nil || !relocated || len(path) > 103 {
		t.Fatalf("long state address: %q %t %v", path, relocated, err)
	}
	for name, mode := range map[string]os.FileMode{runtimeSocketDir(): 0700, path: 0600, filepath.Join(dir, socketPathFile): 0600} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode || !ownedByUser(info) {
			t.Fatalf("unsafe relocated endpoint %s: %v %v", name, info, err)
		}
	}
	pointer, err := os.ReadFile(filepath.Join(dir, socketPathFile))
	if err != nil || string(pointer) != path+"\n" {
		t.Fatalf("socket pointer: %q %v", pointer, err)
	}
	// The pointer grants no routing authority.
	if err := os.WriteFile(filepath.Join(dir, socketPathFile), []byte("/wrong/endpoint\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Dial(t.Context(), dir, Hello{WingID: "long-wing"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, _, err := c.Call(t.Context(), "ping", json.RawMessage(`{}`))
	if err != nil || result["ok"] != true {
		t.Fatalf("long state control: %v %v", result, err)
	}
}

func TestRuntimeSocketDirectoryRejectsUnsafeParent(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []os.FileMode{0755, 0777} {
		if err := os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		if err := verifyRuntimeSocketDir(root, true); err == nil {
			t.Fatal("accepted exposed runtime parent")
		}
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("changed unsafe existing parent's permissions")
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeSocketDir(alias, true); err == nil {
		t.Fatal("accepted symlink runtime parent")
	}
}

func TestLocalSocketRejectsForeignPeer(t *testing.T) {
	if err := verifyPeerUID(uint32(os.Getuid()) + 1); err == nil {
		t.Fatal("foreign UID admitted")
	}
	if err := verifyPeerUID(uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
}
func TestSocketHandshakeAndConcurrentControl(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dir = "."
	entered := make(chan struct{})
	release := make(chan struct{})
	s, err := Listen(context.Background(), dir, "wing", func(Hello) (Welcome, Handler, error) {
		return Welcome{}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
			if r.Tool == "wait" {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"tool": r.Tool}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path, _, err := socketPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0700, path: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions %s: %v %v", path, info, err)
		}
	}
	if _, err := Dial(context.Background(), dir, Hello{WingID: "other"}); err == nil {
		t.Fatal("wrong wing accepted")
	}
	c, err := Dial(context.Background(), dir, Hello{WingID: "wing"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { _, _, err := c.Call(context.Background(), "wait", json.RawMessage(`{}`)); done <- err }()
	<-entered
	result, _, err := c.Call(context.Background(), "ping", json.RawMessage(`{}`))
	if err != nil || result["tool"] != "ping" {
		t.Fatalf("concurrent call: %v %v", result, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = json.NewEncoder(conn).Encode(map[string]any{"version": "old"})
	var response Welcome
	if err := json.NewDecoder(conn).Decode(&response); err != nil || response.Error == "" {
		t.Fatalf("old protocol: %v %v", response, err)
	}
}
func TestSocketRejectsUnsafePathsAndLiveReplacement(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dir = "."
	bind := func(Hello) (Welcome, Handler, error) { return Welcome{}, nil, nil }
	s, err := Listen(context.Background(), dir, "wing", bind)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Listen(context.Background(), dir, "wing", bind); err == nil {
		t.Fatal("replaced live socket")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), alias, "wing", bind); err == nil {
		t.Fatal("symlink state accepted")
	}
}

func TestControlEnvelopeRejectsCallerAuthorityAndTrailingData(t *testing.T) {
	for _, input := range []string{
		`{"version":"` + control.ContractVersion + `","principal":"owner"}`,
		`{"version":"` + control.ContractVersion + `","grants":{"terminal.start":true}}`,
		`{"version":"` + control.ContractVersion + `"} {"client":"default"}`,
	} {
		if err := strictJSON([]byte(input), new(Hello)); err == nil {
			t.Fatalf("accepted caller authority or extra envelope: %s", input)
		}
	}
}
