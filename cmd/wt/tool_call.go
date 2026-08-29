package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/spf13/cobra"
)

func toolCallCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "tool-call [tool] [args...]",
		Short:  "Call a privileged tool via the wing daemon",
		Hidden: true, // called by generated shims, not directly by users
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sockPath := os.Getenv("WT_TOOL_SOCKET")
			if sockPath == "" {
				return exitError(126, "WT_TOOL_SOCKET not set — tool-call must be run inside an egg session with tools configured")
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return exitError(126, "connect to tool socket: %v", err)
			}
			defer closeWithLog("tool socket", conn)
			req := egg.ToolRequest{Tool: args[0], Args: args[1:]}
			data, err := json.Marshal(req)
			if err != nil {
				return exitError(127, "encode tool request: %v", err)
			}
			if _, err := conn.Write(data); err != nil {
				return exitError(127, "write tool request: %v", err)
			}
			if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
				return exitError(127, "finish tool request: %v", err)
			}
			respData, err := io.ReadAll(conn)
			if err != nil {
				return exitError(127, "read tool response: %v", err)
			}
			var resp egg.ToolResponse
			if err := json.Unmarshal(respData, &resp); err != nil {
				return exitError(127, "parse tool response: %v", err)
			}
			if resp.Error != "" {
				return exitError(1, "%s", resp.Error)
			}
			if resp.Stdout != "" {
				if err := writef(os.Stdout, "%s", resp.Stdout); err != nil {
					return exitError(127, "write tool stdout: %v", err)
				}
			}
			if resp.Stderr != "" {
				if err := writef(os.Stderr, "%s", resp.Stderr); err != nil {
					return exitError(127, "write tool stderr: %v", err)
				}
			}
			if resp.ExitCode != 0 {
				return exitError(resp.ExitCode, "")
			}
			return nil
		},
	}
}

func toolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tool-list",
		Short: "List available privileged tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			sockPath := os.Getenv("WT_TOOL_SOCKET")
			if sockPath == "" {
				return exitError(126, "WT_TOOL_SOCKET not set — tool-list must be run inside an egg session with tools configured")
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return exitError(126, "connect to tool socket: %v", err)
			}
			defer closeWithLog("tool socket", conn)
			req := egg.ToolRequest{Action: "list"}
			data, err := json.Marshal(req)
			if err != nil {
				return exitError(127, "encode tool list request: %v", err)
			}
			if _, err := conn.Write(data); err != nil {
				return exitError(127, "write tool list request: %v", err)
			}
			if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
				return exitError(127, "finish tool list request: %v", err)
			}
			respData, err := io.ReadAll(conn)
			if err != nil {
				return exitError(127, "read tool response: %v", err)
			}
			var listResp egg.ToolListResponse
			if err := json.Unmarshal(respData, &listResp); err != nil {
				return exitError(127, "parse tool response: %v", err)
			}
			for _, t := range listResp.Tools {
				if t.Description != "" {
					fmt.Printf("%-20s %s\n", t.Name, t.Description)
				} else {
					fmt.Println(t.Name)
				}
			}
			return nil
		},
	}
}
