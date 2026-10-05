package main

// Real OS process crash/restart acceptance for personal conversation wake.
//
// Every controller here is a separate OS process: this test binary re-executed
// in TestOpusProcessRecoveryHelperProcess with an explicit helper mode, a
// minimal synthetic environment and a temp HOME under one owned root. The
// parent SIGKILLs only the exact *exec.Cmd.Process it started, after verifying
// from durable files that the named boundary was reached, and then reopens the
// same store, outbox, prompt reservations, locks and checkpoint from a new PID.
//
// Real: OS processes, SIGKILL, kernel flock release, SQLite/WAL recovery,
// runConversationWakeController and nativeConversationWakeRuntime (busy phase),
// processConversationWake, store wake/checkpoint CAS, egg.SubmitSessionPrompt
// reservations, the native lifecycle journal and hook/transcript importers,
// toolConversationRead/toolConversationCheckpoint.
// Synthetic: the provider. Providers are benign stub processes (this binary
// blocked on stdin) whose native evidence is written as Claude hook spool files
// and transcript rows; the PTY input transport is a durable test-owned ledger
// that appends the exact native user row a provider would record. No model,
// credential, network or vendor CLI is involved.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

const (
	opusPROwner         = "opus-process-owner"
	opusPRCWD           = "/opusprocessrecovery/workspace"
	opusPRRootID        = "opus-root"
	opusPRChildID       = "opus-child"
	opusPRRootExec      = "opus-exec-root"
	opusPRChildExec     = "opus-exec-child"
	opusPRRootProvider  = "opus-provider-root"
	opusPRChildProvider = "opus-provider-child"
	opusPRHelperTest    = "TestOpusProcessRecoveryHelperProcess"
	// Same receipt wait bound as nativeConversationWakeRuntime; it is part of
	// the reserved prompt spec, so every controller process must agree on it.
	opusPRWakeTimeout = 500 * time.Millisecond
)

type opusPRReport struct {
	PID                 int      `json:"pid"`
	Mode                string   `json:"mode,omitempty"`
	EnvKeys             []string `json:"env_keys,omitempty"`
	Boundary            string   `json:"boundary,omitempty"`
	RequestID           string   `json:"request_id,omitempty"`
	Session             string   `json:"session,omitempty"`
	Error               string   `json:"error,omitempty"`
	ExpectedRevision    int64    `json:"expected_revision"`
	AfterCursor         int64    `json:"after_cursor"`
	StaleError          string   `json:"stale_error,omitempty"`
	RecoveredRevision   int64    `json:"recovered_revision"`
	RecoveredCursor     int64    `json:"recovered_cursor"`
	RecoveredCheckpoint string   `json:"recovered_checkpoint,omitempty"`
	RegressError        string   `json:"regress_error,omitempty"`
	ReplaySequences     []int64  `json:"replay_sequences,omitempty"`
	AdvanceError        string   `json:"advance_error,omitempty"`
	FinalRevision       int64    `json:"final_revision"`
	FinalCursor         int64    `json:"final_cursor"`
}

type opusPRLedgerEntry struct {
	PID         int    `json:"pid"`
	Phase       string `json:"phase"`
	RequestID   string `json:"request_id"`
	InputSHA256 string `json:"input_sha256"`
}

type opusPROutbox struct {
	EventSequence     int64
	Attempt           int
	RequestID         string
	SessionID         string
	ProviderSessionID string
	Input             string
	Status            string
	ReceiptCursor     int64
}

type opusPRReservation struct {
	Version           int                     `json:"version"`
	RequestID         string                  `json:"request_id"`
	ProviderSessionID string                  `json:"provider_session_id"`
	Phase             string                  `json:"phase"`
	Result            egg.SessionPromptResult `json:"result"`
}

// ---------------------------------------------------------------------------
// Subprocess side.

type opusPRHelper struct {
	root, state, ledger, transcript string
	cfg                             *config.Config
}

func TestOpusProcessRecoveryHelperProcess(t *testing.T) {
	mode := os.Getenv("OPUS_PR_HELPER_MODE")
	if mode == "" {
		t.Skip("subprocess entry point; runs only when an owning TestOpusProcessRecovery parent sets OPUS_PR_HELPER_MODE")
	}
	h := opusPRLoadHelper(t)
	var keys []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if mode != "stub-provider" {
		// The parent owns stdin. EOF means it released or lost this process.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); os.Exit(97) }()
	}
	opusPREmit("OPUS_PR_READY", opusPRReport{Mode: mode, EnvKeys: keys})
	switch mode {
	case "stub-provider":
		// Benign local stand-in for a provider process: alive until released.
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "controller-loop":
		// The production daemon loop with its production runtime, personal policy.
		runConversationWakeController(context.Background(), h.cfg, func() (*config.WingConfig, bool) { return &config.WingConfig{}, false })
	case "wake-step":
		opusPRWakeStep(h, os.Getenv("OPUS_PR_BOUNDARY"))
	case "checkpoint-commit":
		opusPRCheckpointCommit(h)
	case "checkpoint-recover":
		opusPRCheckpointRecover(h)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func opusPRLoadHelper(t *testing.T) opusPRHelper {
	root := os.Getenv("OPUS_PR_ROOT")
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatalf("helper root must be an absolute owned temp path, got %q", root)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("helper root unavailable: %v", err)
	}
	if os.Getenv("HOME") != filepath.Join(root, "home") || os.Getenv("TMPDIR") != filepath.Join(root, "tmp") {
		t.Fatal("helper HOME/TMPDIR must be the owned temp root's synthetic home")
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HOME" && k != "TMPDIR" && !strings.HasPrefix(k, "OPUS_PR_") {
			t.Fatalf("helper inherited unexpected environment key %q", k)
		}
	}
	h := opusPRHelper{root: root, state: os.Getenv("OPUS_PR_STATE"), ledger: os.Getenv("OPUS_PR_LEDGER"), transcript: os.Getenv("OPUS_PR_TRANSCRIPT")}
	for _, path := range []string{h.state, h.ledger, h.transcript} {
		rel, err := filepath.Rel(root, path)
		if !filepath.IsAbs(path) || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("helper path %q is outside the owned root", path)
		}
	}
	h.cfg = &config.Config{Dir: h.state}
	return h
}

