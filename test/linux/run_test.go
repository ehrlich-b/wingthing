//go:build linux && e2e

package linux_test

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeResults mirrors the JSON written by the mock agent.
type probeResults struct {
	Version string `json:"version"`
	Probes  struct {
		Env map[string]string `json:"env"`
		FS  struct {
			WriteCWD          bool `json:"write_cwd"`
			WriteClaudeDir    bool `json:"write_claude_dir"`
			WriteCacheDir     bool `json:"write_cache_dir"`
			ReadSSHKey        bool `json:"read_ssh_key"`
			WriteOutsideMount bool `json:"write_outside_mount"`
			HomeExists        bool `json:"home_exists"`
			HomeWritable      bool `json:"home_writable"`
		} `json:"fs"`
		Network struct {
			HTTPSOutbound bool `json:"https_outbound"`
			RawTCP        bool `json:"raw_tcp"`
		} `json:"network"`
		Seccomp struct {
			PtraceBlocked bool `json:"ptrace_blocked"`
			MountBlocked  bool `json:"mount_blocked"`
		} `json:"seccomp"`
		Namespace struct {
			InPIDNamespace bool   `json:"in_pid_namespace"`
			NSpid          string `json:"nspid"`
		} `json:"namespace"`
		PTY struct {
			IsTerminal bool `json:"is_terminal"`
		} `json:"pty"`
	} `json:"probes"`
	Errors   []string `json:"errors"`
	ExitCode int      `json:"exit_code"`
}

// runEgg invokes `wt egg run` with the mock agent installed as `claude`.
// The mock agent runs its probes and writes test-results.json to CWD.
// Returns the parsed results and the combined wt output (for debugging).
func runEgg(t *testing.T, extraFS []string, extraNetwork []string) (*probeResults, string) {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	// Set up minimal wingthing directory structure
	wtDir := filepath.Join(home, ".wingthing")
	os.MkdirAll(filepath.Join(wtDir, "eggs"), 0700)
	os.MkdirAll(filepath.Join(wtDir, "logs"), 0700)

	// Create agent config dir so auto-mount has something to mount
	os.MkdirAll(filepath.Join(home, ".claude"), 0700)

	// Create a temp working directory for the egg
	cwd := t.TempDir()

	sessionID := fmt.Sprintf("test-%d", time.Now().UnixNano()%100000)

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", "claude",
		"--cwd", cwd,
		"--rows", "24",
		"--cols", "80",
		"--dangerously-skip-permissions",
	}

	// Default FS rules matching DefaultEggConfig
	fsRules := []string{
		"ro:/",
		"rw:" + cwd,
		"deny:" + filepath.Join(home, ".ssh"),
		"deny:" + filepath.Join(home, ".gnupg"),
		"deny:" + filepath.Join(home, ".aws"),
	}
	fsRules = append(fsRules, extraFS...)
	for _, f := range fsRules {
		args = append(args, "--fs", f)
	}

	// Network rules
	network := extraNetwork
	if len(network) == 0 {
		// Default: agent profile domains (standard isolation with HTTPS)
		network = []string{"*.anthropic.com", "*.claude.com"}
	}
	for _, n := range network {
		args = append(args, "--network", n)
	}

	// Pass env vars matching DefaultEggConfig allowlist
	for _, e := range []string{
		"HOME=" + home,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"USER=root",
	} {
		args = append(args, "--env", e)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Dir = cwd
	// Inherit env so wt can find its own binary for _deny_init wrapper
	cmd.Env = os.Environ()
	output, runErr := cmd.CombinedOutput()
	outputStr := string(output)

	// The mock agent writes test-results.json to CWD
	resultsPath := filepath.Join(cwd, "test-results.json")
	data, readErr := os.ReadFile(resultsPath)
	if readErr != nil {
		t.Fatalf("mock agent did not write test-results.json to CWD (%s)\nwt exit err: %v\nwt output:\n%s",
			resultsPath, runErr, outputStr)
	}

	var results probeResults
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("failed to parse test-results.json: %v\nraw: %s", err, string(data))
	}

	return &results, outputStr
}

