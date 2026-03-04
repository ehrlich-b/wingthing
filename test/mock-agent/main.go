package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "mock-agent v0.0.1-test"

type Results struct {
	Version  string          `json:"version"`
	Probes   ProbeResults    `json:"probes"`
	Errors   []string        `json:"errors"`
	ExitCode int             `json:"exit_code"`
}

type ProbeResults struct {
	Env       map[string]string `json:"env"`
	FS        FSProbe           `json:"fs"`
	Network   NetworkProbe      `json:"network"`
	Seccomp   SeccompProbe      `json:"seccomp"`
	Namespace NamespaceProbe    `json:"namespace"`
	PTY       PTYProbe          `json:"pty"`
}

type FSProbe struct {
	WriteCWD          bool `json:"write_cwd"`
	WriteClaudeDir    bool `json:"write_claude_dir"`
	WriteCacheDir     bool `json:"write_cache_dir"`
	ReadSSHKey        bool `json:"read_ssh_key"`
	WriteOutsideMount bool `json:"write_outside_mount"`
	HomeExists        bool `json:"home_exists"`
	HomeWritable      bool `json:"home_writable"`
}

type NetworkProbe struct {
	HTTPSOutbound bool `json:"https_outbound"`
	RawTCP        bool `json:"raw_tcp"`
}

type SeccompProbe struct {
	PtraceBlocked bool `json:"ptrace_blocked"`
	MountBlocked  bool `json:"mount_blocked"`
}

type NamespaceProbe struct {
	InPIDNamespace bool   `json:"in_pid_namespace"`
	NSpid          string `json:"nspid"`
}

type PTYProbe struct {
	IsTerminal bool `json:"is_terminal"`
}

func main() {
	args := os.Args[1:]

	// --version
	for _, a := range args {
		if a == "--version" {
			fmt.Println(version)
			os.Exit(0)
		}
	}

	// Parse flags
	interactive := false
	printMode := false
	var printArgs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dangerously-skip-permissions":
			// no-op, accepted silently
		case "--interactive":
			interactive = true
		case "-p":
			printMode = true
			// Everything after -p is the prompt
			if i+1 < len(args) {
				printArgs = args[i+1:]
			}
			i = len(args) // stop parsing
		}
	}

	if printMode {
		prompt := strings.Join(printArgs, " ")
		fmt.Println(prompt)
		os.Exit(0)
	}

	if interactive {
		runInteractive()
		return
	}

	// Run all probes
	results := Results{
		Version: version,
		Errors:  []string{},
	}

	results.Probes.Env = probeEnv()
	results.Probes.FS = probeFS()
	results.Probes.Network = probeNetwork()
	results.Probes.Seccomp = probeSeccomp()
	results.Probes.Namespace = probeNamespace()
	results.Probes.PTY = probePTY()

	// Write results to $HOME/.claude/test-results.json
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	resultsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(resultsDir, 0755); err != nil {
		results.Errors = append(results.Errors, "mkdir ~/.claude: "+err.Error())
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": "json marshal: %s"}`, err)
		os.Exit(1)
	}

	resultsPath := filepath.Join(resultsDir, "test-results.json")
	if err := os.WriteFile(resultsPath, data, 0644); err != nil {
		results.Errors = append(results.Errors, "write results: "+err.Error())
		// Re-marshal with the error included
		data, _ = json.MarshalIndent(results, "", "  ")
		fmt.Fprintln(os.Stderr, string(data))
		os.Exit(1)
	}

	// Also write to CWD so the test runner can find results after sandbox cleanup
	cwdPath := filepath.Join(".", "test-results.json")
	os.WriteFile(cwdPath, data, 0644)

	// Also print to stdout for convenience
	fmt.Println(string(data))
}

func runInteractive() {
	fmt.Println("mock-agent interactive")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGWINCH)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGTERM:
				os.Exit(0)
			case syscall.SIGWINCH:
				fmt.Fprintln(os.Stderr, "mock-agent: SIGWINCH received")
			}
		}
	}()

	// Echo stdin to stdout
	io.Copy(os.Stdout, os.Stdin)
}

func probeEnv() map[string]string {
	env := make(map[string]string)
	for _, key := range []string{
		"HOME", "PATH", "TERM", "LANG", "USER", "SHELL", "TMPDIR",
		"ANTHROPIC_API_KEY",
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
	} {
		env[key] = os.Getenv(key)
	}
	return env
}

func probeFS() FSProbe {
	var p FSProbe

	// Write to CWD
	testFile := "test-write-cwd"
	if err := os.WriteFile(testFile, []byte("probe"), 0644); err == nil {
		p.WriteCWD = true
		os.Remove(testFile)
	}

	// Check HOME exists and is a real directory
	home := os.Getenv("HOME")
	if home != "" {
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			p.HomeExists = true
		}
		// Check if HOME itself is writable (try creating a temp file)
		tmpPath := filepath.Join(home, ".wt-probe-test")
		if err := os.WriteFile(tmpPath, []byte("probe"), 0644); err == nil {
			p.HomeWritable = true
			os.Remove(tmpPath)
		}
	}

	// Write to ~/.claude/test-write (agent profile WriteRegex)
	if home != "" {
		claudeDir := filepath.Join(home, ".claude")
		os.MkdirAll(claudeDir, 0755)
		testPath := filepath.Join(claudeDir, "test-write")
		if err := os.WriteFile(testPath, []byte("probe"), 0644); err == nil {
			p.WriteClaudeDir = true
			os.Remove(testPath)
		}
	}

	// Write to ~/.cache/claude/ (agent profile WriteDirs)
	if home != "" {
		cacheDir := filepath.Join(home, ".cache", "claude")
		os.MkdirAll(cacheDir, 0755)
		testPath := filepath.Join(cacheDir, "test-write")
		if err := os.WriteFile(testPath, []byte("probe"), 0644); err == nil {
			p.WriteCacheDir = true
			os.Remove(testPath)
		}
	}

	// Try to read ~/.ssh/id_rsa
	if home != "" {
		sshKey := filepath.Join(home, ".ssh", "id_rsa")
		if _, err := os.ReadFile(sshKey); err == nil {
			p.ReadSSHKey = true
		}
	}

	// Try to write outside mount
	outsidePath := "/tmp/outside-mount-test"
	if err := os.WriteFile(outsidePath, []byte("probe"), 0644); err == nil {
		p.WriteOutsideMount = true
		os.Remove(outsidePath)
	}

	return p
}

func probeNetwork() NetworkProbe {
	var p NetworkProbe

	// HTTPS outbound: dial TCP to 1.1.1.1:443
	conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	if err == nil {
		p.HTTPSOutbound = true
		conn.Close()
	}

	// Raw TCP to non-standard port
	conn, err = net.DialTimeout("tcp", "8.8.8.8:53", 3*time.Second)
	if err == nil {
		p.RawTCP = true
		conn.Close()
	}

	return p
}

func probePTY() PTYProbe {
	var p PTYProbe

	// Check if stdout is a terminal
	fi, err := os.Stdout.Stat()
	if err == nil {
		p.IsTerminal = (fi.Mode() & os.ModeCharDevice) != 0
	}

	return p
}
