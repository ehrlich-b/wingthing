package egg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestSocketPathByteLimit(t *testing.T) {
	maximum := len(syscall.RawSockaddrUnix{}.Path) - 1
	if runtime.GOOS == "darwin" && maximum != 103 || runtime.GOOS == "linux" && maximum != 107 {
		t.Fatalf("unexpected %s filesystem socket limit %d", runtime.GOOS, maximum)
	}
	for _, path := range []string{
		"/" + strings.Repeat("a", maximum-1),
		"/" + strings.Repeat("é", (maximum-1)/2) + strings.Repeat("a", (maximum-1)%2),
	} {
		if len(path) != maximum {
			t.Fatal("invalid byte-boundary fixture")
		}
		if err := ValidateSocketPath(path); err != nil {
			t.Fatalf("valid boundary rejected: %v", err)
		}
		path += "a"
		var tooLong *SocketPathTooLongError
		if err := ValidateSocketPath(path); !errors.As(err, &tooLong) {
			t.Fatalf("long path did not return typed failure: %v", err)
		}
		if tooLong.Path != path || tooLong.Bytes != len(path) || tooLong.MaxBytes != maximum {
			t.Fatalf("incorrect address details: %#v", tooLong)
		}
		for _, fragment := range []string{fmt.Sprintf("%q", path), fmt.Sprintf("%d bytes", len(path)), fmt.Sprintf("%d bytes", maximum), runtime.GOOS, "WINGTHING_DIR", "shorter"} {
			if !strings.Contains(tooLong.Error(), fragment) {
				t.Fatalf("error missing %q: %s", fragment, tooLong)
			}
		}
	}
}

func TestSocketPathPreflightLeavesEndpointAndProviderStateUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	if _, err := NewServer(dir); err == nil {
		t.Fatal("long server endpoint accepted")
	}
	server := &Server{dir: dir}
	if err := server.RunSession(context.Background(), RunConfig{Command: []string{"/bin/sh"}, Rows: 24, Cols: 80}); err == nil {
		t.Fatal("long session endpoint accepted")
	}
	if _, err := server.prepareEndpoint(); err == nil {
		t.Fatal("long endpoint accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("endpoint preflight wrote state: %v", err)
	}
	// Refusal must precede deletion of an existing artifact at the socket path.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"egg.sock", "tool.sock"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if name == "egg.sock" {
			if _, err := server.prepareEndpoint(); err == nil {
				t.Fatal("long endpoint accepted")
			}
		} else if _, err := NewToolListener(path, nil); err == nil {
			t.Fatal("long tool endpoint accepted")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
			t.Fatalf("preflight changed existing %s: %q, %v", name, data, err)
		}
	}
}
