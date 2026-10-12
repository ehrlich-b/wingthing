//go:build runinspectfixture

package fixture

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type Stamp struct {
	SHA256   string
	Bytes    int64
	Mode     fs.FileMode
	Modified time.Time
}

// Inventory includes directories so creating a sidecar and then removing it
// still changes the preservation inventory through its parent's mtime.
func Inventory(t testing.TB, root string) map[string]Stamp {
	t.Helper()
	out := map[string]Stamp{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		stamp := Stamp{Bytes: info.Size(), Mode: info.Mode(), Modified: info.ModTime()}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			stamp.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		out[filepath.ToSlash(rel)] = stamp
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
