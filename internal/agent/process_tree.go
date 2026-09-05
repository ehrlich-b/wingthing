package agent

import "os/exec"

// ConfigureProcessTree bounds cancellation to a command and its descendants.
func ConfigureProcessTree(cmd *exec.Cmd) { configureProcessTree(cmd) }
