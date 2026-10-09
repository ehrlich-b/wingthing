// Package wingsession owns the session operations shared by wing transports.
package wingsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Authority is supplied by an authenticated adapter, never by tool arguments.
type Authority struct {
	Principal            string
	UserID, Email, Role  string
	Browser              bool
	PublicKey, AuthToken string
	AllowedPaths         []string
	EnforcePaths         bool
	SealedFS             bool
}

type Policy struct {
	Wing  *config.WingConfig
	Egg   *egg.EggConfig
	Keys  []config.AllowKey
	Audit bool
}

// Service belongs to one wing. Local stdio does not instantiate this runtime.
type Service struct {
	Config     *config.Config
	Home       string
	SharedHost bool
	Policy     func() Policy
	AuthCache  *auth.AuthCache
	Inventory  func(*config.Config) []ws.SessionInfo
	// Spawn is replaceable by isolated protocol fixtures; production uses SpawnEgg.
	Spawn func(*Launch, StartOptions) (*egg.Client, error)
}

type Launch struct {
	Config    *egg.EggConfig
	CWD       string
	Identity  eggclient.EggIdentity
	authority Authority
	service   *Service
}

type StartOptions struct {
	SessionID, Agent  string
	Rows, Cols        uint32
	Debug, VTE, Trace bool
	IdleTimeout       time.Duration
	Egg               eggclient.SpawnEggOpts
}

func (s *Service) PrepareLaunch(a Authority, cwd string) (*Launch, error) {
	p := s.Policy()
	if p.Wing == nil || p.Egg == nil {
		return nil, errors.New("wing runtime policy is not ready")
	}
	if a.UserID == "" {
		return nil, errors.New("authenticated user identity is required")
	}
	protected := len(wingpolicy.PasskeysForSubject(p.Keys, a.UserID)) > 0
	if !a.Browser && (p.Wing.Locked || protected) {
		return nil, errors.New("passkey authentication is required; MCP passkey ceremony is unavailable")
	}
	if a.Browser && (p.Wing.Locked || protected) {
		ttl, _ := time.ParseDuration(p.Wing.AuthTTL)
		subject := wingpolicy.PasskeySubject(a.UserID, a.PublicKey)
		if !protected || s.AuthCache == nil {
			return nil, errors.New("not allowed by wing")
		}
		if _, ok := s.AuthCache.Check(a.AuthToken, ttl, subject); !ok {
			return nil, errors.New("passkey authentication is required")
		}
	}
	start := ws.PTYStart{CWD: cwd, UserID: a.UserID, Email: a.Email, OrgRole: a.Role}
	cfg, identity, err := eggclient.PrepareBrowserLaunch(p.Wing, &start, s.Home, s.SharedHost, p.Egg)
	if err != nil {
		return nil, err
	}
	// Remote MCP's sealed boundary and path bounds may be stricter than the web default.
	identity.SealedFS = identity.SealedFS || a.SealedFS
	identity.SharedHost = identity.SharedHost || a.SealedFS
	if a.EnforcePaths && (len(a.AllowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(start.CWD), a.AllowedPaths)) {
		return nil, errors.New("working directory is outside this user's wing paths")
	}
	if !a.Browser && cwd != "" && wingpolicy.CanonicalSessionPath(start.CWD) != wingpolicy.CanonicalSessionPath(cwd) {
		return nil, errors.New("working directory is outside current launch paths")
	}
	copyCfg := *cfg
	copyCfg.Audit = copyCfg.Audit || p.Audit
	return &Launch{Config: &copyCfg, CWD: start.CWD, Identity: identity, authority: a, service: s}, nil
}