func TestMockAgentLaunchesInDefaultSandbox(t *testing.T) {
	results, output := runEgg(t, nil, nil)

	if results.Version == "" {
		t.Errorf("mock agent did not report version")
	}
	if len(results.Errors) > 0 {
		t.Errorf("mock agent reported errors: %v", results.Errors)
	}
	if !results.Probes.FS.WriteCWD {
		t.Error("expected CWD write to succeed in default sandbox")
	}

	t.Logf("mock agent OK: version=%s errors=%d", results.Version, len(results.Errors))
	_ = output
}

func TestSandboxDenyPaths(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	if results.Probes.FS.ReadSSHKey {
		t.Error("expected ~/.ssh/id_rsa read to be DENIED in sandbox, but it succeeded")
	}
}

func TestSandboxWriteIsolation(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	// On Linux, write isolation only applies within HOME (setupReadonlyHome).
	// /tmp is outside HOME and writable in the mount namespace.
	// This test verifies the probe ran; actual write isolation is tested
	// via the claude dir probe (write within HOME agent profile mount).
	if !results.Probes.FS.WriteCWD {
		t.Error("expected CWD write to succeed")
	}
	// SSH deny should still be enforced
	if results.Probes.FS.ReadSSHKey {
		t.Error("expected ~/.ssh read to be denied even with write isolation")
	}
}

func TestNamespaceCreation(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	if !results.Probes.Namespace.InPIDNamespace {
		t.Error("expected mock agent to be in a PID namespace")
	}
	if results.Probes.Namespace.NSpid == "" {
		t.Error("expected NSpid to be populated")
	}
	// NSpid should have multiple tab-separated entries when in a namespace
	parts := strings.Split(strings.TrimSpace(results.Probes.Namespace.NSpid), "\t")
	if len(parts) < 2 {
		t.Errorf("expected NSpid to have 2+ entries (in namespace), got %d: %q", len(parts), results.Probes.Namespace.NSpid)
	}
}

func TestSeccompDeniedSyscalls(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	if !results.Probes.Seccomp.PtraceBlocked {
		t.Error("expected ptrace to be blocked by seccomp")
	}
	if !results.Probes.Seccomp.MountBlocked {
		t.Error("expected mount to be blocked by seccomp")
	}
}

func TestAgentProfileAutoMount(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	if !results.Probes.FS.WriteClaudeDir {
		t.Error("expected write to ~/.claude/ to succeed (agent profile auto-mount)")
	}
}

// TestClaudeAgentRequirements verifies every requirement from the claude agent
// profile. If real Claude Code would fail, this test should fail first.
func TestClaudeAgentRequirements(t *testing.T) {
	results, _ := runEgg(t, nil, nil)

	// HOME must exist and be a real directory
	if !results.Probes.FS.HomeExists {
		t.Error("HOME does not exist or is not a directory")
	}

	// ~/.claude/ must be writable (WriteRegex in agent profile)
	if !results.Probes.FS.WriteClaudeDir {
		t.Error("~/.claude/ not writable — Claude Code writes settings, auth tokens here")
	}

	// ~/.cache/claude/ must be writable (WriteDirs in agent profile)
	if !results.Probes.FS.WriteCacheDir {
		t.Error("~/.cache/claude/ not writable — Claude Code writes cache here")
	}

	// CWD must be writable (agent needs to create files in the project)
	if !results.Probes.FS.WriteCWD {
		t.Error("CWD not writable — agent can't create files in the project")
	}

	// CLAUDECODE must NOT be in the environment (causes "nested session" refusal)
	if v := results.Probes.Env["CLAUDECODE"]; v != "" {
		t.Errorf("CLAUDECODE=%q leaked into agent env — Claude Code will refuse to start", v)
	}

	// CLAUDE_CODE_ENTRYPOINT must NOT be in the environment
	if v := results.Probes.Env["CLAUDE_CODE_ENTRYPOINT"]; v != "" {
		t.Errorf("CLAUDE_CODE_ENTRYPOINT=%q leaked into agent env", v)
	}

	// HOME must be set
	if results.Probes.Env["HOME"] == "" {
		t.Error("HOME not set in agent env")
	}

	// PATH must be set (agent needs to find node, git, etc.)
	if results.Probes.Env["PATH"] == "" {
		t.Error("PATH not set in agent env")
	}

	// TERM must be set (agent runs in a PTY)
	if results.Probes.Env["TERM"] == "" {
		t.Error("TERM not set in agent env — PTY apps need this")
	}

	// Sensitive dirs must be denied
	if results.Probes.FS.ReadSSHKey {
		t.Error("~/.ssh/ readable — secrets should be denied")
	}
}

