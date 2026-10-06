package eggclient

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

// PrepareBrowserTools supplies the same tool listener and launch options to
// fresh browser PTYs and browser forks.
func PrepareBrowserTools(cfg *config.Config, sessionID string, tools []*config.ToolConfig, opts *SpawnEggOpts) (*egg.ToolListener, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	if err := egg.RequireLegacySecretProtection(filepath.Join(cfg.Dir, "eggs", sessionID), true); err != nil {
		log.Printf("pty session %s: tool capability unavailable: %v", sessionID, err)
		return nil, nil
	}
	toolsDir := filepath.Join(cfg.Dir, "eggs", sessionID, ".tools")
	if err := os.MkdirAll(toolsDir, 0700); err != nil {
		return nil, fmt.Errorf("create tool directory: %w", err)
	}
	opts.ToolSocketPath = filepath.Join(toolsDir, "tool.sock")
	listener, err := egg.NewToolListener(opts.ToolSocketPath, tools)
	if err != nil {
		log.Printf("pty session %s: tool listener failed: %v", sessionID, err)
		return nil, nil
	}
	opts.ToolNames = config.ToolNames(tools)
	log.Printf("pty session %s: tool listener started (%d tools)", sessionID, len(opts.ToolNames))
	return listener, nil
}

// Forks return before the PTY exits. Keep their tools alive on a read-only
// stream independent of the launch request, then release both resources.
func serveBrowserSessionTools(client *egg.Client, listener *egg.ToolListener, sessionID string) {
	defer cmdutil.CloseWithLog("forked session tool listener", listener)
	defer cmdutil.CloseWithLog("forked session tool client", client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.AttachSessionWithOptions(ctx, sessionID, egg.AttachOptions{ReadOnly: true, Owner: "wing:tools"})
	if err != nil {
		log.Printf("pty session %s: watch fork tool lifetime: %v", sessionID, err)
		return
	}
	for {
		message, err := stream.Recv()
		if err != nil {
			return
		}
		if _, exited := message.Payload.(*pb.SessionMsg_ExitCode); exited {
			return
		}
	}
}
