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

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
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

func TestPreviewRemoteSelectsPreviewBinaryAndBindsChannel(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	invocation, remote, err := parseRemoteInvocation([]string{"--remote", "work1", "attach", "research"}, false)
	if err != nil || !remote || invocation.binary != "wt-preview" {
		t.Fatalf("preview route = %+v remote=%v err=%v", invocation, remote, err)
	}
	// The explicit staged path still carries the identity guard. Its basename
	// is not trusted as proof that it is a preview build.
	invocation, _, err = parseRemoteInvocation([]string{"--remote", "work1", "--remote-binary", "/staged/build", "attach", "research"}, false)
	if err != nil {
		t.Fatal(err)
	}
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var command bytes.Buffer
	if err := runRemoteInvocation(context.Background(), invocation, remoteIO{in: strings.NewReader(""), out: &command, errOut: io.Discard, sshPath: sshPath}); err != nil {
		t.Fatal(err)
	}
	want := "'/staged/build' '--expected-channel' 'preview' 'attach' 'research'\n"
	if command.String() != want {
		t.Fatalf("remote preview identity = %q want=%q", command.String(), want)
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

func TestParseRemoteInvocationSkipsCommandFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"session", "wait", "review", "--contains", "-ready"},
		{"session", "wait", "review", "--contains", "--remote=other"},
		{"session", "wait", "review", "--contains", "--remote"},
		{"egg", "codex", "--name", "-review"},
		{"terminal", "-n", "-review"},
		{"terminal", "-dn", "--remote-state"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if invocation, remote, err := parseRemoteInvocation(args, false); err != nil || remote {
				t.Fatalf("local flag value routed remotely: %+v remote=%v err=%v", invocation, remote, err)
			}
			argv := append(append([]string(nil), args...), "--remote", "work1")
			invocation, remote, err := parseRemoteInvocation(argv, false)
			if err != nil || !remote || invocation.target != "work1" || !reflect.DeepEqual(invocation.args, args) {
				t.Fatalf("remote flag value changed argv: %+v remote=%v err=%v", invocation, remote, err)
			}
		})
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

func TestBareRemoteJSONIsDiscoverableAndNeverAttaches(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		invocation, remote, err := parseRemoteInvocation([]string{"--remote", "work1", "--remote-cwd", "/work", "--json"}, interactive)
		if err != nil || !remote || invocation.allocateTTY {
			t.Fatalf("interactive=%v invocation=%+v remote=%v err=%v", interactive, invocation, remote, err)
		}
		if invocation.args[0] != "_remote" || invocation.args[1] != "enter" || !boolFlagBeforeDash(invocation.args[2:], "json", "") {
			t.Fatalf("bare JSON did not request read-only inventory: %v", invocation.args)
		}
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

func TestRemoteStateParsesBeforeDashAndStaysLexical(t *testing.T) {
	// The client must not clean, resolve, or Abs the path: "..", "//", a
	// trailing slash, and an apostrophe all belong to the remote filesystem.
	const state = "/srv//wt state/it's/../preview/"
	tests := []struct {
		argv     []string
		wantArgs []string
	}{
		{
			argv:     []string{"--remote", "work1", "--remote-state", state, "terminal", "--json", "--", "printf", "--remote-state", "relative"},
			wantArgs: []string{"terminal", "--json", "--", "printf", "--remote-state", "relative"},
		},
		{
			argv:     []string{"--remote-state=" + state, "--remote=work1", "session", "ps", "--json"},
			wantArgs: []string{"session", "ps", "--json"},
		},
		{
			argv:     []string{"attach", "review", "--remote-state", state, "-r", "work1"},
			wantArgs: []string{"attach", "review"},
		},
	}
	for _, tt := range tests {
		invocation, remote, err := parseRemoteInvocation(tt.argv, false)
		if err != nil || !remote {
			t.Fatalf("parse %q: remote=%v err=%v", tt.argv, remote, err)
		}
		if invocation.state != state || invocation.target != "work1" {
			t.Fatalf("parse %q: state=%q target=%q", tt.argv, invocation.state, invocation.target)
		}
		if !reflect.DeepEqual(invocation.args, tt.wantArgs) {
			t.Fatalf("parse %q: args=%q want %q", tt.argv, invocation.args, tt.wantArgs)
		}
	}

	bare, remote, err := parseRemoteInvocation([]string{"--remote", "work1", "--remote-state", "/state", "--remote-cwd", "/work"}, true)
	if err != nil || !remote || bare.state != "/state" {
		t.Fatalf("bare entry with state: %+v remote=%v err=%v", bare, remote, err)
	}
	if !reflect.DeepEqual(bare.args, []string{"_remote", "enter", "--cwd", "/work"}) {
		t.Fatalf("bare entry args = %q", bare.args)
	}
}

func TestRemoteStateRejectsInvalidOrAmbiguousSelection(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"--remote", "work1", "--remote-state", "", "session", "ps"}, "non-empty absolute path"},
		{[]string{"--remote", "work1", "--remote-state=", "session", "ps"}, "non-empty absolute path"},
		{[]string{"--remote", "work1", "--remote-state", "state", "session", "ps"}, "must be an absolute path"},
		{[]string{"--remote", "work1", "--remote-state", "./state", "session", "ps"}, "must be an absolute path"},
		{[]string{"--remote", "work1", "--remote-state", "~/state", "session", "ps"}, "must be an absolute path"},
		{[]string{"--remote", "work1", "--remote-state", "/a\x00b", "session", "ps"}, "must not contain NUL, CR, or LF"},
		{[]string{"--remote", "work1", "--remote-state", "/a\nb", "session", "ps"}, "must not contain NUL, CR, or LF"},
		{[]string{"--remote", "work1", "--remote-state", "/a\rb", "session", "ps"}, "must not contain NUL, CR, or LF"},
		{[]string{"--remote", "work1", "--remote-state", "\n/a", "session", "ps"}, "must not contain NUL, CR, or LF"},
		// A repeated selection is ambiguous even when both values match.
		{[]string{"--remote", "work1", "--remote-state", "/a", "--remote-state", "/a", "session", "ps"}, "only once"},
		{[]string{"--remote", "work1", "--remote-state=/a", "attach", "x", "--remote-state", "/b"}, "only once"},
		{[]string{"--remote", "work1", "--remote-state"}, "requires a value"},
		{[]string{"--remote-state", "/a", "session", "ps"}, "--remote requires"},
		{[]string{"--remote", "work1", "--remote-state", "/a", "doctor"}, "not available over SSH"},
	}
	for _, tt := range tests {
		_, _, err := parseRemoteInvocation(tt.argv, false)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("parseRemoteInvocation(%q) error = %v, want %q", tt.argv, err, tt.want)
		}
	}
	// The transport rejects an unvalidated state too, before starting SSH.
	sshMarker := filepath.Join(t.TempDir(), "ssh-started")
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\ntouch '"+sshMarker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runRemoteInvocation(context.Background(), remoteInvocation{target: "work1", binary: "wt", state: "relative", args: []string{"session", "ps"}}, remoteIO{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, sshPath: sshPath})
	if err == nil {
		t.Fatal("transport accepted a relative remote state")
	}
	if _, statErr := os.Stat(sshMarker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("SSH started for invalid state: %v", statErr)
	}
}

