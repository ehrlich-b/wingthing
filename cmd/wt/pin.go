package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/spf13/cobra"
)

func pinCmd(pinned bool) *cobra.Command {
	verb, short := "pin", "Pin a session at the top of the fleet inventory"
	if !pinned {
		verb, short = "unpin", "Remove a session from the pinned inventory"
	}
	var jsonFlag bool
	cmd := &cobra.Command{Use: verb + " <session>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			name, ref, err := parseRemoteSession(args[0])
			if err != nil {
				return err
			}
			if name != "" {
				remote, err := remotepkg.ConfiguredRemote(dir, name)
				if err != nil {
					return err
				}
				remoteArgs := []string{verb}
				if jsonFlag {
					remoteArgs = append(remoteArgs, "--json")
				}
				remoteArgs = append(remoteArgs, "--", ref)
				return remotepkg.RunRemoteInvocation(cmd.Context(), remotepkg.ProfileInvocation(remote, remoteArgs), remotepkg.Streams(cmd.Context()))
			}
			wire, _ := json.Marshal(map[string]any{"session": ref, "pinned": pinned})
			clientName := os.Getenv("WT_MCP_CLIENT")
			result, err := localmcp.CallLocalWingTool(cmd.Context(), dir, clientName, "session_pin", wire)
			if standaloneAttachAvailable(err, clientName) {
				// Legacy standalone eggs have the same local owner contract as rename.
				cfg := &config.Config{Dir: dir}
				var session eggclient.LocalSession
				session, err = eggclient.ResolveLifecycleSession(cfg, ref)
				if err == nil {
					m, setErr := eggclient.SetSessionPinned(cfg, session.ID, pinned)
					err = setErr
					result = map[string]any{"session": session.ID, "pinned": m.Pinned, "role": m.Role}
				}
			}
			if err != nil {
				return err
			}
			if jsonFlag {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", result["session"], verb+"ned")
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

// Only fresh, reachable pinned coordinators participate. Duplicate pins require
// an explicit machine-qualified route; the selector never guesses.
func pinnedCoordinator(rows []eggclient.MachineSession) (eggclient.MachineSession, bool, error) {
	var matches []eggclient.MachineSession
	for _, r := range rows {
		if r.Error == "" && r.PID > 0 && r.Pinned && r.Role == "coordinator" {
			matches = append(matches, r)
		}
	}
	if len(matches) > 1 {
		refs := ""
		for _, r := range matches {
			ref := r.ID
			if r.Machine != "local" {
				ref = r.Machine + ":" + ref
			}
			refs += " " + ref
		}
		return eggclient.MachineSession{}, false, fmt.Errorf("multiple pinned coordinators; use wt attach with one of:%s", refs)
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	return eggclient.MachineSession{}, false, nil
}
