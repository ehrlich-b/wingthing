package egg

import (
	"context"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeLifecycleRefusesHostSettingsOutsideFinalPolicy(t *testing.T) {
	oldLog := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(oldLog)
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	for _, attack := range []string{"deny", "ancestor-alias", "unmounted", "hardlink", "controller", "directory-alias", "allowed"} {
		t.Run(attack, func(t *testing.T) {
			temp, err := os.MkdirTemp("", "wt-settings-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(temp)
			temp = config.CanonicalProviderPath(temp)
			home, cwd, state := filepath.Join(temp, "home"), filepath.Join(temp, "work"), filepath.Join(temp, "state")
			// Keep the socket separate from the nested policy fixture: macOS
			// sockaddr_un cannot fit state/eggs/ours below its long temp root.
			dir := config.CanonicalProviderPath(filepath.Dir(shortSockPath(t)))
			for _, path := range []string{home, cwd, state, dir, filepath.Join(temp, "private")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", state)
			bin := filepath.Join(temp, "claude")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", temp+":"+os.Getenv("PATH"))
			secret := filepath.Join(temp, "private", "settings.json")
			if attack == "controller" {
				secret = filepath.Join(state, "device_token.yaml")
			}
			if err := os.WriteFile(secret, []byte(`{"env":{"STOLEN":"other-user-secret"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			path := secret
			if attack == "allowed" {
				path = filepath.Join(cwd, "settings.json")
				if err := os.Rename(secret, path); err != nil {
					t.Fatal(err)
				}
			}
			fs := []string{"ro:/", "rw:" + cwd, "deny:" + filepath.Dir(secret)}
			if attack == "directory-alias" {
				fs = []string{"ro:/", "rw:" + cwd}
				alias := filepath.Join(cwd, "alias")
				if err := os.Symlink(filepath.Dir(secret), alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "settings.json")
			}
			if attack == "ancestor-alias" {
				alias := filepath.Join(cwd, "alias")
				if err := os.Symlink(filepath.Dir(secret), alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "settings.json")
			}
			if attack == "hardlink" {
				path = filepath.Join(cwd, "settings.json")
				if err := os.Link(secret, path); err != nil {
					t.Fatal(err)
				}
			}
			if attack == "unmounted" {
				if runtime.GOOS != "linux" {
					t.Skip("only Linux deny:/ creates a read allowlist")
				}
				fs = []string{"deny:/", "ro:/usr", "ro:/bin", "rw:" + cwd}
			}
			if attack == "allowed" {
				fs = []string{"ro:/", "rw:" + cwd}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server := &Server{dir: dir}
			err = server.RunSession(ctx, RunConfig{Agent: "claude", ProviderSessionID: "ours", AgentArgs: []string{"--settings", path}, CWD: cwd, UserHome: home, FS: fs, Rows: 24, Cols: 80, Network: []string{"*"}, OmitBrowserBridge: true})
			if attack == "allowed" {
				if err != nil {
					t.Fatalf("allowed provider settings refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "settings file is outside the session filesystem policy") {
				t.Fatalf("host settings bridged despite final policy (%s): %v", attack, err)
			}
		})
	}
}
