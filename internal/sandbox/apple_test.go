//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sandboxExecAvailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sandbox-exec", "-p", "(version 1)(allow default)", "/bin/echo", "ok")
	if err := cmd.Run(); err != nil {
		t.Skip("sandbox-exec unavailable (nested sandbox or SIP): ", err)
	}
}

func destroySandboxForTest(t *testing.T, sb Sandbox) {
	t.Helper()
	if err := sb.Destroy(); err != nil {
		t.Errorf("destroy sandbox: %v", err)
	}
}

func TestBuildProfileNetworkDeny(t *testing.T) {
	profile := buildProfile(Config{NetworkNeed: NetworkNone})
	if !strings.Contains(profile, "(deny network*)") {
		t.Errorf("NetworkNone profile should deny network, got:\n%s", profile)
	}
}

func TestBuildProfileNetworkAllow(t *testing.T) {
	profile := buildProfile(Config{NetworkNeed: NetworkFull})
	if strings.Contains(profile, "(deny network*)") {
		t.Errorf("NetworkFull profile should not deny network, got:\n%s", profile)
	}
}

func TestBuildProfileLocalPorts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports []int
	}{
		{name: "no declared ports"},
		{name: "declared provider ports", ports: []int{11434, 4000, 65535}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := buildProfile(Config{NetworkNeed: NetworkLocal, LocalPorts: tc.ports})
			want := "(version 1)\n(allow default)\n(deny network*)\n"
			for _, port := range tc.ports {
				want += fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%d\"))\n", port)
			}
			if profile != want {
				t.Fatalf("local profile =\n%s\nwant\n%s", profile, want)
			}
		})
	}
}

func TestBuildProfileDeclaredPortsAndSocketsSurviveProxyDeny(t *testing.T) {
	socket, err := canonicalSandboxPath(filepath.Join(t.TempDir(), "tool.sock"))
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{
		NetworkNeed:  NetworkHTTPS,
		ProxyPort:    43210,
		LocalPorts:   []int{4000},
		AllowSockets: []string{socket},
	})
	for _, rule := range []string{
		`(allow network-outbound (remote ip "localhost:4000"))`,
		fmt.Sprintf("(allow network-outbound (literal %q))", socket),
	} {
		if i := strings.Index(profile, rule); i < strings.Index(profile, "(deny network*)") || i < 0 {
			t.Fatalf("declared endpoint must be allowed after network deny: %s\n%s", rule, profile)
		}
	}
	if strings.Contains(profile, "localhost:*") {
		t.Fatalf("profile permits undeclared loopback ports:\n%s", profile)
	}
}

func TestBuildProfileProxyHasNoDirectDNS(t *testing.T) {
	for _, need := range []NetworkNeed{NetworkNone, NetworkLocal, NetworkHTTPS, NetworkFull} {
		t.Run(need.String(), func(t *testing.T) {
			profile := buildProfile(Config{NetworkNeed: need, ProxyPort: 43210})
			want := "(version 1)\n(allow default)\n(deny network*)\n" +
				"(allow network-outbound (remote tcp \"localhost:43210\"))\n"
			if profile != want {
				t.Fatalf("proxy must be the only IP/resolver endpoint:\n%s\nwant\n%s", profile, want)
			}
		})
	}
}

func TestBuildProfileDenyPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		NetworkNeed: NetworkNone,
		Deny:        []string{home + "/.ssh", home + "/.gnupg"},
	})
	if !strings.Contains(profile, home+"/.ssh") {
		t.Errorf("profile should deny .ssh, got:\n%s", profile)
	}
	if !strings.Contains(profile, home+"/.gnupg") {
		t.Errorf("profile should deny .gnupg, got:\n%s", profile)
	}
}

