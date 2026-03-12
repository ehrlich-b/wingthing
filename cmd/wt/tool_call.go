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
				fmt.Fprintln(os.Stderr, "WT_TOOL_SOCKET not set — tool-call must be run inside an egg session with tools configured")
				os.Exit(126)
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "connect to tool socket: %v\n", err)
				os.Exit(126)
			}
			defer conn.Close()
			req := egg.ToolRequest{Tool: args[0], Args: args[1:]}
			data, _ := json.Marshal(req)
			conn.Write(data)
			conn.(*net.UnixConn).CloseWrite()
			respData, err := io.ReadAll(conn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read tool response: %v\n", err)
				os.Exit(127)
			}
			var resp egg.ToolResponse
			if err := json.Unmarshal(respData, &resp); err != nil {
				fmt.Fprintf(os.Stderr, "parse tool response: %v\n", err)
				os.Exit(127)
			}
			if resp.Error != "" {
				fmt.Fprintln(os.Stderr, resp.Error)
				os.Exit(1)
			}
			if resp.Stdout != "" {
				fmt.Fprint(os.Stdout, resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Fprint(os.Stderr, resp.Stderr)
			}
			os.Exit(resp.ExitCode)
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
				fmt.Fprintln(os.Stderr, "WT_TOOL_SOCKET not set — tool-list must be run inside an egg session with tools configured")
				os.Exit(126)
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "connect to tool socket: %v\n", err)
				os.Exit(126)
			}
			defer conn.Close()
			req := egg.ToolRequest{Action: "list"}
			data, _ := json.Marshal(req)
			conn.Write(data)
			conn.(*net.UnixConn).CloseWrite()
			respData, err := io.ReadAll(conn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read tool response: %v\n", err)
				os.Exit(127)
			}
			var listResp egg.ToolListResponse
			if err := json.Unmarshal(respData, &listResp); err != nil {
				fmt.Fprintf(os.Stderr, "parse tool response: %v\n", err)
				os.Exit(127)
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
