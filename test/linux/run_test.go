//go:build linux && e2e

package linux_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Setenv("PATH", testPATH())
	if missing := batteryPrerequisites(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "wingthing Linux battery preflight failed: %s\n", strings.Join(missing, "; "))
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func batteryPrerequisites() []string {
	var missing []string
	if info, err := os.Stat(testWTPath()); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		missing = append(missing, fmt.Sprintf("executable wt is required at %s", testWTPath()))
	}
	if _, err := exec.LookPath("claude"); err != nil {
		missing = append(missing, "mock agent must be installed as claude in PATH")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		missing = append(missing, "strace is required in PATH")
	}
	name := os.Getenv("WT_TEST_USER")
	if name == "" {
		name = "testuser"
	}
	if _, err := user.Lookup(name); err != nil {
		missing = append(missing, fmt.Sprintf("test account %q is required (or set WT_TEST_USER)", name))
	}
	return missing
}

func TestBatteryPrerequisitesReportAllMissing(t *testing.T) {
	t.Setenv("WT_TEST_BIN_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WT_TEST_USER", "wingthing-user-that-does-not-exist")
	missing := strings.Join(batteryPrerequisites(), "\n")
	for _, want := range []string{
		"executable wt is required",
		"mock agent must be installed as claude",
		"strace is required",
		"WT_TEST_USER",
	} {
		if !strings.Contains(missing, want) {
			t.Fatalf("preflight output %q does not contain %q", missing, want)
		}
	}
}

func testBinDir() string {
	if dir := os.Getenv("WT_TEST_BIN_DIR"); dir != "" {
		return dir
	}
	return "/usr/local/bin"
}

func testPATH() string {
	return testBinDir() + ":/usr/local/bin:/usr/bin:/bin"
}

