//go:build e2e

package integ

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

// This fixture replaces only the SSH transport. The remote CLI, egg process,
// sandbox, private Unix endpoint, terminal replay, and detach protocol are real.
// It never contacts a host, reads provider credentials, or changes a daemon.
func TestRemoteCLIReattachesExactPersistentSession(t *testing.T) {
	for _, channel := range []struct{ name, env string }{{"stable", "WT_TEST_BINARY"}, {"preview", "WT_TEST_PREVIEW_BINARY"}} {
		t.Run(channel.name, func(t *testing.T) {
			binary := os.Getenv(channel.env)
			if binary == "" {
				t.Fatalf("%s is required; run make test-integ", channel.env)
			}
			remoteCLIReattachJourney(t, binary)
		})
	}
}

func remoteCLIReattachJourney(t *testing.T, binary string) {
	t.Helper()
	fixture, err := os.MkdirTemp("/tmp", "wt-remote-integ-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	remoteState := filepath.Join(fixture, "remote")
	localState := filepath.Join(fixture, "local-must-stay-absent")
	remoteHome := filepath.Join(fixture, "home")
	workspace := filepath.Join(fixture, "work with spaces")
	binDir := filepath.Join(fixture, "bin")
	for _, dir := range []string{workspace, remoteHome, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sshScript := "#!/bin/sh\n" +
		"case \"$1\" in -T|-t) ;; *) exit 91;; esac\n" +
		"[ \"$2\" = fixture-host ] || exit 92\n" +
		"if [ \"$WT_FIXTURE_DISCONNECT\" = 1 ]; then printf 'fixture connection lost\\n' >&2; exit 255; fi\n" +
		"exec env WINGTHING_DIR=\"$WT_FIXTURE_STATE\" HOME=\"$WT_FIXTURE_HOME\" /bin/sh -c \"$3\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(sshScript), 0o700); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(workspace, "fixture.sh")
	command := "#!/bin/sh\nprintf 'fixture-ready\\n'\nwhile IFS= read -r line; do printf 'fixture-result:%s\\n' \"$line\"; done\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "WINGTHING_DIR="+localState, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "WT_FIXTURE_STATE="+remoteState, "WT_FIXTURE_HOME="+remoteHome)
	run := func(input string, extraEnv []string, args ...string) (string, string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		argv := append([]string{"--remote", "fixture-host", "--remote-binary", binary}, args...)
		child := exec.CommandContext(ctx, binary, argv...)
		child.Env = append(append([]string(nil), env...), extraEnv...)
		child.Stdin = strings.NewReader(input)
		var output, diagnostic bytes.Buffer
		child.Stdout, child.Stderr = &output, &diagnostic
		err := child.Run()
		return output.String(), diagnostic.String(), err
	}
	mustRun := func(input string, args ...string) string {
		t.Helper()
		output, diagnostic, err := run(input, nil, args...)
		if err != nil {
			t.Fatalf("remote %q: %v\nstdout: %s\nstderr: %s", args, err, output, diagnostic)
		}
		return output
	}
	if got := mustRun("", "--json"); got != "[]\n" {
		t.Fatalf("empty inventory = %q", got)
	}
	ids := make(map[string]string)
	for _, name := range []string{"first", "second"} {
		output := mustRun("", "terminal", "--cwd", workspace, "--name", name, "--json", "--", "/bin/sh", commandPath)
		var started struct {
			Session string `json:"session"`
			CWD     string `json:"cwd"`
		}
		if err := json.Unmarshal([]byte(output), &started); err != nil || started.Session == "" || started.CWD != workspace {
			t.Fatalf("launch returned invalid session: output=%s err=%v", output, err)
		}
		ids[name] = started.Session
		t.Cleanup(func() {
			// Cleanup remains available if a CLI preflight regression blocks
			// inspection. It addresses only this fixture's exact egg endpoint.
			dir := filepath.Join(remoteState, "eggs", started.Session)
			client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
			if err != nil {
				t.Errorf("open fixture cleanup endpoint %s: %v", started.Session, err)
				return
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Kill(ctx, started.Session); err != nil {
				t.Errorf("stop fixture %s: %v", started.Session, err)
			}
		})
		mustRun("", "session", "wait", started.Session, "--contains", "fixture-ready", "--timeout", "5s", "--json")
	}
	for _, ref := range []string{"first", ids["first"]} {
		_, diagnostic, err := run(string([]byte{0x02, 'q'}), nil, "attach", ref)
		if err != nil || !strings.Contains(diagnostic, "detached from") {
			t.Fatalf("detach exact ref %q: %v %s", ref, err, diagnostic)
		}
	}
	// A non-TTY caller closing stdin must detach rather than hang or stop the
	// egg. The subsequent reconnect and prompt prove this egg is still usable.
	_, diagnostic, err := run("", nil, "attach", ids["first"])
	if err != nil || !strings.Contains(diagnostic, "detached from") {
		t.Fatalf("stdin EOF did not detach: %v %s", err, diagnostic)
	}
	// A transport interruption is neither a completed agent nor permission to
	// launch a replacement. Reconnect discovers the same two session IDs.
	_, diagnostic, err = run("", []string{"WT_FIXTURE_DISCONNECT=1"}, "attach", ids["first"])
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 || !strings.Contains(diagnostic, "state is unknown") {
		t.Fatalf("disconnect contract err=%v diagnostic=%s", err, diagnostic)
	}
	output := mustRun("", "session", "ps", "--json")
	var sessions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(output), &sessions); err != nil || len(sessions) != 2 {
		t.Fatalf("reconnect inventory = %s err=%v", output, err)
	}
	for _, session := range sessions {
		if ids[session.Name] != session.ID {
			t.Fatalf("reconnect changed identity: %+v want=%v", session, ids)
		}
	}
	_, diagnostic, err = run("piped attach input\n", nil, "attach", ids["first"])
	if err != nil || !strings.Contains(diagnostic, "detached from") {
		t.Fatalf("piped input detach: %v %s", err, diagnostic)
	}
	mustRun("", "session", "wait", ids["first"], "--contains", "fixture-result:piped attach input", "--timeout", "5s", "--json")
	mustRun("exact child input", "session", "send", ids["first"], "--stdin", "--enter", "--json")
	mustRun("", "session", "wait", ids["first"], "--contains", "fixture-result:exact child input", "--timeout", "5s", "--json")
	first := mustRun("", "session", "read", ids["first"], "--json")
	second := mustRun("", "session", "read", ids["second"], "--json")
	if !strings.Contains(first, "fixture-result:exact child input") || strings.Contains(second, "exact child input") {
		t.Fatalf("input did not stay on selected session: first=%s second=%s", first, second)
	}
	if _, err := os.Stat(localState); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote invocation touched client state: %v", err)
	}
}
