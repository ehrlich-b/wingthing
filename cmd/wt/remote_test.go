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

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/spf13/cobra"
)

func TestParseRemoteInvocationPreservesCommandArgv(t *testing.T) {
	argv := []string{
		"--remote", "work1", "egg", "codex", "--cwd", "/home/dogfood/work/one two",
		"--name", "review", "--", "-m", "gpt-5.6-sol", "--tools", "", " \t ", "quote'and\nnewline",
	}
	invocation, remote, err := remotepkg.ParseRemoteInvocation(argv, true, newRootCommand)
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
	if !reflect.DeepEqual(invocation.Args, wantArgs) {
		t.Fatalf("remote args = %#v, want %#v", invocation.Args, wantArgs)
	}
	if invocation.Target != "work1" || invocation.Binary != "wt" || !invocation.AllocateTTY {
		t.Fatalf("remote invocation = %#v", invocation)
	}
}

func TestPreviewRemoteSelectsPreviewBinaryAndBindsChannel(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "attach", "research"}, false, newRootCommand)
	if err != nil || !remote || invocation.Binary != "wt-preview" {
		t.Fatalf("preview route = %+v remote=%v err=%v", invocation, remote, err)
	}
	// The explicit staged path still carries the identity guard. Its basename
	// is not trusted as proof that it is a preview build.
	invocation, _, err = remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "--remote-binary", "/staged/build", "attach", "research"}, false, newRootCommand)
	if err != nil {
		t.Fatal(err)
	}
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var command bytes.Buffer
	if err := remotepkg.RunRemoteInvocation(context.Background(), invocation, remotepkg.IO{In: strings.NewReader(""), Out: &command, ErrOut: io.Discard, SSHPath: sshPath}); err != nil {
		t.Fatal(err)
	}
	want := "'/staged/build' '--expected-channel' 'preview' 'attach' 'research'\n"
	if command.String() != want {
		t.Fatalf("remote preview identity = %q want=%q", command.String(), want)
	}
}

func TestParseRemoteInvocationSupportsLegacyAttachPlacement(t *testing.T) {
	invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{
		"attach", "session-name", "--remote-binary=/opt/wing thing/wt", "--remote", "builder.example",
	}, true, newRootCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !remote || invocation.Target != "builder.example" || invocation.Binary != "/opt/wing thing/wt" {
		t.Fatalf("remote invocation = %#v, remote=%v", invocation, remote)
	}
	if !reflect.DeepEqual(invocation.Args, []string{"attach", "session-name"}) || !invocation.AllocateTTY {
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
			if invocation, remote, err := remotepkg.ParseRemoteInvocation(args, false, newRootCommand); err != nil || remote {
				t.Fatalf("local flag value routed remotely: %+v remote=%v err=%v", invocation, remote, err)
			}
			argv := append(append([]string(nil), args...), "--remote", "work1")
			invocation, remote, err := remotepkg.ParseRemoteInvocation(argv, false, newRootCommand)
			if err != nil || !remote || invocation.Target != "work1" || !reflect.DeepEqual(invocation.Args, args) {
				t.Fatalf("remote flag value changed argv: %+v remote=%v err=%v", invocation, remote, err)
			}
		})
	}
}

func TestParseBareRemoteChoosesInteractiveEntryOrReadOnlyJSON(t *testing.T) {
	interactive, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote=work1", "--remote-cwd", "/work/project"}, true, newRootCommand)
	if err != nil || !remote {
		t.Fatalf("interactive parse: remote=%v err=%v", remote, err)
	}
	if !reflect.DeepEqual(interactive.Args, []string{"_remote", "enter", "--cwd", "/work/project"}) || !interactive.AllocateTTY {
		t.Fatalf("interactive entry = %#v", interactive)
	}

	noninteractive, remote, err := remotepkg.ParseRemoteInvocation([]string{"-r=work1"}, false, newRootCommand)
	if err != nil || !remote {
		t.Fatalf("noninteractive parse: remote=%v err=%v", remote, err)
	}
	if !reflect.DeepEqual(noninteractive.Args, []string{"_remote", "enter", "--json"}) || noninteractive.AllocateTTY {
		t.Fatalf("noninteractive entry = %#v", noninteractive)
	}
}

func TestBareRemoteJSONIsDiscoverableAndNeverAttaches(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "--remote-cwd", "/work", "--json"}, interactive, newRootCommand)
		if err != nil || !remote || invocation.AllocateTTY {
			t.Fatalf("interactive=%v invocation=%+v remote=%v err=%v", interactive, invocation, remote, err)
		}
		if invocation.Args[0] != "_remote" || invocation.Args[1] != "enter" || !remotepkg.BoolFlagBeforeDash(invocation.Args[2:], "json", "") {
			t.Fatalf("bare JSON did not request read-only inventory: %v", invocation.Args)
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
		if _, _, err := remotepkg.ParseRemoteInvocation(argv, false, newRootCommand); err == nil {
			t.Errorf("parseRemoteInvocation(%q) unexpectedly succeeded", argv)
		}
	}
}

