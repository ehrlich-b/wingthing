package localmcp

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestLocalMCPClientPolicyRefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.yaml")
	if err := os.WriteFile(path, []byte("require_client: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(t.TempDir(), "writable")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalMCPClientsConfig(&config.Config{Dir: dir}); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("client authority policy admitted hard link: %v", err)
	}
}

func TestLocalMCPClientPolicyAcceptsReadonlyYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.yaml")
	if err := os.WriteFile(path, []byte("require_client: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLocalMCPClientsConfig(&config.Config{Dir: dir})
	if err != nil || !policy.RequireClient {
		t.Fatalf("readonly client policy refused: %+v, %v", policy, err)
	}
	// Group/world writes remain forbidden even for account-owned policy.
	if err := os.Chmod(path, 0664); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalMCPClientsConfig(&config.Config{Dir: dir}); err == nil {
		t.Fatal("accepted group-writable client policy")
	}
}

func TestLocalMCPClientPolicyAcceptsRootOwnedLoader(t *testing.T) {
	info, err := os.Stat("/etc/passwd")
	if err != nil {
		t.Skip(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || os.Getuid() == 0 {
		t.Skip("requires a non-root service account and a root-owned fixture")
	}
	dir := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "clients.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalMCPClientsConfig(&config.Config{Dir: dir}); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("root-owned 0644 loader was refused before YAML parsing: %v", err)
	}
}
