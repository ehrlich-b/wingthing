// Package wingsession owns the session operations shared by wing transports.
package wingsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Authority is supplied by an authenticated adapter, never by tool arguments.
type Authority struct {
	Principal            string
	UserID, Email, Role  string
	DisplayName          string
	Browser              bool
	PublicKey, AuthToken string
	AllowedPaths         []string
	EnforcePaths         bool
	LegacyLocalDefault   bool
	Unsandboxed          bool
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
	// Register installs wing input routing before Start acknowledges the egg.
	Register   func(string) error
	tools      sync.Map // session ID -> wing-owned tool listener
	runMu      sync.Mutex
	RunManager *Runs
	RunBackend *RunBackend
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
	Tools             []*config.ToolConfig
}

func (s *Service) PrepareLaunch(a Authority, cwd string) (*Launch, error) {
	return s.prepareLaunch(s.Policy(), a, cwd)
}

func (s *Service) prepareLaunch(p Policy, a Authority, cwd string) (*Launch, error) {
	if p.Wing == nil || p.Egg == nil {
		return nil, errors.New("wing runtime policy is not ready")
	}
	if a.UserID == "" {
		return nil, errors.New("authenticated user identity is required")
	}
	if a.Unsandboxed && (!p.Wing.AllowUnsandboxed || s.SharedHost || p.Wing.Org != "" || a.SealedFS) {
		return nil, errors.New("unsandboxed launch requires allow_unsandboxed: true in a personal wing.yaml")
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
	start := ws.PTYStart{CWD: cwd, UserID: a.UserID, Email: a.Email, OrgRole: a.Role, DisplayName: a.DisplayName}
	prepare := eggclient.PrepareMCPLaunch
	if a.Browser {
		prepare = eggclient.PrepareBrowserLaunch
	}
	cfg, identity, err := prepare(p.Wing, &start, s.Home, s.SharedHost || a.SealedFS, p.Egg)
	if err != nil {
		return nil, err
	}
	// Remote MCP's sealed boundary and path bounds may be stricter than the web default.
	identity.SealedFS = identity.SealedFS || a.SealedFS
	identity.SharedHost = identity.SharedHost || a.SealedFS
	if a.EnforcePaths && (len(a.AllowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(start.CWD), a.AllowedPaths)) {
		return nil, errors.New("working directory is outside this user's wing paths")
	}
	if a.Unsandboxed {
		cfg = egg.UnsandboxedEggConfig()
	}
	copyCfg := *cfg
	copyCfg.Audit = copyCfg.Audit || p.Audit
	return &Launch{Config: &copyCfg, CWD: start.CWD, Identity: identity, authority: a, service: s}, nil
}

// PrepareWebStart also owns resume admission and provider reservation. It uses
// one policy snapshot for the launch and the source's owner/path checks.
func (s *Service) PrepareWebStart(start *ws.PTYStart) (*Launch, string, func(bool), error) {
	p := s.Policy()
	if p.Wing == nil {
		return nil, "", nil, errors.New("wing runtime policy is not ready")
	}
	if p.Wing.IsAdmin(start.Email) && wingpolicy.IsMemberRole(start.OrgRole) {
		start.OrgRole = "admin"
	}
	a := Authority{UserID: start.UserID, Email: start.Email, Role: start.OrgRole, DisplayName: start.DisplayName, Browser: true, PublicKey: start.PublicKey, AuthToken: start.AuthToken}
	launch, err := s.prepareLaunch(p, a, start.CWD)
	if err != nil {
		return nil, "", nil, err
	}
	start.CWD = launch.CWD
	if start.ResumeSessionID == "" {
		return launch, "", nil, nil
	}
	paths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(p.Wing.Paths, start.Email, start.OrgRole, s.Home))
	provider, cwd, release, err := eggclient.PrepareBrowserResume(s.Config, p.Wing, *start, paths, s.SharedHost)
	if err != nil {
		return nil, "", nil, err
	}
	start.CWD = cwd
	launch.CWD = cwd
	return launch, provider, release, nil
}

