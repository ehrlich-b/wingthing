package main

// Host broker for a sandboxed personal parent's injected MCP client.
//
// The parent's own `wt mcp stdio` runs inside its egg sandbox, where a nested
// child egg cannot bind its network proxy. Instead of widening that sandbox,
// an already-authorized host launch captures the launcher's existing authority
// in provider-write-protected state and starts this broker outside the parent
// sandbox. The parent's stdio client exchanges bounded files with it through a
// mailbox in the parent's already writable workspace (same-owner workspace
// trust, shared with children in that workspace; never a sealed caller; see
// conversation_mailbox.go). The broker is preview-only. Every call is dispatched through the
// existing typed callTool path, strict argument decoding, audit and admission,
// on a fixed subset of conversation/session tools, re-intersected with current
// clients.yaml and wing policy each time. No new grant, mount, socket or
// network permission is created.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const (
	conversationBrokerVersion      = 1
	conversationBrokersDir         = "conversation-brokers"
	conversationBrokerJournalLimit = 512
	// Longer than the request age bound, so a retained request ID is never
	// accepted again after its journal entry is swept.
	conversationBrokerJournalTTL = 2 * conversationMailboxRequestMaxAge
)

// The only operations a host mailbox exposes. Everything else, including raw
// terminal input, arbitrary commands, prompt runs, swarms, wake policy and
// session stop, stays unavailable through this bridge.
var conversationBrokerTools = []string{
	"wingthing_capabilities", "agent_start", "session_status", "session_read",
	"session_wait", "session_prompt", "conversation_list", "conversation_read",
	"conversation_checkpoint",
}

// Mutations pause while the wing is locked, as host wake delivery does. Only
// these calls are journaled: initialize, list, read and wait polling never
// consume the mutation journal and are never replayed after a restart.
var conversationBrokerMutations = map[string]bool{"agent_start": true, "session_prompt": true, "conversation_checkpoint": true}

// A journaled mutation that was dispatched but not answered is replayed after a
// broker restart only when its durable tool-level request_id makes the replay
// a reconciliation rather than a second effect. A checkpoint reports an
// unconfirmed outcome instead.
var conversationBrokerReplaySafe = map[string]bool{"agent_start": true, "session_prompt": true}

var (
	conversationBrokerStartWait = 60 * time.Second
	// Recovery runs after readiness is published and each replay is bounded, so
	// an interrupted call can neither stall startup nor hold a slot forever.
	conversationBrokerRecoveryTimeout = 90 * time.Second
)

// Response outcomes, also returned to the MCP caller as structured error data.
const (
	brokerOutcomeNotDispatched = "not_dispatched"
	brokerOutcomeCompleted     = "completed"
	brokerOutcomeUnconfirmed   = "unconfirmed"
)

// brokerActor names the exact parent execution in audit and delivery records.
func brokerActor(conversation, session string) string {
	return "conversation:" + conversation + ":execution:" + session
}

// conversationBrokerProtection is the provider-write preflight run before the
// parent launch, when the broker starts, and before every child spawn.
var conversationBrokerProtection = defaultConversationBrokerProtection

func defaultConversationBrokerProtection(cfg *config.Config, eggCfg *egg.EggConfig, agentName, cwd, sessionID string, identity EggIdentity, targets []string) error {
	if err := brokerProviderHomeOutsideState(cfg); err != nil {
		return err
	}
	model, err := modelProviderWrites(cfg, eggCfg, agentName, cwd, sessionID, identity)
	if err != nil {
		return err
	}
	return model.verifyProtected(cfg.Dir, targets)
}

// conversationBrokerRegistration is the immutable authority captured at an
// already-authorized host launch. It lives only in protected host state.
type conversationBrokerRegistration struct {
	Version           int      `json:"version"`
	StateDir          string   `json:"state_dir"`
	ConversationID    string   `json:"conversation_id"`
	RootID            string   `json:"root_conversation_id"`
	SessionID         string   `json:"session_id"`
	Principal         string   `json:"principal"`
	LauncherActor     string   `json:"launcher_actor"`
	LauncherSurface   string   `json:"launcher_surface"`
	UserID            string   `json:"user_id,omitempty"`
	Email             string   `json:"email,omitempty"`
	Tools             []string `json:"tools"`
	MaxSessions       int      `json:"max_sessions"`
	MaxSpawnsPerHour  int      `json:"max_spawns_per_hour"`
	AllowedPaths      []string `json:"allowed_paths"`
	EnforcePathBounds bool     `json:"enforce_path_bounds"`
	Workspace         string   `json:"workspace"`
	Mailbox           string   `json:"mailbox"`
	EggConfig         string   `json:"egg_config"`
	Executable        string   `json:"executable"`
	RegisteredAt      int64    `json:"registered_at"`
}

func conversationBrokerDir(cfg *config.Config, session string) string {
	return filepath.Join(cfg.Dir, conversationBrokersDir, session)
}

func conversationBrokerRelative(c *store.Conversation) string {
	return filepath.Join(".wingthing-conversations", c.ID, c.SessionID)
}

// protectedTargets is the whole canonical selected state directory and the
// resolved host controller executable. Every broker-managed parent and child
// egg carries this set to the final-profile checker in sandbox.New, which
// refuses any emitted write rule that could reach either one.
func (r *conversationBrokerRegistration) protectedTargets(cfg *config.Config) []string {
	return []string{canonicalPolicyPath(cfg.Dir), canonicalPolicyPath(r.Executable)}
}

