package protectedfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedFilePinsValidatedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Ino == 0 {
		t.Fatal("missing descriptor identity")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("injected"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := f.ReadAll()
	if err != nil || string(data) != "original" {
		t.Fatalf("read reopened pathname: %q, %v", data, err)
	}
}

func TestProtectedFileRefusesAliasesAndSpecialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, path+".symlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path + ".symlink"); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := ReadFile(filepath.Dir(path)); err == nil {
		t.Fatal("read directory")
	}
	if err := os.Link(path, path+".hardlink"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".hardlink", path + ".symlink"} {
		if _, err := ReadResolved(name); err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("accepted linked file %s: %v", name, err)
		}
	}
}

func TestProtectedFileRechecksLinkCountAfterOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Link(path, path+".alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAll(); err == nil {
		t.Fatal("accepted hard link created after open")
	}
}

func TestProtectedFileMasksRefuseReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Aliases(); err == nil {
		t.Fatal("sealed a replacement instead of the opened identity")
	}
}

func TestSecretStagingRefusesRedirectedDirectory(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "eggs")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(dir, "wing_key"), []byte("secret")); err == nil {
		t.Fatal("staged a secret without a permanently masked real directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("redirected staging directory was modified: %v, %v", entries, err)
	}
}
