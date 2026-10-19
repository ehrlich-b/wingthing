package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"
)

const LaunchDiagnosticFramePrefix = "\x1b]777;wt-launch-diagnostic="

// ErrorKind identifies a known failure independently of its diagnostic text.
// The same kinds are used in direct envelopes and MCP structured tool results.
type ErrorKind string

const (
	ErrorInvalidArgument  ErrorKind = "invalid_argument"
	ErrorUnsupported      ErrorKind = "unsupported"
	ErrorSessionNameInUse ErrorKind = "session_name_in_use"
	ErrorNotFound         ErrorKind = "not_found"
	ErrorNoRows           ErrorKind = "no_rows"
	ErrorCanceled         ErrorKind = "canceled"
	ErrorDeadlineExceeded ErrorKind = "deadline_exceeded"
	ErrorLaunchAdmission  ErrorKind = "launch_admission"
	ErrorLaunchProvider   ErrorKind = "launch_provider"
	ErrorLaunchConnect    ErrorKind = "launch_connect"
	ErrorLaunchAttach     ErrorKind = "launch_attach"
)

// LaunchDiagnostic is an optional, content-free projection. It deliberately
// cannot carry arguments, environment, provider output, or configuration values.
type LaunchDiagnostic struct {
	Stage        string `json:"stage"`
	Reason       string `json:"reason"`
	PolicyOrigin string `json:"policy_origin,omitempty"`
	PolicyPath   string `json:"policy_path,omitempty"`
	PolicySource string `json:"policy_source,omitempty"`
	Session      string `json:"session,omitempty"`
}

// Keep the reason understood by older clients; newer clients can recover the
// specific missing provider without exposing arbitrary peer text.
func (d LaunchDiagnostic) MarshalJSON() ([]byte, error) {
	type diagnostic LaunchDiagnostic
	wire := struct {
		diagnostic
		ProviderReason string `json:"provider_reason,omitempty"`
	}{diagnostic: diagnostic(d.Bounded())}
	if d.Reason == "claude_missing" || d.Reason == "codex_missing" {
		wire.Reason = "provider_failed"
		wire.ProviderReason = d.Reason
	}
	return json.Marshal(wire)
}

