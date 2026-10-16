package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// commandOutput owns the read end instead of relying on Wait to close it:
// provider parsers must finish reading before they can call Wait.
type commandOutput struct {
	*os.File
	mu   sync.Mutex
	stop func() bool
}

func (o *commandOutput) Close() error {
	o.stop()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.File.Close()
}

func startHeadlessCommand(ctx context.Context, cmd *exec.Cmd) (*commandOutput, *commandDiagnostics, error) {
	if cmd.Stdout != nil {
		return nil, nil, fmt.Errorf("stdout already set")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stdout = writer
	diagnostics, err := startAgentCommand(cmd)
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return nil, nil, err
	}
	output := &commandOutput{File: reader}
	output.stop = context.AfterFunc(ctx, func() {
		output.mu.Lock()
		defer output.mu.Unlock()
		// Keep the pipe open during discovery so its identity cannot be reused.
		// A child that starts a new session still owns this exact output pipe.
		stopOutputWriters(reader)
		_ = reader.Close()
	})
	return output, diagnostics, nil
}
