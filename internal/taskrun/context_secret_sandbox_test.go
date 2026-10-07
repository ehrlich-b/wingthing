//go:build darwin || linux

package taskrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// Linux sandbox commands re-exec the test binary to install deny mounts.
func TestMain(m *testing.M) {
	if runtime.GOOS == "linux" && len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestHeadlessContextSecretCannotRead(t *testing.T) {
	root, c := contextSecretFixture(t)
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(root, "private"), alias); err != nil {
		t.Fatal(err)
	}
	policy := &egg.EggConfig{FS: []string{"ro:" + alias}}
	for _, dir := range []string{home + "/.claude", home + "/.codex"} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		policy.FS = append(policy.FS, "rw:"+dir)
	}
	control := filepath.Join(work, "readable")
	if err := os.WriteFile(control, []byte("workspace-ok"), 0600); err != nil {
		t.Fatal(err)
	}
	execInSandbox := func(cfg sandbox.Config, script string, paths ...string) (string, error) {
		t.Helper()
		sb, err := sandbox.New(cfg)
		if err != nil {
			var unavailable *sandbox.EnforcementError
			if errors.As(err, &unavailable) {
				t.Skipf("sandbox unavailable: %v", err)
			}
			t.Fatal(err)
		}
		defer func() {
			if err := sb.Destroy(); err != nil {
				t.Errorf("destroy sandbox: %v", err)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		args := append([]string{"-c", script, "context-secret-test"}, paths...)
		cmd, err := sb.Exec(ctx, "/bin/sh", args)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// Establish that default read access really exposes the secret without
	// the guard, and that the namespace/sandbox harness runs on this host.
	unprotected, err := directAgentSandboxConfigForTask(policy, "custom", "standard", home, work, []string{work}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execInSandbox(unprotected, `cat "$1"`, c.SecretFile)
	if err != nil {
		if runtime.GOOS == "darwin" && strings.Contains(out, "sandbox-exec: sandbox_apply: Operation not permitted") {
			t.Skipf("Seatbelt unavailable in this test environment: %v: %s", err, out)
		}
		lower := strings.ToLower(out)
		if runtime.GOOS == "linux" && (strings.Contains(lower, "operation not permitted") || strings.Contains(lower, "permission denied")) {
			t.Skipf("sandbox namespaces unavailable: %v: %s", err, out)
		}
		t.Fatalf("unprotected sandbox control failed: %v: %s", err, out)
	}
	if !strings.Contains(out, "headless-context-secret-must-stay-private") {
		t.Fatalf("unprotected control did not read the secret: %s", out)
	}
	protected, err := directAgentSandboxConfigForTask(policy, "custom", "standard", home, work, []string{work}, false, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{c.SecretFile, filepath.Join(root, "private", "context.secret"), filepath.Join(alias, "context.secret")} {
		out, _ := execInSandbox(protected, `cat "$1"`, path)
		if strings.Contains(out, "headless-context-secret-must-stay-private") {
			t.Fatalf("headless sandbox read secret via %q: %s", path, out)
		}
	}
	out, err = execInSandbox(protected, `cat "$1" && printf ok > "$2" && printf ok > "$3" && printf ok > "$4"`, control, filepath.Join(work, "writable"), filepath.Join(home, ".claude", "state"), filepath.Join(home, ".codex", "state"))
	if err != nil || !strings.Contains(out, "workspace-ok") {
		t.Fatalf("secret protection broke workspace/provider access: %v: %s", err, out)
	}
}

func TestHeadlessContextSecretRefusesWritableOverlap(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt verifies final protected write targets; Linux uses deny mounts")
	}
	root, c := contextSecretFixture(t)
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	private := filepath.Join(root, "private")
	cfg, err := directAgentSandboxConfigForTask(nil, "custom", "standard", home, private, []string{private}, false, c)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(cfg)
	if sb != nil {
		_ = sb.Destroy()
	}
	var conflict *sandbox.ProtectedWriteTargetError
	if !errors.As(err, &conflict) {
		t.Fatalf("writable secret ancestor was not refused: %v", err)
	}
}