func (s *Service) Start(ctx context.Context, launch *Launch, opts StartOptions) (*egg.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if launch == nil || launch.service != s {
		return nil, errors.New("launch was not prepared by this wing")
	}
	if err := eggclient.ValidateSessionID(opts.SessionID); err != nil {
		return nil, err
	}
	if launch.authority.Principal != "" {
		opts.Egg.Principal = launch.authority.Principal
	}
	if opts.Rows == 0 {
		opts.Rows = 24
	}
	if opts.Cols == 0 {
		opts.Cols = 80
	}
	if s.Spawn != nil {
		return s.Spawn(launch, opts)
	}
	return eggclient.SpawnEgg(s.Config, opts.SessionID, opts.Agent, launch.Config, opts.Rows, opts.Cols, launch.CWD, opts.Debug, opts.VTE, opts.Trace, launch.Identity, opts.IdleTimeout, opts.Egg)
}

func (s *Service) Owns(a Authority, session eggclient.LocalSession) bool {
	dir := filepath.Join(s.Config.Dir, "eggs", session.ID)
	if a.UserID == "" || eggclient.ReadEggOwner(dir) != a.UserID {
		return false
	}
	if a.EnforcePaths && (len(a.AllowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(session.CWD), a.AllowedPaths)) {
		return false
	}
	if a.Browser {
		return true
	}
	// Web-created sessions explicitly belong to the authenticated user's remote
	// MCP principal. Other logical principals remain separate within that user.
	return session.Principal == a.Principal || a.Principal == UserPrincipal(a.UserID) && (session.Principal == "" || session.Principal == "default")
}

func (s *Service) Resolve(ctx context.Context, a Authority, ref string, archived bool) (eggclient.LocalSession, error) {
	owns := func(session eggclient.LocalSession) bool { return s.Owns(a, session) }
	if archived {
		return eggclient.ResolveOwnedLifecycleSession(s.Config, ref, owns)
	}
	return eggclient.ResolveOwnedActiveSession(ctx, s.Config, ref, owns)
}

func (s *Service) List(ctx context.Context, a Authority) ([]eggclient.LocalSession, error) {
	sessions, err := eggclient.DiscoverActiveSessions(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	out := make([]eggclient.LocalSession, 0, len(sessions))
	for _, session := range sessions {
		if s.Owns(a, session) {
			out = append(out, session)
		}
	}
	return out, nil
}

func (s *Service) ListWeb(ctx context.Context, a Authority) ([]ws.SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []ws.SessionInfo{}
	for _, session := range s.Inventory(s.Config) {
		req := ws.TunnelRequest{SenderUserID: a.UserID, SenderOrgRole: a.Role}
		if wingpolicy.IsMemberFiltered(req) && (!wingpolicy.CanSeeSession(req, session.UserID) || !wingpolicy.CanAccessSessionPath(req, session.CWD, a.AllowedPaths)) {
			continue
		}
		session.Forkable, session.ForkUnavailableReason = eggclient.SessionForkStatus(s.Config, filepath.Join(s.Config.Dir, "eggs", session.SessionID), session.Agent, session.CWD)
		out = append(out, session)
	}
	return out, nil
}

func (s *Service) Stop(ctx context.Context, a Authority, ref string) (eggclient.LocalSession, error) {
	session, err := s.Resolve(ctx, a, ref, false)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	_, client, err := eggclient.OpenLocalEgg(ctx, s.Config, session.ID)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	defer client.Close()
	return session, client.Kill(ctx, session.ID)
}

// AuthorityForWeb preserves authenticated roles and the wing's path ACLs.
func AuthorityForWeb(wc *config.WingConfig, req ws.TunnelRequest, home string) Authority {
	role := req.SenderOrgRole
	if wc.IsAdmin(req.SenderEmail) && wingpolicy.IsMemberRole(role) {
		role = "admin"
	}
	paths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wc.Paths, req.SenderEmail, role, home))
	return Authority{UserID: req.SenderUserID, Email: req.SenderEmail, Role: role, Browser: true, AllowedPaths: paths, EnforcePaths: wingpolicy.IsMemberRole(role)}
}

func UserPrincipal(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return "user-" + hex.EncodeToString(digest[:10])
}