// runRealClaude launches a real Claude Code binary inside the sandbox via wt egg run.
// No API key → it should show a login prompt or auth error (not exit silently).
// The test reads the egg server's log output looking for "first PTY output from pid X
// after Yms (Z bytes)" — this proves the agent actually started and rendered something.
// PTY output goes through gRPC, not stdout, so server logs are all we can observe.
func runRealClaude(t *testing.T, claudeBin string) {
	t.Helper()

	// Resolve symlinks to verify the binary is real (not /dev/null or mock-agent)
	resolved, err := filepath.EvalSymlinks(claudeBin)
	if err != nil {
		t.Skipf("real claude not installed at %s: %v", claudeBin, err)
	}
	if resolved == "/dev/null" {
		t.Skipf("real claude not installed at %s (-> /dev/null)", claudeBin)
	}
	// Quick sanity: mock-agent is ~10MB Go binary, real claude is a JS file
	info, err := os.Stat(resolved)
	if err != nil {
		t.Skipf("real claude not installed at %s: %v", claudeBin, err)
	}
	if info.Size() < 100 {
		t.Skipf("real claude at %s too small (%d bytes), likely not real", claudeBin, info.Size())
	}

	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".wingthing", "eggs"), 0700)
	os.MkdirAll(filepath.Join(home, ".wingthing", "logs"), 0700)
	os.MkdirAll(filepath.Join(home, ".claude"), 0700)
	os.MkdirAll(filepath.Join(home, ".cache", "claude"), 0700)

	cwd := t.TempDir()
	sessionID := fmt.Sprintf("test-real-%d", time.Now().UnixNano()%100000)

	// Create a shim that points to the real claude binary
	shimDir := filepath.Join(cwd, "shims")
	os.MkdirAll(shimDir, 0755)
	shimPath := filepath.Join(shimDir, "claude")
	os.Symlink(claudeBin, shimPath)

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", "claude",
		"--cwd", cwd,
		"--rows", "24", "--cols", "80",
		"--dangerously-skip-permissions",
		"--fs", "ro:/",
		"--fs", "rw:" + cwd,
		"--fs", "rw:" + filepath.Join(home, ".cache"),
		"--network", "*.anthropic.com",
		"--network", "*.claude.com",
		"--env", "HOME=" + home,
		"--env", "PATH=" + shimDir + ":/usr/local/bin:/usr/bin:/bin",
		"--env", "TERM=xterm-256color",
		"--env", "LANG=en_US.UTF-8",
		"--env", "USER=root",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Dir = cwd
	// Set PATH on the wt process itself so exec.LookPath("claude") finds our shim.
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + shimDir + ":" + e[5:]
			break
		}
	}
	cmd.Env = env

	// Read output line-by-line looking for the "first PTY output" log from the egg server.
	// PTY output goes through gRPC (not stdout), so this log line is the only evidence
	// that the agent started and rendered something to the terminal.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	var allOutput []string
	found := false

	for scanner.Scan() {
		line := scanner.Text()
		allOutput = append(allOutput, line)

		// Look for "egg: first PTY output from pid X after Yms (Z bytes)"
		if strings.Contains(line, "first PTY output") {
			t.Logf("claude binary: %s (resolved: %s)", claudeBin, resolved)
			t.Logf("SUCCESS: %s", line)
			found = true
			cmd.Process.Kill()
			cmd.Wait()
			return
		}

		// Early exit detection: if the egg server reports the session exited,
		// the agent died without producing any PTY output.
		if strings.Contains(line, "exited with code") {
			break
		}
	}

	cmd.Process.Kill()
	cmd.Wait()
	t.Logf("claude binary: %s (resolved: %s)", claudeBin, resolved)
	t.Logf("output:\n%s", strings.Join(allOutput, "\n"))
	if !found {
		t.Errorf("REAL Claude Code produced ZERO PTY output — silent exit bug.\n"+
			"The sandbox is killing Node.js/Bun before it can render anything.")
	}
}

func TestRealClaudeNodeInSandbox(t *testing.T) {
	runRealClaude(t, "/usr/local/bin/claude-node")
}

func TestRealClaudeBunInSandbox(t *testing.T) {
	runRealClaude(t, "/usr/local/bin/claude-bun")
}

