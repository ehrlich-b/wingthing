package egg

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestInputAuditorReplacesSymlinkPrivately(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "audit.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	auditor, err := newInputAuditor(path)
	if err != nil {
		t.Fatal(err)
	}
	auditor.Process([]byte("hello\n"))
	auditor.Close()
	assertPrivateRegularFile(t, path)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestCreatePTYAuditReplacesSymlinkPrivately(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "audit.pty.gz")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	f, gw, err := createPTYAudit(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertPrivateRegularFile(t, path)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("symlink target changed: %q", data)
	}

	audit, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audit.Close() }()
	reader, err := gzip.NewReader(audit)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatal(err)
	}
	if string(header) != "WTA2" {
		t.Fatalf("audit header = %q", header)
	}
}

func TestValidatePTYSizeRejectsWraparound(t *testing.T) {
	for _, size := range [][2]uint32{{0, 24}, {80, 0}, {65536, 24}, {80, 65536}} {
		if err := validatePTYSize(size[0], size[1]); err == nil {
			t.Fatalf("accepted invalid PTY size %dx%d", size[0], size[1])
		}
	}
	if err := validatePTYSize(65535, 65535); err != nil {
		t.Fatalf("rejected maximum PTY size: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteVarintReportsWriterFailure(t *testing.T) {
	if err := writeVarint(failingWriter{}, 42); err == nil {
		t.Fatal("writeVarint ignored writer failure")
	}
}

func assertPrivateRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want private regular file", path, info.Mode())
	}
}