func testWTPath() string {
	return filepath.Join(testBinDir(), "wt")
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func testWingthingDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("WINGTHING_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}
	return filepath.Join(home, ".wingthing")
}

func configuredTestUser(t *testing.T) (string, string) {
	t.Helper()
	name := os.Getenv("WT_TEST_USER")
	if name == "" {
		name = "testuser"
	}
	account, err := user.Lookup(name)
	if err != nil {
		t.Fatalf("lookup test user %q: %v", name, err)
	}
	return name, account.HomeDir
}

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
			ReadDeniedCanary  bool `json:"read_denied_canary"`
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
	wtDir := testWingthingDir(t)
	os.MkdirAll(filepath.Join(wtDir, "eggs"), 0700)
	os.MkdirAll(filepath.Join(wtDir, "logs"), 0700)

	// Create agent config dir so auto-mount has something to mount
	os.MkdirAll(filepath.Join(home, ".claude"), 0700)

	// Plant a unique file under a denied directory. The mock agent receives the
	// exact path and attempts to read it from inside the live mount namespace.
	// O_EXCL keeps this test safe when someone deliberately runs it against a
	// non-disposable HOME.
	deniedDir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(deniedDir, 0700); err != nil {
		t.Fatalf("create denied canary directory: %v", err)
	}
	deniedCanary := filepath.Join(deniedDir, fmt.Sprintf("wt-e2e-canary-%d", time.Now().UnixNano()))
	canary, err := os.OpenFile(deniedCanary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("create denied canary: %v", err)
	}
	if _, err := canary.WriteString("wingthing-denied-canary"); err != nil {
		canary.Close()
		t.Fatalf("write denied canary: %v", err)
	}
	if err := canary.Close(); err != nil {
		t.Fatalf("close denied canary: %v", err)
	}
	defer os.Remove(deniedCanary)

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
		"PATH=" + testPATH(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"USER=root",
		"WT_TEST_DENIED_CANARY=" + deniedCanary,
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
	if results.Probes.FS.ReadDeniedCanary {
		t.Error("expected the live denied-path canary read to fail, but it succeeded")
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

// runRealAgent launches a real coding-agent binary inside the sandbox via wt egg run.
// Without credentials it should show a login prompt or auth error, not exit silently.
// The test reads the egg server's log output looking for "first PTY output from pid X
// after Yms (Z bytes)" — this proves the agent actually started and rendered something.
// PTY output goes through gRPC, not stdout, so server logs are all we can observe.
func runRealAgent(t *testing.T, agentName, commandName, agentBin string, writeDirs, domains []string) {
	t.Helper()

	// Resolve symlinks to verify the binary is real (not /dev/null or mock-agent)
	resolved, err := filepath.EvalSymlinks(agentBin)
	if err != nil {
		t.Skipf("real %s not installed at %s: %v", agentName, agentBin, err)
	}
	if resolved == "/dev/null" {
		t.Skipf("real %s not installed at %s (-> /dev/null)", agentName, agentBin)
	}
	// Reject missing placeholder links while accepting scripts and native binaries.
	info, err := os.Stat(resolved)
	if err != nil {
		t.Skipf("real %s not installed at %s: %v", agentName, agentBin, err)
	}
	if info.Size() < 100 {
		t.Skipf("real %s at %s too small (%d bytes), likely not real", agentName, agentBin, info.Size())
	}

	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".wingthing", "eggs"), 0700)
	os.MkdirAll(filepath.Join(home, ".wingthing", "logs"), 0700)
	for _, dir := range writeDirs {
		os.MkdirAll(filepath.Join(home, dir), 0700)
	}

	cwd := t.TempDir()
	sessionID := fmt.Sprintf("test-real-%d", time.Now().UnixNano()%100000)

	// Create a shim under the canonical executable name the catalog resolves.
	shimDir := filepath.Join(cwd, "shims")
	os.MkdirAll(shimDir, 0755)
	shimPath := filepath.Join(shimDir, commandName)
	os.Symlink(agentBin, shimPath)

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", agentName,
		"--cwd", cwd,
		"--rows", "24", "--cols", "80",
		"--dangerously-skip-permissions",
		"--fs", "ro:/",
		"--fs", "rw:" + cwd,
		"--fs", "rw:" + filepath.Join(home, ".cache"),
		"--env", "HOME=" + home,
		"--env", "PATH=" + shimDir + ":" + testPATH(),
		"--env", "TERM=xterm-256color",
		"--env", "LANG=en_US.UTF-8",
		"--env", "USER=root",
	}
	for _, domain := range domains {
		args = append(args, "--network", domain)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Dir = cwd
	// Set PATH on the wt process itself so exec.LookPath finds our shim.
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
			t.Logf("%s binary: %s (resolved: %s)", agentName, agentBin, resolved)
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
	t.Logf("%s binary: %s (resolved: %s)", agentName, agentBin, resolved)
	t.Logf("output:\n%s", strings.Join(allOutput, "\n"))
	if !found {
		t.Errorf("real %s produced zero PTY output before exit", agentName)
	}
}

func TestRealClaudeNodeInSandbox(t *testing.T) {
	runRealAgent(t, "claude", "claude", "/usr/local/bin/claude-node",
		[]string{".claude", ".cache/claude"},
		[]string{"*.anthropic.com", "*.claude.com"})
}

func TestRealClaudeBunInSandbox(t *testing.T) {
	runRealAgent(t, "claude", "claude", "/usr/local/bin/claude-bun",
		[]string{".claude", ".cache/claude"},
		[]string{"*.anthropic.com", "*.claude.com"})
}

func TestRealCursorInSandbox(t *testing.T) {
	runRealAgent(t, "cursor", "agent", "/usr/local/bin/cursor-agent-real",
		[]string{".cursor", ".config"},
		[]string{"*.cursor.sh", "api.anthropic.com", "api.openai.com"})
}

func TestRealGeminiInSandbox(t *testing.T) {
	runRealAgent(t, "gemini", "gemini", "/usr/local/bin/gemini-real",
		[]string{".gemini"},
		[]string{"*.googleapis.com", "*.google.com"})
}

func TestRealHermesInSandbox(t *testing.T) {
	runRealAgent(t, "hermes", "hermes", "/usr/local/bin/hermes-real", []string{".hermes"}, []string{"*"})
}

func TestRealOpenCodeInSandbox(t *testing.T) {
	runRealAgent(t, "opencode", "opencode", "/usr/local/bin/opencode-real",
		[]string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".cache/opencode"},
		[]string{"*.opencode.ai", "models.dev"})
}

func TestRealOllamaInSandbox(t *testing.T) {
	runRealAgent(t, "ollama", "ollama", "/usr/local/bin/ollama-real",
		[]string{".ollama"}, []string{"localhost"})
}