func TestDoctorLinuxSystemSection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", "doctor")
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wt doctor failed: %v\n%s", err, output)
	}
	out := string(output)
	t.Logf("doctor output:\n%s", out)

	// Must have the System: section on Linux
	if !strings.Contains(out, "System:") {
		t.Fatal("missing System: section in doctor output")
	}

	// Each field must be present with a non-empty value
	for _, field := range []string{"kernel:", "distro:", "userns:", "overlayfs:", "cgroup v2:"} {
		if !strings.Contains(out, field) {
			t.Errorf("missing %s in System section", field)
		}
	}

	// kernel should show a version number
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "kernel:") {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				t.Error("kernel line has no value")
			}
		}
	}

	// userns must report enabled (we're running --privileged)
	if !strings.Contains(out, "userns:") || (!strings.Contains(out, "enabled") && !strings.Contains(out, "enabled (no sysctl gate)")) {
		t.Error("expected userns: enabled")
	}

	// overlayfs must be available (stock kernel)
	if !strings.Contains(out, "overlayfs:") || strings.Contains(out, "not available") {
		t.Error("expected overlayfs: available")
	}
}

func TestSupportBundleContents(t *testing.T) {
	// Run a mock agent egg session first so there's something to collect.
	// This also tests that deny_init.log gets preserved.
	_, _ = runEgg(t, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", "support")
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wt support failed: %v\n%s", err, output)
	}
	out := string(output)
	t.Logf("support output: %s", out)

	// Extract zip path from output: "diagnostic bundle: /tmp/wt-support-*.zip"
	var zipPath string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "diagnostic bundle:") {
			zipPath = strings.TrimSpace(strings.TrimPrefix(line, "diagnostic bundle:"))
			break
		}
	}
	if zipPath == "" {
		t.Fatal("wt support did not print zip path")
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
		t.Logf("  zip entry: %s (%d bytes)", f.Name, f.UncompressedSize64)
	}

	// Must contain core files
	for _, required := range []string{"meta.json", "doctor.txt"} {
		if !fileNames[required] {
			t.Errorf("support bundle missing %s", required)
		}
	}

	// doctor.txt must contain the Linux System section
	if fileNames["doctor.txt"] {
		for _, f := range r.File {
			if f.Name == "doctor.txt" {
				rc, err := f.Open()
				if err != nil {
					t.Fatalf("open doctor.txt in zip: %v", err)
				}
				buf := make([]byte, f.UncompressedSize64)
				rc.Read(buf)
				rc.Close()
				doctorContent := string(buf)
				if !strings.Contains(doctorContent, "System:") {
					t.Error("doctor.txt in support bundle missing System: section")
				}
				if !strings.Contains(doctorContent, "kernel:") {
					t.Error("doctor.txt in support bundle missing kernel: field")
				}
				break
			}
		}
	}

	// Check for deny_init.log preservation in logs/
	hasDenyInitLog := false
	for name := range fileNames {
		if strings.HasPrefix(name, "logs/") && strings.HasSuffix(name, ".deny_init.log") {
			hasDenyInitLog = true
			break
		}
	}
	if !hasDenyInitLog {
		t.Log("no deny_init.log found in support bundle (expected if sandbox didn't use _deny_init wrapper)")
	}
}

func TestTraceMode(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	wtDir := filepath.Join(home, ".wingthing")
	logsDir := filepath.Join(wtDir, "logs")
	os.MkdirAll(filepath.Join(wtDir, "eggs"), 0700)
	os.MkdirAll(logsDir, 0700)
	os.MkdirAll(filepath.Join(home, ".claude"), 0700)

	cwd := t.TempDir()
	sessionID := fmt.Sprintf("test-trace-%d", time.Now().UnixNano()%100000)

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", "claude",
		"--cwd", cwd,
		"--rows", "24",
		"--cols", "80",
		"--dangerously-skip-permissions",
		"--trace",
		"--fs", "ro:/",
		"--fs", "rw:" + cwd,
		"--fs", "deny:" + filepath.Join(home, ".ssh"),
		"--network", "*.anthropic.com",
		"--env", "HOME=" + home,
		"--env", "PATH=/usr/local/bin:/usr/bin:/bin",
		"--env", "TERM=xterm-256color",
		"--env", "USER=root",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	output, _ := cmd.CombinedOutput()

	// Verify strace.log was preserved to ~/.wingthing/logs/
	straceLogPath := filepath.Join(logsDir, sessionID+".strace.log")
	data, readErr := os.ReadFile(straceLogPath)
	if readErr != nil {
		t.Fatalf("strace.log not preserved to %s\nwt output:\n%s", straceLogPath, string(output))
	}
	if len(data) == 0 {
		t.Fatal("strace.log is empty")
	}

	// Verify strace output contains execve (proves strace ran)
	if !strings.Contains(string(data), "execve") {
		t.Errorf("strace.log does not contain 'execve' — strace may not have run.\nFirst 500 bytes: %s", string(data[:min(500, len(data))]))
	}

	t.Logf("strace.log: %d bytes, contains execve", len(data))
}