func (d *LaunchDiagnostic) UnmarshalJSON(data []byte) error {
	type diagnostic LaunchDiagnostic
	var wire struct {
		diagnostic
		ProviderReason string `json:"provider_reason"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Reason == "provider_failed" && (wire.ProviderReason == "claude_missing" || wire.ProviderReason == "codex_missing") {
		wire.Reason = wire.ProviderReason
	}
	*d = LaunchDiagnostic(wire.diagnostic).Bounded()
	return nil
}

func (d LaunchDiagnostic) Bounded() LaunchDiagnostic {
	switch d.Stage {
	case "connect/startup", "admission/policy", "provider startup", "attach-after-admission":
	default:
		d.Stage = "admission/policy"
	}
	switch d.Reason {
	case "claude_missing", "codex_missing", "connection_unavailable", "startup_failed", "startup_exited", "canceled", "admission_unknown", "policy_refused", "invalid_workspace", "session_name_in_use", "executable_denied", "provider_home_denied", "workspace_denied", "provider_config_denied", "network_denied", "preparation_failed", "provider_failed", "attach_failed", "sandbox_unavailable", "authentication_required", "client_required", "client_not_configured":
	default:
		d.Reason = "policy_refused"
	}
	switch d.PolicyOrigin {
	case "selected policy", "built-in defaults", "captured runtime policy":
	default:
		d.PolicyOrigin = "unavailable"
	}
	for _, path := range []*string{&d.PolicyPath, &d.PolicySource} {
		if len(*path) > 1024 || !filepath.IsAbs(*path) || strings.IndexFunc(*path, unicode.IsControl) >= 0 {
			*path = ""
		}
	}
	if len(d.Session) > 128 || strings.IndexFunc(d.Session, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':' || r == '.')
	}) >= 0 || strings.Count(d.Session, ":") > 1 || strings.HasPrefix(d.Session, ":") || strings.HasSuffix(d.Session, ":") {
		d.Session = ""
	}
	return d
}

type launchError struct {
	cause      error
	diagnostic LaunchDiagnostic
}

func (e *launchError) Error() string {
	message := "launch failed during " + e.diagnostic.Stage + ": " + e.diagnostic.Reason
	if e.diagnostic.Reason == "sandbox_unavailable" {
		message += ". Run wt doctor --fix to inspect and repair host sandbox capability."
	}
	return message
}
func (e *launchError) Unwrap() error                      { return e.cause }
func (e *launchError) LaunchDiagnostic() LaunchDiagnostic { return e.diagnostic }

func WithLaunchDiagnostic(err error, d LaunchDiagnostic) error {
	if err == nil {
		return nil
	}
	return &launchError{cause: err, diagnostic: d.Bounded()}
}

func LaunchDiagnosticOf(err error) (LaunchDiagnostic, bool) {
	var source interface{ LaunchDiagnostic() LaunchDiagnostic }
	if errors.As(err, &source) {
		return source.LaunchDiagnostic().Bounded(), true
	}
	return LaunchDiagnostic{}, false
}

// ErrSessionNameInUse is also exported by eggclient for local store callers.
var ErrSessionNameInUse = errors.New("session name is already in use")

var knownErrors = []struct {
	kind ErrorKind
	err  error
}{
	{ErrorSessionNameInUse, ErrSessionNameInUse},
	{ErrorInvalidArgument, fs.ErrInvalid},
	{ErrorUnsupported, errors.ErrUnsupported},
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
	if d, ok := LaunchDiagnosticOf(err); ok {
		switch d.Stage {
		case "connect/startup":
			return ErrorLaunchConnect
		case "provider startup":
			return ErrorLaunchProvider
		case "attach-after-admission":
			return ErrorLaunchAttach
		default:
			return ErrorLaunchAdmission
		}
	}
	var remote *remoteError
	if errors.As(err, &remote) && remote.kind != "" {
		return remote.kind
	}
	return ""
}

// ErrorResult retains the kind before the Go error crosses a wire boundary.
func ErrorResult(err error) map[string]any {
	result := map[string]any{"error": err.Error()}
	if kind := ErrorKindOf(err); kind != "" {
		result["error_kind"] = string(kind)
	}
	if d, ok := LaunchDiagnosticOf(err); ok {
		result["launch_diagnostic"] = d
		result["error"] = "launch failed during " + d.Stage + ": " + d.Reason
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
			return &remoteError{message: message, cause: known.err, kind: kind}
		}
	}
	return &remoteError{message: message, kind: kind}
}

type remoteError struct {
	message string
	cause   error
	kind    ErrorKind
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
	err := DecodeError(ErrorKind(kind), message)
	return restoreLaunchDiagnostic(err, result)
}

func restoreLaunchDiagnostic(err error, result map[string]any) error {
	if raw, exists := result["launch_diagnostic"]; exists {
		// Decode only approved fields; arbitrary peer fields never reach the UI.
		wire, marshalErr := json.Marshal(raw)
		var d LaunchDiagnostic
		if marshalErr == nil && len(wire) <= 4096 && json.Unmarshal(wire, &d) == nil {
			return WithLaunchDiagnostic(err, d)
		}
	}
	return err
}

func FormatLaunchDiagnostic(d LaunchDiagnostic) string {
	d = d.Bounded()
	if d.Reason == "claude_missing" || d.Reason == "codex_missing" {
		return launchReason(d.Reason) + ". Run wt setup on the selected machine for install commands, then sign in to the provider there and retry."
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Claude launch failed during %s: %s. ", d.Stage, launchReason(d.Reason))
	switch d.Stage {
	case "connect/startup":
		out.WriteString("Check the selected wing with wt wing start --local-only; inspect its wing.log.")
	case "attach-after-admission":
		if d.Session != "" {
			separator := ""
			if strings.HasPrefix(d.Session, "-") {
				separator = "-- "
			}
			fmt.Fprintf(&out, "Session %s was admitted; reattach with wt attach %s%s.", d.Session, separator, d.Session)
		} else {
			out.WriteString("Inspect wt ls before starting another session.")
		}
	default:
		switch d.Reason {
		case "authentication_required":
			out.WriteString("Use the configured wing UI to authenticate; native MCP has no passkey ceremony.")
		case "client_required", "client_not_configured":
			out.WriteString("Choose an authorized WT_MCP_CLIENT from clients.yaml.")
		case "admission_unknown":
			out.WriteString("Inspect wt ls before starting another session.")
		case "executable_denied":
			out.WriteString("Choose a provider executable allowed by this policy.")
		case "provider_home_denied":
			out.WriteString("Choose a provider home writable under this policy.")
		case "provider_config_denied":
			out.WriteString("Review the provider config under this policy.")
		case "invalid_workspace", "workspace_denied":
			out.WriteString("Choose an existing workspace allowed by this policy.")
		case "session_name_in_use":
			out.WriteString("Choose another session name or attach to the existing session.")
		case "sandbox_unavailable":
			out.WriteString("Run wt doctor --fix to inspect and repair host sandbox capability.")
		default:
			out.WriteString("Review the selected policy and launch requirements.")
		}
	}
	fmt.Fprintf(&out, " Policy: %s", d.PolicyOrigin)
	if d.PolicyPath != "" {
		fmt.Fprintf(&out, " %q", d.PolicyPath)
	}
	if d.PolicySource != "" && d.PolicySource != d.PolicyPath {
		fmt.Fprintf(&out, "; denial from %q", d.PolicySource)
	}
	out.WriteString(".")
	if d.Stage == "admission/policy" || d.Stage == "provider startup" {
		out.WriteString(" Doctor launch-plan needs an explicit --allowed-home-root; supply the exact --home, --executable and policy/base inputs. No complete request was captured.")
	}
	return out.String()
}

func launchReason(reason string) string {
	switch reason {
	case "claude_missing":
		return "Claude executable (claude) was not found on PATH"
	case "codex_missing":
		return "Codex executable (codex) was not found on PATH"
	case "authentication_required":
		return "passkey authentication is required"
	case "client_required":
		return "clients.yaml requires an explicit MCP client"
	case "client_not_configured":
		return "MCP client is not configured in clients.yaml"
	case "connection_unavailable":
		return "selected wing connection unavailable"
	case "startup_failed":
		return "wing did not become ready"
	case "startup_exited":
		return "wing daemon exited before readiness"
	case "canceled":
		return "context canceled"
	case "admission_unknown":
		return "admission acknowledgement was lost"
	case "invalid_workspace":
		return "working directory is unavailable or outside allowed paths"
	case "session_name_in_use":
		return "session name is already in use"
	case "executable_denied":
		return "configured policy denies the provider executable"
	case "provider_home_denied":
		return "configured policy blocks required provider home writes"
	case "workspace_denied":
		return "configured policy blocks required workspace writes"
	case "provider_config_denied":
		return "provider config cannot be prepared under the selected boundary"
	case "network_denied":
		return "configured network policy blocks the provider"
	case "preparation_failed":
		return "launch preparation failed"
	case "sandbox_unavailable":
		return "native sandbox capability is unavailable"
	case "provider_failed":
		return "provider could not start under the selected boundary"
	case "attach_failed":
		return "attachment failed after admission"
	default:
		return "wing refused admission under its configured policy"
	}
}
