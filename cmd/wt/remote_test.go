package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRemoteInvocationPreservesCommandArgv(t *testing.T) {
	argv := []string{
		"--remote", "work1", "egg", "codex", "--cwd", "/home/dogfood/work/one two",
		"--name", "review", "--", "-m", "gpt-5.6-sol", "--tools", "", " \t ", "quote'and\nnewline",
	}
	invocation, remote, err := parseRemoteInvocation(argv, true)
	if err != nil {
		t.Fatal(err)
	}
	if !remote {
		t.Fatal("remote invocation was not detected")
	}
	wantArgs := []string{
		"egg", "codex", "--cwd", "/home/dogfood/work/one two", "--name", "review", "--",
		"-m", "gpt-5.6-sol", "--tools", "", " \t ", "quote'and\nnewline",
	}
	if !reflect.DeepEqual(invocation.args, wantArgs) {
		t.Fatalf("remote args = %#v, want %#v", invocation.args, wantArgs)
	}
	if invocation.target != "work1" || invocation.binary != "wt" || !invocation.allocateTTY {
		t.Fatalf("remote invocation = %#v", invocation)
	}
}

func TestParseRemoteInvocationSupportsLegacyAttachPlacement(t *testing.T) {
	invocation, remote, err := parseRemoteInvocation([]string{
		"attach", "session-name", "--remote-binary=/opt/wing thing/wt", "--remote", "builder.example",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !remote || invocation.target != "builder.example" || invocation.binary != "/opt/wing thing/wt" {
		t.Fatalf("remote invocation = %#v, remote=%v", invocation, remote)
	}
	if !reflect.DeepEqual(invocation.args, []string{"attach", "session-name"}) || !invocation.allocateTTY {
		t.Fatalf("legacy attach routing = %#v", invocation)
	}
}

func TestParseBareRemoteChoosesInteractiveEntryOrReadOnlyJSON(t *testing.T) {
	interactive, remote, err := parseRemoteInvocation([]string{"--remote=work1", "--remote-cwd", "/work/project"}, true)
	if err != nil || !remote {
		t.Fatalf("interactive parse: remote=%v err=%v", remote, err)
	}
	if !reflect.DeepEqual(interactive.args, []string{"_remote", "enter", "--cwd", "/work/project"}) || !interactive.allocateTTY {
		t.Fatalf("interactive entry = %#v", interactive)
	}

	noninteractive, remote, err := parseRemoteInvocation([]string{"-r=work1"}, false)
	if err != nil || !remote {
		t.Fatalf("noninteractive parse: remote=%v err=%v", remote, err)
	}
	if !reflect.DeepEqual(noninteractive.args, []string{"_remote", "enter", "--json"}) || noninteractive.allocateTTY {
		t.Fatalf("noninteractive entry = %#v", noninteractive)
	}
}

func TestRemoteInteractiveEmptyInventoryRequiresWorkingDirectory(t *testing.T) {
	t.Setenv("WINGTHING_DIR", t.TempDir())
	command := remoteEnterCmd()
	command.SetArgs([]string{"enter"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "use --remote-cwd PATH") {
		t.Fatalf("remote entry error = %v", err)
	}
}

func TestRemoteNoninteractiveEmptyInventoryIsJSONArray(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WINGTHING_DIR", stateDir)
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = originalStdout })

	command := remoteEnterCmd()
	command.SetArgs([]string{"enter", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); got != "[]\n" {
		t.Fatalf("empty remote inventory = %q, want []", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "eggs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only inventory created session state: %v", err)
	}
}

func TestRemoteTTYPolicy(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"attach", "work"}, true},
		{[]string{"attach", "--select"}, true},
		{[]string{"attach", "--select=false"}, false},
		{[]string{"attach", "--json"}, false},
		{[]string{"attach", "work", "--json=false"}, true},
		{[]string{"attach"}, false},
		{[]string{"terminal"}, true},
		{[]string{"terminal", "--detach"}, false},
		{[]string{"terminal", "--detach=false"}, true},
		{[]string{"terminal", "-d=false"}, true},
		{[]string{"terminal", "-dd=false"}, true},
		{[]string{"terminal", "--detach=false", "--detach"}, false},
		{[]string{"terminal", "--detach", "--detach=false"}, true},
		{[]string{"terminal", "--json", "--", "sh", "-c", "echo hi"}, false},
		{[]string{"terminal", "--json=false", "--", "sh", "-c", "echo hi"}, true},
		{[]string{"terminal", "--", "printf", "--detach"}, true},
		{[]string{"egg", "codex", "--", "-m", "gpt-5.6-sol"}, true},
		{[]string{"egg", "codex", "--json"}, false},
		{[]string{"egg", "codex", "--json=false"}, true},
		{[]string{"egg", "list"}, false},
		{[]string{"session", "send", "work", "--stdin"}, false},
	}
	for _, tt := range tests {
		if got := remoteCommandNeedsTTY(tt.args); got != tt.want {
			t.Errorf("remoteCommandNeedsTTY(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestRemoteRoutingRejectsUnsupportedOrIncompleteTransport(t *testing.T) {
	tests := [][]string{
		{"--remote", "work1", "start"},
		{"--remote", "work1", "doctor"},
		{"--remote", "work1", "mcp", "stdio", "--client", "codex"},
		{"--remote", "work1", "mcp", "connect"},
		{"--remote", "work1", "sandbox", "run", "internal"},
		{"--remote", "work1", "egg", "--json", "codex"},
		{"--remote", "work1", "session", "sync", "abc", "--from", "wing"},
		{"--remote"},
		{"--remote="},
		{"--remote-binary", "/opt/wt", "doctor"},
		{"--remote", "-bad", "doctor"},
		{"--remote", "work1", "--remote-cwd", "/work", "doctor"},
	}
	for _, argv := range tests {
		if _, _, err := parseRemoteInvocation(argv, false); err == nil {
			t.Errorf("parseRemoteInvocation(%q) unexpectedly succeeded", argv)
		}
	}
}

func TestRemoteSessionListCommandIsExecutable(t *testing.T) {
	if err := validateRemoteCommand([]string{"session", "list"}); err != nil {
		t.Fatalf("remote validation rejected session list: %v", err)
	}
	t.Setenv("WINGTHING_DIR", t.TempDir())
	command := newRootCommand()
	command.SetArgs([]string{"session", "list"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute session list alias: %v", err)
	}
}

func TestRemoteUnsupportedSessionSubcommandNamesTheSubcommand(t *testing.T) {
	err := validateRemoteCommand([]string{"session", "sync", "work"})
	if err == nil || !strings.Contains(err.Error(), `session subcommand "sync" is not available over SSH`) {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, command := range []string{"kill", "stop"} {
		if !strings.Contains(err.Error(), command) {
			t.Errorf("supported remote session command %q missing from error: %v", command, err)
		}
	}
}

func TestRemoteStopCommandsAreForwardedWithoutATTY(t *testing.T) {
	for _, args := range [][]string{
		{"session", "kill", "work", "--json"},
		{"session", "stop", "work", "--json"},
		{"egg", "stop", "work"},
	} {
		if err := validateRemoteCommand(args); err != nil {
			t.Fatalf("remote validation rejected %q: %v", args, err)
		}
		if remoteCommandNeedsTTY(args) {
			t.Fatalf("remote stop unexpectedly requested a TTY: %q", args)
		}
	}
}

func TestRemoteFlagsAfterDashStayWithRemoteCommand(t *testing.T) {
	invocation, remote, err := parseRemoteInvocation([]string{
		"--remote", "work1", "terminal", "--json", "--", "printf", "%s", "--remote", "literal-host-flag",
	}, false)
	if err != nil || !remote {
		t.Fatalf("parse: remote=%v err=%v", remote, err)
	}
	want := []string{"terminal", "--json", "--", "printf", "%s", "--remote", "literal-host-flag"}
	if !reflect.DeepEqual(invocation.args, want) {
		t.Fatalf("args = %#v, want %#v", invocation.args, want)
	}
}

func TestNewRootCommandFailsClosedWhenRemotePreflightIsBypassed(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"--remote", "work1", "terminal", "--json"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "remote routing was not initialized") {
		t.Fatalf("root error = %v", err)
	}
}

func TestRunRemoteInvocationStreamsStdioAndPreservesExitStatus(t *testing.T) {
	tempDir := t.TempDir()
	sshPath := filepath.Join(tempDir, "ssh")
	remoteBinary := filepath.Join(tempDir, "remote wt")
	sshScript := "#!/bin/sh\n" +
		"printf 'ssh-mode=%s\\nssh-host=%s\\n' \"$1\" \"$2\" >&2\n" +
		"exec /bin/sh -c \"$3\"\n"
	remoteScript := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf 'arg=<%s>\\n' \"$arg\"; done\n" +
		"cat\n" +
		"printf 'remote-stderr\\n' >&2\n" +
		"exit 23\n"
	if err := os.WriteFile(sshPath, []byte(sshScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteBinary, []byte(remoteScript), 0o700); err != nil {
		t.Fatal(err)
	}

	input := "remote stdin canary\n"
	var stdout, stderr bytes.Buffer
	err := runRemoteInvocation(context.Background(), remoteInvocation{
		target: "work1", binary: remoteBinary,
		args: []string{"terminal", "--detach", "--name", "codex's remote"},
	}, remoteIO{in: strings.NewReader(input), out: &stdout, errOut: &stderr, sshPath: sshPath})
	var exitErr *commandExitError
	if !errors.As(err, &exitErr) || exitErr.code != 23 {
		t.Fatalf("remote error = %#v, want exit code 23", err)
	}
	wantStdout := "arg=<terminal>\narg=<--detach>\narg=<--name>\narg=<codex's remote>\n" + input
	if stdout.String() != wantStdout {
		t.Fatalf("stdout = %q, want %q", stdout.String(), wantStdout)
	}
	if got := stderr.String(); !strings.Contains(got, "ssh-mode=-T\nssh-host=work1\n") || !strings.Contains(got, "remote-stderr\n") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunRemoteInvocationPreservesEmptyAndWhitespaceArgs(t *testing.T) {
	tempDir := t.TempDir()
	sshPath := filepath.Join(tempDir, "ssh")
	remoteBinary := filepath.Join(tempDir, "remote-wt")
	sshScript := "#!/bin/sh\nexec /bin/sh -c \"$3\"\n"
	remoteScript := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s:<%s>\\n' \"${#arg}\" \"$arg\"; done\n"
	if err := os.WriteFile(sshPath, []byte(sshScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteBinary, []byte(remoteScript), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runRemoteInvocation(context.Background(), remoteInvocation{
		target: "work1", binary: remoteBinary,
		args: []string{"egg", "claude", "--", "--tools", "", " \t "},
	}, remoteIO{in: strings.NewReader(""), out: &stdout, errOut: &stderr, sshPath: sshPath})
	if err != nil {
		t.Fatal(err)
	}
	want := "3:<egg>\n6:<claude>\n19:<--remote-exact-argv>\n2:<-->\n7:<--tools>\n0:<>\n3:< \t >\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("remote argv stdout=%q stderr=%q, want stdout=%q", stdout.String(), stderr.String(), want)
	}
}

func TestRemoteExactArgvMarkerOnlyWrapsAgentLaunches(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{
			args: []string{"egg", "codex", "--name", "review", "--", "--tools", ""},
			want: []string{"egg", "codex", "--name", "review", "--remote-exact-argv", "--", "--tools", ""},
		},
		{
			args: []string{"egg", "codex", "--json"},
			want: []string{"egg", "codex", "--json", "--remote-exact-argv"},
		},
		{args: []string{"egg", "list"}, want: []string{"egg", "list"}},
		{args: []string{"terminal", "--json"}, want: []string{"terminal", "--json"}},
	}
	for _, tt := range tests {
		if got := remoteCommandArgs(tt.args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("remoteCommandArgs(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestRemoteExactAgentArgsRemainNULSafe(t *testing.T) {
	if err := validateExactAgentArgs([]string{"", " \t ", "--tools"}); err != nil {
		t.Fatalf("exact remote argv rejected: %v", err)
	}
	if err := validateExactAgentArgs([]string{"bad\x00arg"}); err == nil {
		t.Fatal("exact remote argv accepted NUL")
	}
}

func TestRunRemoteInvocationCancellationStopsSSHClient(t *testing.T) {
	tempDir := t.TempDir()
	sshPath := filepath.Join(tempDir, "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := runRemoteInvocation(ctx, remoteInvocation{
		target: "work1", binary: "wt", args: []string{"terminal", "--json"},
	}, remoteIO{in: strings.NewReader(""), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, sshPath: sshPath})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ssh cancellation took %s", elapsed)
	}
}
