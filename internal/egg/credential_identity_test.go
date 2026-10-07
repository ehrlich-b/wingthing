package egg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestControllerIsolationRefusesCredentialHardLinks(t *testing.T) {
	for _, name := range []string{"wing_key", "sync.key", "device_token.yaml", "local_device_token.yaml", "eggs/old/egg.token", ".gnupg/wingthing-control/old/egg.token"} {
		t.Run(name, func(t *testing.T) {
			home := config.CanonicalProviderPath(t.TempDir())
			t.Setenv("HOME", home)
			state, work := filepath.Join(home, ".wingthing"), filepath.Join(home, "work")
			t.Setenv("WINGTHING_DIR", state)
			path := filepath.Join(state, name)
			if strings.HasPrefix(name, ".gnupg/") {
				path = filepath.Join(home, name)
			}
			for _, dir := range []string{work, filepath.Dir(path)} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("controller-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, filepath.Join(work, "ordinary")); err != nil {
				t.Fatal(err)
			}
			_, err := IsolateControl(sandbox.Config{Mounts: []sandbox.Mount{{Source: work, Target: work}}}, "", nil, "")
			if err == nil || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("controller credential exposed through workspace: %v", err)
			}
		})
	}
}

func TestEggDialRefusesHardLinkedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "egg.token")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(t.TempDir(), "ordinary")); err != nil {
		t.Fatal(err)
	}
	client, err := Dial(filepath.Join(dir, "egg.sock"), path)
	if client != nil {
		client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("dial admitted linked controller token: %v", err)
	}
}
