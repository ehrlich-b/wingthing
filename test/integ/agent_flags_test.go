//go:build e2e

package integ

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
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
			output, err := runBounded(t, cmd, 30*time.Second)

			// --help exits zero on every supported CLI. A non-zero exit here is
			// either an unknown flag or a flag that changed arity; both mean the
			// unattended path is broken for this agent.
			if err != nil {
				t.Fatalf("%s %s: %v\n%s", definition.Command, strings.Join(args, " "), err, output)
			}
			for _, complaint := range []string{"unexpected argument", "unknown flag", "unrecognized"} {
				if strings.Contains(strings.ToLower(output), complaint) {
					t.Fatalf("%s rejected %q: %s", definition.Command, definition.UnattendedArgs, output)
				}
			}
		})
	}
}

func runBounded(t *testing.T, cmd *exec.Cmd, limit time.Duration) (string, error) {
	t.Helper()
	var out strings.Builder
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
		return out.String(), nil
	}
}