func opusPREmit(tag string, r opusPRReport) {
	r.PID = os.Getpid()
	data, _ := json.Marshal(r)
	_, _ = fmt.Fprintf(os.Stdout, "%s %s\n", tag, data)
}

// opusPRReach announces a boundary and waits for the owning parent's SIGKILL.
// Deferred locks, store handles and journals are deliberately never released.
func opusPRReach(r opusPRReport) {
	opusPREmit("OPUS_PR_BOUNDARY", r)
	opusPRPark()
}

func opusPRPark() {
	time.Sleep(80 * time.Second) // orphan bound only; the parent kills long before
	os.Exit(98)
}

func opusPRErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func opusPRAppendDurable(path string, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// One production processConversationWake step. Only the PTY transport is
// replaced (as promptSession would compose it, but without gRPC to an egg).
func opusPRWakeStep(h opusPRHelper, boundary string) {
	s := &localMCPServer{cfg: h.cfg, principal: opusPROwner}
	runtime := nativeConversationWakeRuntime(h.cfg)
	runtime.Prompt = func(ctx context.Context, session localSession, id, text string) (egg.SessionPromptResult, error) {
		if boundary == "bound_before_reservation" {
			opusPRReach(opusPRReport{Boundary: boundary, RequestID: id, Session: session.ID})
		}
		dir := filepath.Join(h.cfg.Dir, "eggs", session.ID)
		provider := readEggMetaValues(dir)["provider_session_id"]
		return egg.SubmitSessionPrompt(ctx, dir, egg.SessionPromptOptions{RequestID: id, Input: text, Timeout: opusPRWakeTimeout,
			Read: func(ctx context.Context, after int64, limit int) (egg.SessionView, error) {
				if err := ctx.Err(); err != nil {
					return egg.SessionView{}, err
				}
				return lifecycleViewForSession(h.cfg, session, after, limit)
			},
			Send: func(_ context.Context, input string) (egg.PromptDelivery, error) {
				return opusPRSyntheticTransport(h, boundary, id, provider, input)
			},
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := processConversationWake(ctx, s, opusPRRootID, runtime)
	opusPREmit("OPUS_PR_RESULT", opusPRReport{Mode: "wake-step", Error: opusPRErr(err)})
}

// The synthetic provider transport: a durable ledger of attempts, plus the
// exact native human user row the provider transcript would contain.
func opusPRSyntheticTransport(h opusPRHelper, boundary, requestID, provider, input string) (egg.PromptDelivery, error) {
	if provider != opusPRRootProvider {
		return egg.PromptDelivery{NoInputAttempted: true}, errors.New("synthetic transport serves only the fixture root provider")
	}
	digest := sha256.Sum256([]byte(input))
	entry := opusPRLedgerEntry{PID: os.Getpid(), Phase: "send_enter", RequestID: requestID, InputSHA256: hex.EncodeToString(digest[:])}
	if err := opusPRAppendDurable(h.ledger, entry); err != nil {
		return egg.PromptDelivery{NoInputAttempted: true}, err
	}
	if boundary == "reserved_before_transport" {
		opusPRReach(opusPRReport{Boundary: boundary, RequestID: requestID})
	}
	native := map[string]any{"type": "user", "sessionId": provider, "message": map[string]any{"role": "user", "content": input}}
	if err := opusPRAppendDurable(h.transcript, native); err != nil {
		return egg.PromptDelivery{}, err
	}
	entry.Phase = "delivered"
	if err := opusPRAppendDurable(h.ledger, entry); err != nil {
		return egg.PromptDelivery{BytesEnqueued: len(input)}, err
	}
	if boundary == "transport_delivered_before_outcome" {
		opusPRReach(opusPRReport{Boundary: boundary, RequestID: requestID})
	}
	return egg.PromptDelivery{BytesEnqueued: len(input)}, nil
}

func opusPRRead(s *localMCPServer, after int64, limit int) (map[string]any, error) {
	args, _ := json.Marshal(map[string]any{"conversation_id": opusPRRootID, "after_cursor": after, "limit": limit})
	return s.toolConversationRead(context.Background(), args)
}

func opusPRCheckpoint(s *localMCPServer, expected, after int64, text string) (map[string]any, error) {
	args, _ := json.Marshal(map[string]any{"conversation_id": opusPRRootID, "expected_revision": expected, "after_cursor": after, "checkpoint": text})
	return s.toolConversationCheckpoint(args)
}

func opusPRCheckpointCommit(h opusPRHelper) {
	s := &localMCPServer{cfg: h.cfg, principal: opusPROwner}
	read, err := opusPRRead(s, 0, 1)
	if err != nil {
		opusPREmit("OPUS_PR_RESULT", opusPRReport{Error: err.Error()})
		return
	}
	c := read["conversation"].(*store.Conversation)
	next := read["next_cursor"].(int64)
	opusPREmit("OPUS_PR_BOUNDARY", opusPRReport{Boundary: "checkpoint_invoked_reply_withheld", ExpectedRevision: c.Revision, AfterCursor: next})
	if _, err = opusPRCheckpoint(s, c.Revision, next, os.Getenv("OPUS_PR_CHECKPOINT")); err != nil {
		opusPREmit("OPUS_PR_RESULT", opusPRReport{Error: err.Error()})
		return
	}
	// The CAS committed; its reply is never emitted to the waiting caller.
	opusPRPark()
}

func opusPRCheckpointRecover(h opusPRHelper) {
	s := &localMCPServer{cfg: h.cfg, principal: opusPROwner}
	text := os.Getenv("OPUS_PR_CHECKPOINT")
	expected, _ := strconv.ParseInt(os.Getenv("OPUS_PR_EXPECTED"), 10, 64)
	after, _ := strconv.ParseInt(os.Getenv("OPUS_PR_AFTER"), 10, 64)
	wakeEvent, _ := strconv.ParseInt(os.Getenv("OPUS_PR_WAKE_EVENT"), 10, 64)
	report := opusPRReport{Mode: "checkpoint-recover"}
	// A caller that never saw the reply first retries its identical request.
	_, err := opusPRCheckpoint(s, expected, after, text)
	report.StaleError = opusPRErr(err)
	read, err := opusPRRead(s, 0, 1)
	if err != nil {
		report.Error = err.Error()
		opusPREmit("OPUS_PR_RESULT", report)
		return
	}
	c := read["conversation"].(*store.Conversation)
	report.RecoveredRevision, report.RecoveredCursor, report.RecoveredCheckpoint = c.Revision, c.DeliveredCursor, c.Checkpoint
	_, err = opusPRCheckpoint(s, c.Revision, max(c.DeliveredCursor-1, 0), "opus regress attempt")
	report.RegressError = opusPRErr(err)
	replay, err := opusPRRead(s, c.DeliveredCursor, 100)
	if err != nil {
		report.Error = err.Error()
		opusPREmit("OPUS_PR_RESULT", report)
		return
	}
	for _, event := range replay["events"].([]store.ConversationEvent) {
		report.ReplaySequences = append(report.ReplaySequences, event.Sequence)
	}
	_, err = opusPRCheckpoint(s, c.Revision, wakeEvent, text+"; handled wake event after restart")
	report.AdvanceError = opusPRErr(err)
	final, err := opusPRRead(s, 0, 1)
	if err != nil {
		report.Error = err.Error()
	} else {
		fc := final["conversation"].(*store.Conversation)
		report.FinalRevision, report.FinalCursor = fc.Revision, fc.DeliveredCursor
	}
	opusPREmit("OPUS_PR_RESULT", report)
}

// ---------------------------------------------------------------------------
// Parent side: owned temp world and owned process handles.

type opusPRWorld struct {
	t          *testing.T
	base       string
	home       string
	tmp        string
	cfg        *config.Config
	ledger     string
	transcript string
	exe        string
	hooks      int
}

func opusPRNewWorld(t *testing.T) *opusPRWorld {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := &opusPRWorld{t: t, base: base, home: filepath.Join(base, "home"), tmp: filepath.Join(base, "tmp"), cfg: &config.Config{Dir: filepath.Join(base, "state")}, ledger: filepath.Join(base, "transport-ledger.jsonl"), exe: exe}
	// Claude project directory for a cwd made only of '/' and lowercase letters.
	w.transcript = filepath.Join(w.home, egg.Profile("claude").SessionDir, strings.ReplaceAll(opusPRCWD, "/", "-"), opusPRRootProvider+".jsonl")
	for _, dir := range []string{w.home, w.tmp, w.cfg.Dir, filepath.Dir(w.transcript)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(w.transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(w.cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, parent, exec, provider string }{{opusPRRootID, "", opusPRRootExec, opusPRRootProvider}, {opusPRChildID, opusPRRootID, opusPRChildExec, opusPRChildProvider}} {
		if _, _, err := db.ReserveConversation(store.Conversation{ID: c.id, ParentID: c.parent, OwnerID: opusPROwner, SessionID: c.exec, LaunchKey: "opus-launch-" + c.id, SpecDigest: "opus-spec-" + c.id, Agent: "claude", CWD: opusPRCWD, WingID: "opus-wing"}); err != nil {
			t.Fatal(err)
		}
		dir := w.eggDir(c.exec)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		meta := "agent=claude\nkind=agent\ncwd=" + opusPRCWD + "\nprovider_session_id=" + c.provider + "\nprovider_home=" + w.home + "\n"
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeSessionPrincipal(dir, opusPROwner); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetConversationWake(opusPROwner, opusPRRootID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *opusPRWorld) eggDir(exec string) string { return filepath.Join(w.cfg.Dir, "eggs", exec) }

func (w *opusPRWorld) wakeLockPath() string {
	digest := sha256.Sum256([]byte(opusPRRootID))
	return filepath.Join(w.cfg.Dir, fmt.Sprintf("wake-%x.lock", digest[:8]))
}

func (w *opusPRWorld) writePID(exec string, pid int) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.eggDir(exec), "egg.pid"), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// hook publishes a native Claude hook record the way the installed hook
// command does (complete file, then rename to *.json).
func (w *opusPRWorld) hook(exec, provider, event string, extra map[string]any) {
	w.t.Helper()
	w.hooks++
	dir := filepath.Join(w.home, ".claude", "wingthing-events", exec)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		w.t.Fatal(err)
	}
	payload := map[string]any{"session_id": provider, "hook_event_name": event}
	for k, v := range extra {
		payload[k] = v
	}
	data, _ := json.Marshal(payload)
	path := filepath.Join(dir, fmt.Sprintf("event.%04d", w.hooks))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
	if err := os.Rename(path, path+".json"); err != nil {
		w.t.Fatal(err)
	}
}

func (w *opusPRWorld) withStore(fn func(*store.Store)) {
	w.t.Helper()
	db, err := store.Open(w.cfg.DBPath())
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fn(db)
}

func (w *opusPRWorld) outbox() []opusPROutbox {
	w.t.Helper()
	var out []opusPROutbox
	w.withStore(func(db *store.Store) {
		rows, err := db.DB().Query(`SELECT event_sequence,attempt,request_id,session_id,provider_session_id,input,status,receipt_cursor FROM conversation_wake_outbox WHERE root_id=? ORDER BY event_sequence`, opusPRRootID)
		if err != nil {
			w.t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var o opusPROutbox
			if err := rows.Scan(&o.EventSequence, &o.Attempt, &o.RequestID, &o.SessionID, &o.ProviderSessionID, &o.Input, &o.Status, &o.ReceiptCursor); err != nil {
				w.t.Fatal(err)
			}
			out = append(out, o)
		}
		if err := rows.Err(); err != nil {
			w.t.Fatal(err)
		}
	})
	return out
}

func (w *opusPRWorld) onlyOutbox() opusPROutbox {
	w.t.Helper()
	rows := w.outbox()
	if len(rows) != 1 {
		w.t.Fatalf("expected exactly one wake outbox row, got %#v", rows)
	}
	return rows[0]
}

func (w *opusPRWorld) pending() *store.ConversationWake {
	w.t.Helper()
	var p *store.ConversationWake
	w.withStore(func(db *store.Store) {
		var err error
		if p, err = db.PendingConversationWake(opusPRRootID); err != nil {
			w.t.Fatal(err)
		}
	})
	return p
}

func (w *opusPRWorld) policy() store.ConversationWakePolicy {
	w.t.Helper()
	var p store.ConversationWakePolicy
	w.withStore(func(db *store.Store) {
		var err error
		if p, err = db.ConversationWakePolicy(opusPRRootID); err != nil {
			w.t.Fatal(err)
		}
	})
	return p
}

func (w *opusPRWorld) conversation() *store.Conversation {
	w.t.Helper()
	var c *store.Conversation
	w.withStore(func(db *store.Store) {
		var err error
		if c, err = db.GetConversation(opusPROwner, opusPRRootID); err != nil {
			w.t.Fatal(err)
		}
	})
	return c
}

func (w *opusPRWorld) countEvents(session, state, source string) int {
	w.t.Helper()
	var n int
	w.withStore(func(db *store.Store) {
		if err := db.DB().QueryRow(`SELECT COUNT(*) FROM conversation_events WHERE session_id=? AND state=? AND state_source=?`, session, state, source).Scan(&n); err != nil {
			w.t.Fatal(err)
		}
	})
	return n
}

func (w *opusPRWorld) ledgerEntries() []opusPRLedgerEntry {
	w.t.Helper()
	data, err := os.ReadFile(w.ledger)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var out []opusPRLedgerEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e opusPRLedgerEntry
		if err := json.Unmarshal(line, &e); err != nil {
			w.t.Fatalf("corrupt transport ledger line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// nativeUserRecords counts exact-provider native human rows equal to input.
func (w *opusPRWorld) nativeUserRecords(input string) int {
	w.t.Helper()
	data, err := os.ReadFile(w.transcript)
	if err != nil {
		w.t.Fatal(err)
	}
	n := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		var r struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Message   struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &r) == nil && r.Type == "user" && r.SessionID == opusPRRootProvider && r.Message.Role == "user" && r.Message.Content == input {
			n++
		}
	}
	return n
}

func (w *opusPRWorld) reservationFiles() []string {
	w.t.Helper()
	entries, err := os.ReadDir(w.eggDir(opusPRRootExec))
	if err != nil {
		w.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "prompt.") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	return names
}

func (w *opusPRWorld) reservation(requestID string) (opusPRReservation, bool) {
	w.t.Helper()
	key := sha256.Sum256([]byte(requestID))
	data, err := os.ReadFile(filepath.Join(w.eggDir(opusPRRootExec), "prompt."+hex.EncodeToString(key[:])+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return opusPRReservation{}, false
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var r opusPRReservation
	if err := json.Unmarshal(data, &r); err != nil {
		w.t.Fatal(err)
	}
	return r, true
}

func (w *opusPRWorld) stat(path string) os.FileInfo {
	w.t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		w.t.Fatal(err)
	}
	return info
}

func (w *opusPRWorld) sameFile(label, path string, before os.FileInfo) {
	w.t.Helper()
	after := w.stat(path)
	if !os.SameFile(before, after) {
		w.t.Fatalf("%s %s was replaced across controller restart", label, path)
	}
	w.t.Logf("OPUS_PR_RECEIPT reopened %s inode=%d (unchanged, not deleted or recreated)", label, opusPRInode(after))
}

func opusPRInode(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// opusPRFlockFree probes a lock file from this (parent) process, a different
// open file description and a different PID than any helper.
func opusPRFlockFree(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return true
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false
	}
	t.Fatalf("flock probe %s: %v", path, err)
	return false
}

func (w *opusPRWorld) eventually(what string, timeout time.Duration, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type opusPRSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *opusPRSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() < 1<<20 {
		s.buf.Write(p)
	}
	return len(p), nil
}

func (s *opusPRSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type opusPRProc struct {
	t     *testing.T
	role  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   opusPRSink
	errs  opusPRSink
	done  chan struct{}
}

func (w *opusPRWorld) start(role, mode string, extra ...string) *opusPRProc {
	t := w.t
	t.Helper()
	cmd := exec.Command(w.exe, "-test.run=^"+opusPRHelperTest+"$", "-test.count=1", "-test.timeout=90s")
	cmd.Dir = w.base
	// Minimal synthetic environment: nothing is forwarded from the parent.
	cmd.Env = append([]string{"HOME=" + w.home, "TMPDIR=" + w.tmp, "OPUS_PR_HELPER_MODE=" + mode, "OPUS_PR_ROOT=" + w.base, "OPUS_PR_STATE=" + w.cfg.Dir, "OPUS_PR_LEDGER=" + w.ledger, "OPUS_PR_TRANSCRIPT=" + w.transcript}, extra...)
	p := &opusPRProc{t: t, role: role, cmd: cmd, done: make(chan struct{})}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdin = stdin
	cmd.Stdout, cmd.Stderr = &p.out, &p.errs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(p.release)
	ready := p.await("OPUS_PR_READY", 20*time.Second)
	if ready.PID != cmd.Process.Pid {
		t.Fatalf("helper reported pid %d, owned handle is %d", ready.PID, cmd.Process.Pid)
	}
	for _, key := range ready.EnvKeys {
		if key != "HOME" && key != "TMPDIR" && !strings.HasPrefix(key, "OPUS_PR_") {
			t.Fatalf("helper environment leaked %q", key)
		}
	}
	t.Logf("OPUS_PR_RECEIPT spawn role=%s mode=%s pid=%d env_keys=%v", role, mode, cmd.Process.Pid, ready.EnvKeys)
	return p
}

func (p *opusPRProc) pid() int { return p.cmd.Process.Pid }

func (p *opusPRProc) find(tag string) (opusPRReport, bool) {
	for _, line := range strings.Split(p.out.String(), "\n") {
		if payload, ok := strings.CutPrefix(line, tag+" "); ok {
			var r opusPRReport
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				p.t.Fatalf("%s: bad %s payload %q", p.role, tag, payload)
			}
			return r, true
		}
	}
	return opusPRReport{}, false
}

func (p *opusPRProc) await(tag string, timeout time.Duration) opusPRReport {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if r, ok := p.find(tag); ok {
			return r
		}
		select {
		case <-p.done:
			if r, ok := p.find(tag); ok {
				return r
			}
			p.t.Fatalf("%s pid=%d exited (%v) before %s\nstdout:\n%s\nstderr:\n%s", p.role, p.pid(), p.cmd.ProcessState, tag, p.out.String(), p.errs.String())
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("%s pid=%d: no %s within %s\nstdout:\n%s\nstderr:\n%s", p.role, p.pid(), tag, timeout, p.out.String(), p.errs.String())
		}
	}
}

func (p *opusPRProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// kill sends SIGKILL to exactly this owned process and reaps it.
func (p *opusPRProc) kill(boundary string) {
	p.t.Helper()
	if p.exited() {
		p.t.Fatalf("%s pid=%d exited before the %s kill\nstdout:\n%s\nstderr:\n%s", p.role, p.pid(), boundary, p.out.String(), p.errs.String())
	}
	pid := p.pid()
	if err := p.cmd.Process.Kill(); err != nil {
		p.t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("%s pid=%d not reaped after SIGKILL", p.role, pid)
	}
	status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		p.t.Fatalf("%s pid=%d did not die by SIGKILL: %v", p.role, pid, p.cmd.ProcessState)
	}
	if _, ok := p.find("OPUS_PR_RESULT"); ok {
		p.t.Fatalf("%s pid=%d completed its step before the %s kill", p.role, pid, boundary)
	}
	p.t.Logf("OPUS_PR_RECEIPT sigkill role=%s pid=%d boundary=%s wait_status=%q", p.role, pid, boundary, p.cmd.ProcessState.String())
}

func (p *opusPRProc) finish() opusPRReport {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		p.t.Fatalf("%s pid=%d did not finish\nstdout:\n%s\nstderr:\n%s", p.role, p.pid(), p.out.String(), p.errs.String())
	}
	if !p.cmd.ProcessState.Success() {
		p.t.Fatalf("%s pid=%d failed: %v\nstdout:\n%s\nstderr:\n%s", p.role, p.pid(), p.cmd.ProcessState, p.out.String(), p.errs.String())
	}
	r, ok := p.find("OPUS_PR_RESULT")
	if !ok {
		p.t.Fatalf("%s pid=%d exited without a result\nstdout:\n%s", p.role, p.pid(), p.out.String())
	}
	p.t.Logf("OPUS_PR_RECEIPT exit role=%s pid=%d status=%q error=%q", p.role, p.pid(), p.cmd.ProcessState.String(), r.Error)
	return r
}

// release is the bounded cleanup path: EOF first, then SIGKILL of this exact
// handle. The Wait goroutine always returns once the process is reaped.
func (p *opusPRProc) release() {
	if p.exited() {
		return
	}
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		p.t.Errorf("owned helper %s pid=%d was not reaped", p.role, p.pid())
	}
}

func opusPRRequireSends(t *testing.T, entries []opusPRLedgerEntry, pid int, request string, phases ...string) {
	t.Helper()
	if len(entries) != len(phases) {
		t.Fatalf("transport ledger %#v, want phases %v from pid %d", entries, phases, pid)
	}
	for i, e := range entries {
		if e.PID != pid || e.RequestID != request || e.Phase != phases[i] {
			t.Fatalf("transport ledger %#v, want phases %v from pid %d request %s", entries, phases, pid, request)
		}
	}
}

// ---------------------------------------------------------------------------
// Acceptance tests.

func TestOpusProcessRecoveryQueuedWakeSurvivesKilledControllerLoop(t *testing.T) {
	w := opusPRNewWorld(t)
	rootStub := w.start("root-provider-stub", "stub-provider")
	w.writePID(opusPRRootExec, rootStub.pid())
	childStub := w.start("child-provider-stub", "stub-provider")
	w.writePID(opusPRChildExec, childStub.pid())
	w.hook(opusPRRootExec, opusPRRootProvider, "SessionStart", nil)
	w.hook(opusPRRootExec, opusPRRootProvider, "PermissionRequest", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "SessionStart", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "UserPromptSubmit", map[string]any{"prompt": "benign fixture task"})

	rootSession := localSession{ID: opusPRRootExec, Agent: "claude", CWD: opusPRCWD}
	childSession := localSession{ID: opusPRChildExec, Agent: "claude", CWD: opusPRCWD}
	rootView, err := lifecycleViewForSession(w.cfg, rootSession, 0, 200)
	if err != nil || !rootView.ProcessAlive || rootView.State != "needs_input" || egg.NativePromptReady(rootView) {
		t.Fatalf("root fixture should be a live parent awaiting a human: %+v %v", rootView, err)
	}
	childView, err := lifecycleViewForSession(w.cfg, childSession, 0, 200)
	if err != nil || !childView.ProcessAlive || childView.State != "working" {
		t.Fatalf("child stub should be a live working child: %+v %v", childView, err)
	}
	// Owned child lifecycle: the real child stub process dies abruptly and its
	// exit is journaled through the production process-event API (this test
	// plays the egg supervisor; the child model itself is simulated).
	childStub.kill("child_provider_runtime_exit")
	const childReason = "opus fixture: owned benign child stub received SIGKILL"
	if err := egg.RecordSessionProcessEvent(w.eggDir(opusPRChildExec), "session_exit", "stopped", childReason); err != nil {
		t.Fatal(err)
	}
	childView, err = lifecycleViewForSession(w.cfg, childSession, 0, 200)
	if err != nil || childView.ProcessAlive || childView.State != "stopped" || childView.StateSource != "egg_process" {
		t.Fatalf("child exit not journaled: %+v %v", childView, err)
	}
	dbInfo := w.stat(w.cfg.DBPath())

	// Production daemon loop process #1 persists the child completion and its
	// wake outbox row, then is killed while the parent still needs input.
	loop1 := w.start("controller-loop-1", "controller-loop")
	var queued *store.ConversationWake
	w.eventually("production controller loop to queue the child exit", 15*time.Second, func() bool {
		if loop1.exited() {
			t.Fatalf("controller loop exited\nstdout:\n%s\nstderr:\n%s", loop1.out.String(), loop1.errs.String())
		}
		queued = w.pending()
		return queued != nil
	})
	if queued.Status != "queued" || queued.Attempt != 0 || queued.RequestID != "" || queued.Event.SessionID != opusPRChildExec || queued.Event.ConversationID != opusPRChildID || queued.Event.State != "stopped" || queued.Event.StateSource != "egg_process" || queued.Event.Type != "session_exit" {
		t.Fatalf("committed wake row %#v", queued)
	}
	if len(w.ledgerEntries()) != 0 || len(w.reservationFiles()) != 0 {
		t.Fatal("busy parent received input")
	}
	t.Logf("OPUS_PR_RECEIPT boundary=queued_parent_needs_input persisted outbox seq=%d status=%s scan_cursor=%d", queued.EventSequence, queued.Status, w.policy().ScanCursor)
	loop1.kill("queued_parent_needs_input")

	// Parent becomes busy in a different native way; a new loop process resumes.
	w.hook(opusPRRootExec, opusPRRootProvider, "UserPromptSubmit", map[string]any{"prompt": "human keeps parent busy"})
	loop2 := w.start("controller-loop-2", "controller-loop")
	if loop2.pid() == loop1.pid() {
		t.Fatal("restart reused the killed pid")
	}
	w.eventually("restarted loop to reconcile the working parent", 15*time.Second, func() bool {
		if loop2.exited() {
			t.Fatalf("controller loop exited\nstderr:\n%s", loop2.errs.String())
		}
		return w.countEvents(opusPRRootExec, "working", "claude_hook") > 0
	})
	loop2.kill("parent_working_reconciled")
	w.sameFile("store wt.db", w.cfg.DBPath(), dbInfo)

	step := w.start("wake-step-parent-working", "wake-step")
	if r := step.finish(); r.Error != "" {
		t.Fatalf("busy step: %s", r.Error)
	}
	still := w.pending()
	if still == nil || still.EventSequence != queued.EventSequence || still.Status != "queued" || still.Attempt != 0 || len(w.ledgerEntries()) != 0 || len(w.reservationFiles()) != 0 {
		t.Fatalf("queued wake lost or sent while parent busy: %#v ledger=%v", still, w.ledgerEntries())
	}

	// Native idle: a fresh controller process delivers under existing policy.
	w.hook(opusPRRootExec, opusPRRootProvider, "Notification", map[string]any{"notification_type": "idle_prompt"})
	deliver := w.start("wake-step-parent-idle", "wake-step")
	if r := deliver.finish(); r.Error != "" {
		t.Fatalf("idle delivery: %s", r.Error)
	}
	row := w.onlyOutbox()
	if row.Status != "observed" || row.Attempt != 1 || row.EventSequence != queued.EventSequence || row.SessionID != opusPRRootExec || row.ProviderSessionID != opusPRRootProvider {
		t.Fatalf("delivered row %#v", row)
	}
	opusPRRequireSends(t, w.ledgerEntries(), deliver.pid(), row.RequestID, "send_enter", "delivered")
	if w.nativeUserRecords(row.Input) != 1 {
		t.Fatal("native receipt row missing")
	}
	for _, want := range []string{`"source":"egg_process"`, `"state":"stopped"`, `"child_provider_identity_known":true`, `"child_provider_session_id":"` + opusPRChildProvider + `"`, `"child_execution_id":"` + opusPRChildExec + `"`} {
		if !strings.Contains(row.Input, want) {
			t.Fatalf("wake text missing %s: %q", want, row.Input)
		}
	}
	if strings.Contains(row.Input, childReason) {
		t.Fatal("private runtime reason injected into the parent")
	}
	if res, ok := w.reservation(row.RequestID); !ok || res.Phase != "native_receipt_observed" || !res.Result.NativeReceiptObserved {
		t.Fatalf("reservation %#v", res)
	}
	if p, c := w.policy(), w.conversation(); p.DeliveryCursor != queued.EventSequence || c.Revision != 0 || c.DeliveredCursor != 0 {
		t.Fatalf("wake receipt acknowledged checkpoint: %#v %#v", p, c)
	}
	again := w.start("wake-step-after-observed", "wake-step")
	if r := again.finish(); r.Error != "" {
		t.Fatal(r.Error)
	}
	if len(w.ledgerEntries()) != 2 || w.nativeUserRecords(row.Input) != 1 || w.pending() != nil {
		t.Fatalf("observed wake was resent: %v", w.ledgerEntries())
	}
	w.sameFile("store wt.db", w.cfg.DBPath(), dbInfo)
}

func TestOpusProcessRecoveryKilledControllerPromptBoundaries(t *testing.T) {
	for _, boundary := range []string{"bound_before_reservation", "reserved_before_transport", "transport_delivered_before_outcome"} {
		t.Run(boundary, func(t *testing.T) { opusPRPromptBoundary(t, boundary) })
	}
}

func opusPRPromptBoundary(t *testing.T, boundary string) {
	w := opusPRNewWorld(t)
	rootStub := w.start("root-provider-stub", "stub-provider")
	w.writePID(opusPRRootExec, rootStub.pid())
	w.hook(opusPRRootExec, opusPRRootProvider, "SessionStart", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "SessionStart", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "UserPromptSubmit", map[string]any{"prompt": "benign fixture task"})
	w.hook(opusPRChildExec, opusPRChildProvider, "Stop", nil)

	owner := w.start("controller-owner", "wake-step", "OPUS_PR_BOUNDARY="+boundary)
	reached := owner.await("OPUS_PR_BOUNDARY", 20*time.Second)
	if reached.Boundary != boundary || reached.PID != owner.pid() {
		t.Fatalf("reached %#v", reached)
	}
	// Independent proof, from the parent process, of what was durable before death.
	bound := w.onlyOutbox()
	if bound.Status != "pending" || bound.Attempt != 1 || bound.RequestID == "" || bound.SessionID != opusPRRootExec || bound.ProviderSessionID != opusPRRootProvider || bound.Input == "" {
		t.Fatalf("binding not durable before boundary: %#v", bound)
	}
	if reached.RequestID != bound.RequestID {
		t.Fatalf("boundary request %s != bound %s", reached.RequestID, bound.RequestID)
	}
	res, reserved := w.reservation(bound.RequestID)
	switch boundary {
	case "bound_before_reservation":
		if reserved || len(w.reservationFiles()) != 0 || len(w.ledgerEntries()) != 0 || w.nativeUserRecords(bound.Input) != 0 {
			t.Fatal("reservation or transport happened before the bound-only boundary")
		}
	case "reserved_before_transport":
		if !reserved || res.Phase != "reserved" || res.RequestID != bound.RequestID || res.ProviderSessionID != opusPRRootProvider || w.nativeUserRecords(bound.Input) != 0 {
			t.Fatalf("reservation %#v", res)
		}
		opusPRRequireSends(t, w.ledgerEntries(), owner.pid(), bound.RequestID, "send_enter")
	case "transport_delivered_before_outcome":
		if !reserved || res.Phase != "reserved" || res.ProviderSessionID != opusPRRootProvider || w.nativeUserRecords(bound.Input) != 1 {
			t.Fatalf("reservation %#v", res)
		}
		opusPRRequireSends(t, w.ledgerEntries(), owner.pid(), bound.RequestID, "send_enter", "delivered")
	}
	t.Logf("OPUS_PR_RECEIPT boundary=%s persisted_before_kill pid=%d outbox_status=%s attempt=%d request=%s session=%s provider=%s reservation_phase=%q ledger=%d native_rows=%d", boundary, owner.pid(), bound.Status, bound.Attempt, bound.RequestID, bound.SessionID, bound.ProviderSessionID, res.Phase, len(w.ledgerEntries()), w.nativeUserRecords(bound.Input))

	dbInfo := w.stat(w.cfg.DBPath())
	wakeLock := w.stat(w.wakeLockPath())
	promptLockPath := filepath.Join(w.eggDir(opusPRRootExec), "prompt.lock")
	holdsPrompt := boundary != "bound_before_reservation"
	if opusPRFlockFree(t, w.wakeLockPath()) {
		t.Fatal("live owner does not hold the root wake lock")
	}
	var promptLock os.FileInfo
	if holdsPrompt {
		promptLock = w.stat(promptLockPath)
		if opusPRFlockFree(t, promptLockPath) {
			t.Fatal("live owner does not hold the prompt writer lock")
		}
	}
	rival := w.start("controller-rival-while-owner-alive", "wake-step")
	if r := rival.finish(); r.Error == "" {
		t.Fatal("rival controller progressed while the owner held the root lock")
	} else {
		t.Logf("OPUS_PR_RECEIPT rival pid=%d refused while owner pid=%d alive: %s", rival.pid(), owner.pid(), r.Error)
	}
	if again := w.onlyOutbox(); again != bound {
		t.Fatalf("rival changed the outbox: %#v", again)
	}

	owner.kill(boundary)

	w.sameFile("wake lock", w.wakeLockPath(), wakeLock)
	if !opusPRFlockFree(t, w.wakeLockPath()) {
		t.Fatal("kernel did not release the killed owner's wake lock")
	}
	if holdsPrompt {
		w.sameFile("prompt lock", promptLockPath, promptLock)
		if !opusPRFlockFree(t, promptLockPath) {
			t.Fatal("kernel did not release the killed owner's prompt lock")
		}
	}
	t.Logf("OPUS_PR_RECEIPT flock released by kernel after SIGKILL of pid=%d; lock files retained", owner.pid())

	restart := w.start("controller-restart", "wake-step")
	if restart.pid() == owner.pid() {
		t.Fatal("restart reused the killed pid")
	}
	if r := restart.finish(); r.Error != "" {
		t.Fatalf("restart: %s", r.Error)
	}
	got := w.onlyOutbox()
	if got.RequestID != bound.RequestID || got.Attempt != 1 || got.EventSequence != bound.EventSequence || got.SessionID != bound.SessionID || got.ProviderSessionID != bound.ProviderSessionID || got.Input != bound.Input {
		t.Fatalf("restart changed immutable request identity: %#v -> %#v", bound, got)
	}
	res, reserved = w.reservation(bound.RequestID)
	ledger := w.ledgerEntries()
	switch boundary {
	case "bound_before_reservation":
		// No before-send reservation existed, so nothing could have been sent.
		// The restart's first send keeps the same bound request ID.
		opusPRRequireSends(t, ledger, restart.pid(), bound.RequestID, "send_enter", "delivered")
		if got.Status != "observed" || !reserved || res.Phase != "native_receipt_observed" || w.nativeUserRecords(bound.Input) != 1 {
			t.Fatalf("restart after bound-only death: %#v %#v", got, res)
		}
	case "reserved_before_transport":
		// No native proof: delivery stays unknown, never resent, never retryable.
		opusPRRequireSends(t, ledger, owner.pid(), bound.RequestID, "send_enter")
		if got.Status != "unconfirmed" || !reserved || res.Phase != "reserved" || res.Result.NativeReceiptObserved || res.Result.DefinitelyNotSent || w.nativeUserRecords(bound.Input) != 0 {
			t.Fatalf("ambiguous reservation became a claim: %#v %#v", got, res)
		}
		w.withStore(func(db *store.Store) {
			if err := db.RetryNotSentConversationWake(opusPROwner, opusPRRootID, time.Now().Unix()); err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("ambiguous delivery was retryable: %v", err)
			}
		})
	case "transport_delivered_before_outcome":
		// Native receipt proof exists: the restart records it without resending.
		opusPRRequireSends(t, ledger, owner.pid(), bound.RequestID, "send_enter", "delivered")
		if got.Status != "observed" || !reserved || res.Phase != "native_receipt_observed" || res.Result.ReceiptCursor <= res.Result.ReservedCursor || got.ReceiptCursor != res.Result.ReceiptCursor || w.nativeUserRecords(bound.Input) != 1 {
			t.Fatalf("restart did not reconcile native receipt: %#v %#v", got, res)
		}
	}
	p, c := w.policy(), w.conversation()
	if (got.Status == "observed") != (p.DeliveryCursor == bound.EventSequence) || c.Revision != 0 || c.DeliveredCursor != 0 {
		t.Fatalf("wake/checkpoint cursors mixed: %#v %#v", p, c)
	}
	t.Logf("OPUS_PR_RECEIPT restart pid=%d outcome=%s request=%s wake_delivery_cursor=%d checkpoint_revision=%d ledger=%d", restart.pid(), got.Status, got.RequestID, p.DeliveryCursor, c.Revision, len(ledger))
	w.sameFile("store wt.db", w.cfg.DBPath(), dbInfo)

	second := w.start("controller-second-restart", "wake-step")
	if r := second.finish(); r.Error != "" {
		t.Fatal(r.Error)
	}
	if final := w.onlyOutbox(); final.Status != got.Status || final.RequestID != got.RequestID || len(w.ledgerEntries()) != len(ledger) || w.nativeUserRecords(bound.Input) != map[bool]int{true: 1, false: 0}[got.Status == "observed"] {
		t.Fatalf("second restart resent or changed outcome: %#v ledger=%v", final, w.ledgerEntries())
	}
}

func TestOpusProcessRecoveryCommittedCheckpointReplyLost(t *testing.T) {
	w := opusPRNewWorld(t)
	rootStub := w.start("root-provider-stub", "stub-provider")
	w.writePID(opusPRRootExec, rootStub.pid())
	w.hook(opusPRRootExec, opusPRRootProvider, "SessionStart", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "SessionStart", nil)
	w.hook(opusPRChildExec, opusPRChildProvider, "UserPromptSubmit", map[string]any{"prompt": "benign fixture task"})
	w.hook(opusPRChildExec, opusPRChildProvider, "Stop", nil)

	deliver := w.start("wake-step-deliver", "wake-step")
	if r := deliver.finish(); r.Error != "" {
		t.Fatal(r.Error)
	}
	row := w.onlyOutbox()
	wakeEvent := row.EventSequence
	if row.Status != "observed" || w.policy().DeliveryCursor != wakeEvent {
		t.Fatalf("wake not observed: %#v", row)
	}
	if c := w.conversation(); c.Revision != 0 || c.DeliveredCursor != 0 {
		t.Fatalf("wake receipt changed checkpoint %#v", c)
	}
	sends := len(w.ledgerEntries())

	const text = "opus checkpoint: inspected first root delivery"
	committer := w.start("checkpoint-committer", "checkpoint-commit", "OPUS_PR_CHECKPOINT="+text)
	invoked := committer.await("OPUS_PR_BOUNDARY", 20*time.Second)
	if invoked.Boundary != "checkpoint_invoked_reply_withheld" || invoked.ExpectedRevision != 0 || invoked.AfterCursor < 1 || invoked.AfterCursor >= wakeEvent {
		t.Fatalf("checkpoint request %#v (wake event %d)", invoked, wakeEvent)
	}
	w.eventually("checkpoint CAS commit to become durable", 10*time.Second, func() bool {
		if r, ok := committer.find("OPUS_PR_RESULT"); ok {
			t.Fatalf("checkpoint failed in committer: %s", r.Error)
		}
		return w.conversation().Revision == 1
	})
	committed := w.conversation()
	if committed.DeliveredCursor != invoked.AfterCursor || committed.Checkpoint != text {
		t.Fatalf("committed %#v", committed)
	}
	t.Logf("OPUS_PR_RECEIPT boundary=checkpoint_committed_reply_unsent pid=%d revision=%d delivered_cursor=%d wake_delivery_cursor=%d (reply never emitted)", committer.pid(), committed.Revision, committed.DeliveredCursor, w.policy().DeliveryCursor)
	committer.kill("checkpoint_committed_reply_unsent")

	recoverer := w.start("checkpoint-recoverer", "checkpoint-recover", "OPUS_PR_CHECKPOINT="+text, "OPUS_PR_EXPECTED=0", fmt.Sprintf("OPUS_PR_AFTER=%d", invoked.AfterCursor), fmt.Sprintf("OPUS_PR_WAKE_EVENT=%d", wakeEvent))
	if recoverer.pid() == committer.pid() {
		t.Fatal("restart reused the killed pid")
	}
	r := recoverer.finish()
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	if !strings.Contains(r.StaleError, "checkpoint conflict") {
		t.Fatalf("stale-revision retry of committed checkpoint was accepted: %q", r.StaleError)
	}
	if r.RecoveredRevision != 1 || r.RecoveredCursor != invoked.AfterCursor || r.RecoveredCheckpoint != text {
		t.Fatalf("reload after lost reply: %#v", r)
	}
	if !strings.Contains(r.RegressError, "checkpoint conflict") {
		t.Fatalf("cursor regression accepted: %q", r.RegressError)
	}
	replayedWake := false
	for _, seq := range r.ReplaySequences {
		if seq <= invoked.AfterCursor {
			t.Fatalf("replay returned acknowledged cursor %d", seq)
		}
		replayedWake = replayedWake || seq == wakeEvent
	}
	if !replayedWake {
		t.Fatalf("wake receipt acknowledged event %d: replay %v", wakeEvent, r.ReplaySequences)
	}
	if r.AdvanceError != "" || r.FinalRevision != 2 || r.FinalCursor != wakeEvent {
		t.Fatalf("fresh-revision advance: %#v", r)
	}
	if p := w.policy(); p.DeliveryCursor != wakeEvent || w.pending() != nil || len(w.ledgerEntries()) != sends {
		t.Fatalf("checkpoint recovery touched wake delivery: %#v", p)
	}
	t.Logf("OPUS_PR_RECEIPT restart pid=%d stale_retry=%q recovered_revision=%d recovered_cursor=%d final_revision=%d final_cursor=%d wake_delivery_cursor=%d", recoverer.pid(), r.StaleError, r.RecoveredRevision, r.RecoveredCursor, r.FinalRevision, r.FinalCursor, w.policy().DeliveryCursor)
}
