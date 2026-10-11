package egg

import (
	"errors"
	"github.com/ehrlich-b/wingthing/internal/agent"
)

// LaunchCommandPreview uses the runtime builders without preparing directories,
// hooks on disk, a PTY, or a process. Identity is a disposable planning identity;
// no prompt, resume ID, passthrough argv, or environment values are accepted.
func LaunchCommandPreview(agentName, cwd, home, versionOutput string, unattended bool) (args []string, nativeRun bool, err error) {
	name, argv, ok := agent.InteractiveInvocation(agentName, unattended, "")
	if !ok {
		return nil, false, errors.New("unsupported agent")
	}
	if agentName == "codex" {
		if CodexLifecycleVersionSupported(versionOutput) {
			argv, nativeRun, err = codexRunArgs(argv, home, "launch-plan", false)
			if err != nil {
				return nil, false, err
			}
		}
		argv = codexStartupArgs(argv, cwd)
	}
	return append([]string{name}, argv...), nativeRunSupported("agent", agentName, nil, nativeRun), nil
}
