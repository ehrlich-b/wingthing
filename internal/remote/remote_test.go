package remote

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

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
)

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

func TestRemoteUnsupportedSessionSubcommandNamesTheSubcommand(t *testing.T) {
	err := ValidateRemoteCommand([]string{"session", "sync", "work"})
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
		if err := ValidateRemoteCommand(args); err != nil {
			t.Fatalf("remote validation rejected %q: %v", args, err)
		}
		if remoteCommandNeedsTTY(args) {
			t.Fatalf("remote stop unexpectedly requested a TTY: %q", args)
		}
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
	err := RunRemoteInvocation(context.Background(), Invocation{
		Target: "work1", Binary: remoteBinary,
		Args: []string{"terminal", "--detach", "--name", "codex's remote"},
	}, IO{In: strings.NewReader(input), Out: &stdout, ErrOut: &stderr, SSHPath: sshPath})
	var exitErr *cmdutil.CommandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 23 {
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
	err := RunRemoteInvocation(context.Background(), Invocation{
		Target: "work1", Binary: remoteBinary,
		Args: []string{"egg", "claude", "--", "--tools", "", " \t "},
	}, IO{In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr, SSHPath: sshPath})
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

func TestRunRemoteInvocationCancellationStopsSSHClient(t *testing.T) {
	tempDir := t.TempDir()
	sshPath := filepath.Join(tempDir, "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := RunRemoteInvocation(ctx, Invocation{
		Target: "work1", Binary: "wt", Args: []string{"terminal", "--json"},
	}, IO{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, SSHPath: sshPath})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ssh cancellation took %s", elapsed)
	}
}

func TestRemoteStateSendsQuotedPOSIXAssignmentsOnlyWhenSelected(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := func(channel string, invocation Invocation) string {
		t.Helper()
		previous := config.ReleaseChannel
		config.ReleaseChannel = channel
		defer func() { config.ReleaseChannel = previous }()
		var out bytes.Buffer
		if err := RunRemoteInvocation(context.Background(), invocation, IO{In: strings.NewReader(""), Out: &out, ErrOut: io.Discard, SSHPath: sshPath}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	base := Invocation{Target: "work1", Args: []string{"session", "ps", "--json"}}

	// Absent flag: byte-identical legacy argv for both channels.
	stable := base
	stable.Binary = "wt"
	if got, want := command("stable", stable), "'wt' 'session' 'ps' '--json'\n"; got != want {
		t.Fatalf("stable legacy command = %q want %q", got, want)
	}
	preview := base
	preview.Binary = "wt-preview"
	if got, want := command("preview", preview), "'wt-preview' '--expected-channel' 'preview' 'session' 'ps' '--json'\n"; got != want {
		t.Fatalf("preview legacy command = %q want %q", got, want)
	}

	// Selected: both names carry the same single-quoted lexical path.
	preview.State = "/srv/wt's state/$HOME/`id`"
	quoted := `'/srv/wt'"'"'s state/$HOME/` + "`id`'"
	want := "WINGTHING_DIR=" + quoted + " WINGTHING_PREVIEW_DIR=" + quoted + " 'wt-preview' '--expected-channel' 'preview' 'session' 'ps' '--json'\n"
	if got := command("preview", preview); got != want {
		t.Fatalf("preview state command = %q want %q", got, want)
	}
	// A selected stable state also binds the stable channel, so a receiver
	// released before the channel-marker check rejects the request instead of
	// honoring WINGTHING_DIR unguarded.
	stable.State = "/srv/stable state"
	want = "WINGTHING_DIR='/srv/stable state' WINGTHING_PREVIEW_DIR='/srv/stable state' 'wt' '--expected-channel' 'stable' 'session' 'ps' '--json'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable state command = %q want %q", got, want)
	}
	// Egg launches keep --remote-exact-argv after the channel guard.
	stable.Args = []string{"egg", "claude", "--", "--remote-state", "x"}
	want = "WINGTHING_DIR='/srv/stable state' WINGTHING_PREVIEW_DIR='/srv/stable state' 'wt' '--expected-channel' 'stable' 'egg' 'claude' '--remote-exact-argv' '--' '--remote-state' 'x'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable state egg command = %q want %q", got, want)
	}
	stable.State = ""
	want = "'wt' 'egg' 'claude' '--remote-exact-argv' '--' '--remote-state' 'x'\n"
	if got := command("stable", stable); got != want {
		t.Fatalf("stable legacy egg command = %q want %q", got, want)
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
	err := RunRemoteInvocation(context.Background(), Invocation{
		Target: "work1", Binary: remoteBinary, State: state, Args: []string{"channel", "--json"},
	}, IO{In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr, SSHPath: sshPath})
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

func TestRemoteStateDisconnectNamesTheSameStateForReconnect(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := RunRemoteInvocation(context.Background(), Invocation{Target: "work1", Binary: "wt", State: "/state", Args: []string{"attach", "research"}}, IO{In: strings.NewReader(""), Out: io.Discard, ErrOut: io.Discard, SSHPath: sshPath})
	var exitErr *cmdutil.CommandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 255 || !strings.Contains(exitErr.Message, "state is unknown; reconnect with the same --remote-state and list sessions before relaunching") {
		t.Fatalf("SSH disconnect result = %+v", err)
	}
}

func TestRemoteDisconnectPreserves255AndDoesNotSuggestRelaunch(t *testing.T) {
	sshPath := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nprintf 'Connection closed\\n' >&2\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err := RunRemoteInvocation(context.Background(), Invocation{Target: "work1", Binary: "wt", Args: []string{"attach", "research"}}, IO{In: strings.NewReader(""), Out: io.Discard, ErrOut: &stderr, SSHPath: sshPath})
	var exitErr *cmdutil.CommandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 255 || !strings.Contains(exitErr.Message, "state is unknown") || !strings.Contains(exitErr.Message, "list sessions before relaunching") {
		t.Fatalf("SSH disconnect result = %+v", err)
	}
	if stderr.String() != "Connection closed\n" {
		t.Fatalf("SSH diagnostic was not preserved: %q", stderr.String())
	}
}
