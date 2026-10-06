//go:build linux

package egg

import (
	"context"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
	}
}

func TestLinuxControlPolicyPreservesBridgesAndBlocksFutureSiblings(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	root, control, bridges, capability := controlIsolationFixture(t)
	createIsolationSibling(t, root, "sibling")
	dotfile := filepath.Join(root, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(dotfile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dotfile, []byte("real dotfile symlink"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfile, filepath.Join(root, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("WT_TEST_BIND_ALIASES") != "" {
		if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
			t.Fatal(err)
		}
		for name, source := range map[string]string{"state": filepath.Join(root, "state"), "sibling": filepath.Join(root, "state", "eggs", "sibling")} {
			alias := filepath.Join(root, "mnt", name)
			if err := os.MkdirAll(alias, 0700); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mount(source, alias, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(alias, unix.MNT_DETACH) })
		}
		control = append(control, filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml"))
	}
	own := filepath.Join(root, "state", "eggs", "own")
	ownControl, err := net.Listen("unix", filepath.Join(own, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownControl.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mounts := []sandbox.Mount{{Source: root, Target: root, ReadOnly: true}, {Source: filepath.Join(root, ".claude"), Target: filepath.Join(root, ".claude")}, {Source: filepath.Dir(exe), Target: filepath.Dir(exe), ReadOnly: true}}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			mounts = append(mounts, sandbox.Mount{Source: path, Target: path, ReadOnly: true})
		}
	}
	mounts, err = isolateLinuxEggControl(mounts, control, bridges)
	if err != nil {
		t.Fatal(err)
	}
	// This launch must exclude a sibling that did not exist when the
	// allowlist was compiled, without needing any refreshed deny list.
	createIsolationSibling(t, root, "future")
	home := filepath.Join(own, ".sandbox-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := sandbox.Config{Mounts: mounts, Deny: []string{"/", filepath.Join(root, "state", "wing_key"), filepath.Join(root, "state", "device_token.yaml")}, UserHome: home, NetworkNeed: sandbox.NetworkNone}
	sb, err := sandbox.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Destroy() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, exe, []string{"-test.run=^TestEggControlIsolationProcess$", "-test.v"})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "WT_TEST_CONTROL_ROOT="+root, "HOME="+root, "WT_TEST_DOTFILE_ALIAS=1", ToolCapabilityEnv+"="+capability)
	output, err := cmd.CombinedOutput()
	if err != nil {
		diag, _ := os.ReadFile(sb.DiagLog())
		t.Fatalf("control isolation: %v\n%s\n%s", err, output, diag)
	}
}

func TestLinuxControlPolicyExcludesPhysicalBindAliases(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLinuxControlPolicyPreservesBridgesAndBlocksFutureSiblings$", "-test.v")
	cmd.Env = append(os.Environ(), "WT_TEST_BIND_ALIASES=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	if os.Getuid() != 0 {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("physical aliases: %v\n%s", err, output)
	}
}
