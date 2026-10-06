//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const hardeningTestArg = "_sandbox_hardening_test"

// Mount and pivot operations run only in a disposable child namespace. This
// re-exec also works without the integration battery's TestMain.
func init() {
	if len(os.Args) != 4 || os.Args[1] != hardeningTestArg {
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := runHardeningScenario(os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runHardeningNamespace(t *testing.T, scenario, root string) {
	t.Helper()
	hasCapability := hasEffectiveCAPSYSADMIN()
	if err := namespaceCapabilityError(hasCapability); err != nil {
		t.Skipf("Linux mount namespaces unavailable: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, hardeningTestArg, scenario, root)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	if !hasCapability {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", scenario, err, output)
	}
}

func runHardeningScenario(scenario, root string) error {
	switch scenario {
	case "jail-home", "jail-home-ro":
		home := filepath.Join(root, "home")
		config := filepath.Join(home, ".codex")
		readonly := filepath.Join(home, "reference")
		secret := filepath.Join(home, "private")
		for _, dir := range []string{config, readonly} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.WriteFile(secret, []byte("host-only"), 0o600); err != nil {
			return err
		}
		ro := []string{"/usr", readonly}
		if scenario == "jail-home-ro" {
			ro = append(ro, home)
		}
		setupJail(root, ro, []string{config}, home)
		if scenario == "jail-home" {
			if _, err := os.ReadFile(secret); !os.IsNotExist(err) {
				return fmt.Errorf("undeclared HOME file exposed: %v", err)
			}
		} else if err := os.WriteFile(secret, []byte("overwritten"), 0o600); err == nil {
			return fmt.Errorf("read-only HOME became writable")
		}
		if err := os.WriteFile(filepath.Join(config, "state"), []byte("persisted"), 0o600); err != nil {
			return fmt.Errorf("agent config not writable: %w", err)
		}
		if err := os.WriteFile(filepath.Join(readonly, "new"), nil, 0o600); err == nil {
			return fmt.Errorf("declared read-only HOME path became writable")
		}
		return nil
	default:
		return fmt.Errorf("unknown hardening scenario %q", scenario)
	}
}

func TestJailExposesOnlyDeclaredHomePaths(t *testing.T) {
	for _, scenario := range []string{"jail-home", "jail-home-ro"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			runHardeningNamespace(t, scenario, root)
			data, err := os.ReadFile(filepath.Join(root, "home", ".codex", "state"))
			if err != nil || string(data) != "persisted" {
				t.Fatalf("agent config did not persist: %q, %v", data, err)
			}
		})
	}
}

func TestJailMountOrderPreservesDeclaredModes(t *testing.T) {
	mounts := jailMounts([]string{"/home/u/.codex/config", "/home/u", "/usr"}, []string{"/home/u/.codex"})
	want := []expectedMount{
		{Path: "/usr", ReadOnly: true},
		{Path: "/home/u", ReadOnly: true},
		{Path: "/home/u/.codex", Writable: true},
		{Path: "/home/u/.codex/config", ReadOnly: true},
	}
	if fmt.Sprint(mounts) != fmt.Sprint(want) {
		t.Fatalf("mount order = %v, want %v", mounts, want)
	}
}
