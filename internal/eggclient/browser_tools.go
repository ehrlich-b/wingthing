package eggclient

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/contextclient"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

// PrepareBrowserTools supplies the same tool listener and launch options to
// fresh browser PTYs and browser forks.
func PrepareBrowserTools(cfg *config.Config, sessionID string, tools []*config.ToolConfig, opts *SpawnEggOpts, identities ...EggIdentity) (*egg.ToolListener, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	if isolation := egg.InspectLegacyIsolation(filepath.Join(cfg.Dir, "eggs", sessionID), true); isolation != nil {
		log.Printf("pty session %s: tool capability unavailable: %s", sessionID, isolation.Warning())
		return nil, nil
	}
	contextCfg, err := config.LoadContextConfig(cfg.Dir)
	if err != nil {
		return nil, err
	}
	for _, tool := range tools {
		if tool.Context != "" && contextCfg == nil {
			return nil, fmt.Errorf("tool %s: context requires a wing context block", tool.Name)
		}
	}
	client, err := contextclient.New(contextCfg)
	if err != nil {
		return nil, err
	}
	owner, ownerID := "", ""
	if len(identities) > 0 {
		owner = identities[0].Email
		ownerID = identities[0].UserID
	}
	toolsDir := filepath.Join(cfg.Dir, "eggs", sessionID, ".tools")
	if err := os.MkdirAll(toolsDir, 0700); err != nil {
		return nil, fmt.Errorf("create tool directory: %w", err)
	}
	opts.ToolSocketPath = filepath.Join(toolsDir, "tool.sock")
	listener, err := egg.NewToolListener(opts.ToolSocketPath, tools, egg.ToolContext{Client: client, Owner: owner, OwnerID: ownerID})
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

// AttachBrowserController records only confirmed input claims, before any
// browser input is routed to the replacement stream.
func AttachBrowserController(ctx context.Context, client *egg.Client, sessionID string, options egg.AttachOptions, listener *egg.ToolListener, userID string) (pb.Egg_SessionClient, error) {
	stream, err := client.AttachSessionWithOptions(ctx, sessionID, options)
	if err == nil {
		listener.ObserveController(userID)
	}
	return stream, err
}

// Local CLI callers have OS authority but no relay-verified user ID. Authenticated
// host adapters supply their verified identity separately from writer labels.
func observeSessionController(cfg *config.Config, sessionID string, verifiedUserIDs []string) {
	if len(verifiedUserIDs) > 0 && verifiedUserIDs[0] != "" {
		egg.ObserveToolController(filepath.Join(cfg.Dir, "eggs", sessionID, ".tools", "tool.sock"), verifiedUserIDs[0])
	}
}
