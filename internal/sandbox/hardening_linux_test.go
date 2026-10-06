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
	case "deny-write":
		home, workspace, tmp := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "session")
		for _, dir := range []string{home, workspace, tmp} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		existing := filepath.Join(workspace, "policy.yaml")
		if err := os.WriteFile(existing, []byte("readable policy"), 0o600); err != nil {
			return err
		}
		if err := os.Chdir(workspace); err != nil {
			return err
		}
		command := `test "$(cat policy.yaml)" = "readable policy" && ! printf evil > policy.yaml && ! printf evil > egg.yaml && ! rm -rf egg.yaml && ! mv replacement egg.yaml && ! mkdir egg.yaml && printf allowed > ordinary`
		if err := os.WriteFile(filepath.Join(workspace, "replacement"), []byte("base: none"), 0o600); err != nil {
			return err
		}
		DenyInit([]string{"--uid", "0", "--gid", "0", "--log", filepath.Join(tmp, "deny.log"), "--home", home, "--writable", workspace, "--deny-write", existing, "--deny-write", filepath.Join(workspace, "egg.yaml"), "--", "/bin/sh", "-c", command})
		return fmt.Errorf("DenyInit returned")
	case "readonly-prefix":
		home := filepath.Join(root, "home")
		writable := []string{filepath.Join(home, ".cache"), filepath.Join(home, ".claude")}
		for _, dir := range writable {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		for _, name := range []string{".zshrc", ".cache-sibling", ".claude.json"} {
			if err := os.WriteFile(filepath.Join(home, name), []byte("unchanged"), 0o600); err != nil {
				return err
			}
		}
		for _, name := range []string{".cache-host", ".claude-host"} {
			if err := os.Symlink(filepath.Join(home, ".zshrc"), filepath.Join(home, name)); err != nil {
				return err
			}
		}
		if err := setupReadonlyHome(home, writable, []string{".claude"}); err != nil {
			return err
		}
		for _, name := range []string{".zshrc", ".cache-host", ".claude-host", ".cache-sibling"} {
			if err := os.WriteFile(filepath.Join(home, name), []byte("overwritten"), 0o600); err == nil {
				return fmt.Errorf("undeclared prefix write allowed: %s", name)
			}
		}
		return os.WriteFile(filepath.Join(home, ".claude.json"), []byte("allowed"), 0o600)
	case "overlay-home", "overlay-root-agent":
		home, tmp := filepath.Join(root, "home"), filepath.Join(root, "session")
		config := filepath.Join(home, ".claude")
		for _, dir := range []string{config, tmp} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		secret := filepath.Join(home, ".ssh", "key")
		if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
			return err
		}
		if scenario == "overlay-root-agent" {
			command := fmt.Sprintf(`for fd in /proc/%d/fd/*; do if test -r "$fd/.ssh/key"; then exit 1; fi; done; test ! -r %s/real-home/.ssh/key && printf persisted > %s/.claude.json`, os.Getpid(), tmp, home)
			DenyInit([]string{"--uid", "0", "--gid", "0", "--log", filepath.Join(tmp, "deny.log"), "--home", home, "--writable", config, "--overlay-prefix", ".claude", "--", "/bin/sh", "-c", command})
			return fmt.Errorf("DenyInit returned")
		}
		persist := setupOverlayHome(home, []string{config}, []string{".claude"}, tmp)
		if persist == nil {
			return fmt.Errorf("overlayfs unavailable")
		}
		alias := filepath.Join(tmp, "real-home")
		if _, err := os.ReadFile(filepath.Join(alias, ".ssh", "key")); !os.IsNotExist(err) {
			return fmt.Errorf("overlay exposed backing secrets: %v", err)
		}
		if _, err := os.Stat(filepath.Join(alias, ".claude")); !os.IsNotExist(err) {
			return fmt.Errorf("overlay exposed a writable backing config alias: %v", err)
		}
		if err := os.WriteFile(filepath.Join(config, "state"), []byte("persistent config"), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("atomic config"), 0o600); err != nil {
			return err
		}
		persist()
		return nil
	case "overlay-fallback":
		home, tmp := filepath.Join(root, "home"), filepath.Join(root, "session")
		for _, dir := range []string{home, tmp} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(home, "secret"), []byte("host secret"), 0o600); err != nil {
			return err
		}
		// A regular file makes upperdir setup fail after real-home was bound.
		if err := os.WriteFile(filepath.Join(tmp, "overlay-upper"), nil, 0o600); err != nil {
			return err
		}
		if persist := setupOverlayHome(home, nil, nil, tmp); persist != nil {
			return fmt.Errorf("expected overlay fallback")
		}
		entries, err := os.ReadDir(filepath.Join(tmp, "real-home"))
		if err != nil || len(entries) != 0 {
			return fmt.Errorf("fallback retained backing HOME: %v, %v", entries, err)
		}
		return nil
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

func TestOverlayBackingHomeIsHidden(t *testing.T) {
	root := t.TempDir()
	runHardeningNamespace(t, "overlay-home", root)
	for _, file := range []string{".claude/state", ".claude.json"} {
		data, err := os.ReadFile(filepath.Join(root, "home", file))
		if err != nil || len(data) == 0 {
			t.Fatalf("overlay config did not persist: %s: %q, %v", file, data, err)
		}
	}
}

func TestOverlayFallbackHidesBackingHome(t *testing.T) {
	runHardeningNamespace(t, "overlay-fallback", t.TempDir())
}

func TestOverlayRootAgentCannotReadWrapperBackingFDs(t *testing.T) {
	root := t.TempDir()
	runHardeningNamespace(t, "overlay-root-agent", root)
	data, err := os.ReadFile(filepath.Join(root, "home", ".claude.json"))
	if err != nil || string(data) != "persisted" {
		t.Fatalf("root agent's prefix config did not persist: %q, %v", data, err)
	}
}

func TestReadonlyHomeRejectsSiblingSymlinkWrites(t *testing.T) {
	runHardeningNamespace(t, "readonly-prefix", t.TempDir())
}

func TestDenyWriteMissingFileCannotBeCreatedOrReplaced(t *testing.T) {
	root := t.TempDir()
	runHardeningNamespace(t, "deny-write", root)
	data, err := os.ReadFile(filepath.Join(root, "work", "ordinary"))
	if err != nil || string(data) != "allowed" {
		t.Fatalf("ordinary workspace writes failed: %q, %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(root, "work", "egg.yaml")); err != nil || !info.IsDir() {
		t.Fatalf("absent policy became a discoverable file: %v, %v", info, err)
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
