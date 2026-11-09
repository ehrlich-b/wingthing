package testprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestParentLifecycleFixtureSocketPaths(t *testing.T) {
	scratch := TempDir(t)
	// Reproduce the measured standalone fixture address without a listener.
	// Its 104-byte address fits Linux but fails Darwin before attachment.
	legacy := filepath.Join("/Users/fixture/repos/w7-wingthing-parent-lifecycle-20261011", ".scratch", "a1234567890", "s", "eggs", "fixture", "egg.sock")
	t.Logf("legacy socket: %d bytes (Darwin maximum 103, Linux maximum 107)", len(legacy))
	if len(legacy) <= 103 || len(legacy) > 107 {
		t.Fatal("fixture no longer distinguishes the recorded Mac and Linux paths")
	}
	if err := egg.ValidateSocketPath(legacy); runtime.GOOS == "darwin" {
		var tooLong *egg.SocketPathTooLongError
		if !errors.As(err, &tooLong) || tooLong.Bytes != 104 || tooLong.MaxBytes != 103 {
			t.Fatalf("measured Mac preflight differs: %v", err)
		}
	} else if runtime.GOOS == "linux" && err != nil {
		t.Fatalf("Linux rejected the measured Mac address: %v", err)
	}
	seen := map[string]bool{}
	for _, state := range []string{"local", "remote", "standalone"} {
		dir := ShortStateDir(t, scratch)
		if seen[dir] || filepath.Dir(dir) != scratch {
			t.Fatalf("fixture state is shared or outside scratch: %s", dir)
		}
		seen[dir] = true
		endpoint := filepath.Join(dir, "eggs", cmdutil.NewRuntimeID(), "egg.sock")
		if len(endpoint) > 103 {
			t.Fatalf("fixture socket exceeds Darwin's byte limit: %s", endpoint)
		}
		if _, err := egg.NewServer(filepath.Dir(endpoint)); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("fixture state is not a private physical directory: %v %v", info, err)
		}
		control, err := controlsocket.Path(dir)
		if err != nil || control != filepath.Join(dir, controlsocket.SocketName) {
			t.Fatalf("control socket escaped clone-local state: %s %v", control, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "receipt"), []byte(state), 0600); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "receipt")); err != nil || string(data) != state {
			t.Fatalf("state lost its receipt identity: %q %v", data, err)
		}
		t.Logf("%s socket: %d bytes", state, len(endpoint))
	}
}

func TestParentLifecycleFixtureRefusesUnusableCheckoutBeforeWriting(t *testing.T) {
	scratch := filepath.Join(testsocket.Dir(t), strings.Repeat("x", 104))
	if _, err := shortStateDir(scratch); err == nil || !strings.Contains(err.Error(), "shorter checkout path") {
		t.Fatalf("unusable checkout accepted: %v", err)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("unusable checkout created state: %v", err)
	}
}

func TestShortFixtureRootIgnoresLongTMPDIR(t *testing.T) {
	ambient := filepath.Join(testsocket.Dir(t), strings.Repeat("x", 150))
	if err := os.MkdirAll(ambient, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", ambient)
	root := TempDir(t)
	physicalTmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(root) != physicalTmp || !strings.HasPrefix(filepath.Base(root), "wt") {
		t.Fatalf("fixture root depends on ambient paths: %s", root)
	}
	state := ShortStateDir(t, root)
	endpoint := filepath.Join(state, "eggs", cmdutil.NewRuntimeID(), "egg.sock")
	if len(endpoint) > 103 {
		t.Fatalf("fixture endpoint exceeds socket budget: %s", endpoint)
	}
}
