package localmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func (s *Server) ToolSessionFork(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if s.BoundConversation != "" {
		return nil, errors.New("fork is unavailable on conversation-bound MCP connections; a sibling may be outside their task tree")
	}
	result, err := eggclient.ForkSession(ctx, s.Cfg, args.Session, args.Name, eggclient.SessionForkScope{
		Principal: s.clientPrincipal(), Identity: s.identity, AllowedPaths: s.allowedPaths, EnforcePathBounds: s.enforcePathBounds,
		Admit: s.admitSpawn, Spawn: s.spawnFork,
		Prepare: func(plan *eggclient.SessionForkPlan) error {
			if plan.Conversation == nil {
				return nil
			}
			// The fork inherits the source's actual isolation policy. Bind its new
			// logical identity with the caller's existing grants and bounds, even
			// when that source was launched by an earlier unsandboxed connection.
			binding := &Server{Version: s.Version, Cfg: s.Cfg, Principal: s.clientPrincipal(), Actor: s.Actor, Surface: s.Surface,
				Unsandboxed: !egg.RequiresSandbox(plan.Config, plan.Source.Agent), Grants: s.Grants,
				MaxSessions: s.MaxSessions, MaxSpawnsPerHour: s.MaxSpawnsPerHour, identity: s.identity,
				allowedPaths: s.allowedPaths, enforcePathBounds: s.enforcePathBounds, tools: s.tools, hostMailboxUnavailable: s.hostMailboxUnavailable}
			args, managed, err := binding.prepareBoundParentLaunch(plan.Conversation, plan.Config, plan.Options.AgentArgs)
			if err != nil {
				return err
			}
			plan.Options.AgentArgs = args
			if managed != nil {
				plan.Options = managed.launchOpts(s.Cfg, plan.Options)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": result.Session, "source_session": result.SourceSession, "label": result.Label, "agent": result.Agent, "cwd": result.CWD,
		"conversation_id": result.ConversationID, "root_conversation_id": result.RootConversationID, "parent_conversation_id": result.ParentConversationID}, nil
}