func (s *Service) Start(ctx context.Context, launch *Launch, opts StartOptions) (*egg.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Register == nil {
		return nil, errors.New("wing session registration is not ready")
	}
	if launch == nil || launch.service != s {
		return nil, errors.New("launch was not prepared by this wing")
	}
	if err := eggclient.ValidateSessionID(opts.SessionID); err != nil {
		return nil, err
	}
	if launch.authority.Principal != "" {
		opts.Egg.Principal = launch.authority.Principal
	} else if opts.Egg.Principal == "" {
		opts.Egg.Principal = UserPrincipal(launch.authority.UserID)
	}
	if opts.Rows == 0 {
		opts.Rows = 24
	}
	if opts.Cols == 0 {
		opts.Cols = 80
	}
	listener, toolErr := eggclient.PrepareBrowserTools(s.Config, opts.SessionID, opts.Tools, &opts.Egg, launch.Identity)
	if toolErr != nil {
		return nil, toolErr
	}
	if listener != nil {
		s.tools.Store(opts.SessionID, listener)
	}
	accepted := false
	defer func() {
		if !accepted {
			s.ReleaseTools(opts.SessionID)
		}
	}()
	var client *egg.Client
	var err error
	if s.Spawn != nil {
		client, err = s.Spawn(launch, opts)
	} else {
		client, err = eggclient.SpawnEgg(s.Config, opts.SessionID, opts.Agent, launch.Config, opts.Rows, opts.Cols, launch.CWD, opts.Debug, opts.VTE, opts.Trace, launch.Identity, opts.IdleTimeout, opts.Egg)
	}
	if err != nil {
		return nil, err
	}
	if err = s.Register(opts.SessionID); err != nil {
		// A wing shutting down leaves the running egg for the next wing to reclaim.
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.StopAttached(stopCtx, client, opts.SessionID)
			cancel()
		}
		_ = client.Close()
		return nil, err
	}
	accepted = true
	return client, nil
}

func (s *Service) Owns(a Authority, session eggclient.LocalSession) bool {
	dir := filepath.Join(s.Config.Dir, "eggs", session.ID)
	legacy := a.LegacyLocalDefault && !s.SharedHost && a.Principal == UserPrincipal(a.UserID) && (session.Principal == "" || session.Principal == "default") && eggclient.ReadEggOwner(dir) == ""
	if legacy && s.Policy != nil {
		legacy = s.Policy().Wing.Org == ""
	}
	if a.UserID == "" || (!a.Browser && eggclient.ReadEggOwner(dir) != a.UserID && !legacy) || (a.Browser && !wingpolicy.CanAttachSession(a.UserID, a.Role, eggclient.ReadEggOwner(dir))) {
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
	// Stale entries still require the same owner and path checks before cleanup.
	session, err := s.Resolve(ctx, a, ref, true)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	return session, eggclient.KillOrphanEggContext(ctx, s.Config, session.ID)
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

// Attach authenticates the session separately from transport key exchange.
func (s *Service) Attach(ctx context.Context, a Authority, client *egg.Client, id string, opts egg.AttachOptions, tools *egg.ToolListener) (pb.Egg_SessionClient, error) {
	if _, err := s.Resolve(ctx, a, id, true); err != nil {
		return nil, err
	}
	if opts.ReadOnly {
		return client.AttachSessionWithOptions(ctx, id, opts)
	}
	return eggclient.AttachBrowserController(ctx, client, id, opts, tools, a.UserID)
}

// Observe belongs to the wing, so it does not acquire a client writer lease.
func (s *Service) Observe(ctx context.Context, client *egg.Client, id, owner string) (pb.Egg_SessionClient, error) {
	return client.AttachSessionWithOptions(ctx, id, egg.AttachOptions{ReadOnly: true, Owner: owner})
}

// Input uses the already acknowledged attachment capability. The transport
// verifies its controller/key binding before passing decrypted bytes here.
func (s *Service) Input(stream pb.Egg_SessionClient, id string, data []byte) error {
	return stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Input{Input: data}})
}

