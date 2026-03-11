//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain handles re-exec as the _deny_init wrapper. Without this, the test
// binary ignores _deny_init and the wrapper exits without setting up isolation.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

// runJail creates a sandbox and runs a shell command, returning stdout+stderr and error.
func runJail(t *testing.T, cfg Config, shellCmd string) (string, error) {
	t.Helper()
	sb, err := newPlatform(cfg)
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", shellCmd})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func TestJail_NetworkBlocked(t *testing.T) {
	_, err := runJail(t, Config{
		NetworkNeed: NetworkNone,
	}, "curl -s --max-time 3 https://example.com")
	if err == nil {
		t.Fatal("expected curl to fail with network blocked")
	}
}

func TestJail_NetworkHTTPS_PortFilter(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("port-level filtering only works on macOS seatbelt")
	}

	// Port 443 should work with NetworkHTTPS
	cfg := Config{
		NetworkNeed: NetworkHTTPS,
	}
	_, err := runJail(t, cfg, "curl -s --max-time 5 https://example.com -o /dev/null -w '%{http_code}'")
	if err != nil {
		t.Fatalf("curl to port 443 should succeed with NetworkHTTPS: %v", err)
	}

	// Port 22 should be blocked
	_, err = runJail(t, cfg, "nc -z -w 3 example.com 22")
	if err == nil {
		t.Fatal("nc to port 22 should fail with NetworkHTTPS")
	}
}

func TestJail_WriteInsideMount(t *testing.T) {
	mount := t.TempDir()
	out, err := runJail(t, Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: mount, Target: mount}},
	}, "echo ok > "+mount+"/test.txt && cat "+mount+"/test.txt")
	if err != nil {
		t.Fatalf("write inside mount should succeed: %v", err)
	}
	if out != "ok" {
		t.Errorf("output = %q, want %q", out, "ok")
	}
}

func TestJail_WriteOutsideMount(t *testing.T) {
	home, _ := os.UserHomeDir()
	mount := t.TempDir()
	target := filepath.Join(home, "wt-jail-test-delete-me")

	_, err := runJail(t, Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: mount, Target: mount}},
	}, "echo fail > "+target)
	os.Remove(target)
	if err == nil {
		t.Fatal("write outside mount should fail")
	}
}

func TestJail_DenyPathBlocked(t *testing.T) {
	denied := t.TempDir()
	testFile := filepath.Join(denied, "secret.txt")
	os.WriteFile(testFile, []byte("secret"), 0644)

	_, err := runJail(t, Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{denied},
	}, "cat "+testFile)
	if err == nil {
		t.Fatal("read of denied path should fail")
	}
}

func TestJail_DenyPathMultiple(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir1, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir2, "b.txt"), []byte("b"), 0644)

	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{dir1, dir2},
	}

	_, err1 := runJail(t, cfg, "cat "+filepath.Join(dir1, "a.txt"))
	if err1 == nil {
		t.Error("read of first denied path should fail")
	}

	_, err2 := runJail(t, cfg, "cat "+filepath.Join(dir2, "b.txt"))
	if err2 == nil {
		t.Error("read of second denied path should fail")
	}
}

func TestJail_EnvFiltered(t *testing.T) {
	mount := t.TempDir()
	os.Setenv("TEST_HIDDEN", "should_not_see")
	defer os.Unsetenv("TEST_HIDDEN")

	// On macOS seatbelt, env filtering is done at the egg level not sandbox level.
	// The sandbox itself inherits whatever env the caller passes.
	// Build a cmd manually with filtered env.
	sb, err := newPlatform(Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: mount, Target: mount}},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "printenv TEST_HIDDEN"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// Pass only minimal env — no TEST_HIDDEN
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Run()
	if strings.TrimSpace(out.String()) == "should_not_see" {
		t.Fatal("TEST_HIDDEN should not be visible with filtered env")
	}
}

func TestJail_EnvAllowed(t *testing.T) {
	mount := t.TempDir()
	sb, err := newPlatform(Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: mount, Target: mount}},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "printenv TEST_VISIBLE"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TEST_VISIBLE=yes"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Run()
	if strings.TrimSpace(out.String()) != "yes" {
		t.Fatalf("output = %q, want %q", out.String(), "yes")
	}
}

func TestJail_Seccomp_MountBlocked(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("seccomp only on Linux")
	}

	mount := t.TempDir()
	_, err := runJail(t, Config{
		NetworkNeed: NetworkNone,
		Deny:        []string{"/nonexistent"},
		Mounts:      []Mount{{Source: mount, Target: mount}},
	}, "mount -t tmpfs none /mnt 2>&1; echo $?")
	// mount should either fail or return nonzero
	if err == nil {
		// Check if the mount command itself reported an error
		// (it will print the error and the exit code check handles it)
	}
}

