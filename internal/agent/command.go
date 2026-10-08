package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

const maxAgentStderr = 64 * 1024

type commandDiagnostics struct {
	stderr   cappedBuffer
	redactor *Redactor
}

func startAgentCommand(cmd *exec.Cmd, credentials ...string) (*commandDiagnostics, error) {
	redactor := NewRedactor(append(os.Environ(), cmd.Environ()...), credentials...)
	lookahead := 32
	if len(redactor.secrets) > 0 && len(redactor.secrets[0]) > lookahead {
		lookahead = len(redactor.secrets[0])
	}
	diagnostics := &commandDiagnostics{stderr: cappedBuffer{limit: maxAgentStderr + lookahead}, redactor: redactor}
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
	stderr := diagnostics.redactor.Text(diagnostics.stderr.String())
	if len(stderr) > maxAgentStderr {
		stderr = stderr[:maxAgentStderr]
		for !utf8.ValidString(stderr) {
			stderr = stderr[:len(stderr)-1]
		}
	}
	stderr = strings.TrimSpace(stderr)
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