func TestRemoteStateByteContractRejectsOnlyNULCRLF(t *testing.T) {
	// The contract is exactly NUL, CR, and LF, matching --remote and
	// --remote-binary. Other bytes are legal POSIX path bytes and stay inert
	// inside single quotes; the receiver sees them literally.
	for _, state := range []string{"/a\tb", "/a\x1bb", "/a\x7fb", "/a\x01b", "/é/ü"} {
		invocation, remote, err := parseRemoteInvocation([]string{"--remote", "work1", "--remote-state", state, "session", "ps"}, false)
		if err != nil || !remote || invocation.state != state {
			t.Errorf("state %q: remote=%v state=%q err=%v", state, remote, invocation.state, err)
		}
	}
	for _, b := range []byte{0, '\r', '\n'} {
		if err := validateRemoteState("/a" + string(b) + "b"); err == nil {
			t.Errorf("state with byte %#x accepted", b)
		}
	}
}

func TestRemoteStateSendsQuotedPOSIXAssignmentsOnlyWhenSelected(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := func(channel string, invocation remoteInvocation) string {
		t.Helper()
		previous := config.ReleaseChannel
		config.ReleaseChannel = channel
		defer func() { config.ReleaseChannel = previous }()
		var out bytes.Buffer
		if err := runRemoteInvocation(context.Background(), invocation, remoteIO{in: strings.NewReader(""), out: &out, errOut: io.Discard, sshPath: sshPath}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	base := remoteInvocation{target: "work1", args: []string{"session", "ps", "--json"}}

	// Absent flag: byte-identical legacy argv for both channels.
	stable := base
	stable.binary = "wt"
	if got, want := command("stable", stable), "'wt' 'session' 'ps' '--json'\n"; got != want {
		t.Fatalf("stable legacy command = %q want %q", got, want)
	}
	preview := base
	preview.binary = "wt-preview"
	if got, want := command("preview", preview), "'wt-preview' '--expected-channel' 'preview' 'session' 'ps' '--json'\n"; got != want {
		t.Fatalf("preview legacy command = %q want %q", got, want)
	}

	// Selected: both names carry the same single-quoted lexical path.
	preview.state = "/srv/wt's state/$HOME/`id`"
	quoted := `'/srv/wt'"'"'s state/$HOME/` + "`id`'"
	want := "WINGTHING_DIR=" + quoted + " WINGTHING_PREVIEW_DIR=" + quoted + " 'wt-preview' '--expected-channel' 'preview' 'session' 'ps' '--json'\n"
	if got := command("preview", preview); got != want {
		t.Fatalf("preview state command = %q want %q", got, want)
	}
	// A selected stable state also binds the stable channel, so a receiver
	// released before the channel-marker check rejects the request instead of
	// honoring WINGTHING_DIR unguarded.
	stable.state = "/srv/stable state"
	want = "WINGTHING_DIR='/srv/stable state' WINGTHING_PREVIEW_DIR='/srv/stable state' 'wt' '--expected-channel' 'stable' 'session' 'ps' '--json'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable state command = %q want %q", got, want)
	}
	// Egg launches keep --remote-exact-argv after the channel guard.
	stable.args = []string{"egg", "claude", "--", "--remote-state", "x"}
	want = "WINGTHING_DIR='/srv/stable state' WINGTHING_PREVIEW_DIR='/srv/stable state' 'wt' '--expected-channel' 'stable' 'egg' 'claude' '--remote-exact-argv' '--' '--remote-state' 'x'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable state egg command = %q want %q", got, want)
	}
	stable.state = ""
	want = "'wt' 'egg' 'claude' '--remote-exact-argv' '--' '--remote-state' 'x'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable legacy egg command = %q want %q", got, want)
	}
}

