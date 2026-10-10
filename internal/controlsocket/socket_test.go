//go:build darwin || linux

package controlsocket

import (
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/control"
	"net"
	"os"
	"path/filepath"
	"testing"
)

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
	for path, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, SocketName): 0600} {
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
	conn, err := net.Dial("unix", filepath.Join(dir, SocketName))
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
