package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

const maxAgentStderr = 64 * 1024

type commandDiagnostics struct {
	stderr cappedBuffer
}

func startAgentCommand(cmd *exec.Cmd) (*commandDiagnostics, error) {
	diagnostics := &commandDiagnostics{stderr: cappedBuffer{limit: maxAgentStderr}}
	cmd.Stderr = &diagnostics.stderr
	configureProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func waitAgentCommand(cmd *exec.Cmd, diagnostics *commandDiagnostics) error {
	err := cmd.Wait()
	if err == nil || diagnostics == nil {
		return err
	}
	stderr := strings.TrimSpace(diagnostics.stderr.String())
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

// cappedBuffer keeps CLI diagnostics useful without allowing a noisy child to
// consume unbounded memory. It intentionally retains the first output, which
// is where CLIs normally print their actionable startup/authentication error.
type cappedBuffer struct {
	data  []byte
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	return string(b.data)
}