func TestRemoteSessionListCommandIsExecutable(t *testing.T) {
	if err := remotepkg.ValidateRemoteCommand([]string{"session", "list"}); err != nil {
		t.Fatalf("remote validation rejected session list: %v", err)
	}
	t.Setenv("WINGTHING_DIR", t.TempDir())
	command := newRootCommand()
	command.SetArgs([]string{"session", "list"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute session list alias: %v", err)
	}
}

func TestRemoteFlagsAfterDashStayWithRemoteCommand(t *testing.T) {
	invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{
		"--remote", "work1", "terminal", "--json", "--", "printf", "%s", "--remote", "literal-host-flag",
	}, false, newRootCommand)
	if err != nil || !remote {
		t.Fatalf("parse: remote=%v err=%v", remote, err)
	}
	want := []string{"terminal", "--json", "--", "printf", "%s", "--remote", "literal-host-flag"}
	if !reflect.DeepEqual(invocation.Args, want) {
		t.Fatalf("args = %#v, want %#v", invocation.Args, want)
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
		invocation, remote, err := remotepkg.ParseRemoteInvocation(tt.argv, false, newRootCommand)
		if err != nil || !remote {
			t.Fatalf("parse %q: remote=%v err=%v", tt.argv, remote, err)
		}
		if invocation.State != state || invocation.Target != "work1" {
			t.Fatalf("parse %q: state=%q target=%q", tt.argv, invocation.State, invocation.Target)
		}
		if !reflect.DeepEqual(invocation.Args, tt.wantArgs) {
			t.Fatalf("parse %q: args=%q want %q", tt.argv, invocation.Args, tt.wantArgs)
		}
	}

	bare, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "--remote-state", "/state", "--remote-cwd", "/work"}, true, newRootCommand)
	if err != nil || !remote || bare.State != "/state" {
		t.Fatalf("bare entry with state: %+v remote=%v err=%v", bare, remote, err)
	}
	if !reflect.DeepEqual(bare.Args, []string{"_remote", "enter", "--cwd", "/work"}) {
		t.Fatalf("bare entry args = %q", bare.Args)
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
		_, _, err := remotepkg.ParseRemoteInvocation(tt.argv, false, newRootCommand)
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
	err := remotepkg.RunRemoteInvocation(context.Background(), remotepkg.Invocation{Target: "work1", Binary: "wt", State: "relative", Args: []string{"session", "ps"}}, remotepkg.IO{In: strings.NewReader(""), Out: io.Discard, ErrOut: io.Discard, SSHPath: sshPath})
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
		invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "--remote-state", state, "session", "ps"}, false, newRootCommand)
		if err != nil || !remote || invocation.State != state {
			t.Errorf("state %q: remote=%v state=%q err=%v", state, remote, invocation.State, err)
		}
	}
	for _, b := range []byte{0, '\r', '\n'} {
		if err := remotepkg.ValidateRemoteState("/a" + string(b) + "b"); err == nil {
			t.Errorf("state with byte %#x accepted", b)
		}
	}
}

// receivedRemoteArgv runs the client transport against a fake SSH that
// evaluates the command in /bin/sh and a fake receiver that prints its argv,
// returning exactly what the remote executable would receive after argv[0].
func receivedRemoteArgv(t *testing.T, channel string, invocation remotepkg.Invocation) []string {
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
	invocation.Target, invocation.Binary = "work1", receiver
	var out bytes.Buffer
	if err := remotepkg.RunRemoteInvocation(context.Background(), invocation, remotepkg.IO{In: strings.NewReader(""), Out: &out, ErrOut: io.Discard, SSHPath: sshPath}); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(out.String(), "\x1e"), "\x1e")
}

func TestRemoteStateStableRequiresChannelAwareReceiver(t *testing.T) {
	selected := receivedRemoteArgv(t, "stable", remotepkg.Invocation{State: "/srv/state", Args: []string{"session", "ps", "--json"}})
	if want := []string{"--expected-channel", "stable", "session", "ps", "--json"}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("stable selected argv = %q want %q", selected, want)
	}
	legacy := receivedRemoteArgv(t, "stable", remotepkg.Invocation{Args: []string{"session", "ps", "--json"}})
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

func TestRemoteChannelInspectionIsForwardedWithoutATTY(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		invocation, remote, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "work1", "--remote-state", "/state", "channel", "--json"}, interactive, newRootCommand)
		if err != nil || !remote {
			t.Fatalf("channel inspection: remote=%v err=%v", remote, err)
		}
		if !reflect.DeepEqual(invocation.Args, []string{"channel", "--json"}) || invocation.AllocateTTY {
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
