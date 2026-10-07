package localmcp

import (
	"os"
	"path/filepath"
	"strings"
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
