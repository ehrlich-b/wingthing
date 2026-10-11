package testprovider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
)

// ShortStateDir creates disposable clone-local state that fits Darwin's Unix
// socket limit with a full runtime ID. MkdirTemp's decimal suffix is too long
// in some checkout paths. A physical directory also keeps the control socket's
// canonical address local instead of relocating it to the host's /tmp.
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
	// Reserve the actual routing ID width and the egg endpoint suffix. Keep
	// Linux fixtures within the same limit so CI exercises the Mac layout.
	nameBytes := 103 - len(filepath.Join(scratch, "eggs", cmdutil.NewRuntimeID(), "egg.sock")) - 1
	if nameBytes < 2 {
		return "", fmt.Errorf("fixture checkout path %q leaves fewer than two bytes for clone-local state; use a shorter checkout path", scratch)
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
