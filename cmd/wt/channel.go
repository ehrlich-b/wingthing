package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
)

// expected-channel is a transport identity guard. Check it before resolving
// state or SSH routing so a preview caller can never fall into a stable runtime.
func channelInvocationArgs(argv []string) ([]string, error) {
	if len(argv) > 0 && argv[0] == "tool-call" {
		return append([]string(nil), argv...), nil
	}
	args := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--" {
			args = append(args, argv[i:]...)
			break
		}
		value, inline := strings.CutPrefix(argv[i], "--expected-channel=")
		if argv[i] != "--expected-channel" && !inline {
			args = append(args, argv[i])
			continue
		}
		if !inline {
			i++
			if i == len(argv) {
				return nil, fmt.Errorf("--expected-channel requires a value")
			}
			value = argv[i]
		}
		if value != config.Channel() {
			return nil, fmt.Errorf("release channel mismatch: requested %q, executable is %q", value, config.Channel())
		}
	}
	return args, nil
}

func validatePreviewInvocation(args []string) error {
	if config.Channel() != "preview" {
		return nil
	}
	dir, err := config.StateDir()
	if err != nil {
		return err
	}
	if err := config.ValidateStateDirectory(dir); err != nil {
		return err
	}
	if _, err := config.LoadWingConfig(dir); err != nil {
		return err
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--org":
			return fmt.Errorf("preview is personal-only; organization enrollment is disabled")
		case "--roost", "--relay", "--addr", "--https-addr":
			if !inline {
				i++
				if i == len(args) {
					return fmt.Errorf("%s requires a value", name)
				}
				value = args[i]
			}
			if name == "--addr" || name == "--https-addr" {
				if err := config.ValidatePreviewListenAddr(value); err != nil {
					return err
				}
			} else if err := config.ValidatePreviewRelay(value); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"WT_BASE_URL", "WT_LOGIN_ADDR", "WT_ENTITLEMENT_URL"} {
		if err := config.ValidatePreviewRelay(os.Getenv(name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if authProvidersConfigured() {
		return fmt.Errorf("preview requires local personal mode; ambient OAuth or SMTP configuration is disabled")
	}
	// Roost mode would import OAuth/enrollment authority from ambient env.
	if os.Getenv("WT_ROOST_ALLOWED_EMAILS") != "" || os.Getenv("WT_ROOST_MODE") != "" {
		return fmt.Errorf("preview does not accept ambient organization enrollment settings")
	}
	return nil
}

func channelCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "channel", Short: "Show executable channel, isolated state, and update feed", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			info := map[string]string{"release_channel": config.Channel(), "executable": config.BinaryName(), "channel_label": config.ChannelLabel(), "version": version, "state_dir": dir, "update_feed": releaseMetadataURL()}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\nstate: %s\nupdates: %s\n", config.ChannelLabel(), version, dir, releaseMetadataURL())
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print structured channel information")
	return cmd
}

func defaultHTTPSAddr() string {
	if config.Channel() == "preview" {
		return "127.0.0.1:8181"
	}
	return defaultLocalHTTPSAddr
}

func previewEggProcessMatches(pid int, sessionID string) bool {
	argv, err := processArgv(pid)
	if err != nil || len(argv) < 5 {
		return false
	}
	exe, err := os.Executable()
	if err != nil || canonicalPolicyPath(argv[0]) != canonicalPolicyPath(exe) || argv[1] != "egg" || argv[2] != "run" {
		return false
	}
	for i, arg := range argv {
		if arg == "--session-id" && i+1 < len(argv) && argv[i+1] == sessionID {
			return true
		}
	}
	return false
}
