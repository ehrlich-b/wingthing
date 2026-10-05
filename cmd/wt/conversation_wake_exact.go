package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/ehrlich-b/wingthing/internal/store"
)

// resolveExactWakeTarget resolves a wake destination that the host recorded
// itself: the root's current execution for a fresh wake, or the immutable
// execution bound to an unknown-delivery request. Unlike the human selectors in
// resolveLifecycleSession it never matches a name or ID prefix, so a retired
// execution such as a1b2c3d4 cannot alias a surviving a1b2c3d4e5f60718 or a
// session named a1b2c3d4. It fails closed before any native read when the ID
// is not a recorded execution of root, its directory is missing or is not a
// real directory (a symlink is refused, not followed), or the caller does not
// own it within its path bounds.
//
// Known limit, deliberately not recovered here: BindConversationWake commits
// the request before SubmitSessionPrompt persists its reservation. A crash in
// between leaves a pending binding with no reservation, which proves that
// request was never sent. If that exact execution is still ready, the next step
// reserves and sends the same request once. If its directory is gone, the row
// stays pending here. If it is retained but not ready, the readiness error is
// recorded as unconfirmed. Either way this root's single outstanding wake then
// blocks every later child event. Nothing rebinds it, and retry_not_sent
// refuses it, because the outbox holds no typed no-input evidence.
func (s *localMCPServer) resolveExactWakeTarget(db *store.Store, c *store.Conversation, id string) (localSession, error) {
	if err := validateSessionID(id); err != nil {
		return localSession{}, errors.New("wake target is not an exact execution ID")
	}
	executions, err := db.ConversationExecutions(c.ID)
	if err != nil {
		return localSession{}, err
	}
	if !slices.Contains(executions, id) {
		return localSession{}, errors.New("wake target is not a recorded execution of this root conversation")
	}
	dir := filepath.Join(s.cfg.Dir, "eggs", id)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
		return localSession{}, errors.New("wake target execution not found; its original binding is retained")
	}
	if err != nil {
		return localSession{}, err
	}
	meta := readEggMetaValues(dir)
	pid, _ := readAliveEggPID(dir)
	session := localSession{ID: id, Name: readSessionName(dir), Principal: readSessionPrincipal(dir), Agent: meta["agent"], Kind: meta["kind"], CWD: meta["cwd"], PID: pid}
	if !s.ownsSession(session) || (s.enforcePathBounds && (len(s.allowedPaths) == 0 || !isUnderPaths(canonicalSessionPath(session.CWD), s.allowedPaths))) {
		return localSession{}, errors.New("session not found or not owned by caller")
	}
	return session, nil
}