// receivedRemoteArgv runs the client transport against a fake SSH that
// evaluates the command in /bin/sh and a fake receiver that prints its argv,
// returning exactly what the remote executable would receive after argv[0].
func receivedRemoteArgv(t *testing.T, channel string, invocation remoteInvocation) []string {
	t.Helper()
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	receiver := filepath.Join(dir, "receiver")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexec /bin/sh -c \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiver, []byte("#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\036' \"$arg\"; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := config.ReleaseChannel
	config.ReleaseChannel = channel
	defer func() { config.ReleaseChannel = previous }()
	invocation.target, invocation.binary = "work1", receiver
	var out bytes.Buffer
	if err := runRemoteInvocation(context.Background(), invocation, remoteIO{in: strings.NewReader(""), out: &out, errOut: io.Discard, sshPath: sshPath}); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(out.String(), "\x1e"), "\x1e")
}

func TestRemoteStateStableRequiresChannelAwareReceiver(t *testing.T) {
	selected := receivedRemoteArgv(t, "stable", remoteInvocation{state: "/srv/state", args: []string{"session", "ps", "--json"}})
	if want := []string{"--expected-channel", "stable", "session", "ps", "--json"}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("stable selected argv = %q want %q", selected, want)
	}
	legacy := receivedRemoteArgv(t, "stable", remoteInvocation{args: []string{"session", "ps", "--json"}})
	if want := []string{"session", "ps", "--json"}; !reflect.DeepEqual(legacy, want) {
		t.Fatalf("stable legacy argv = %q want %q", legacy, want)
	}

	// A legacy-shaped receiver: Cobra root without --expected-channel, a
	// persistent hook, and a command that would open state. Released stable
	// builds through v0.147.0 have no pre-Cobra state access and no
	// DisableFlagParsing, so the unknown flag fails before either hook runs.
	stateTouched := false
	legacyRoot := &cobra.Command{
		Use: "wt", SilenceErrors: true, SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) error { stateTouched = true; return nil },
	}
	session := &cobra.Command{Use: "session"}
	ps := &cobra.Command{Use: "ps", RunE: func(*cobra.Command, []string) error { stateTouched = true; return nil }}
	ps.Flags().Bool("json", false, "")
	session.AddCommand(ps)
	legacyRoot.AddCommand(session)
	legacyRoot.SetOut(io.Discard)
	legacyRoot.SetErr(io.Discard)
	legacyRoot.SetArgs(selected)
	if err := legacyRoot.Execute(); err == nil || stateTouched {
		t.Fatalf("legacy receiver accepted selected stable state: err=%v touched=%v", err, stateTouched)
	}
	legacyRoot.SetArgs(legacy)
	if err := legacyRoot.Execute(); err != nil || !stateTouched {
		t.Fatalf("legacy receiver rejected legacy argv: err=%v touched=%v", err, stateTouched)
	}

	// The current receiver strips a matching guard and refuses a mismatch
	// before any state resolution.
	previous := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = previous })
	config.ReleaseChannel = "stable"
	args, err := channelInvocationArgs(selected)
	if err != nil || !reflect.DeepEqual(args, []string{"session", "ps", "--json"}) {
		t.Fatalf("stable receiver args = %q err=%v", args, err)
	}
	config.ReleaseChannel = "preview"
	if _, err := channelInvocationArgs(selected); err == nil || !strings.Contains(err.Error(), "release channel mismatch") {
		t.Fatalf("preview receiver accepted a stable request: %v", err)
	}
}

