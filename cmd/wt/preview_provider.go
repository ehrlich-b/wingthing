package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/spf13/cobra"
)

const previewProviderOutputLimit = 64 * 1024

const previewProviderObservation = "Vendor-reported status for this profile; account ownership and subscription entitlement are unverified. A sandbox or Keychain access failure can report no login."

type previewProviderProfile struct {
	Provider        string `json:"provider"`
	ReleaseChannel  string `json:"release_channel"`
	StateDir        string `json:"state_dir"`
	Home            string `json:"data_home"`
	HomeBinding     string `json:"data_home_binding,omitempty"`
	OSHome          string `json:"os_home"`
	ConfigDirectory string `json:"config_directory"`
	Executable      string `json:"executable,omitempty"`
}

type previewProviderAccount struct {
	Email            string `json:"email,omitempty"`
	OrgID            string `json:"org_id,omitempty"`
	OrgName          string `json:"org_name,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
}

type previewProviderStatus struct {
	previewProviderProfile
	State       string                  `json:"state"`
	LoggedIn    *bool                   `json:"reported_logged_in,omitempty"`
	AuthMethod  string                  `json:"auth_method,omitempty"`
	APIProvider string                  `json:"api_provider,omitempty"`
	Account     *previewProviderAccount `json:"reported_account,omitempty"`
	Diagnostic  string                  `json:"diagnostic"`
	Observation string                  `json:"observation"`
	SetupGuide  string                  `json:"setup_guide"`
}

type previewProviderGuide struct {
	previewProviderProfile
	PrepareCommands []string `json:"prepare_commands"`
	LoginCommand    string   `json:"login_command"`
	StatusCommand   string   `json:"status_command"`
	HumanSteps      []string `json:"human_steps"`
	Observation     string   `json:"observation"`
	MinimumCLI      string   `json:"minimum_status_cli_version"`
}

// This is an explicitly invoked diagnostic. Agent launches and startup never
// call vendor authentication commands or use their observational result as a gate.
func previewProviderCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "provider", Short: "Inspect or plan setup of an isolated personal preview provider"}
	var statusJSON, guideJSON bool
	var timeout time.Duration
	status := &cobra.Command{
		Use: "status claude", Short: "Ask Claude for metadata in the selected preview profile", Args: previewClaudeArgument,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 || timeout > 30*time.Second {
				return errors.New("provider status timeout must be greater than zero and at most 30s")
			}
			profile, err := resolvePreviewProviderProfile()
			if err != nil {
				return err
			}
			result := inspectPreviewClaude(cmd.Context(), profile, timeout)
			if statusJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Claude preview: %s\ndata home: %s\nOS HOME: %s\nconfiguration: %s\n%s\n", result.State, result.Home, result.OSHome, result.ConfigDirectory, result.Diagnostic); err != nil {
				return err
			}
			if result.Account != nil {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "reported account: %s; organization: %s; subscription: %s\n", result.Account.Email, result.Account.OrgName, result.Account.SubscriptionType); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\nsetup: %s\n", result.Observation, result.SetupGuide)
			return err
		},
	}
	status.Flags().BoolVar(&statusJSON, "json", false, "print allowlisted profile and account metadata")
	status.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "bound the explicit vendor status check (maximum 30s)")
	guide := &cobra.Command{
		Use: "setup-guide claude", Short: "Print manual personal login steps without running login", Args: previewClaudeArgument,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := resolvePreviewProviderProfile()
			if err != nil {
				return err
			}
			result, err := previewClaudeSetupGuide(profile)
			if err != nil {
				return err
			}
			if guideJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Claude personal preview profile: %s\ndata home: %s\nOS HOME: %s\n", result.ConfigDirectory, result.Home, result.OSHome); err != nil {
				return err
			}
			for _, step := range result.HumanSteps {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), step); err != nil {
					return err
				}
			}
			for _, command := range result.PrepareCommands {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), command); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n%s\n", result.LoginCommand, result.StatusCommand, result.Observation)
			return err
		},
	}
	guide.Flags().BoolVar(&guideJSON, "json", false, "print structured manual setup commands")
	cmd.AddCommand(status, guide, previewProviderLoginCmd())
	return cmd
}

func previewClaudeArgument(cmd *cobra.Command, args []string) error {
	if len(args) != 1 || args[0] != "claude" {
		return errors.New("this preview diagnostic supports exactly one provider: claude")
	}
	return nil
}

func resolvePreviewProviderProfile() (previewProviderProfile, error) {
	if config.Channel() != "preview" {
		return previewProviderProfile{}, errors.New("isolated provider diagnostics require wt-preview")
	}
	dir, err := config.StateDir()
	if err != nil {
		return previewProviderProfile{}, err
	}
	if err := config.ValidateStateDirectory(dir); err != nil {
		return previewProviderProfile{}, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return previewProviderProfile{}, err
	}
	dir = canonicalPolicyPath(dir)
	home, bound, err := config.ResolvePreviewProviderHome(dir)
	if err != nil {
		return previewProviderProfile{}, err
	}
	osHome, configDir := home, filepath.Join(home, ".claude")
	if runtime.GOOS == "darwin" {
		osHome, configDir, err = egg.PreviewClaudeOSContext(home)
		if err != nil {
			return previewProviderProfile{}, err
		}
	}
	profile := previewProviderProfile{Provider: "claude", ReleaseChannel: "preview", StateDir: dir, Home: home, OSHome: osHome, ConfigDirectory: configDir}
	if bound {
		profile.HomeBinding = filepath.Join(dir, config.ProviderHomeBinding)
	}
	if executable, err := exec.LookPath("claude"); err == nil {
		absolute, err := filepath.Abs(executable)
		if err == nil {
			profile.Executable = absolute
		}
	}
	return profile, nil
}

// Start with a small environment rather than removing a changing list of API,
// OAuth, gateway, proxy, secure-storage, or provider configuration variables.
func previewClaudeStatusEnv(profile previewProviderProfile) []string {
	env := make([]string, 0, 10)
	for _, name := range []string{"PATH", "LANG", "TERM", "USER", "TMPDIR"} {
		// Match the clean Mac login context. Claude uses USER as a Keychain
		// account selector; inheriting it can hide this profile's saved login.
		if name == "USER" && runtime.GOOS == "darwin" {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, "HOME="+profile.OSHome, "CLAUDE_CONFIG_DIR="+profile.ConfigDirectory,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1")
}

// Both streams are capped independently. Overflow cancels the subprocess;
// raw stderr and unrecognized JSON fields are never published in diagnostics.
type previewProviderOutput struct {
	Data     bytes.Buffer
	Overflow bool
	Cancel   context.CancelFunc
}

func (b *previewProviderOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := previewProviderOutputLimit - b.Data.Len()
	if n > remaining {
		_, _ = b.Data.Write(p[:remaining])
		b.Overflow = true
		b.Cancel()
		return n, nil
	}
	return b.Data.Write(p)
}

func inspectPreviewClaude(ctx context.Context, profile previewProviderProfile, timeout time.Duration) previewProviderStatus {
	result := previewProviderStatus{previewProviderProfile: profile, State: "unknown", Observation: previewProviderObservation,
		SetupGuide: "wt-preview provider setup-guide claude --json"}
	if env, executable, err := previewProviderScope(profile); err == nil {
		result.SetupGuide = previewProviderShellCommand(env, executable, "provider", "setup-guide", "claude", "--json")
	}
	info, err := os.Lstat(profile.Home)
	if errors.Is(err, os.ErrNotExist) {
		result.State = "profile_not_initialized"
		result.Diagnostic = "The isolated preview provider home does not exist. Use the setup guide to prepare this exact profile and sign in manually."
		return result
	}
	if err != nil || !info.IsDir() {
		result.Diagnostic = "The isolated provider home must be an existing real directory; no vendor status check was run."
		return result
	}
	if profile.Executable == "" {
		result.State = "not_installed"
		result.Diagnostic = "Claude was not found on PATH. Install its supported vendor CLI deliberately, then use the setup guide."
		return result
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &previewProviderOutput{Cancel: cancel}, &previewProviderOutput{Cancel: cancel}
	command := exec.CommandContext(checkCtx, profile.Executable, "auth", "status", "--json")
	command.Env = previewClaudeStatusEnv(profile)
	command.Dir = profile.Home
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = 250 * time.Millisecond
	runErr := command.Run()
	if stdout.Overflow || stderr.Overflow {
		result.Diagnostic = "Claude status exceeded the bounded output limit; no account metadata was accepted."
		return result
	}
	if checkCtx.Err() != nil {
		result.Diagnostic = "Claude status timed out or was canceled; authentication is unknown."
		return result
	}
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			result.Diagnostic = "Claude status could not execute or finish cleanly; authentication is unknown."
			return result
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != 0 && exitCode != 1 {
		result.Diagnostic = "Claude status returned an unsupported exit code; authentication is unknown."
		return result
	}
	var report struct {
		LoggedIn         *bool   `json:"loggedIn"`
		AuthMethod       string  `json:"authMethod"`
		APIProvider      string  `json:"apiProvider"`
		ConfigDirectory  string  `json:"configDirectory"`
		Email            *string `json:"email"`
		OrgID            *string `json:"orgId"`
		OrgName          *string `json:"orgName"`
		SubscriptionType *string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(stdout.Data.Bytes(), &report); err != nil || report.LoggedIn == nil ||
		!filepath.IsAbs(report.ConfigDirectory) || canonicalPolicyPath(report.ConfigDirectory) != canonicalPolicyPath(profile.ConfigDirectory) {
		result.Diagnostic = "Claude status did not return valid metadata for the selected configuration directory. Use Claude Code 2.1.268 or later; authentication is unknown."
		return result
	}
	switch report.AuthMethod {
	case "none", "claude.ai", "oauth_token", "api_key", "api_key_helper", "third_party":
	default:
		result.Diagnostic = "Claude status returned an unknown authentication method; authentication is unknown."
		return result
	}
	if (*report.LoggedIn && (exitCode != 0 || report.AuthMethod == "none")) || (!*report.LoggedIn && exitCode != 1) {
		result.Diagnostic = "Claude status metadata and exit code disagree; authentication is unknown."
		return result
	}
	result.LoggedIn, result.AuthMethod, result.APIProvider = report.LoggedIn, report.AuthMethod, previewProviderMetadata(&report.APIProvider)
	if *report.LoggedIn {
		result.State = "reported_authenticated"
		result.Account = &previewProviderAccount{Email: previewProviderMetadata(report.Email), OrgID: previewProviderMetadata(report.OrgID),
			OrgName: previewProviderMetadata(report.OrgName), SubscriptionType: previewProviderMetadata(report.SubscriptionType)}
		result.Diagnostic = "Claude reports authentication in this preview profile. Confirm the reported personal account before any model invocation; this check does not verify token freshness or entitlement."
	} else {
		result.State = "reported_not_logged_in"
		result.Diagnostic = "Claude reports no login in this preview profile. Use the manual setup guide; a Keychain or sandbox access failure can give the same report."
	}
	return result
}

func previewProviderMetadata(value *string) string {
	if value == nil || len(*value) > 512 || strings.IndexFunc(*value, unicode.IsControl) >= 0 {
		return ""
	}
	return *value
}

func previewClaudeSetupGuide(profile previewProviderProfile) (previewProviderGuide, error) {
	initEnv, executable, err := previewProviderScope(profile)
	if err != nil {
		return previewProviderGuide{}, err
	}
	statusCommand := previewProviderShellCommand(initEnv, executable, "provider", "status", "claude", "--json")
	loginCommand := previewProviderShellCommand(initEnv, executable, "provider", "login", "claude")
	prepare := []string{previewProviderShellCommand(initEnv, executable, "init")}
	// A bound data home already exists by contract; never suggest changing it.
	if profile.HomeBinding == "" {
		prepare = append(prepare, "mkdir -p -m 700 "+shellQuote(profile.Home))
	}
	return previewProviderGuide{previewProviderProfile: profile,
		PrepareCommands: prepare,
		LoginCommand:    loginCommand, StatusCommand: statusCommand, Observation: previewProviderObservation, MinimumCLI: "2.1.268",
		HumanSteps: []string{
			"This is a command guide. Wingthing has not initialized the profile, run login, or opened a browser.",
			"Install a supported Claude CLI if absent. Prepare this exact preview state and provider home with the commands below.",
			"Run the login command directly in a private user terminal, never through a captured agent tool or Wingthing egg. It invokes only the vendor's auth login --claudeai; macOS uses the same canonical config namespace and OS context as the matching preview runtime.",
			"Choose a time for the human browser sign-in. Select and confirm the intended personal Claude subscription account before completing OAuth; stop if it shows an unintended organization account.",
			"Rerun the scoped status command and inspect its reported email, organization, and subscription. Resolve ownership or unknown status before invoking a model. Do not copy host credentials or settings into this profile.",
		}}, nil
}

func previewProviderScope(profile previewProviderProfile) ([]string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	env := []string{"HOME=" + hostHome, "PATH=" + os.Getenv("PATH"), "WINGTHING_DIR=" + profile.StateDir, "WINGTHING_PREVIEW_DIR=" + profile.StateDir}
	return env, executable, nil
}

func previewProviderShellCommand(env []string, executable string, args ...string) string {
	entries := append([]string(nil), env...)
	sort.Strings(entries)
	words := []string{"env", "-i"}
	for _, entry := range entries {
		words = append(words, shellQuote(entry))
	}
	words = append(words, shellQuote(executable))
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	return strings.Join(words, " ")
}