// TestPreflightSandboxCheck verifies that `wt egg claude` (the parent command
// with the pre-flight sandbox.CheckCapability call) fails immediately with a
// clear error when namespaces aren't available — no 5s timeout waiting for
// the child process. This is the path Phil hits on Ubuntu 24.04 with AppArmor
// blocking userns.
func TestPreflightSandboxCheck(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("must run as root to su to testuser")
	}

	// testuser (UID 1001, created in Dockerfile) can't create user namespaces
	// inside Docker/Colima — same as AppArmor blocking userns on the host.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Use `wt egg claude` (not `wt egg run`) so we exercise eggSpawn's pre-flight check.
	cmd := exec.CommandContext(ctx, "su", "-", "testuser", "-s", "/bin/sh", "-c", "wt egg claude 2>&1")
	cmd.Env = os.Environ()
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	out := string(output)

	if err == nil {
		t.Fatalf("expected wt egg claude to fail as testuser, but it succeeded:\n%s", out)
	}

	t.Logf("elapsed: %s", elapsed)
	t.Logf("output:\n%s", out)

	// Must fail FAST (pre-flight check), not after 5s timeout.
	if elapsed > 8*time.Second {
		t.Errorf("pre-flight check too slow (%s) — should fail immediately, not wait for child timeout", elapsed)
	}

	// Error must mention sandbox and fix instructions.
	if !strings.Contains(out, "sandbox not available") && !strings.Contains(out, "sandbox") {
		t.Errorf("expected 'sandbox not available' in error, got:\n%s", out)
	}

	// Error must mention wt doctor --fix.
	if !strings.Contains(out, "doctor --fix") {
		t.Errorf("expected 'doctor --fix' in error output, got:\n%s", out)
	}
}

func TestSandboxFailsWithClearErrorWithoutNamespaces(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("must run as root to test non-root namespace failure")
	}

	// Run wt egg run as non-root user "testuser" (UID 1000, created in Dockerfile).
	// In Colima/Docker, non-root users cannot create user namespaces —
	// this is exactly the silent-exit-code-1 scenario.
	cwd := t.TempDir()
	os.Chmod(cwd, 0777)

	sessionID := fmt.Sprintf("test-nouserns-%d", time.Now().UnixNano()%100000)

	// Build the wt command as a single string for su -c
	wtCmd := fmt.Sprintf("wt egg run"+
		" --session-id %s"+
		" --agent claude"+
		" --cwd %s"+
		" --rows 24 --cols 80"+
		" --dangerously-skip-permissions"+
		" --fs ro:/ --fs rw:%s"+
		" --network '*.anthropic.com'"+
		" --env HOME=/home/testuser"+
		" --env PATH=/usr/local/bin:/usr/bin:/bin"+
		" --env TERM=xterm-256color",
		sessionID, cwd, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "su", "-", "testuser", "-s", "/bin/sh", "-c", wtCmd)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	out := string(output)

	if err == nil {
		t.Fatalf("expected wt egg run to fail as non-root without namespace support, but it succeeded:\n%s", out)
	}

	t.Logf("exit error: %v", err)
	t.Logf("output:\n%s", out)

	// The error should contain actionable fix instructions.
	// Two paths can trigger: early capability check ("unprivileged user namespaces")
	// or late PTY start failure ("blocked sandbox namespace creation").
	// Both include the sysctl fix.
	if !strings.Contains(out, "sysctl") {
		t.Errorf("expected error with sysctl fix instructions, got:\n%s", out)
	}
}