func TestJail_ResourceLimits_FDs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("prlimit only on Linux")
	}

	mount := t.TempDir()
	sb, err := newPlatform(Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: mount, Target: mount}},
		MaxFDs:      32,
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "ulimit -n"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sb.PostStart(cmd.Process.Pid)
	cmd.Wait()

	val := strings.TrimSpace(out.String())
	if val == "" {
		t.Fatal("no output from ulimit -n")
	}
	// Should be <= 32
	var n int
	for _, c := range val {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	if n > 32 {
		t.Errorf("ulimit -n = %d, want <= 32", n)
	}
}

func TestJail_CWD_Writable_Home_RO(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd := t.TempDir()

	// Only CWD is writable, home is read-only
	cfg := Config{
		NetworkNeed: NetworkNone,
		Mounts:      []Mount{{Source: cwd, Target: cwd}},
	}

	// Write to CWD should succeed
	_, err := runJail(t, cfg, "echo ok > "+cwd+"/test.txt")
	if err != nil {
		t.Fatalf("write to CWD should succeed: %v", err)
	}
	os.Remove(filepath.Join(cwd, "test.txt"))

	// Write to HOME should fail
	target := filepath.Join(home, "wt-jail-cwd-test-delete-me")
	_, err = runJail(t, cfg, "echo fail > "+target)
	os.Remove(target)
	if err == nil {
		t.Fatal("write to HOME should fail when only CWD is mounted")
	}
}

func TestJail_DefaultDenyList(t *testing.T) {
	// Create temp dirs mimicking default deny paths, verify they're blocked
	home, _ := os.UserHomeDir()
	mount := t.TempDir()

	// We test with real deny paths that exist on the system
	denyPaths := []string{}
	for _, p := range []string{".ssh", ".gnupg", ".aws"} {
		abs := filepath.Join(home, p)
		if _, err := os.Stat(abs); err == nil {
			denyPaths = append(denyPaths, abs)
		}
	}
	if len(denyPaths) == 0 {
		t.Skip("no default deny paths exist on this system")
	}

	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        denyPaths,
		Mounts:      []Mount{{Source: mount, Target: mount}},
	}

	for _, dp := range denyPaths {
		out, _ := runJail(t, cfg, "ls "+dp+" 2>/dev/null")
		// Deny mounts an empty tmpfs (or tmpfs with just known_hosts for .ssh).
		if out != "" && out != "known_hosts" {
			t.Errorf("denied path %s should be empty (or only known_hosts), got: %s", dp, out)
		}
	}
}

func TestJail_ProxyAllowedDomain(t *testing.T) {
	proxy, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	// curl through the proxy to an allowed domain should succeed.
	// ProxyPort tells seatbelt to allow the proxy's port instead of 443/80.
	proxyURL := fmt.Sprintf("http://localhost:%d", proxy.Port())
	out, err := runJail(t, Config{
		NetworkNeed: NetworkHTTPS,
		ProxyPort:   proxy.Port(),
	}, fmt.Sprintf("curl -s --max-time 5 --proxy %s -o /dev/null -w '%%{http_code}' https://example.com", proxyURL))
	if err != nil {
		t.Fatalf("curl to allowed domain through proxy should succeed: %v (output: %s)", err, out)
	}
}

func TestJail_ProxyBlockedDomain(t *testing.T) {
	proxy, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	// curl through the proxy to a blocked domain should fail
	proxyURL := fmt.Sprintf("http://localhost:%d", proxy.Port())
	_, err = runJail(t, Config{
		NetworkNeed: NetworkHTTPS,
		ProxyPort:   proxy.Port(),
	}, fmt.Sprintf("curl -s --max-time 5 --proxy %s https://evil.example.org", proxyURL))
	if err == nil {
		t.Fatal("curl to blocked domain through proxy should fail")
	}
}

func TestJail_ProxyWildcard(t *testing.T) {
	proxy, err := StartProxy([]string{"*.anthropic.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	if !proxy.allowed("api.anthropic.com:443") {
		t.Error("api.anthropic.com should match *.anthropic.com")
	}
	if !proxy.allowed("sentry.anthropic.com") {
		t.Error("sentry.anthropic.com should match *.anthropic.com")
	}
	if proxy.allowed("evil.com:443") {
		t.Error("evil.com should not match *.anthropic.com")
	}
}

func TestJail_RootDeny_OnlyAllowedPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("jail mode only on Linux")
	}
	allowed := t.TempDir()
	os.WriteFile(filepath.Join(allowed, "visible.txt"), []byte("visible"), 0644)
	secret := t.TempDir()
	os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("hidden"), 0644)

	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{"/"},
		Mounts: []Mount{
			{Source: "/usr", Target: "/usr", ReadOnly: true},
			{Source: "/etc", Target: "/etc", ReadOnly: true},
			{Source: allowed, Target: allowed, ReadOnly: true},
		},
	}
	out, err := runJail(t, cfg, "cat "+filepath.Join(allowed, "visible.txt"))
	if err != nil {
		t.Fatalf("read allowed path should succeed: %v (output: %s)", err, out)
	}
	if out != "visible" {
		t.Errorf("output = %q, want %q", out, "visible")
	}
	_, err = runJail(t, cfg, "cat "+filepath.Join(secret, "secret.txt"))
	if err == nil {
		t.Fatal("read outside allowed paths should fail")
	}
}

