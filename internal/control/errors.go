package control

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
)

// ErrorKind identifies a known failure independently of its diagnostic text.
// The same kinds are used in direct envelopes and MCP structured tool results.
type ErrorKind string

const (
	ErrorSessionNameInUse ErrorKind = "session_name_in_use"
	ErrorNotFound         ErrorKind = "not_found"
	ErrorNoRows           ErrorKind = "no_rows"
	ErrorCanceled         ErrorKind = "canceled"
	ErrorDeadlineExceeded ErrorKind = "deadline_exceeded"
)

// ErrSessionNameInUse is also exported by eggclient for local store callers.
var ErrSessionNameInUse = errors.New("session name is already in use")

var knownErrors = []struct {
	kind ErrorKind
	err  error
}{
	{ErrorSessionNameInUse, ErrSessionNameInUse},
	{ErrorNotFound, fs.ErrNotExist},
	{ErrorNoRows, sql.ErrNoRows},
	{ErrorCanceled, context.Canceled},
	{ErrorDeadlineExceeded, context.DeadlineExceeded},
}

func ErrorKindOf(err error) ErrorKind {
	for _, known := range knownErrors {
		if errors.Is(err, known.err) {
			return known.kind
		}
	}
	return ""
}

// ErrorResult retains the kind before the Go error crosses a wire boundary.
func ErrorResult(err error) map[string]any {
	result := map[string]any{"error": err.Error()}
	if kind := ErrorKindOf(err); kind != "" {
		result["error_kind"] = string(kind)
	}
	return result
}

// DecodeError preserves diagnostic text and restores known sentinel identity.
// Unknown kinds remain ordinary errors, allowing newer peers to add kinds.
func DecodeError(kind ErrorKind, message string) error {
	for _, known := range knownErrors {
		if kind == known.kind {
			if message == "" {
				return known.err
			}
			return &remoteError{message: message, cause: known.err}
		}
	}
	return errors.New(message)
}

type remoteError struct {
	message string
	cause   error
}

func (e *remoteError) Error() string { return e.message }
func (e *remoteError) Unwrap() error { return e.cause }

// ToolError restores a structured tool error. Legacy unclassified failures
// continue to use the tool result's isError flag and error text.
func ToolError(result map[string]any) error {
	kind, _ := result["error_kind"].(string)
	if kind == "" {
		return nil
	}
	message, _ := result["error"].(string)
	return DecodeError(ErrorKind(kind), message)
}
