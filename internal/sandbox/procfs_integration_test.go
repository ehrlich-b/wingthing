//go:build linux && integration

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func procfsDogfoodDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func TestSandboxPrivateProcfs(t *testing.T) {
	for _, mode := range []string{"ordinary", "home-readonly", "sealed"} {
		t.Run(mode, func(t *testing.T) {
			root := procfsDogfoodDir(t)
			secret := filepath.Join(root, "secret")
			if err := os.WriteFile(secret, []byte("host-file-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			// The sealed jail mounts canonical paths; the symlinked parent is
			// resolved before installing its allowlist and deny mask.
			secret = filepath.Join(canonicalRoot, "secret")
			cfg := Config{NetworkNeed: NetworkNone, Deny: []string{secret}}
			switch mode {
			case "home-readonly":
				cfg.UserHome = canonicalRoot
				cfg.Mounts = []Mount{{Source: filepath.Join(canonicalRoot, "work"), Target: filepath.Join(canonicalRoot, "work")}}
				if err := os.Mkdir(cfg.Mounts[0].Source, 0o700); err != nil {
					t.Fatal(err)
				}
			case "sealed":
				cfg.Deny = append(cfg.Deny, "/")
				cfg.Mounts = []Mount{{Source: "/usr", Target: "/usr", ReadOnly: true}, {Source: canonicalRoot, Target: canonicalRoot, ReadOnly: true}}
			}
			hostPIDNS, err := os.Readlink("/proc/self/ns/pid")
			if err != nil {
				t.Fatal(err)
			}
			host := exec.Command("/bin/sleep", "30")
			host.Env = append(os.Environ(), "WT_PROCFS_HOST_SECRET=host-process-secret")
			if err := host.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })
			probe := fmt.Sprintf(`python3 -c '
import os
pid = os.getpid()
status = dict(line.split(":", 1) for line in open("/proc/self/status") if ":" in line)
assert int(status["Pid"].strip()) == pid, status
assert status["NSpid"].split() == [str(pid)], status
assert os.getuid() == %d, os.getuid()
assert os.readlink("/proc/self/ns/pid") != %q
assert not os.path.exists("/proc/%%d" %% %d), "host process visible"
assert "host-file-secret" not in open(%q).read(), "filesystem mask lost"
' && ps -p $$ -o pid= && echo private-procfs-ok`, os.Getuid(), hostPIDNS, host.Process.Pid, secret)
			out, err := runJail(t, cfg, probe)
			if err != nil || !strings.HasSuffix(out, "private-procfs-ok") {
				t.Fatalf("private procfs probe failed: %v\n%s", err, out)
			}
		})
	}
}

func TestSandboxProcfsSwapRefusesLostPolicyMask(t *testing.T) {
	root := procfsDogfoodDir(t)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "executed")
	out, err := runJail(t, Config{
		NetworkNeed: NetworkNone,
		Deny:        []string{"/proc/sys"},
		Mounts:      []Mount{{Source: canonicalRoot, Target: canonicalRoot}},
	}, fmt.Sprintf("touch %q", marker))
	if err == nil || !strings.Contains(out, "verify filesystem policy after procfs swap") {
		t.Fatalf("lost /proc mask was not refused: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran after losing its policy mask: %v", err)
	}
}