func TestRealOllamaToolCalling(t *testing.T) {
	const (
		baseURL = "http://127.0.0.1:11434"
		model   = "qwen3:4b"
	)
	client := &http.Client{Timeout: 60 * time.Second}

	response, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		t.Skipf("local Ollama service is unavailable: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Skipf("local Ollama service returned %s", response.Status)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tags); err != nil {
		t.Fatalf("decode Ollama tags: %v", err)
	}
	installed := false
	for _, candidate := range tags.Models {
		if candidate.Name == model {
			installed = true
			break
		}
	}
	if !installed {
		t.Skipf("Ollama model %s is not installed", model)
	}

	root := t.TempDir()
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{name: "hello", path: filepath.Join(root, "hello.txt"), content: "Hello World!"},
		{name: "status", path: filepath.Join(root, "status.txt"), content: "Wingthing tool canary OK"},
		{name: "nested", path: filepath.Join(root, "nested", "result.txt"), content: "structured tool calls work"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{
				"model": model,
				"messages": []map[string]string{{
					"role": "user",
					"content": fmt.Sprintf(
						"Call write_file exactly once. Write the requested content to the requested path. path=%q content=%q. Do not answer in prose.",
						test.path, test.content,
					),
				}},
				"tools": []any{map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "write_file",
						"description": "Write text content to an absolute file path.",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"path":    map[string]string{"type": "string"},
								"content": map[string]string{"type": "string"},
							},
							"required": []string{"path", "content"},
						},
					},
				}},
				"stream":  false,
				"options": map[string]any{"temperature": 0, "seed": 42, "num_ctx": 8192},
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			response, err := client.Post(baseURL+"/api/chat", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("Ollama chat: %v", err)
			}
			if response.StatusCode != http.StatusOK {
				failure, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
				response.Body.Close()
				t.Fatalf("Ollama chat returned %s: %s", response.Status, failure)
			}
			var result struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Name      string            `json:"name"`
							Arguments map[string]string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			}
			err = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if err != nil {
				t.Fatalf("decode Ollama response: %v", err)
			}
			if len(result.Message.ToolCalls) != 1 {
				t.Fatalf("tool calls = %d, want 1; content=%q", len(result.Message.ToolCalls), result.Message.Content)
			}
			call := result.Message.ToolCalls[0].Function
			if call.Name != "write_file" || call.Arguments["path"] != test.path || call.Arguments["content"] != test.content {
				t.Fatalf("unexpected tool call: name=%q arguments=%q", call.Name, call.Arguments)
			}

			// Dispatch only after exact validation, and only to this test's temporary root.
			if err := os.MkdirAll(filepath.Dir(test.path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(test.path, []byte(call.Arguments["content"]), 0600); err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(test.path)
			if err != nil || string(written) != test.content {
				t.Fatalf("dispatched write = %q, %v", written, err)
			}
			t.Logf("exact tool call and dispatch in %s", time.Since(started).Round(time.Millisecond))
		})
	}
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

	wtDir := testWingthingDir(t)
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
		"--env", "PATH=" + testPATH(),
		"--env", "TERM=xterm-256color",
		"--env", "USER=root",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wt", args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	output, _ := cmd.CombinedOutput()

	// Verify strace.log was preserved to the configured Wingthing state dir.
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

// TestDenyInitFailsClosedOnRejectedMount bypasses the parent capability check
// and drives the security wrapper itself. It is intended for an unprivileged
// Ubuntu 24.04 host before the executable-scoped AppArmor profile is installed.
// The marker command represents the agent: mount rejection must keep it from
// ever existing.
func TestDenyInitFailsClosedOnRejectedMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user subject to AppArmor's userns profile")
	}
	policy, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	if err != nil || strings.TrimSpace(string(policy)) != "1" {
		t.Skip("requires AppArmor restricted unprivileged user namespaces")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	doctor := exec.CommandContext(ctx, testWTPath(), "doctor")
	doctor.Env = os.Environ()
	doctorOutput, err := doctor.CombinedOutput()
	if err != nil {
		t.Fatalf("wt doctor: %v\n%s", err, doctorOutput)
	}
	if !strings.Contains(string(doctorOutput), "NOT AVAILABLE") {
		t.Skip("the wt executable already has an AppArmor profile")
	}

	tmp := t.TempDir()
	denied := filepath.Join(tmp, "denied")
	if err := os.MkdirAll(denied, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(tmp, "agent-launched")
	logPath := filepath.Join(tmp, "deny_init.log")
	uid, gid := os.Getuid(), os.Getgid()

	cmd := exec.CommandContext(ctx, testWTPath(),
		"_deny_init",
		"--uid", fmt.Sprintf("%d", uid),
		"--gid", fmt.Sprintf("%d", gid),
		"--log", logPath,
		"--deny", denied,
		"--", "/usr/bin/touch", marker,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      uid,
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 0,
			HostID:      gid,
			Size:        1,
		}},
	}
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatal("sandbox wrapper launched the agent marker after enforcement was rejected")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("agent marker exists after rejected enforcement: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read deny_init diagnostic: %v (run error: %v)", err, runErr)
	}
	logText := string(logData)
	if !strings.Contains(logText, "filesystem enforcement failed") ||
		!strings.Contains(logText, "refusing to launch agent") {
		t.Fatalf("missing fail-closed diagnostic:\n%s", logText)
	}
	t.Logf("wrapper refused before agent launch: %s", strings.TrimSpace(logText))
}

