package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
	"golang.org/x/sys/unix"
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
// A missing directory retains the binding: losing the execution's artifacts
// cannot prove that input was never attempted.
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

// Called only after resolving the retained original execution. SubmitSessionPrompt
// holds this same lock and durably reserves before sending; absence under the
// lock proves that the binding-before-reservation crash did not attempt input.
// Any existing artifact, including unreadable or malformed evidence, prevents
// rebinding. Persist the proof before releasing the lock.
func reconcileUnreservedConversationWake(ctx context.Context, cfg *config.Config, db *store.Store, w *store.ConversationWake, now int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(filepath.Join(cfg.Dir, "eggs", w.SessionID))
	if err != nil {
		return false, err
	}
	defer cmdutil.CloseWithLog("wake original execution", root)
	lock, err := root.OpenFile("prompt.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
		// Match the prompt writer's Darwin first-creation race handling without
		// replacing an existing lock inode.
		lock, err = root.OpenFile("prompt.lock", os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	}
	if err != nil {
		return false, err
	}
	defer cmdutil.CloseWithLog("wake original prompt lock", lock)
	info, statErr := lock.Stat()
	named, namedErr := root.Lstat("prompt.lock")
	if statErr != nil || namedErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, named) {
		return false, errors.New("original prompt lock is not a bound regular file")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return false, err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	key := sha256.Sum256([]byte(w.RequestID))
	if _, err := root.Lstat(fmt.Sprintf("prompt.%x.json", key)); !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	err = db.RecordConversationWake(w, "not_sent", "prompt reservation absent under original execution's prompt lock; no input attempted", 0, now)
	return err == nil, err
}
