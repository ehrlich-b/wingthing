package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

// A verified route must use wing-owned operations. Direct standalone egg/file
// operations cannot prove origin identity and must never be their fallback.
func bindVerifiedCLI(cmd *cobra.Command, expected string) error {
	if !cmd.Flags().Changed("expected-wing-id") {
		return nil
	}
	if expected == "" || strings.ContainsAny(expected, "\x00\r\n:") {
		return fmt.Errorf("invalid expected wing ID")
	}
	path := strings.TrimPrefix(cmd.CommandPath(), config.BinaryName()+" ")
	allowed := path == "attach" || path == "claude" || path == "terminal" || path == "sandbox" || path == "pin" || path == "unpin" || path == "session ps" || path == "session rename" || path == "session kill"
	if !allowed {
		return fmt.Errorf("this command has no wing-owned verified SSH adapter; use the pinned MCP route")
	}
	cmd.SetContext(controlsocket.WithExpectedWingID(cmd.Context(), expected))
	return nil
}

func verifiedInventory(cmd *cobra.Command, dir string) error {
	client, err := controlsocket.Dial(cmd.Context(), dir, controlsocket.Hello{Client: os.Getenv("WT_MCP_CLIENT")})
	if err != nil {
		return err
	}
	defer client.Close()
	result, failed, err := client.Call(cmd.Context(), "terminal_list", json.RawMessage(`{}`))
	if err != nil || failed {
		return fmt.Errorf("verified inventory refused: %v %v", err, result["error"])
	}
	wire, err := json.Marshal(result["sessions"])
	if err != nil {
		return err
	}
	var sessions []eggclient.LocalSession
	if err := json.Unmarshal(wire, &sessions); err != nil {
		return err
	}
	if sessions == nil {
		sessions = []eggclient.LocalSession{}
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(eggclient.RemoteSessionInventory{WingID: client.Welcome.WingID, Version: version, ContractVersion: eggclient.RemoteSessionContractVersion, Sessions: sessions})
}

func verifiedMutation(cmd *cobra.Command, dir, tool string, args map[string]any, jsonOutput bool) error {
	wire, err := json.Marshal(args)
	if err != nil {
		return err
	}
	result, err := localmcp.CallLocalWingTool(cmd.Context(), dir, os.Getenv("WT_MCP_CLIENT"), tool, wire)
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if tool == "terminal_rename" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "session %s named %s\n", args["session"], args["name"])
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "session %s stopped\n", args["session"])
	}
	return err
}

func verifiedStart(ctx context.Context, input io.Reader, output io.Writer, tool string, args map[string]any, detach, jsonOutput bool) error {
	dir, err := config.StateDir()
	if err != nil {
		return err
	}
	cwd, _ := args["cwd"].(string)
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return err
	}
	args["cwd"] = cwd
	if tool == "terminal_start" && len(args["command"].([]string)) == 0 {
		args["command"] = []string{}
	}
	if tool == "agent_start" && len(args["args"].([]string)) == 0 {
		args["args"] = []string{}
	}
	wire, err := json.Marshal(args)
	if err != nil {
		return err
	}
	clientName := os.Getenv("WT_MCP_CLIENT")
	result, err := localmcp.CallLocalWingTool(ctx, dir, clientName, tool, wire)
	if err != nil {
		return err
	}
	session, _ := result["session"].(string)
	if !ws.ValidSessionID(session) {
		return fmt.Errorf("wing admission did not return a session; do not relaunch automatically")
	}
	if detach {
		if jsonOutput {
			return json.NewEncoder(output).Encode(result)
		}
		_, err = fmt.Fprintf(output, "started %s\n", session)
		return err
	}
	_, err = localmcp.AttachWingIO(ctx, dir, clientName, session, input, output, egg.AttachOptions{})
	return err
}
