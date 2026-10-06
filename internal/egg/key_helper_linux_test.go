//go:build linux

package egg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestClaudeExplicitJailReadsOwnerKeyHelperReadOnly(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
	key := filepath.Join(home, ".anthropic_key")
	if err := os.WriteFile(key, []byte("owner-fixture-key"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(shortEndpointTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fs := []string{"deny:/", "rw:" + workspace}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			fs = append(fs, "ro:"+path)
		}
	}
	command := `cat "$HOME/.anthropic_key" > key-result && ! printf changed > "$HOME/.anthropic_key"`
	err = server.RunSession(ctx, RunConfig{Agent: "claude", Command: []string{"/bin/sh", "-c", command}, CWD: workspace, UserHome: home, FS: fs, Env: map[string]string{"HOME": home, "PATH": "/usr/bin:/bin"}, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "key-result")); err != nil || string(data) != "owner-fixture-key" {
		t.Fatalf("jailed key helper: %q %v", data, err)
	}
	if data, err := os.ReadFile(key); err != nil || string(data) != "owner-fixture-key" {
		t.Fatalf("owner key changed: %q %v", data, err)
	}
}
