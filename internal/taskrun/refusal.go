package taskrun

import (
	"errors"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

type sandboxRefusal struct {
	cause   error
	verbose bool
}

func (e *sandboxRefusal) Unwrap() error { return e.cause }
func (e *sandboxRefusal) Error() string {
	var limits *sandbox.ResourceLimitError
	message := "Task refused: this machine cannot enforce the sandbox. Nothing ran. Run wt doctor to check this machine's sandbox support."
	if errors.As(e.cause, &limits) {
		message = "Task refused: this machine cannot enforce the sandbox memory/process limits. Nothing ran. Use a host with delegated memory and process cgroups, or ask its administrator to enable them."
	}
	if e.verbose {
		cause := e.cause
		var enforcement *sandbox.EnforcementError
		if errors.As(cause, &enforcement) && enforcement.Cause != nil {
			cause = enforcement.Cause
		}
		if limits != nil {
			cause = limits.Cause
		}
		message += "\nSandbox details: " + cause.Error()
	} else {
		message += " Details: wt run --verbose."
	}
	return message
}
