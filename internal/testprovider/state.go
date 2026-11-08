package testprovider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

// TempDir creates a private physical fixture root independent of the checkout
// and TMPDIR. Keep socket state below this short root on every platform.
func TempDir(t *testing.T) string {
	t.Helper()
	return testsocket.Dir(t)
}

// ShortStateDir creates disposable state below scratch that fits Darwin's Unix
// socket limit with a full runtime ID. Use TempDir for the scratch root to keep
// sockets independent of checkout depth. The physical directory also keeps the
// control socket at the same canonical address as the egg endpoints.
func ShortStateDir(t *testing.T, scratch string) string {
	t.Helper()
	dir, err := shortStateDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func shortStateDir(scratch string) (string, error) {
	return shortFixtureDir(scratch, "")
}

// ScopedHomeDirs keeps both wings' protected state under the provider's HOME
// write-deny root, alongside (not inside) its writable workspaces and TMPDIR.
// Reserve a one-byte state component before choosing the physical HOME name.
func ScopedHomeDirs(t *testing.T, scratch string) (home, localState, remoteState string) {
	t.Helper()
	home, err := shortFixtureDir(scratch, "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	localState, remoteState = filepath.Join(home, "l"), filepath.Join(home, "r")
	for _, dir := range []string{localState, remoteState} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return home, localState, remoteState
}

// ScopedHomePolicy retains the default sandbox and pins the clone-local HOME
// and its ancestors so a provider cannot rename the protected fixture tree.
func ScopedHomePolicy(home string) *egg.EggConfig {
	policy := egg.DefaultEggConfig()
	policy.Base.Name = "none"
	for dir := home; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		policy.FS = append(policy.FS, "deny-write:"+dir)
	}
	return policy
}

func shortFixtureDir(scratch, stateChild string) (string, error) {
	// Reserve the actual routing ID width and the egg endpoint suffix. Keep
	// Linux fixtures within the same limit so CI exercises the Mac layout.
	nameBytes := 103 - len(filepath.Join(scratch, stateChild, "eggs", cmdutil.NewRuntimeID(), "egg.sock")) - 1
	if nameBytes < 1 {
		return "", fmt.Errorf("fixture checkout path %q leaves no bytes for clone-local state; use a shorter checkout path", scratch)
	}
	if nameBytes > 10 {
		nameBytes = 10
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		return "", err
	}
	for range 32 {
		var entropy [5]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", err
		}
		dir := filepath.Join(scratch, hex.EncodeToString(entropy[:])[:nameBytes])
		if err := os.Mkdir(dir, 0700); err == nil {
			return dir, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("fixture state paths occupied under %q", scratch)
}
