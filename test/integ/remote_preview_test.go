//go:build e2e

package integ

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemotePreviewRejectsStableExecutableBeforeStateAccess(t *testing.T) {
	stable, preview := os.Getenv("WT_TEST_BINARY"), os.Getenv("WT_TEST_PREVIEW_BINARY")
	if stable == "" || preview == "" {
		t.Fatal("stable and preview test binaries are required; run make test-integ")
	}
	fixture := t.TempDir()
	binDir, home := filepath.Join(fixture, "bin"), filepath.Join(fixture, "home")
	for _, dir := range []string{binDir, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	localState := filepath.Join(fixture, "local-preview-must-stay-absent")
	remoteState := filepath.Join(fixture, "remote-stable-must-stay-absent")
	sshPath := filepath.Join(binDir, "ssh")
	script := "#!/bin/sh\nexec env WINGTHING_DIR=\"$WT_FIXTURE_REMOTE_STATE\" HOME=\"$WT_FIXTURE_HOME\" /bin/sh -c \"$3\"\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// Give the default remote name a stable build deliberately. Its filename
	// must never substitute for the executable's actual channel identity.
	if err := os.Symlink(stable, filepath.Join(binDir, "wt-preview")); err != nil {
		t.Fatal(err)
	}
	for _, explicitPath := range []bool{false, true} {
		args := []string{"--remote", "fixture-host"}
		if explicitPath {
			args = append(args, "--remote-binary", stable)
		}
		args = append(args, "--json")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		child := exec.CommandContext(ctx, preview, args...)
		child.Env = append(os.Environ(), "HOME="+home, "WINGTHING_DIR="+localState, "WINGTHING_PREVIEW_DIR="+localState, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "WT_FIXTURE_REMOTE_STATE="+remoteState, "WT_FIXTURE_HOME="+home)
		output, err := child.CombinedOutput()
		cancel()
		if err == nil || !strings.Contains(string(output), "release channel mismatch") {
			t.Fatalf("explicitPath=%v preview accepted stable remote: err=%v output=%s", explicitPath, err, output)
		}
		for _, untouched := range []string{localState, remoteState} {
			if _, err := os.Stat(untouched); !os.IsNotExist(err) {
				t.Fatalf("channel mismatch touched %s: %v", untouched, err)
			}
		}
	}
}
