//go:build linux

package taskrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
	}
}

func TestHeadlessContextPhysicalAliases(t *testing.T) {
	if os.Getenv("WT_TEST_CONTEXT_BINDS") == "" {
		if ok, reason := sandbox.CheckCapability(); !ok {
			t.Skipf("namespaces unavailable: %s", reason)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestHeadlessContextPhysicalAliases$", "-test.v")
		cmd.Env = append(os.Environ(), "WT_TEST_CONTEXT_BINDS=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		if os.Getuid() != 0 {
			cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
			cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
			cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("physical Context aliases: %v\n%s", err, out)
		}
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	root, c := contextSecretFixture(t)
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	private := filepath.Join(root, "private")
	dirAlias, fileAlias := filepath.Join(work, "private"), filepath.Join(work, "secret-file")
	if err := os.Mkdir(dirAlias, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileAlias, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for target, source := range map[string]string{dirAlias: private, fileAlias: filepath.Join(private, "context.secret")} {
		if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Unmount(target, unix.MNT_DETACH) })
	}
	run := func(cfg sandbox.Config, script string, args ...string) (string, error) {
		t.Helper()
		// Other packages can remove temporary siblings between default root
		// enumeration and exec. Retain this fixture and the stable runtime.
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		var mounts []sandbox.Mount
		for _, mount := range cfg.Mounts {
			if strings.HasPrefix(mount.Source, root+"/") || mount.Source == exe || mount.Source == "/usr" || mount.Source == "/bin" || mount.Source == "/lib" || mount.Source == "/lib64" {
				mounts = append(mounts, mount)
			}
		}
		cfg.Mounts = mounts
		sb, err := sandbox.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer sb.Destroy()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd, err := sb.Exec(ctx, "/bin/sh", append([]string{"-c", script, "context-bind-test"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		if err != nil {
			diag, _ := os.ReadFile(sb.DiagLog())
			out = append(out, diag...)
		}
		return string(out), err
	}
	policy := &egg.EggConfig{}
	unprotected, err := directAgentSandboxConfigForTask(policy, "custom", "standard", home, work, []string{work}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{filepath.Join(dirAlias, "context.secret"), fileAlias} {
		out, err := run(unprotected, `cat "$1"`, alias)
		if err != nil || !strings.Contains(out, "headless-context-secret-must-stay-private") {
			t.Fatalf("alias control did not read secret: %v, %s", err, out)
		}
	}
	protected, err := directAgentSandboxConfigForTask(policy, "custom", "standard", home, work, []string{work}, false, c)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(protected, `for path do if [ "$(cat "$path" 2>/dev/null)" = "headless-context-secret-must-stay-private" ]; then exit 1; fi; done; printf ok > ordinary`, c.SecretFile, filepath.Join(dirAlias, "context.secret"), fileAlias)
	if err != nil {
		t.Fatalf("Context bind alias readable: %v, %s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(work, "ordinary")); err != nil || string(data) != "ok" {
		t.Fatalf("workspace write failed: %q, %v", data, err)
	}
}

func TestHeadlessClaudeConfigAtomicReplacementPersists(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("namespaces unavailable: %s", reason)
	}
	for _, readonly := range []bool{false, true} {
		name := "inferred root read"
		if readonly {
			name = "explicit file read"
		}
		t.Run(name, func(t *testing.T) {
			root, _ := contextSecretFixture(t)
			home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
			t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
			t.Setenv("WT_PROVIDER_BASE_URL", "")
			if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".claude.json")
			if err := os.WriteFile(path, []byte("initial"), 0600); err != nil {
				t.Fatal(err)
			}
			policy := egg.DefaultEggConfig()
			if readonly {
				policy.FS = append(policy.FS, "ro:"+path)
			}
			cfg, err := directAgentSandboxConfigForTask(policy, "claude", "standard", home, work, []string{work}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the default HOME split, but omit unrelated temporary siblings
			// that another package can remove before the namespace starts.
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var mounts []sandbox.Mount
			for _, mount := range cfg.Mounts {
				if strings.HasPrefix(mount.Source, root+"/") || mount.Source == exe || mount.Source == "/usr" || mount.Source == "/bin" || mount.Source == "/lib" || mount.Source == "/lib64" {
					mounts = append(mounts, mount)
				}
			}
			cfg.Mounts = mounts
			sb, err := sandbox.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer sb.Destroy()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `test "$(cat "$HOME/.claude.json")" = initial && printf persisted > "$HOME/.claude/state" && printf rewritten > "$HOME/.claude.json.tmp" && mv "$HOME/.claude.json.tmp" "$HOME/.claude.json"`})
			if err != nil {
				t.Fatal(err)
			}
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "HOME="+home)
			out, runErr := cmd.CombinedOutput()
			want := "rewritten"
			if readonly {
				want = "initial"
				if runErr == nil {
					t.Fatal("explicit read-only file became replaceable")
				}
			} else if runErr != nil {
				diag, _ := os.ReadFile(sb.DiagLog())
				t.Fatalf("atomic Claude config replacement failed: %v\n%s\n%s", runErr, out, diag)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != want {
				t.Fatalf("prefix config persistence: %q, %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(home, ".claude", "state")); err != nil || string(data) != "persisted" {
				t.Fatalf("agent directory write lost: %q, %v", data, err)
			}
		})
	}
}
