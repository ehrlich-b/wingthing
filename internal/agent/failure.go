package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type ErrorKind string

const (
	ProviderRefused  ErrorKind = "provider_refused"
	AuthFailed       ErrorKind = "auth_failed"
	RateLimited      ErrorKind = "rate_limited"
	ContextExhausted ErrorKind = "context_exhausted"
	SandboxDenied    ErrorKind = "sandbox_denied"
	ProviderError    ErrorKind = "provider_error"
	ProviderExit     ErrorKind = "provider_exit"
)

// Failure contains only Wingthing-authored diagnostics. Provider messages are
// classification input and must never be retained in an error or its cause.
type Failure struct {
	Kind     ErrorKind
	Provider string
	exit     *exec.ExitError
}

func (f *Failure) Error() string {
	if f.exit != nil {
		return fmt.Sprintf("%s: %s exited with status %d", f.Kind, f.Provider, f.exit.ExitCode())
	}
	return fmt.Sprintf("%s: %s reported a failure", f.Kind, f.Provider)
}

func (f *Failure) Unwrap() error {
	if f.exit == nil {
		return nil
	}
	return f.exit
}

type kindError struct {
	kind ErrorKind
	err  error
}

func (e *kindError) Error() string { return string(e.kind) + ": " + e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

// SandboxFailure wraps a diagnostic produced by Wingthing's own policy code.
func SandboxFailure(err error) error { return &kindError{SandboxDenied, err} }

func FailureKind(err error) ErrorKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	var local *kindError
	if errors.As(err, &local) {
		return local.kind
	}
	return ""
}

func classifyProviderText(text string) ErrorKind {
	text = strings.ToLower(text)
	contains := func(patterns ...string) bool {
		for _, pattern := range patterns {
			if strings.Contains(text, pattern) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("content was flagged", "content_policy", "content policy", "policy violation", "safety policy", "provider refused", "content_filter", "cybersecurity risk"):
		return ProviderRefused
	case contains("authentication failed", "authentication_failed", "authentication_error", "auth_failed", "invalid api key", "invalid_api_key", "incorrect api key", "unauthorized", "not logged in", "login required", "token expired", "401"):
		return AuthFailed
	case contains("rate limit", "rate_limit", "too many requests", "quota exceeded", "exhausted the quota", "insufficient_quota", "429"):
		return RateLimited
	case contains("context window", "context length", "context_length", "prompt is too long", "prompt too long", "prompt_too_long", "max_tokens", "token limit exceeded"):
		return ContextExhausted
	case contains("sandbox", "operation not permitted", "permission denied", "filesystem enforcement failed"):
		return SandboxDenied
	default:
		return ""
	}
}

func eventFailureKind(text string) ErrorKind {
	if kind := classifyProviderText(text); kind != "" {
		return kind
	}
	return ProviderError
}

func providerErrorText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var detail struct {
		Type    string          `json:"type"`
		Code    string          `json:"code"`
		Message json.RawMessage `json:"message"`
	}
	_ = json.Unmarshal(raw, &detail)
	_ = json.Unmarshal(detail.Message, &text)
	return detail.Type + "\n" + detail.Code + "\n" + text
}

func preferFailureKind(current, next ErrorKind) ErrorKind {
	if next == "" {
		return current
	}
	if current == "" || current == ProviderError || current == ProviderExit {
		return next
	}
	return current
}

func parseClaudeFailure(line string) (ErrorKind, bool) {
	var ev struct {
		Type    string          `json:"type"`
		IsError bool            `json:"is_error"`
		Error   json.RawMessage `json:"error"`
		Message json.RawMessage `json:"message"`
		Result  string          `json:"result"`
		Errors  []string        `json:"errors"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return "", false
	}
	if ev.Type == "error" || ev.Type == "result" && ev.IsError || ev.Type == "assistant" && len(ev.Error) > 0 && string(ev.Error) != "null" {
		message := providerErrorText(ev.Error)
		var topMessage string
		if json.Unmarshal(ev.Message, &topMessage) != nil {
			var body messageBody
			_ = json.Unmarshal(ev.Message, &body)
			for _, block := range body.Content {
				topMessage += "\n" + block.Text
			}
		}
		return eventFailureKind(message + "\n" + topMessage + "\n" + ev.Result + "\n" + strings.Join(ev.Errors, "\n")), true
	}
	return "", false
}
