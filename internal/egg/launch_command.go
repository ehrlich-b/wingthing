package egg

import (
	"os"
	"path/filepath"
)

// LaunchCommandPreview runs real preparation in disposable mirrored state,
// without writing the selected HOME or starting a PTY or process. Identity is a disposable planning identity;
// no prompt, resume ID, passthrough argv, or environment values are accepted.
func LaunchCommandPreview(agentName, cwd, home, versionOutput string, unattended bool) (args []string, nativeRun bool, err error) {
	root, err := os.MkdirTemp("", "wt-launch-preview-*")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(root)
	state, err := NewDryLaunchState(root)
	if err != nil {
		return nil, false, err
	}
	rc := RunConfig{Agent: agentName, CWD: cwd, UserHome: home, OuterBoundary: true, DangerouslySkipPermissions: unattended}
	prepared, err := PrepareLaunch(rc, LaunchPreparationOptions{Home: home, HostHome: home, EggDir: filepath.Join(root, "launch-plan"), CodexCapability: SavedCodexCapability(versionOutput)}, state)
	if err != nil {
		return nil, false, err
	}
	name, _ := sessionCommand(rc)
	return append([]string{name}, prepared.Args...), prepared.NativeRun, nil
}
