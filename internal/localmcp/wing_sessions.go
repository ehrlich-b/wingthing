package localmcp

import (
	"context"
	"errors"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func (s *Server) sessionAuthority() wingsession.Authority {
	return wingsession.Authority{Principal: s.clientPrincipal(), UserID: s.identity.UserID, Email: s.identity.Email, Role: s.sessionRole, Browser: s.sessionBrowser, PublicKey: s.sessionPublicKey, AuthToken: s.sessionAuthToken, AllowedPaths: s.allowedPaths, EnforcePaths: s.enforcePathBounds, SealedFS: s.identity.SealedFS, Unsandboxed: s.Unsandboxed, LegacyLocalDefault: s.legacyLocalDefault}
}

func (s *Server) startSession(id, agent, cwd string, cfg *egg.EggConfig, opts eggclient.SpawnEggOpts) (*egg.Client, error) {
	if s.Sessions == nil {
		return nil, errors.New("wing session service is not ready")
	}
	launch := s.sessionLaunch
	if launch == nil || launch.CWD != cwd {
		return nil, errors.New("session launch policy was not prepared")
	}
	launch.Config = cfg
	return s.Sessions.Start(context.Background(), launch, wingsession.StartOptions{SessionID: id, Agent: agent, Egg: opts, Tools: s.forkTools, Trace: s.forkTrace && cfg.Trace, IdleTimeout: s.forkIdleTimeout})
}

func (s *Server) loadSessionLaunchConfig(cwd string) (*egg.EggConfig, error) {
	if s.Sessions == nil {
		return nil, errors.New("wing session service is not ready")
	}
	launch, err := s.Sessions.PrepareLaunch(s.sessionAuthority(), cwd)
	if err != nil {
		return nil, err
	}
	s.sessionLaunch = launch
	if s.broker != nil {
		captured, err := s.broker.childEggConfig(cwd)
		if err != nil {
			return nil, err
		}
		launch.Config = captured
	}
	return launch.Config, nil
}