func TestRemoteStateReachesReceiverEnvironmentLiterally(t *testing.T) {
	tempDir := t.TempDir()
	sshPath := filepath.Join(tempDir, "ssh")
	remoteBinary := filepath.Join(tempDir, "remote wt")
	// The fake transport evaluates the command like OpenSSH's remote shell,
	// with a hostile ambient WINGTHING_DIR that the assignment must override.
	sshScript := "#!/bin/sh\nWINGTHING_DIR=/ambient/wrong; export WINGTHING_DIR\nexec /bin/sh -c \"$3\"\n"
	remoteScript := "#!/bin/sh\n" +
		"printf 'dir=<%s>\\npreview=<%s>\\n' \"$WINGTHING_DIR\" \"$WINGTHING_PREVIEW_DIR\"\n" +
		"for arg in \"$@\"; do printf 'arg=<%s>\\n' \"$arg\"; done\n"
	if err := os.WriteFile(sshPath, []byte(sshScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteBinary, []byte(remoteScript), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(tempDir, "evaluated")
	state := "/remote/it's a state/$(touch " + canary + ")/`touch " + canary + "`/../x\ttab\x1besc\x7fdel"
	var stdout, stderr bytes.Buffer
	err := runRemoteInvocation(context.Background(), remoteInvocation{
		target: "work1", binary: remoteBinary, state: state, args: []string{"channel", "--json"},
	}, remoteIO{in: strings.NewReader(""), out: &stdout, errOut: &stderr, sshPath: sshPath})
	if err != nil {
		t.Fatalf("remote run: %v stderr=%s", err, stderr.String())
	}
	want := "dir=<" + state + ">\npreview=<" + state + ">\narg=<--expected-channel>\narg=<" + config.Channel() + ">\narg=<channel>\narg=<--json>\n"
	if stdout.String() != want {
		t.Fatalf("receiver saw %q want %q", stdout.String(), want)
	}
	if _, err := os.Stat(canary); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("remote state path was evaluated by the shell")
	}
}

func TestRemoteChannelInspectionIsForwardedWithoutATTY(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		invocation, remote, err := parseRemoteInvocation([]string{"--remote", "work1", "--remote-state", "/state", "channel", "--json"}, interactive)
		if err != nil || !remote {
			t.Fatalf("channel inspection: remote=%v err=%v", remote, err)
		}
		if !reflect.DeepEqual(invocation.args, []string{"channel", "--json"}) || invocation.allocateTTY {
			t.Fatalf("channel inspection invocation = %+v", invocation)
		}
	}
}

func TestNewRootCommandFailsClosedWhenRemoteStateBypassesPreflight(t *testing.T) {
	for _, args := range [][]string{
		{"--remote-state", "/state", "terminal", "--json"},
		{"--remote-state", "/state"},
	} {
		root := newRootCommand()
		root.SetArgs(args)
		err := root.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), "remote routing was not initialized") {
			t.Fatalf("root %q error = %v", args, err)
		}
	}
}

func TestRemoteStateDisconnectNamesTheSameStateForReconnect(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runRemoteInvocation(context.Background(), remoteInvocation{target: "work1", binary: "wt", state: "/state", args: []string{"attach", "research"}}, remoteIO{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard, sshPath: sshPath})
	var exitErr *commandExitError
	if !errors.As(err, &exitErr) || exitErr.code != 255 || !strings.Contains(exitErr.message, "state is unknown; reconnect with the same --remote-state and list sessions before relaunching") {
		t.Fatalf("SSH disconnect result = %+v", err)
	}
}

func TestRemoteDisconnectPreserves255AndDoesNotSuggestRelaunch(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf 'Connection closed\\n' >&2\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err := runRemoteInvocation(context.Background(), remoteInvocation{target: "work1", binary: "wt", args: []string{"attach", "research"}}, remoteIO{in: strings.NewReader(""), out: io.Discard, errOut: &stderr, sshPath: sshPath})
	var exitErr *commandExitError
	if !errors.As(err, &exitErr) || exitErr.code != 255 || !strings.Contains(exitErr.message, "state is unknown") || !strings.Contains(exitErr.message, "list sessions before relaunching") {
		t.Fatalf("SSH disconnect result = %+v", err)
	}
	if stderr.String() != "Connection closed\n" {
		t.Fatalf("SSH diagnostic was not preserved: %q", stderr.String())
	}
}