func TestBuildProfileDenyPathCoversExactMissingPathAndDescendants(t *testing.T) {
	missing := filepath.Join(t.TempDir(), ".aws")
	canonical, err := canonicalSandboxPath(missing)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{Deny: []string{missing}})
	for _, want := range []string{
		`(deny file-read* file-write* (literal "` + canonical + `"))`,
		`(deny file-read* file-write* (subpath "` + canonical + `"))`,
		`(deny network-outbound (literal "` + canonical + `"))`,
		`(deny network-outbound (subpath "` + canonical + `"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %s:\n%s", want, profile)
		}
	}
}

func TestBuildProfileDenyPathOverridesAllowedSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent.sock")
	canonical, err := canonicalSandboxPath(socket)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{
		NetworkNeed:  NetworkFull,
		AllowSockets: []string{socket},
		Deny:         []string{socket},
	})
	allow := `(allow network-outbound (literal "` + canonical + `"))`
	deny := `(deny network-outbound (literal "` + canonical + `"))`
	allowIndex := strings.Index(profile, allow)
	denyIndex := strings.Index(profile, deny)
	if allowIndex < 0 || denyIndex < 0 || denyIndex < allowIndex {
		t.Fatalf("socket deny must follow overlapping allow: allow=%d deny=%d\n%s", allowIndex, denyIndex, profile)
	}
}

func TestBuildProfileDenyPathOverridesWritableMount(t *testing.T) {
	denied := t.TempDir()
	canonical, err := canonicalSandboxPath(denied)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{
		Mounts: []Mount{{Source: denied, Target: denied}},
		Deny:   []string{denied},
	})
	allow := `(allow file-write* (subpath "` + canonical + `"))`
	deny := `(deny file-read* file-write* (subpath "` + canonical + `"))`
	allowIndex := strings.Index(profile, allow)
	denyIndex := strings.Index(profile, deny)
	if allowIndex < 0 || denyIndex < 0 || denyIndex < allowIndex {
		t.Fatalf("deny must follow overlapping writable mount allow: allow=%d deny=%d\n%s", allowIndex, denyIndex, profile)
	}
}

func TestCanonicalSandboxPathResolvesMissingPathThroughSymlinkedAncestor(t *testing.T) {
	parent := t.TempDir()
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "missing", "credential")
	got, err := canonicalSandboxPath(missing)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realParent, "missing", "credential")
	if got != want {
		t.Fatalf("canonicalSandboxPath(%q) = %q, want %q", missing, got, want)
	}
}

func TestBuildProfileMountWriteIsolation(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		NetworkNeed: NetworkNone,
		Mounts: []Mount{
			{Source: home + "/scratch/jail", Target: home + "/scratch/jail"},
		},
	})
	// Should deny writes to home
	if !strings.Contains(profile, "(deny file-write* (subpath \""+home+"\"))") {
		t.Errorf("profile should deny writes to home, got:\n%s", profile)
	}
	// Should allow writes to mount path
	if !strings.Contains(profile, "(allow file-write* (subpath \""+home+"/scratch/jail\"))") {
		t.Errorf("profile should allow writes to mount, got:\n%s", profile)
	}
}

func TestSeatbeltExecBuildsCommand(t *testing.T) {
	sb := &seatbeltSandbox{
		cfg:     Config{NetworkNeed: NetworkNone},
		profile: "(version 1)(allow default)",
		tmpDir:  "/tmp/test",
	}
	cmd, err := sb.Exec(context.Background(), "echo", []string{"hello"})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	args := cmd.Args
	if len(args) < 4 {
		t.Fatalf("expected at least 4 args, got %d: %v", len(args), args)
	}
	// args: [sandbox-exec, -p, <profile>, echo, hello]
	if args[1] != "-p" {
		t.Errorf("args[1] = %q, want -p", args[1])
	}
	if args[3] != "echo" {
		t.Errorf("args[3] = %q, want echo", args[3])
	}
	if args[4] != "hello" {
		t.Errorf("args[4] = %q, want hello", args[4])
	}
}

func TestBuildProfileDenyWritePaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	projectDir := home + "/project"
	eggYaml := projectDir + "/egg.yaml"
	profile := buildProfile(Config{
		NetworkNeed: NetworkFull,
		Mounts:      []Mount{{Source: projectDir, ReadOnly: false}},
		DenyWrite:   []string{eggYaml},
	})
	// Should contain a deny file-write* with literal for the specific file
	want := `(deny file-write* (literal "` + eggYaml + `"))`
	if !strings.Contains(profile, want) {
		t.Errorf("profile should deny writes to egg.yaml, got:\n%s", profile)
	}
	// deny-write must come AFTER mount allows so it takes precedence in SBPL
	mountAllow := `(allow file-write* (subpath "` + projectDir + `"))`
	mountIdx := strings.Index(profile, mountAllow)
	denyIdx := strings.Index(profile, want)
	if mountIdx < 0 || denyIdx < 0 {
		t.Fatalf("profile missing expected rules:\n%s", profile)
	}
	if denyIdx < mountIdx {
		t.Errorf("deny-write rule must come AFTER mount allow to take precedence in SBPL.\nmount allow at %d, deny-write at %d\nprofile:\n%s", mountIdx, denyIdx, profile)
	}
	// Should NOT deny reads
	denyRead := `(deny file-read* (literal "` + eggYaml + `"))`
	if strings.Contains(profile, denyRead) {
		t.Error("deny-write should not block reads")
	}
}

// Integration tests — actually run sandboxed processes

// Opt in outside a nested sandbox; an unavailable Seatbelt must fail this gate,
// rather than silently skipping the enforcement checks requested by the caller.
func requireSeatbeltEnforcement(t *testing.T) {
	t.Helper()
	if os.Getenv("WT_TEST_SEATBELT_ENFORCEMENT") != "1" {
		t.Skip("set WT_TEST_SEATBELT_ENFORCEMENT=1 on an unsandboxed Mac")
	}
	if out, err := exec.Command("sandbox-exec", "-p", "(version 1)(allow default)", "/bin/echo", "ok").CombinedOutput(); err != nil {
		t.Fatalf("Seatbelt enforcement unavailable: %v: %s", err, out)
	}
}

// A subprocess helper tests Unix-socket access with the same Go runtime as the
// parent. The parent first establishes that the host socket is reachable.
func TestSeatbeltSocketProbe(t *testing.T) {
	socket := os.Getenv("WT_SEATBELT_TEST_SOCKET")
	if socket == "" {
		t.Skip("subprocess helper")
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err == nil {
		_ = conn.Close()
	}
	if os.Getenv("WT_SEATBELT_TEST_DENY_SOCKET") == "1" {
		if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
			t.Fatalf("socket access must be denied by Seatbelt, got %v", err)
		}
	} else if err != nil {
		t.Fatalf("declared socket access failed: %v", err)
	}
}

func TestSeatbeltLocalPortsEnforced(t *testing.T) {
	requireSeatbeltEnforcement(t)
	// Keep the test socket within macOS's sockaddr_un path limit.
	t.Setenv("TMPDIR", "/tmp")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("seatbelt-local-ok"))
	})
	allowed := httptest.NewServer(handler)
	defer allowed.Close()
	blocked := httptest.NewServer(handler)
	defer blocked.Close()
	socket := filepath.Join(t.TempDir(), "tool.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	sb, err := newPlatform(Config{
		NetworkNeed:  NetworkLocal,
		LocalPorts:   []int{allowed.Listener.Addr().(*net.TCPAddr).Port},
		AllowSockets: []string{socket},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, sb)
	for _, tc := range []struct {
		name  string
		url   string
		allow bool
	}{{"declared port", allowed.URL, true}, {"undeclared port", blocked.URL, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/usr/bin/curl", []string{"--silent", "--show-error", "--fail", "--max-time", "3", "--noproxy", "*", tc.url})
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			if tc.allow && (err != nil || string(out) != "seatbelt-local-ok") {
				t.Fatalf("declared loopback port failed: %v: %s", err, out)
			}
			if !tc.allow && (err == nil || ctx.Err() != nil) {
				t.Fatalf("undeclared loopback port must be blocked: %v: %s", err, out)
			}
		})
	}
	t.Setenv("WT_SEATBELT_TEST_SOCKET", socket)
	t.Setenv("WT_SEATBELT_TEST_DENY_SOCKET", "0")
	cmd, err := sb.Exec(context.Background(), os.Args[0], []string{"-test.run=^TestSeatbeltSocketProbe$"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("declared Unix control socket failed: %v: %s", err, out)
	}
}

func TestSeatbeltProxyResolverBlocked(t *testing.T) {
	requireSeatbeltEnforcement(t)
	const resolverSocket = "/private/var/run/mDNSResponder"
	conn, err := net.DialTimeout("unix", resolverSocket, 2*time.Second)
	if err != nil {
		t.Fatalf("unsandboxed resolver socket must be reachable: %v", err)
	}
	_ = conn.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("seatbelt-proxy-ok"))
	}))
	defer server.Close()
	proxy, err := StartProxy([]string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	sb, err := newPlatform(Config{NetworkNeed: NetworkHTTPS, ProxyPort: proxy.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, sb)
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", proxy.Port())
	targetURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	for _, tc := range []struct {
		name  string
		url   string
		proxy bool
		allow bool
	}{
		{"allowed HTTPS through proxy", targetURL, true, true},
		{"blocked CONNECT domain", "https://seatbelt-blocked.invalid", true, false},
		{"direct HTTPS bypass", server.URL, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--silent", "--show-error", "--fail", "--insecure", "--max-time", "3"}
			if tc.proxy {
				args = append(args, "--noproxy", "", "--proxy", proxyURL)
			} else {
				args = append(args, "--noproxy", "*")
			}
			args = append(args, tc.url)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/usr/bin/curl", args)
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			if tc.allow && (err != nil || string(out) != "seatbelt-proxy-ok") {
				t.Fatalf("proxied HTTPS failed without agent DNS: %v: %s", err, out)
			}
			if !tc.allow && (err == nil || ctx.Err() != nil) {
				t.Fatalf("forbidden connection must fail: %v: %s", err, out)
			}
		})
	}
	if events := proxy.Events(); len(events) != 2 || events[0].Blocked || !events[1].Blocked || events[1].Host != "seatbelt-blocked.invalid:443" {
		t.Fatalf("expected an allowed tunnel and a domain-filter refusal, got %#v", events)
	}

	t.Setenv("WT_SEATBELT_TEST_SOCKET", resolverSocket)
	t.Setenv("WT_SEATBELT_TEST_DENY_SOCKET", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, os.Args[0], []string{"-test.run=^TestSeatbeltSocketProbe$"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resolver socket must be denied while proxied HTTPS works: %v: %s", err, out)
	}
}

func TestSeatbeltNetworkBlocked(t *testing.T) {
	sandboxExecAvailable(t)
	sb, err := newPlatform(Config{NetworkNeed: NetworkNone})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer func() {
		if err := sb.Destroy(); err != nil {
			t.Errorf("destroy sandbox: %v", err)
		}
	}()

	cmd, err := sb.Exec(context.Background(), "/usr/bin/curl", []string{"-s", "--max-time", "3", "https://example.com"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected curl to fail with network denied, but it succeeded")
	}
}

func TestSeatbeltNetworkAllowed(t *testing.T) {
	sandboxExecAvailable(t)
	sb, err := newPlatform(Config{NetworkNeed: NetworkFull})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer func() {
		if err := sb.Destroy(); err != nil {
			t.Errorf("destroy sandbox: %v", err)
		}
	}()

	// Just verify the process runs — don't actually hit the network in tests
	cmd, err := sb.Exec(context.Background(), "/bin/echo", []string{"network-ok"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "network-ok" {
		t.Errorf("output = %q, want %q", got, "network-ok")
	}
}

func TestSeatbeltDenyPathBlocked(t *testing.T) {
	sandboxExecAvailable(t)
	// Create a temp file, deny access to its directory
	tmpDir := t.TempDir()
	testFile := tmpDir + "/secret.txt"
	if err := os.WriteFile(testFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{tmpDir},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	cmd, err := sb.Exec(context.Background(), "/bin/cat", []string{testFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected cat to fail on denied path, but it succeeded")
	}
}

func TestSeatbeltSSHKnownHostsExceptionSurvivesExactDirectoryDeny(t *testing.T) {
	sandboxExecAvailable(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	knownHosts := filepath.Join(sshDir, "known_hosts")
	if _, err := os.Stat(knownHosts); err != nil {
		t.Skip("no SSH known_hosts file to exercise: ", err)
	}

	allowed, err := newPlatform(Config{NetworkNeed: NetworkFull, Deny: []string{sshDir}})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, allowed)
	cmd, err := allowed.Exec(context.Background(), "/usr/bin/head", []string{"-c", "1", knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("known_hosts exception was blocked by exact directory deny: %v", err)
	}

	blocked, err := newPlatform(Config{NetworkNeed: NetworkFull, Deny: []string{sshDir, knownHosts}})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, blocked)
	cmd, err = blocked.Exec(context.Background(), "/usr/bin/head", []string{"-c", "1", knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("explicit known_hosts deny was bypassed")
	}
}

func TestSeatbeltWriteRestriction(t *testing.T) {
	sandboxExecAvailable(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	jail := t.TempDir()

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Mounts: []Mount{
			{Source: jail, Target: jail},
		},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	// Write inside mount should succeed
	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo ok > " + jail + "/test.txt"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("write to mount path should succeed: %v", err)
	}
	if err := os.Remove(jail + "/test.txt"); err != nil {
		t.Fatal(err)
	}

	// Write outside mount (in home) should fail
	target := home + "/wt-sandbox-test-delete-me"
	cmd2, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo fail > " + target})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	err = cmd2.Run()
	if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) { // clean up in case it leaked
		t.Fatal(removeErr)
	}
	if err == nil {
		t.Fatal("expected write outside mount to fail, but it succeeded")
	}
}

func TestSeatbeltDenyWriteBlocksWrite(t *testing.T) {
	sandboxExecAvailable(t)
	// Create a file that should be readable but not writable.
	// Include a writable mount for the parent dir — this is the real scenario:
	// the project dir is rw-mounted AND egg.yaml inside it is deny-write.
	// Without the mount, deny-write trivially works (no competing allow rule).
	tmpDir := t.TempDir()
	protectedFile := tmpDir + "/egg.yaml"
	if err := os.WriteFile(protectedFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Mounts:      []Mount{{Source: tmpDir, ReadOnly: false}},
		DenyWrite:   []string{protectedFile},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	// Read should succeed
	cmd, err := sb.Exec(context.Background(), "/bin/cat", []string{protectedFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("reading deny-write file should succeed: %v", err)
	}
	if got := out.String(); got != "original content" {
		t.Errorf("read content = %q, want %q", got, "original content")
	}

	// Write should fail
	cmd2, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo hacked > " + protectedFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	err = cmd2.Run()
	if err == nil {
		// Verify file wasn't modified
		data, _ := os.ReadFile(protectedFile)
		if string(data) != "original content" {
			t.Fatal("deny-write file was modified!")
		}
		t.Fatal("expected write to deny-write file to fail, but it succeeded")
	}
}
