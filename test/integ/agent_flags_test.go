//go:build e2e

package integ

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

// TestUnattendedFlagsStillExistInInstalledAgents catches vendor flag drift, which
// no unit test can catch. An exact-argv test pins what Wingthing intends to send;
// it passes happily while the agent CLI removes the flag underneath it, and the
// first person to find out is a user whose session exits instantly with a usage
// error. That is exactly how `codex --full-auto` rotted: the catalog kept sending
// it long after Codex dropped it from the interactive CLI.
//
// Running the real binary with --help is cheap, offline, and makes an upstream
// removal a test failure. Agents that are not installed are skipped, which is a
// capability-driven skip, the only kind allowed.
func TestUnattendedFlagsStillExistInInstalledAgents(t *testing.T) {
	for _, definition := range agentpkg.Definitions() {
		if len(definition.UnattendedArgs) == 0 {
			continue
		}
		t.Run(definition.Name, func(t *testing.T) {
			if _, err := exec.LookPath(definition.Command); err != nil {
				t.Skipf("%s is not installed", definition.Command)
			}

			args := append(append([]string(nil), definition.UnattendedArgs...), "--help")
			cmd := exec.Command(definition.Command, args...)
			output, err := unattendedHelp(t, cmd, 30*time.Second)

			if err != nil {
				t.Fatalf("%s %s: %v\n%s", definition.Command, strings.Join(args, " "), err, output)
			}
		})
	}
}

// A startup refusal is not evidence that a vendor removed a flag. Every
// nonzero exit still fails the installed-agent check, with the original cause.
func unattendedHelp(t *testing.T, cmd *exec.Cmd, limit time.Duration) (string, error) {
	t.Helper()
	output, err := runBounded(t, cmd, limit)
	rejected := false
	for _, complaint := range []string{"unexpected argument", "unknown flag", "unrecognized"} {
		rejected = rejected || strings.Contains(strings.ToLower(output), complaint)
	}
	if err != nil {
		var exit *exec.ExitError
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return output, fmt.Errorf("provider help timed out: %w", err)
		case errors.As(err, &exit) && exit.ExitCode() == 75:
			return output, fmt.Errorf("temporary provider startup failure (exit 75): %w", err)
		case errors.As(err, &exit) && exit.ExitCode() == -1:
			return output, fmt.Errorf("provider process disappeared during help: %w", err)
		case rejected:
			return output, fmt.Errorf("provider rejected unattended flags: %w", err)
		default:
			return output, fmt.Errorf("provider help failed: %w", err)
		}
	}
	if rejected {
		return output, fmt.Errorf("provider rejected unattended flags: %s", output)
	}
	return output, nil
}

func runBounded(t *testing.T, cmd *exec.Cmd, limit time.Duration) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd.WaitDelay = time.Second
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), err
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		waitErr := <-done
		return out.String(), errors.Join(context.DeadlineExceeded, waitErr)
	}
}

// Exercise the same helper as the installed-agent gate using the existing
// embedded provider boundary. No executable is discovered on the user's PATH.
func TestProviderHelpFailureClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.py")
	if err := os.WriteFile(path, []byte(testprovider.Codex), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, want string
		code       int
	}{
		{"ok", "", 0},
		{"exit-75", "temporary provider startup failure (exit 75)", 75},
		{"disappear", "provider process disappeared during help", -1},
		{"reject", "provider rejected unattended flags", 2},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command("/usr/bin/python3", path, "--full-auto", "--help")
			cmd.Env = append(os.Environ(), "WT_TEST_PROVIDER_HELP="+tc.mode)
			output, err := unattendedHelp(t, cmd, time.Second)
			if tc.want == "" {
				if err != nil || !strings.Contains(output, "fixture codex help") {
					t.Fatal(output, err)
				}
				return
			}
			var exit *exec.ExitError
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.As(err, &exit) || exit.ExitCode() != tc.code {
				t.Fatalf("startup cause hidden or misclassified: output=%q err=%v", output, err)
			}
			if tc.mode == "exit-75" && !strings.Contains(output, "fixture quota supervisor") {
				t.Fatal("startup diagnostic lost", output)
			}
			t.Logf("visible failure: %v; output=%q", err, output)
		})
	}
}

func TestProviderHelpTimeoutRemainsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.py")
	if err := os.WriteFile(path, []byte(testprovider.Codex), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command("/usr/bin/python3", path, "--help")
	cmd.Env = append(os.Environ(), "WT_TEST_PROVIDER_HELP=wait")
	cmd.Stdin = reader
	// An already-expired budget must kill and reap the owned fake, never pass.
	_, err = unattendedHelp(t, cmd, 0)
	if !errors.Is(err, context.DeadlineExceeded) || cmd.ProcessState == nil {
		t.Fatalf("help timeout passed or left an unjoined provider: %v", err)
	}
	t.Logf("visible failure: %v", err)
}