func (s *Service) Send(ctx context.Context, a Authority, ref string, input []byte, enter bool) (eggclient.LocalSession, error) {
	session, err := s.Resolve(ctx, a, ref, false)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	return eggclient.SendSessionInput(ctx, s.Config, session.ID, input, enter, a.UserID)
}

// StopAttached is for a wing bridge holding an authenticated attachment.
func (s *Service) StopAttached(ctx context.Context, client *egg.Client, id string) error {
	return client.Kill(ctx, id)
}

func (s *Service) ToolListener(id string) *egg.ToolListener {
	if value, ok := s.tools.Load(id); ok {
		return value.(*egg.ToolListener)
	}
	return nil
}
func (s *Service) ReleaseTools(id string) {
	if value, ok := s.tools.LoadAndDelete(id); ok {
		_ = value.(*egg.ToolListener).Close()
	}
}

// Fork keeps provider/name reservations and the resulting egg under the same
// wing lifecycle as fresh launches. Prepare/Admit preserve the caller's bounds
// and conversation metadata without handing it a process executor.
func (s *Service) Fork(ctx context.Context, a Authority, ref, label string, scope eggclient.SessionForkScope) (*eggclient.SessionForkResult, error) {
	source, err := s.Resolve(ctx, a, ref, true)
	if err != nil {
		return nil, err
	}
	var launch *Launch
	tools := scope.Tools
	scope.Tools = nil
	scope.Principal = a.Principal
	scope.LoadConfig = func(cwd string) (*egg.EggConfig, error) {
		var err error
		launch, err = s.PrepareLaunch(a, cwd)
		if err != nil {
			return nil, err
		}
		if wingpolicy.CanonicalSessionPath(launch.CWD) != wingpolicy.CanonicalSessionPath(cwd) {
			return nil, errors.New("source working directory is outside current browser launch paths")
		}
		return launch.Config, nil
	}
	scope.Spawn = func(plan *eggclient.SessionForkPlan) error {
		launch.Config = plan.Config
		client, err := s.Start(ctx, launch, StartOptions{SessionID: plan.SessionID, Agent: plan.Source.Agent, Egg: plan.Options, Tools: tools, Trace: scope.TraceFromConfig && plan.Config.Trace, IdleTimeout: scope.IdleTimeout})
		if client != nil {
			_ = client.Close()
		}
		return err
	}
	return eggclient.ForkSession(ctx, s.Config, source.ID, label, scope)
}

func (s *Service) Snapshot(ctx context.Context, a Authority, ref string) (eggclient.LocalSession, []byte, error) {
	session, err := s.Resolve(ctx, a, ref, false)
	if err != nil {
		return eggclient.LocalSession{}, nil, err
	}
	return eggclient.ReadSessionSnapshot(ctx, s.Config, session.ID)
}

func (s *Service) Read(ctx context.Context, a Authority, ref string, after int64, limit int) (egg.SessionView, error) {
	if err := ctx.Err(); err != nil {
		return egg.SessionView{}, err
	}
	session, err := s.Resolve(ctx, a, ref, true)
	if err != nil {
		return egg.SessionView{}, err
	}
	return eggclient.LifecycleViewForSession(s.Config, session, after, limit)
}

func (s *Service) Prompt(ctx context.Context, a Authority, ref, request, input string, timeout time.Duration, actor string) (eggclient.LocalSession, egg.SessionPromptResult, error) {
	session, err := s.Resolve(ctx, a, ref, true)
	if err != nil {
		return eggclient.LocalSession{}, egg.SessionPromptResult{}, err
	}
	receipt, err := eggclient.PromptSession(ctx, s.Config, session, request, input, timeout, actor, a.UserID)
	return session, receipt, err
}

func (s *Service) Wait(ctx context.Context, a Authority, ref string, after int64, state string) (egg.SessionView, bool, error) {
	session, err := s.Resolve(ctx, a, ref, true)
	if err != nil {
		return egg.SessionView{}, false, err
	}
	return eggclient.WaitSessionLifecycle(ctx, s.Config, session, after, state)
}