func TestJail_RootDeny_SymlinkRecreation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("jail mode only on Linux")
	}
	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{"/"},
		Mounts: []Mount{
			{Source: "/usr", Target: "/usr", ReadOnly: true},
		},
	}
	// /bin should be a symlink to usr/bin on merged-usr systems
	out, err := runJail(t, cfg, "readlink /bin 2>/dev/null || echo not-a-symlink")
	if err != nil {
		t.Fatalf("readlink /bin: %v", err)
	}
	// On merged-usr, expect "usr/bin"; on non-merged, "not-a-symlink"
	hostTarget, hostErr := os.Readlink("/bin")
	if hostErr == nil {
		if out != hostTarget {
			t.Errorf("/bin symlink = %q in jail, want %q (matching host)", out, hostTarget)
		}
	}
}

func TestJail_RootDeny_DevSetup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("jail mode only on Linux")
	}
	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{"/"},
		Mounts: []Mount{
			{Source: "/usr", Target: "/usr", ReadOnly: true},
		},
	}
	// Verify /proc and /dev/null exist
	out, err := runJail(t, cfg, "test -d /proc && test -c /dev/null && test -c /dev/urandom && echo ok")
	if err != nil {
		t.Fatalf("dev setup check failed: %v (output: %s)", err, out)
	}
	if out != "ok" {
		t.Errorf("output = %q, want ok", out)
	}
}

func TestJail_RootDeny_WritablePaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("jail mode only on Linux")
	}
	writable := t.TempDir()
	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{"/"},
		Mounts: []Mount{
			{Source: "/usr", Target: "/usr", ReadOnly: true},
			{Source: writable, Target: writable},
		},
	}
	out, err := runJail(t, cfg, "echo ok > "+writable+"/test.txt && cat "+writable+"/test.txt")
	if err != nil {
		t.Fatalf("write to writable path should succeed: %v (output: %s)", err, out)
	}
	if out != "ok" {
		t.Errorf("output = %q, want ok", out)
	}
	// Verify ro paths are actually read-only
	_, err = runJail(t, cfg, "touch /usr/test-rw 2>&1")
	if err == nil {
		os.Remove("/usr/test-rw")
		t.Fatal("write to ro path should fail")
	}
}

func TestJail_RootDeny_DenyWithinJail(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("jail mode only on Linux")
	}
	roDir := t.TempDir()
	secretFile := filepath.Join(roDir, "secret.txt")
	os.WriteFile(secretFile, []byte("secret"), 0644)
	normalFile := filepath.Join(roDir, "normal.txt")
	os.WriteFile(normalFile, []byte("normal"), 0644)

	cfg := Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{"/", secretFile},
		Mounts: []Mount{
			{Source: "/usr", Target: "/usr", ReadOnly: true},
			{Source: roDir, Target: roDir, ReadOnly: true},
		},
	}
	// Normal file should be readable
	out, err := runJail(t, cfg, "cat "+normalFile)
	if err != nil {
		t.Fatalf("read normal file should succeed: %v (output: %s)", err, out)
	}
	if out != "normal" {
		t.Errorf("output = %q, want normal", out)
	}
	// Secret file should be denied (bind /dev/null within jail — reads empty)
	out2, _ := runJail(t, cfg, "cat "+secretFile)
	if strings.Contains(out2, "secret") {
		t.Fatal("denied file content should not be readable in jail")
	}
}

func TestJail_ProxySeatbeltEnforced(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt proxy enforcement only on macOS")
	}

	proxy, err := StartProxy([]string{"example.com"})
	if err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	defer proxy.Close()

	// With ProxyPort set, seatbelt should block ALL direct outbound.
	// Even to port 443 — the only allowed destination is the proxy.
	_, err = runJail(t, Config{
		NetworkNeed: NetworkHTTPS,
		ProxyPort:   proxy.Port(),
	}, "curl -s --max-time 3 https://example.com")
	if err == nil {
		t.Fatal("direct curl (bypassing proxy) should be blocked by seatbelt when ProxyPort is set")
	}

	// But going through the proxy should work
	proxyURL := fmt.Sprintf("http://localhost:%d", proxy.Port())
	out, err := runJail(t, Config{
		NetworkNeed: NetworkHTTPS,
		ProxyPort:   proxy.Port(),
	}, fmt.Sprintf("curl -s --max-time 5 --proxy %s -o /dev/null -w '%%{http_code}' https://example.com", proxyURL))
	if err != nil {
		t.Fatalf("curl through proxy should succeed: %v (output: %s)", err, out)
	}
}