// launchOpts applies the broker-managed launch contract to an egg spawn: the
// protected set above, and no optional browser bridge, whose writable request
// file lies inside the protected state directory.
func (r *conversationBrokerRegistration) launchOpts(cfg *config.Config, opts spawnEggOpts) spawnEggOpts {
	opts.ProtectedWriteTargets = r.protectedTargets(cfg)
	opts.OmitBrowserBridge = true
	return opts
}

// The provider data home D is writable by the provider, so it must lie outside
// the protected state directory S. Its physical-alias binding is checked
// separately when the configuration is loaded.
func brokerProviderHomeOutsideState(cfg *config.Config) error {
	state := canonicalPolicyPath(cfg.Dir)
	home := canonicalPolicyPath(cfg.ProviderDataHome())
	if sessionPolicyContains(state, home) || sessionPolicyContains(home, state) {
		return fmt.Errorf("provider data home %s overlaps protected state %s; the host mailbox requires a provider data home outside the state directory", home, state)
	}
	return nil
}

// brokerToolCeiling is the finite subset of bridge tools this launcher may
// already call. A launcher with no clients.yaml entry (nil grants) receives the
// bridge subset, never an unrestricted server; bounds default to the existing
// direct-MCP limits instead of being unlimited.
func (s *localMCPServer) brokerToolCeiling() ([]string, int, int) {
	tools := make([]string, 0, len(conversationBrokerTools))
	for _, name := range conversationBrokerTools {
		if s.toolAllowed(name) {
			tools = append(tools, name)
		}
	}
	maxSessions, maxSpawns := s.maxSessions, s.maxSpawnsPerHour
	if maxSessions <= 0 {
		maxSessions = defaultDirectMCPMaxSessions
	}
	if maxSpawns <= 0 {
		maxSpawns = defaultDirectMCPMaxSpawnsPerHour
	}
	return tools, maxSessions, maxSpawns
}

// The child policy ceiling is the parent's trusted launch-time egg policy with
// workspace-relative rules made absolute, so a child started from any
// directory inside the parent workspace gets the same rules. Workspace egg.yaml
// files, which the parent provider can write, are never consulted for children.
func brokerChildPolicySnapshot(eggCfg *egg.EggConfig, workspace string) (string, error) {
	snapshot := *eggCfg
	snapshot.DangerouslySkipPermissions = false
	snapshot.FS = make([]string, 0, len(eggCfg.FS))
	for _, entry := range eggCfg.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		if path == "." || path == "./" {
			path = workspace
		} else if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			path = filepath.Join(workspace, path)
		}
		snapshot.FS = append(snapshot.FS, mode+":"+path)
	}
	return snapshot.YAML()
}

// prepareBrokerParentMCP selects the host mailbox only where the direct
// in-sandbox stdio server is already refused because it cannot write the
// Wingthing state. Writable-state configurations keep the original transport.
//
// The returned registration's launchOpts must be applied to the parent spawn.
func (s *localMCPServer) prepareBrokerParentMCP(c *store.Conversation, eggCfg *egg.EggConfig, args []string) ([]string, *conversationBrokerRegistration, error) {
	// Stable keeps its deployed refusal for this layout unchanged.
	if config.Channel() != "preview" {
		return nil, nil, errors.New("the host mailbox is available only in the preview channel")
	}
	if s.unsandboxed {
		return nil, nil, errors.New("outer-boundary sessions use direct MCP")
	}
	if s.hostMailboxUnavailable != "" {
		return nil, nil, errors.New(s.hostMailboxUnavailable)
	}
	wc, err := config.LoadWingConfig(s.cfg.Dir)
	if err != nil {
		return nil, nil, err
	}
	if wc.Org != "" || s.identity.OrgWing || s.identity.SharedHost {
		return nil, nil, errors.New("the host mailbox is available only for personal wings")
	}
	if err := validateSessionID(c.ID); err != nil {
		return nil, nil, err
	}
	if err := validateSessionID(c.SessionID); err != nil {
		return nil, nil, err
	}
	tools, maxSessions, maxSpawns := s.brokerToolCeiling()
	if len(tools) == 0 {
		return nil, nil, errors.New("this launcher has no grant for any host mailbox operation")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	executable = canonicalPolicyPath(executable)
	workspace := canonicalPolicyPath(c.CWD)
	snapshot, err := brokerChildPolicySnapshot(eggCfg, workspace)
	if err != nil {
		return nil, nil, err
	}
	relative := conversationBrokerRelative(c)
	paths := canonicalPaths(s.allowedPaths)
	sort.Strings(paths)
	reg := conversationBrokerRegistration{
		Version: conversationBrokerVersion, StateDir: canonicalPolicyPath(s.cfg.Dir),
		ConversationID: c.ID, RootID: c.RootID, SessionID: c.SessionID,
		Principal: s.clientPrincipal(), LauncherActor: s.clientActor(), LauncherSurface: string(s.controlSurface()),
		UserID: s.identity.UserID, Email: s.identity.Email, Tools: tools,
		MaxSessions: maxSessions, MaxSpawnsPerHour: maxSpawns,
		AllowedPaths: paths, EnforcePathBounds: s.enforcePathBounds,
		Workspace: workspace, Mailbox: filepath.Join(relative, "mailbox"), EggConfig: snapshot,
		Executable: executable, RegisteredAt: time.Now().Unix(),
	}
	if reg.RootID == "" {
		reg.RootID = c.ID
	}
	if err := conversationBrokerProtection(s.cfg, eggCfg, c.Agent, workspace, c.SessionID, s.identity, reg.protectedTargets(s.cfg)); err != nil {
		return nil, nil, err
	}
	if err := writeConversationBrokerRegistration(s.cfg, reg); err != nil {
		return nil, nil, err
	}
	configuration := map[string]any{"mcpServers": map[string]any{"wingthing": map[string]any{
		"command": executable,
		"args":    []string{"mcp", "stdio", "--client", reg.Principal, "--conversation", c.ID, "--execution", c.SessionID, "--host-mailbox", filepath.Join(workspace, reg.Mailbox)},
	}}}
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, nil, err
	}
	defer closeWithLog("parent MCP workspace", root)
	if err := root.MkdirAll(reg.Mailbox, 0700); err != nil {
		return nil, nil, err
	}
	configPath := filepath.Join(relative, "mcp.json")
	if err := writeExclusiveOrSame(root, configPath, data); err != nil {
		return nil, nil, err
	}
	if err := startConversationBroker(s.cfg, reg); err != nil {
		return nil, nil, fmt.Errorf("start host mailbox broker: %w", err)
	}
	return append(args, "--mcp-config", filepath.Join(workspace, configPath), "--append-system-prompt", publicCoordinatorPrompt(c)), &reg, nil
}

