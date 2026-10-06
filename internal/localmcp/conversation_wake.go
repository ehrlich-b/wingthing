package localmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

type conversationWakeRuntime struct {
	Read   func(context.Context, eggclient.LocalSession) (egg.SessionView, error)
	Prompt func(context.Context, eggclient.LocalSession, string, string) (egg.SessionPromptResult, error)
	Now    func() time.Time
}

func nativeConversationWakeRuntime(cfg *config.Config) conversationWakeRuntime {
	return conversationWakeRuntime{
		Read: func(ctx context.Context, s eggclient.LocalSession) (egg.SessionView, error) {
			return eggclient.LifecycleViewForSession(cfg, s, 0, 1)
		},
		Prompt: func(ctx context.Context, s eggclient.LocalSession, id, text string) (egg.SessionPromptResult, error) {
			return eggclient.PromptSession(ctx, cfg, s, id, text, 500*time.Millisecond, "host:conversation-wake")
		},
		Now: time.Now,
	}
}

func (s *Server) ToolConversationWake(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		ConversationID string `json:"conversation_id"`
		Enabled        *bool  `json:"enabled,omitempty"`
		RetryNotSent   bool   `json:"retry_not_sent,omitempty"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	wc, err := config.LoadWingConfig(s.Cfg.Dir)
	if err != nil {
		return nil, err
	}
	if wc.Org != "" || s.identity.OrgWing || s.identity.SharedHost {
		return nil, errors.New("automatic wake is available only on personal wings")
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("conversation wake store", db)
	c, err := s.ownedConversation(db, args.ConversationID)
	if err != nil {
		return nil, err
	}
	if c.ParentID != "" {
		return nil, errors.New("automatic wake requires a root conversation")
	}
	if args.RetryNotSent {
		if args.Enabled != nil {
			return nil, errors.New("retry_not_sent and enabled are separate user actions")
		}
		if err = db.RetryNotSentConversationWake(s.clientPrincipal(), c.ID, time.Now().Unix()); err != nil {
			return nil, err
		}
	}
	if args.Enabled != nil {
		if err = db.SetConversationWake(s.clientPrincipal(), c.ID, *args.Enabled); err != nil {
			return nil, err
		}
	}
	p, err := db.ConversationWakePolicy(c.ID)
	if err != nil {
		return nil, err
	}
	w, err := db.PendingConversationWake(c.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversation_id": c.ID, "wake": p, "pending": w, "paused": wc.Locked, "total_attempt_cap": store.MaxConversationWakeAttempts, "acknowledgement": "wake text receipt does not acknowledge parent checkpoint events", "approval": "queued child attention never answers permissions"}, nil
}

// The body contains only bounded host-selected identities and enums. Neither
// child prose nor permission questions are injected as controller instructions.
func conversationWakeText(cfg *config.Config, c *store.Conversation, w *store.ConversationWake) (string, error) {
	provider := eggclient.ReadEggMetaValues(filepath.Join(cfg.Dir, "eggs", w.Event.SessionID))["provider_session_id"]
	providerKnown := eggclient.ValidateSessionID(provider) == nil
	if !providerKnown && w.Event.StateSource != "egg_process" {
		return "", errors.New("child exact provider identity unavailable")
	}
	if !providerKnown {
		provider = ""
	}
	reason := "child foreground completion observed; this is not proof its task is done"
	switch w.Event.State {
	case "needs_input":
		reason = "child needs human input or approval; do not answer permissions automatically"
	case "stopped", "failed":
		reason = "child execution stopped; inspect retained evidence"
	}
	data, err := json.Marshal(map[string]any{"root_conversation_id": c.ID, "child_conversation_id": w.Event.ConversationID, "child_execution_id": w.Event.SessionID, "child_provider_session_id": provider, "child_provider_identity_known": providerKnown, "event_cursor": w.EventSequence, "state": w.Event.State, "source": w.Event.StateSource, "reason": reason, "inspect_link": "#conversation/" + url.QueryEscape(w.Event.ConversationID) + "?wing=" + url.QueryEscape(c.WingID)})
	if err != nil || len(data) > 3072 {
		return "", errors.New("wake event exceeds payload bound")
	}
	return "Wingthing observed a native child lifecycle event. Inspect it using conversation_read/session_read. Keep task outcome separate from foreground turn completion. A wake receipt does not acknowledge the event; checkpoint explicitly after handling it. Human approvals remain human decisions.\n" + string(data), nil
}

// One root step reserves before sending. Unknown requests reconcile only their
// saved execution/provider. A resumed parent cannot receive a replacement send.
func processConversationWake(ctx context.Context, s *Server, root string, runtime conversationWakeRuntime) error {
	digest := sha256.Sum256([]byte(root))
	lock, err := daemonctl.AcquireDaemonLifecycleLockAt(filepath.Join(s.Cfg.Dir, fmt.Sprintf("wake-%x.lock", digest[:8])))
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("conversation wake lock", lock)
	db, err := s.openMessageStore()
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("conversation wake store", db)
	c, err := s.ownedConversation(db, root)
	if err != nil {
		return err
	}
	p, err := db.ConversationWakePolicy(root)
	if err != nil || !p.Enabled {
		return err
	}
	// Reuse full-tree reconciliation, including immutable old execution tails.
	args, _ := json.Marshal(map[string]any{"conversation_id": root, "limit": 1})
	if _, err = s.ToolConversationRead(ctx, args); err != nil {
		return err
	}
	c, err = s.ownedConversation(db, root)
	if err != nil {
		return err
	}
	w, err := db.QueueConversationWake(root)
	if err != nil || w == nil {
		return err
	}
	if w.Status == "blocked" {
		return nil
	}
	fresh := w.Status == "queued" || w.Status == "not_sent"
	target := w.SessionID
	if fresh {
		target = c.SessionID
	}
	// Targets are durable execution IDs, never human name/prefix selectors.
	session, err := s.resolveExactWakeTarget(db, c, target)
	if err != nil {
		return err
	}
	meta := eggclient.ReadEggMetaValues(filepath.Join(s.Cfg.Dir, "eggs", target))
	owner := eggclient.ReadEggOwner(filepath.Join(s.Cfg.Dir, "eggs", target))
	if session.Principal != c.OwnerID || (owner != "" && roostSessionPrincipal(owner) != c.OwnerID) {
		return errors.New("wake parent ownership does not match retained principal")
	}
	if !fresh && target != c.SessionID {
		// Binding precedes prompt reservation. After a resume, prove that the
		// original execution never reserved this request before allowing a
		// fresh attempt to select the current execution.
		absent, err := reconcileUnreservedConversationWake(ctx, s.Cfg, db, w, runtime.Now().Unix())
		if err != nil || absent {
			return err
		}
	}
	view, err := runtime.Read(ctx, session)
	if err != nil {
		return err
	}
	if view.ProviderSessionID == "" || view.ProviderSessionID != meta["provider_session_id"] {
		return errors.New("wake parent exact provider identity unavailable")
	}
	if fresh {
		if !egg.NativePromptReady(view) || w.RetryAfter > runtime.Now().Unix() {
			return nil
		}
		text, err := conversationWakeText(s.Cfg, c, w)
		if err != nil {
			return err
		}
		if err = db.BindConversationWake(w, target, view.ProviderSessionID, text, runtime.Now().Unix()); err != nil {
			return err
		}
	} else if view.ProviderSessionID != w.ProviderSessionID {
		return errors.New("unknown wake target changed; reconcile its original native receipt before proceeding")
	}
	// promptSession retains the existing exact provider check and single writer
	// lease. Reconciliation on a busy original target never enqueues more input.
	result, promptErr := runtime.Prompt(ctx, session, w.RequestID, w.Input)
	status, reason, receipt := "unconfirmed", "delivery unknown; inspect original request without resending", int64(0)
	if promptErr == nil && result.Status == "native_receipt_observed" && result.NativeReceiptObserved {
		status, reason, receipt = "observed", "exact native user text receipt; causal provider request acknowledgement remains unverified", result.ReceiptCursor
	} else if promptErr == nil && result.Status == "not_sent" && result.DefinitelyNotSent {
		status, reason = "not_sent", "typed transport proof: no input attempted"
	}
	return db.RecordConversationWake(w, status, reason, receipt, runtime.Now().Unix())
}

// Started by the existing host wing daemon. No provider process, credentials,
// grants, permission replies, or new transport listeners are created here.
func RunConversationWakeController(version string, ctx context.Context, cfg *config.Config, policy func() (*config.WingConfig, bool)) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	after := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		wc, shared := policy()
		if wc.Org != "" || shared || wc.Locked {
			continue
		}
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
			continue
		}
		db, err := store.Open(cfg.DBPath())
		if err != nil {
			log.Printf("conversation wake inventory: %v", err)
			continue
		}
		roots, err := db.ConversationWakeRoots(after, 4)
		_ = db.Close()
		if err != nil {
			log.Printf("conversation wake inventory: %v", err)
			continue
		}
		if len(roots) == 0 {
			after = ""
			continue
		}
		for _, c := range roots {
			if ctx.Err() != nil {
				return
			}
			after = c.ID
			paths := wingpolicy.CanonicalPaths(wc.Paths.Strings())
			// This private helper only reconciles owned artifacts and the opted
			// root prompt. It is never registered as a general MCP grant server.
			s := &Server{Version: version, Cfg: cfg, Principal: c.OwnerID, Logs: os.Stderr, allowedPaths: paths, enforcePathBounds: len(paths) > 0}
			step, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = processConversationWake(step, s, c.ID, nativeConversationWakeRuntime(cfg))
			cancel()
			if err != nil {
				log.Printf("conversation wake %s: %v", c.ID, err)
			}
		}
	}
}
