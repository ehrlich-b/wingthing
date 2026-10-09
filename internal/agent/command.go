package agent

import (
	"errors"
	"os/exec"
)

const maxAgentStderr = 64 * 1024

type commandDiagnostics struct {
	stderr  cappedBuffer
	failure ErrorKind
}

func startAgentCommand(cmd *exec.Cmd) (*commandDiagnostics, error) {
	configureProcessTree(cmd)
	return startConfiguredAgentCommand(cmd)
}

func startConfiguredAgentCommand(cmd *exec.Cmd) (*commandDiagnostics, error) {
	diagnostics := &commandDiagnostics{stderr: cappedBuffer{limit: maxAgentStderr}}
	cmd.Stderr = &diagnostics.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func waitAgentCommand(cmd *exec.Cmd, diagnostics *commandDiagnostics, provider string) error {
	return agentCommandError(cmd.Wait(), diagnostics, provider)
}

func agentCommandError(err error, diagnostics *commandDiagnostics, provider string) error {
	var kind ErrorKind
	if diagnostics != nil {
		kind = diagnostics.failure
		if err != nil || kind != "" {
			kind = preferFailureKind(kind, classifyProviderText(diagnostics.stderr.String()))
		}
		// Discard the classification input before returning anything to callers.
		diagnostics.stderr.data = nil
	}
	if err == nil && kind == "" {
		return nil
	}
	if kind == "" {
		kind = ProviderExit
	}
	failure := &Failure{Kind: kind, Provider: provider}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// Keep only the process status, never ExitError.Stderr.
		failure.exit = &exec.ExitError{ProcessState: exit.ProcessState}
	}
	return failure
}

// cappedBuffer retains only a bounded classification input. Its bytes must
// never be returned to callers or persisted.
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