func writeExclusiveOrSame(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := mailboxRead(root, name, int64(len(data))+1)
		if readErr != nil || string(existing) != string(data) {
			return errors.New("existing parent MCP configuration differs from the reserved binding")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writePrivateDurable(path string, data []byte, exclusive bool) error {
	if exclusive {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	} else {
		temporary, err := os.CreateTemp(filepath.Dir(path), ".broker-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(temporary.Name()) }()
		if _, err = temporary.Write(data); err == nil {
			err = temporary.Sync()
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := os.Rename(temporary.Name(), path); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func writeConversationBrokerRegistration(cfg *config.Config, reg conversationBrokerRegistration) error {
	dir := conversationBrokerDir(cfg, reg.SessionID)
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("register host mailbox execution: %w", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "journal"), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateDurable(filepath.Join(dir, "registration.json"), data, true)
}

func loadConversationBrokerRegistration(cfg *config.Config, session string) (conversationBrokerRegistration, error) {
	var reg conversationBrokerRegistration
	if err := validateSessionID(session); err != nil {
		return reg, err
	}
	path := filepath.Join(conversationBrokerDir(cfg, session), "registration.json")
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return reg, fmt.Errorf("host mailbox registration unavailable: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return reg, err
	}
	if len(data) > 1<<20 {
		return reg, errors.New("host mailbox registration exceeds bound")
	}
	if err := decodeMailbox(data, &reg); err != nil {
		return reg, fmt.Errorf("invalid host mailbox registration: %w", err)
	}
	if reg.Version != conversationBrokerVersion || reg.SessionID != session || reg.StateDir != canonicalPolicyPath(cfg.Dir) {
		return reg, errors.New("host mailbox registration does not belong to this execution and state directory")
	}
	if validateSessionID(reg.ConversationID) != nil || validateSessionID(reg.RootID) != nil || validateSessionName(reg.Principal) != nil || reg.Principal == "" {
		return reg, errors.New("host mailbox registration has an invalid identity")
	}
	if reg.MaxSessions <= 0 || reg.MaxSpawnsPerHour <= 0 || len(reg.Tools) == 0 {
		return reg, errors.New("host mailbox registration lacks a finite authority ceiling")
	}
	allowed := map[string]bool{}
	for _, name := range conversationBrokerTools {
		allowed[name] = true
	}
	for _, name := range reg.Tools {
		if !allowed[name] {
			return reg, fmt.Errorf("host mailbox registration names unsupported tool %q", name)
		}
	}
	if !filepath.IsAbs(reg.Workspace) || filepath.IsAbs(reg.Mailbox) || !strings.HasPrefix(reg.Mailbox, ".wingthing-conversations"+string(filepath.Separator)) || strings.Contains(reg.Mailbox, "..") {
		return reg, errors.New("host mailbox registration has an invalid workspace binding")
	}
	return reg, nil
}

// server builds the dispatcher for one call: the captured ceiling intersected
// with current clients.yaml and wing policy. Revocation applies to the next call.
func (r *conversationBrokerRegistration) server(cfg *config.Config, admission *mcpAdmissionState) (*localMCPServer, *config.WingConfig, error) {
	wc, err := config.LoadWingConfig(cfg.Dir)
	if err != nil {
		return nil, nil, err
	}
	if wc.Org != "" {
		return nil, nil, errors.New("host mailbox is unavailable on an organization wing")
	}
	clients, err := loadLocalMCPClientsConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	tools := map[string]bool{}
	for _, name := range r.Tools {
		tools[name] = true
	}
	maxSessions, maxSpawns := r.MaxSessions, r.MaxSpawnsPerHour
	restrict := func(key string, entry localMCPClientConfig) error {
		owner := strings.TrimSpace(entry.Owner)
		if owner == "" {
			owner = key
		}
		if owner != r.Principal {
			return fmt.Errorf("clients.yaml entry %q no longer maps to the captured owner", key)
		}
		grants := grantSet(entry.Grants)
		for name := range tools {
			if tool, ok := control.Lookup(name); !ok || !grants[tool.Grant] {
				delete(tools, name)
			}
		}
		if entry.Bounds.MaxSessions > 0 && entry.Bounds.MaxSessions < maxSessions {
			maxSessions = entry.Bounds.MaxSessions
		}
		if entry.Bounds.MaxSpawnsPerHour > 0 && entry.Bounds.MaxSpawnsPerHour < maxSpawns {
			maxSpawns = entry.Bounds.MaxSpawnsPerHour
		}
		return nil
	}
	if r.LauncherSurface == string(control.SurfaceLocalMCP) {
		entry, configured := clients.Clients[r.LauncherActor]
		if !configured && (len(clients.Clients) > 0 || clients.RequireClient) {
			return nil, nil, fmt.Errorf("launcher MCP client %q is no longer configured in clients.yaml", r.LauncherActor)
		}
		if configured {
			if err := restrict(r.LauncherActor, entry); err != nil {
				return nil, nil, err
			}
		}
	}
	if entry, configured := clients.Clients[r.Principal]; configured && r.Principal != r.LauncherActor {
		if err := restrict(r.Principal, entry); err != nil {
			return nil, nil, err
		}
	}
	grants := map[string]bool{}
	for name := range tools {
		if tool, ok := control.Lookup(name); ok {
			grants[tool.Grant] = true
		}
	}
	home, _ := os.UserHomeDir()
	paths, enforce := intersectBrokerPaths(r.AllowedPaths, r.EnforcePathBounds, canonicalPaths(pathsForRequest(wc.Paths, r.Email, "owner", home)))
	reg := *r
	return &localMCPServer{
		cfg: cfg, logs: os.Stderr, principal: r.Principal, actor: brokerActor(r.ConversationID, r.SessionID),
		surface: control.SurfaceLocalMCP, grants: grants, tools: tools,
		maxSessions: maxSessions, maxSpawnsPerHour: maxSpawns, admission: admission,
		identity:     EggIdentity{UserID: r.UserID, Email: r.Email},
		allowedPaths: paths, enforcePathBounds: enforce,
		boundConversation: r.ConversationID, broker: &reg,
	}, wc, nil
}

// intersectBrokerPaths narrows the captured path ceiling by the owner's current
// wing path ACL. An empty current list means the wing is unbounded for its
// owner, as for browser control; it never widens a captured bound.
func intersectBrokerPaths(captured []string, enforced bool, current []string) ([]string, bool) {
	if len(current) == 0 {
		return append([]string(nil), captured...), enforced
	}
	if !enforced {
		return append([]string(nil), current...), true
	}
	var paths []string
	for _, a := range captured {
		for _, b := range current {
			switch {
			case sessionPolicyContains(a, b):
				paths = append(paths, b)
			case sessionPolicyContains(b, a):
				paths = append(paths, a)
			}
		}
	}
	sort.Strings(paths)
	return slices.Compact(paths), true
}

// childEggConfig returns the parent's captured policy for a child inside the
// parent workspace.
func (r *conversationBrokerRegistration) childEggConfig(cwd string) (*egg.EggConfig, error) {
	if !sessionPolicyContains(r.Workspace, canonicalPolicyPath(cwd)) {
		return nil, errors.New("a host mailbox child must run inside the parent's workspace")
	}
	return egg.LoadEggConfigFromYAML(r.EggConfig)
}

// preflightBrokerChild runs inside admission, immediately before each child
// spawn: the child's resolved policy must not expose protected state, and the
// owner's durable launch rate is checked across processes and restarts.
func (s *localMCPServer) preflightBrokerChild(eggCfg *egg.EggConfig, agentName, cwd, sessionID string) error {
	if s.broker == nil {
		return nil
	}
	if err := conversationBrokerProtection(s.cfg, eggCfg, agentName, canonicalPolicyPath(cwd), sessionID, s.identity, s.broker.protectedTargets(s.cfg)); err != nil {
		return fmt.Errorf("child policy preflight: %w", err)
	}
	db, err := s.openMessageStore()
	if err != nil {
		return err
	}
	defer closeWithLog("host mailbox admission store", db)
	launched, err := db.CountConversationLaunchesSince(s.clientPrincipal(), time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	// The count includes this execution's own reservation.
	if launched > s.maxSpawnsPerHour {
		return fmt.Errorf("principal %q reached durable max_spawns_per_hour=%d", s.clientPrincipal(), s.maxSpawnsPerHour)
	}
	return nil
}

// checkBoundSessionTarget keeps a host mailbox connection inside its task
// tree. It requires the exact execution ID (no label or prefix), an existing
// execution directory so the handler's exact-ID match is selected, and linkage
// of that execution, including archived and resumed executions, to the bound
// root. Ordinary direct `--conversation` MCP connections keep their deployed
// terminal contract; these rules apply only to broker-dispatched calls.
var boundSessionTargetTools = map[string]bool{
	"session_status": true, "session_read": true, "session_wait": true, "session_prompt": true,
	"terminal_read": true, "terminal_send": true, "terminal_wait": true, "terminal_rename": true, "terminal_stop": true,
}

func (s *localMCPServer) checkBoundSessionTarget(tool string, arguments json.RawMessage) error {
	if s.broker == nil || s.boundConversation == "" || !boundSessionTargetTools[tool] {
		return nil
	}
	var selector struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(arguments, &selector); err != nil || selector.Session == "" {
		return nil // the handler's strict decoder reports the argument error
	}
	outside := errors.New("session is outside this MCP connection's bound task tree; use its exact execution session ID")
	if validateSessionID(selector.Session) != nil {
		return outside
	}
	info, err := os.Lstat(filepath.Join(s.cfg.Dir, "eggs", selector.Session))
	if err != nil || !info.IsDir() {
		return outside
	}
	db, err := s.openMessageStore()
	if err != nil {
		return err
	}
	defer closeWithLog("bound session target store", db)
	bound, err := db.GetConversation(s.clientPrincipal(), s.boundConversation)
	if err != nil {
		return outside
	}
	c, err := db.ConversationForSession(selector.Session)
	if err != nil || c.OwnerID != s.clientPrincipal() || c.RootID != bound.RootID {
		return outside
	}
	return nil
}

var startConversationBroker = defaultStartConversationBroker

// A Go test binary ignores the broker argv and reruns its whole suite as a
// detached process. The real receiver is always an explicitly built wt binary.
func brokerReceiverRefused(executable string) error {
	if strings.HasSuffix(filepath.Base(executable), ".test") {
		return fmt.Errorf("refusing to start host broker from test binary %s; use a built wt receiver", executable)
	}
	return nil
}

func defaultStartConversationBroker(cfg *config.Config, reg conversationBrokerRegistration) error {
	if err := brokerReceiverRefused(reg.Executable); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(conversationBrokerDir(cfg, reg.SessionID), "broker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(reg.Executable, "conversation", "broker", reg.SessionID)
	allowed := map[string]bool{"HOME": true, "PATH": true, "TERM": true, "LANG": true, "USER": true, "SHELL": true, "TMPDIR": true, "WINGTHING_PREVIEW_DIR": true}
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && allowed[key] {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "WINGTHING_DIR="+cfg.Dir)
	cmd.Dir = reg.Workspace
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	go func() { _ = cmd.Wait() }()
	return nil
}

type conversationBrokerJournal struct {
	Version    int             `json:"version"`
	ID         string          `json:"request_id"`
	Method     string          `json:"method"`
	Tool       string          `json:"tool,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	Digest     string          `json:"payload_sha256"`
	AcceptedAt int64           `json:"accepted_at"`
	Phase      string          `json:"phase"`
	Outcome    string          `json:"outcome,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type conversationBroker struct {
	cfg       *config.Config
	reg       conversationBrokerRegistration
	dir       string
	epoch     string
	provider  string
	admission *mcpAdmissionState
	mailbox   *os.Root
	logs      io.Writer
	mu        sync.Mutex
	inflight  map[string]bool
	slots     chan struct{}
	calls     sync.WaitGroup
}

var errConversationBrokerRunning = errors.New("host mailbox broker already running for this execution")

func runConversationBroker(ctx context.Context, cfg *config.Config, session string, logs io.Writer) error {
	if config.Channel() != "preview" {
		return errors.New("the host mailbox broker is available only in the preview channel")
	}
	reg, err := loadConversationBrokerRegistration(cfg, session)
	if err != nil {
		return err
	}
	dir := conversationBrokerDir(cfg, session)
	lock, err := os.OpenFile(filepath.Join(dir, "broker.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errConversationBrokerRunning
	}
	if err := writePrivateDurable(filepath.Join(dir, "broker.pid"), fmt.Appendf(nil, "%d\n", os.Getpid()), false); err != nil {
		return err
	}
	eggCfg, err := egg.LoadEggConfigFromYAML(reg.EggConfig)
	if err != nil {
		return err
	}
	// Recheck protection with the current host layout before serving.
	if err := conversationBrokerProtection(cfg, eggCfg, "claude", reg.Workspace, reg.SessionID, EggIdentity{UserID: reg.UserID, Email: reg.Email}, reg.protectedTargets(cfg)); err != nil {
		return fmt.Errorf("host mailbox protection preflight: %w", err)
	}
	workspace, err := os.OpenRoot(reg.Workspace)
	if err != nil {
		return err
	}
	defer closeWithLog("host mailbox workspace", workspace)
	if err := workspace.MkdirAll(reg.Mailbox, 0700); err != nil {
		return err
	}
	mailbox, err := workspace.OpenRoot(reg.Mailbox)
	if err != nil {
		return err
	}
	defer closeWithLog("host mailbox", mailbox)
	epoch, err := newMailboxID()
	if err != nil {
		return err
	}
	b := &conversationBroker{cfg: cfg, reg: reg, dir: dir, epoch: epoch, admission: newMCPAdmissionState(), mailbox: mailbox, logs: logs, inflight: map[string]bool{}, slots: make(chan struct{}, conversationMailboxConcurrency)}
	if err := b.waitForParent(ctx); err != nil {
		b.publishReady(false, err.Error())
		return err
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.publishReady(true, "")
	b.reconcileJournal(callCtx)
	_ = writef(logs, "wingthing host mailbox: serving conversation %s execution %s epoch %s\n", reg.ConversationID, reg.SessionID, epoch)
	poll := time.NewTicker(conversationMailboxPoll)
	defer poll.Stop()
	heartbeat := time.NewTicker(time.Second)
	defer heartbeat.Stop()
	reason := "broker stopped"
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-heartbeat.C:
			if _, alive := readAliveEggPID(filepath.Join(cfg.Dir, "eggs", reg.SessionID)); !alive {
				reason, running = "parent execution ended", false
				break
			}
			b.publishReady(true, "")
			b.sweep()
		case <-poll.C:
			b.scan(callCtx)
		}
	}
	b.publishReady(false, reason)
	cancel()
	b.calls.Wait()
	_ = writef(logs, "wingthing host mailbox: %s\n", reason)
	return nil
}

func (b *conversationBroker) waitForParent(ctx context.Context) error {
	dir := filepath.Join(b.cfg.Dir, "eggs", b.reg.SessionID)
	deadline := time.Now().Add(conversationBrokerStartWait)
	for {
		if _, alive := readAliveEggPID(dir); alive {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("parent execution did not start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	meta := readEggMetaValues(dir)
	if readSessionPrincipal(dir) != b.reg.Principal || (b.reg.UserID != "" && readEggOwner(dir) != b.reg.UserID) || meta["agent"] != "claude" || canonicalPolicyPath(meta["cwd"]) != b.reg.Workspace {
		return errors.New("parent execution does not match the registered owner, provider and workspace")
	}
	b.provider = meta["provider_session_id"]
	return nil
}

func (b *conversationBroker) publishReady(ready bool, reason string) {
	value := conversationMailboxReady{Version: conversationMailboxVersion, Epoch: b.epoch, ConversationID: b.reg.ConversationID, SessionID: b.reg.SessionID, ProviderSessionID: b.provider, HostReady: ready, ObservedAt: time.Now().Unix(), Reason: reason}
	if err := mailboxWrite(b.mailbox, conversationMailboxReadyFile, value, conversationMailboxReadyBytes); err != nil {
		_ = writef(b.logs, "wingthing host mailbox: publish readiness: %v\n", err)
	}
}

func (b *conversationBroker) journalPath(id string) string {
	return filepath.Join(b.dir, "journal", id+".json")
}

func (b *conversationBroker) readJournal(id string) (conversationBrokerJournal, bool) {
	var entry conversationBrokerJournal
	file, err := os.OpenFile(b.journalPath(id), os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return entry, false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 2*conversationMailboxResponseBytes))
	if err != nil || json.Unmarshal(data, &entry) != nil || entry.ID != id {
		return entry, false
	}
	return entry, true
}

func (b *conversationBroker) writeJournal(entry conversationBrokerJournal) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return writePrivateDurable(b.journalPath(entry.ID), data, false)
}

func (b *conversationBroker) respond(id, outcome string, payload json.RawMessage, message string) {
	response := conversationMailboxResponse{Version: conversationMailboxVersion, ID: id, Epoch: b.epoch, ConversationID: b.reg.ConversationID, SessionID: b.reg.SessionID, ProviderSessionID: b.provider, Dispatched: outcome != brokerOutcomeNotDispatched, Outcome: outcome, Payload: payload, Error: message}
	if err := mailboxWrite(b.mailbox, mailboxResponseName(id), response, conversationMailboxResponseBytes); err != nil {
		_ = writef(b.logs, "wingthing host mailbox: publish response %s: %v\n", id, err)
	}
}

// claim marks a request ID in flight; release clears it.
func (b *conversationBroker) claim(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inflight[id] {
		return false
	}
	b.inflight[id] = true
	return true
}

func (b *conversationBroker) release(id string) {
	b.mu.Lock()
	delete(b.inflight, id)
	b.mu.Unlock()
}

func (b *conversationBroker) scan(ctx context.Context) {
	entries, err := mailboxEntries(b.mailbox, conversationMailboxEntryLimit)
	if err != nil {
		_ = writef(b.logs, "wingthing host mailbox: %v\n", err)
		return
	}
	for _, entry := range entries {
		id, ok := mailboxRequestID(entry.Name())
		if !ok || !b.claim(id) {
			continue
		}
		if !acquireLocalMCPCallSlot(b.slots) {
			b.release(id)
			continue
		}
		b.calls.Add(1)
		go func() {
			defer b.calls.Done()
			defer func() { <-b.slots }()
			defer b.release(id)
			b.accept(ctx, id)
		}()
	}
}

// accept validates one published request and removes its artifact before
// dispatch. A mutation is journaled durably first: a request ID with a journal
// entry is never dispatched as a new call. Reads and protocol calls have no
// effect to reconcile, so they are not journaled and cannot fill the journal.
func (b *conversationBroker) accept(ctx context.Context, id string) {
	name := mailboxRequestName(id)
	if entry, ok := b.readJournal(id); ok {
		_ = b.mailbox.Remove(name)
		if entry.Phase == "completed" {
			b.respond(id, entry.Outcome, entry.Response, entry.Error)
		}
		return
	}
	reject := func(message string) {
		_ = b.mailbox.Remove(name)
		b.respond(id, brokerOutcomeNotDispatched, nil, message+"; the request was not dispatched")
	}
	data, err := mailboxRead(b.mailbox, name, conversationMailboxRequestBytes)
	if err != nil {
		reject("invalid mailbox request artifact: " + err.Error())
		return
	}
	var request conversationMailboxRequest
	if err := decodeMailbox(data, &request); err != nil {
		reject("invalid mailbox request: " + err.Error())
		return
	}
	if request.Version != conversationMailboxVersion || request.ID != id {
		reject("mailbox request identity differs from its artifact")
		return
	}
	if request.Epoch != b.epoch {
		reject("host broker restarted before accepting this request")
		return
	}
	age := time.Since(time.Unix(request.CreatedAt, 0))
	if age > conversationMailboxRequestMaxAge || age < -5*time.Second {
		reject("mailbox request is outside its age bound")
		return
	}
	var call conversationMailboxCall
	if err := decodeMailbox(request.Payload, &call); err != nil {
		reject("invalid MCP call envelope: " + err.Error())
		return
	}
	tool := ""
	switch call.Method {
	case "initialize", "ping", "tools/list":
	case "tools/call":
		var selector struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(call.Params, &selector)
		tool = selector.Name
	default:
		reject("method not available through the host mailbox: " + call.Method)
		return
	}
	if call.Method != "tools/call" || !conversationBrokerMutations[tool] {
		_ = b.mailbox.Remove(name)
		payload, outcome, message := b.dispatch(ctx, call, tool)
		b.respond(id, outcome, payload, message)
		return
	}
	if b.journalCount() >= conversationBrokerJournalLimit {
		reject("host mailbox mutation journal is full")
		return
	}
	digest := sha256.Sum256(request.Payload)
	entry := conversationBrokerJournal{Version: 1, ID: id, Method: call.Method, Tool: tool, Payload: request.Payload, Digest: hex.EncodeToString(digest[:]), AcceptedAt: time.Now().Unix(), Phase: "dispatching"}
	if err := b.writeJournal(entry); err != nil {
		reject("host mailbox journal unavailable: " + err.Error())
		return
	}
	_ = b.mailbox.Remove(name)
	b.complete(ctx, entry, call, false)
}

func (b *conversationBroker) complete(ctx context.Context, entry conversationBrokerJournal, call conversationMailboxCall, recovering bool) {
	payload, outcome, message := b.dispatch(ctx, call, entry.Tool)
	if recovering && outcome == brokerOutcomeNotDispatched {
		// A refusal proves only that this replay did not dispatch. The original
		// interrupted call may already have taken effect before the crash.
		outcome = brokerOutcomeUnconfirmed
		message = "host broker could not reconcile interrupted " + entry.Tool + "; its outcome is unconfirmed; reread the conversation before acting again"
	}
	if len(payload) > conversationMailboxResponseBytes-4096 {
		payload, message = nil, "the operation ran but its result exceeds the mailbox bound; read with a smaller limit"
	}
	if outcome != brokerOutcomeNotDispatched && ctx.Err() != nil {
		// Shutdown or the recovery bound interrupted the call mid-flight. The
		// entry stays dispatching for the next broker's bounded recovery.
		b.respond(entry.ID, brokerOutcomeUnconfirmed, nil, "host broker stopped during "+entry.Tool+"; its outcome is unconfirmed; reread the conversation before acting again")
		return
	}
	entry.Phase, entry.Outcome, entry.Response, entry.Error = "completed", outcome, payload, message
	if err := b.writeJournal(entry); err != nil {
		_ = writef(b.logs, "wingthing host mailbox: journal response %s: %v\n", entry.ID, err)
	}
	b.respond(entry.ID, outcome, payload, message)
}

func (b *conversationBroker) dispatch(ctx context.Context, call conversationMailboxCall, tool string) (json.RawMessage, string, string) {
	server, wc, err := b.reg.server(b.cfg, b.admission)
	if err != nil {
		return nil, brokerOutcomeNotDispatched, "host mailbox policy refused the call: " + err.Error() + "; the request was not dispatched"
	}
	if wc.Locked && conversationBrokerMutations[tool] {
		return nil, brokerOutcomeNotDispatched, "the wing is locked; host mailbox mutations are paused; the request was not dispatched"
	}
	response, _ := server.handle(ctx, localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: call.Method, Params: call.Params})
	response.ID = nil
	data, err := json.Marshal(response)
	if err != nil {
		return nil, brokerOutcomeUnconfirmed, "the operation ran but its result could not be encoded"
	}
	return data, brokerOutcomeCompleted, ""
}

// reconcileJournal finishes mutations interrupted by a broker stop without
// repeating an effect. It runs after readiness is published, with a small
// dedicated concurrency and a per-replay bound: request_id tools reconcile
// through their durable IDs; a checkpoint, or any entry older than the request
// age bound, reports an unconfirmed outcome.
func (b *conversationBroker) reconcileJournal(ctx context.Context) {
	recovery := make(chan struct{}, 2)
	for _, id := range b.journalIDs() {
		entry, ok := b.readJournal(id)
		if !ok || entry.Phase == "completed" || !b.claim(id) {
			continue
		}
		b.calls.Add(1)
		go func() {
			defer b.calls.Done()
			defer b.release(id)
			select {
			case recovery <- struct{}{}:
				defer func() { <-recovery }()
			case <-ctx.Done():
				return
			}
			b.recover(ctx, entry)
		}()
	}
}

func (b *conversationBroker) recover(ctx context.Context, entry conversationBrokerJournal) {
	var call conversationMailboxCall
	fresh := time.Since(time.Unix(entry.AcceptedAt, 0)) <= conversationMailboxRequestMaxAge
	if fresh && conversationBrokerReplaySafe[entry.Tool] && decodeMailbox(entry.Payload, &call) == nil {
		replay, cancel := context.WithTimeout(ctx, conversationBrokerRecoveryTimeout)
		defer cancel()
		b.complete(replay, entry, call, true)
		return
	}
	entry.Phase, entry.Outcome = "completed", brokerOutcomeUnconfirmed
	entry.Error = "host broker restarted during " + entry.Tool + "; its outcome is unconfirmed; reread the conversation before acting again"
	if err := b.writeJournal(entry); err == nil {
		b.respond(entry.ID, entry.Outcome, nil, entry.Error)
	}
}

// journalIDs lists at most the journal bound of well-formed entry IDs.
func (b *conversationBroker) journalIDs() []string {
	file, err := os.Open(filepath.Join(b.dir, "journal"))
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	entries, _ := file.ReadDir(conversationBrokerJournalLimit)
	ids := make([]string, 0, len(entries))
	for _, item := range entries {
		if id, ok := strings.CutSuffix(item.Name(), ".json"); ok && validMailboxID(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (b *conversationBroker) journalCount() int {
	file, err := os.Open(filepath.Join(b.dir, "journal"))
	if err != nil {
		return conversationBrokerJournalLimit
	}
	defer func() { _ = file.Close() }()
	entries, _ := file.ReadDir(conversationBrokerJournalLimit + 1)
	return len(entries)
}

// sweep removes completed journal entries and unclaimed responses after their
// retention bound. A swept request ID cannot be redispatched: its artifact is
// then outside the request age bound, and a restart changes the epoch.
func (b *conversationBroker) sweep() {
	b.sweepResponses()
	for _, id := range b.journalIDs() {
		entry, ok := b.readJournal(id)
		if !ok || entry.Phase != "completed" {
			continue
		}
		age := time.Since(time.Unix(entry.AcceptedAt, 0))
		if age > conversationMailboxRequestMaxAge {
			_ = b.mailbox.Remove(mailboxResponseName(id))
		}
		if age > conversationBrokerJournalTTL {
			_ = os.Remove(b.journalPath(id))
		}
	}
}

// Read and protocol calls have no journal entry. Sweep their abandoned responses
// by artifact age as well, streaming bounded batches even when the directory is
// already above the count limit that pauses dispatch.
func (b *conversationBroker) sweepResponses() {
	file, err := b.mailbox.Open(".")
	if err != nil {
		return
	}
	defer closeWithLog("host mailbox response sweep", file)
	now := time.Now()
	for {
		entries, err := file.ReadDir(conversationMailboxEntryLimit)
		for _, entry := range entries {
			if _, ok := mailboxResponseID(entry.Name()); !ok {
				continue
			}
			info, err := b.mailbox.Lstat(entry.Name())
			if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > conversationMailboxRequestMaxAge {
				_ = b.mailbox.Remove(entry.Name())
			}
		}
		if err != nil {
			return
		}
	}
}

func conversationBrokerStatus(cfg *config.Config, session string) (map[string]any, error) {
	reg, err := loadConversationBrokerRegistration(cfg, session)
	if err != nil {
		return nil, err
	}
	status := map[string]any{"conversation_id": reg.ConversationID, "root_conversation_id": reg.RootID, "session_id": reg.SessionID, "principal": reg.Principal, "launcher_actor": reg.LauncherActor, "tools": reg.Tools, "max_sessions": reg.MaxSessions, "max_spawns_per_hour": reg.MaxSpawnsPerHour, "workspace": reg.Workspace, "mailbox": filepath.Join(reg.Workspace, reg.Mailbox), "trust": "same-owner workspace mailbox, writable by the parent and children sharing its workspace; not a sealed caller transport", "audit_actor": brokerActor(reg.ConversationID, reg.SessionID), "host_ready": false}
	if workspace, err := os.OpenRoot(reg.Workspace); err == nil {
		defer closeWithLog("host mailbox workspace", workspace)
		if mailbox, err := workspace.OpenRoot(reg.Mailbox); err == nil {
			defer closeWithLog("host mailbox", mailbox)
			ready, readyErr := readMailboxReady(mailbox, reg.ConversationID, reg.SessionID, time.Now())
			status["host_ready"] = readyErr == nil
			status["provider_session_id"] = ready.ProviderSessionID
			if readyErr != nil {
				status["unavailable"] = readyErr.Error()
			}
		}
	}
	return status, nil
}

func conversationBrokerCmds() []*cobra.Command {
	cmd := &cobra.Command{Use: "broker <session>", Short: "Serve a registered parent's host mailbox (started by the host launcher)", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		err = runConversationBroker(ctx, cfg, args[0], os.Stderr)
		if errors.Is(err, errConversationBrokerRunning) {
			return nil
		}
		return err
	}}
	status := &cobra.Command{Use: "transport <session>", Short: "Inspect a parent execution's host mailbox registration and readiness", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		result, err := conversationBrokerStatus(cfg, args[0])
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	status.Flags().Bool("json", true, "print structured transport state")
	return []*cobra.Command{cmd, status}
}