// TestPreflightSandboxCheck verifies that `wt egg claude` (the parent command
// with the pre-flight sandbox.CheckCapability call) fails immediately with a
// clear error when namespaces aren't available — no 5s timeout waiting for
// the child process. This is the path Phil hits on Ubuntu 24.04 with AppArmor
// blocking userns.
func TestPreflightSandboxCheck(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("must run as root to switch to the non-root test account")
	}
	testUser, _ := configuredTestUser(t)
	if sandboxAvailableForUser(t, testUser) {
		t.Skipf("%s can create the required namespaces; negative preflight path is not available on this host", testUser)
	}

	// The configured non-root account cannot create user namespaces inside
	// Docker/Colima or on an AppArmor-restricted Ubuntu host.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Use `wt egg claude` (not `wt egg run`) so we exercise eggSpawn's pre-flight check.
	command := "PATH=" + shellArg(testPATH()) + " " + shellArg(testWTPath()) + " egg claude 2>&1"
	cmd := exec.CommandContext(ctx, "su", "-", testUser, "-s", "/bin/sh", "-c", command)
	cmd.Env = os.Environ()
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	out := string(output)

	if err == nil {
		t.Fatalf("expected wt egg claude to fail as %s, but it succeeded:\n%s", testUser, out)
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
	testUser, testUserHome := configuredTestUser(t)
	if sandboxAvailableForUser(t, testUser) {
		t.Skipf("%s can create the required namespaces; namespace failure path is not available on this host", testUser)
	}

	// Run wt egg run as the configured non-root account. This exercises the
	// explicit namespace diagnostic on AppArmor-restricted hosts.
	cwd := t.TempDir()
	os.Chmod(cwd, 0777)

	sessionID := fmt.Sprintf("test-nouserns-%d", time.Now().UnixNano()%100000)

	// Build the wt command as a single string for su -c
	wtCmd := fmt.Sprintf("PATH=%s %s egg run"+
		" --session-id %s"+
		" --agent claude"+
		" --cwd %s"+
		" --rows 24 --cols 80"+
		" --dangerously-skip-permissions"+
		" --fs ro:/ --fs rw:%s"+
		" --network '*.anthropic.com'"+
		" --env HOME=%s"+
		" --env PATH=%s"+
		" --env TERM=xterm-256color",
		shellArg(testPATH()), shellArg(testWTPath()), sessionID, cwd, cwd, testUserHome, testPATH())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "su", "-", testUser, "-s", "/bin/sh", "-c", wtCmd)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	out := string(output)

	if err == nil {
		t.Fatalf("expected wt egg run to fail as non-root without namespace support, but it succeeded:\n%s", out)
	}

	t.Logf("exit error: %v", err)
	t.Logf("output:\n%s", out)

	// Namespace policy and AppArmor denial have distinct safe remediations.
	// Accept the scoped executable profile for AppArmor and the sysctl for a
	// host where unprivileged user namespaces themselves are disabled.
	if !strings.Contains(out, "doctor --fix") && !strings.Contains(out, "sysctl") {
		t.Errorf("expected actionable sandbox remediation, got:\n%s", out)
	}
}

// sandboxAvailableForUser asks wt itself whether the named user's host policy
// permits the namespace operations required by the Linux sandbox. Docker and
// AppArmor-restricted Ubuntu hosts exercise the negative tests above; WSL2 and
// permissive Linux hosts exercise the successful sandbox tests instead.
func sandboxAvailableForUser(t *testing.T, user string) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "su", "-", user, "-s", "/bin/sh", "-c", fmt.Sprintf("%q doctor", testWTPath()))
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("check sandbox capability for %s: %v", user, ctx.Err())
	}
	if err != nil {
		t.Fatalf("check sandbox capability for %s: %v\n%s", user, err, output)
	}

	out := string(output)
	if strings.Contains(out, "NOT AVAILABLE") {
		return false
	}
	if strings.Contains(out, "Sandbox:") && strings.Contains(out, "available") {
		return true
	}
	t.Fatalf("could not determine sandbox capability for %s from wt doctor output:\n%s", user, out)
	return false
}
