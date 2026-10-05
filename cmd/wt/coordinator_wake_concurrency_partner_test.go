package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// opusWakeParent is a synthetic exact-provider parent: an in-memory native
// reader and a PTY-free transport. No provider process, credential, network
// endpoint or permission reply is involved.
type opusWakeParent struct {
	mu       sync.Mutex
	session  string
	provider string
	state    string
	events   []egg.SessionEvent
	reads    int
	sent     []string
	receipt  bool                          // publish the exact native user receipt on send
	onSend   func(context.Context, string) // gate or failpoint, after the send is counted
}

func (p *opusWakeParent) read(_ context.Context, after int64, limit int) (egg.SessionView, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	v := egg.SessionView{SessionID: p.session, Agent: "claude", ProviderSessionID: p.provider, State: p.state, StateSource: "claude_hook", Ready: true, ProcessAlive: true, Cursor: after, HeadCursor: 1, Events: []egg.SessionEvent{}}
	for _, e := range p.events {
		v.HeadCursor = e.Sequence
		if e.Sequence <= after {
			continue
		}
		if len(v.Events) < limit {
			v.Events = append(v.Events, e)
			v.Cursor = e.Sequence
		} else {
			v.HasMore = true
		}
	}
	return v, nil
}

func (p *opusWakeParent) publish(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": p.provider, "message": map[string]any{"role": "user", "content": text}})
	p.events = append(p.events, egg.SessionEvent{Sequence: int64(len(p.events)) + 2, Type: "message", Source: "claude_transcript", ProviderSessionID: p.provider, Raw: raw})
}

func (p *opusWakeParent) send(ctx context.Context, text string) (egg.PromptDelivery, error) {
	p.mu.Lock()
	p.sent = append(p.sent, text)
	hook, receipt := p.onSend, p.receipt
	p.mu.Unlock()
	if hook != nil {
		hook(ctx, text)
	}
	if receipt {
		p.publish(text)
	}
	return egg.PromptDelivery{BytesEnqueued: len(text)}, nil
}

func (p *opusWakeParent) configure(state string, receipt bool, onSend func(context.Context, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.receipt, p.onSend = state, receipt, onSend
}

func (p *opusWakeParent) snapshot() (reads int, sent []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reads, append([]string(nil), p.sent...)
}

type opusWakeHarness struct {
	cfg         *config.Config
	parents     map[string]*opusWakeParent
	timeout     time.Duration
	mu          sync.Mutex
	prompts     []string
	afterPrompt func(egg.SessionPromptResult, error)
}

type opusWakeReservation struct {
	RequestID         string                  `json:"request_id"`
	ProviderSessionID string                  `json:"provider_session_id"`
	Phase             string                  `json:"phase"`
	Result            egg.SessionPromptResult `json:"result"`
}

type opusWakeOutcome struct {
	Sequence int64
	Request  string
	Receipt  int64
	Reason   string
}

type opusWakeSnapshot struct {
	pending *store.ConversationWake
	policy  store.ConversationWakePolicy
	conv    *store.Conversation
}

func newOpusWakeHarness(t *testing.T) *opusWakeHarness {
	return &opusWakeHarness{cfg: &config.Config{Dir: t.TempDir()}, parents: map[string]*opusWakeParent{}, timeout: 300 * time.Millisecond}
}

func (h *opusWakeHarness) open(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(h.cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// root creates an opted-in root and one child. Child IDs never share a prefix
// with a root execution, keeping this slice independent of prefix lookup.
func (h *opusWakeHarness) root(t *testing.T, db *store.Store, id string, childStates ...string) (*store.Conversation, *store.Conversation, *opusWakeParent) {
	t.Helper()
	root := fixtureConversation(t, db, h.cfg, id, "", "owner", "idle")
	child := fixtureConversation(t, db, h.cfg, "kid-"+id, root.ID, "owner", childStates...)
	if err := db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	p := &opusWakeParent{session: root.SessionID, provider: "provider-" + id, state: "idle", receipt: true}
	h.parents[root.SessionID] = p
	return root, child, p
}

func (h *opusWakeHarness) runtime() conversationWakeRuntime {
	return conversationWakeRuntime{Now: time.Now,
		Read: func(ctx context.Context, s localSession) (egg.SessionView, error) {
			return h.parents[s.ID].read(ctx, 0, 1)
		},
		Prompt: func(ctx context.Context, s localSession, id, text string) (egg.SessionPromptResult, error) {
			h.mu.Lock()
			h.prompts = append(h.prompts, id)
			after := h.afterPrompt
			h.mu.Unlock()
			p := h.parents[s.ID]
			result, err := egg.SubmitSessionPrompt(ctx, filepath.Join(h.cfg.Dir, "eggs", s.ID), egg.SessionPromptOptions{RequestID: id, Input: text, Timeout: h.timeout, Read: p.read, Send: p.send})
			if after != nil {
				after(result, err)
			}
			return result, err
		}}
}

// step is one controller invocation from a fresh server; the store and root
// lock are reopened inside, so consecutive steps model controller restarts.
func (h *opusWakeHarness) step(root string) error {
	return processConversationWake(context.Background(), &localMCPServer{cfg: h.cfg, principal: "owner"}, root, h.runtime())
}

func (h *opusWakeHarness) promptIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.prompts...)
}

func (h *opusWakeHarness) snapshot(t *testing.T, root string) opusWakeSnapshot {
	t.Helper()
	db := h.open(t)
	defer func() { _ = db.Close() }()
	var s opusWakeSnapshot
	var err error
	if s.pending, err = db.PendingConversationWake(root); err != nil {
		t.Fatal(err)
	}
	if s.policy, err = db.ConversationWakePolicy(root); err != nil {
		t.Fatal(err)
	}
	if s.conv, err = db.GetConversation("owner", root); err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *opusWakeHarness) events(t *testing.T, root string) []store.ConversationEvent {
	t.Helper()
	db := h.open(t)
	defer func() { _ = db.Close() }()
	events, err := db.ConversationEvents("owner", root, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func (h *opusWakeHarness) observed(t *testing.T, root string) []opusWakeOutcome {
	t.Helper()
	db := h.open(t)
	defer func() { _ = db.Close() }()
	rows, err := db.DB().Query(`SELECT event_sequence,request_id,receipt_cursor,reason FROM conversation_wake_outbox WHERE root_id=? AND status='observed' ORDER BY event_sequence`, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []opusWakeOutcome
	for rows.Next() {
		var o opusWakeOutcome
		if err = rows.Scan(&o.Sequence, &o.Request, &o.Receipt, &o.Reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

func (h *opusWakeHarness) reservations(t *testing.T, session string) []opusWakeReservation {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(h.cfg.Dir, "eggs", session, "prompt.*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []opusWakeReservation
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r opusWakeReservation
		if err = json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (h *opusWakeHarness) appendHook(t *testing.T, session, state string) {
	t.Helper()
	dir := filepath.Join(h.cfg.Dir, "eggs", session)
	data, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(egg.SessionEvent{Sequence: int64(strings.Count(string(data), "\n") + 1), Type: "native-fixture", Source: "claude_hook", State: state, ProviderSessionID: readEggMetaValues(dir)["provider_session_id"]})
	if err = os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), append(append(data, line...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func (h *opusWakeHarness) lockPath(root string) string {
	digest := sha256.Sum256([]byte(root))
	return filepath.Join(h.cfg.Dir, fmt.Sprintf("wake-%x.lock", digest[:8]))
}

func (h *opusWakeHarness) read(t *testing.T, root string, after int64, limit int) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"conversation_id": root, "after_cursor": after, "limit": limit})
	out, err := (&localMCPServer{cfg: h.cfg, principal: "owner"}).toolConversationRead(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// paginate replays conversation_read pages and returns sequences and page ends.
func (h *opusWakeHarness) paginate(t *testing.T, root string, after int64, limit int) ([]int64, []int64) {
	t.Helper()
	var seqs, ends []int64
	for page := 0; page < 50; page++ {
		out := h.read(t, root, after, limit)
		events := out["events"].([]store.ConversationEvent)
		if len(events) > limit {
			t.Fatalf("page exceeded limit %d", len(events))
		}
		for _, e := range events {
			if e.Sequence <= after || (len(seqs) > 0 && e.Sequence <= seqs[len(seqs)-1]) {
				t.Fatalf("non-monotonic replay %d after %d", e.Sequence, after)
			}
			seqs = append(seqs, e.Sequence)
		}
		after = out["next_cursor"].(int64)
		ends = append(ends, after)
		if !out["has_more"].(bool) {
			return seqs, ends
		}
	}
	t.Fatal("pagination did not terminate")
	return nil, nil
}

func (h *opusWakeHarness) checkpoint(root string, expected, after int64, text string) error {
	args, _ := json.Marshal(map[string]any{"conversation_id": root, "expected_revision": expected, "after_cursor": after, "checkpoint": text})
	_, err := (&localMCPServer{cfg: h.cfg, principal: "owner"}).toolConversationCheckpoint(args)
	return err
}

func opusWakeWait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func opusWakeSameAck(a, b *store.Conversation) bool {
	return a.Revision == b.Revision && a.DeliveredCursor == b.DeliveredCursor && a.Checkpoint == b.Checkpoint && a.UpdatedAt.Equal(b.UpdatedAt)
}

func opusWakeLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "another daemon start/stop")
}

// Case 1. Deterministic overlap: controller A is parked inside the transport
// while holding the real root lock, prompt lock and a bound reservation.
func TestOpusWakeRecoveryCompetingControllersShareOneRootLock(t *testing.T) {
	h := newOpusWakeHarness(t)
	h.timeout = 10 * time.Second
	db := h.open(t)
	alpha, alphaChild, alphaParent := h.root(t, db, "alpha", "completed")
	bravo, _, bravoParent := h.root(t, db, "bravo", "completed")
	_ = db.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	alphaParent.configure("idle", true, func(ctx context.Context, _ string) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	first := make(chan error, 1)
	go func() { first <- h.step(alpha.ID) }()
	opusWakeWait(t, entered, "first controller to reach transport")
	held := h.snapshot(t, alpha.ID)
	if held.pending == nil || held.pending.Status != "pending" || held.pending.Attempt != 1 || held.pending.Event.ConversationID != alphaChild.ID {
		t.Fatalf("first controller binding %#v", held.pending)
	}
	reads, _ := alphaParent.snapshot()
	// Same-process goroutine competition: each competitor opens its own file
	// description on the root lock and must stop before store, read or send.
	for i := 0; i < 3; i++ {
		if err := h.step(alpha.ID); !opusWakeLocked(err) {
			t.Fatalf("competitor %d was not excluded: %v", i, err)
		}
	}
	afterReads, sent := alphaParent.snapshot()
	if afterReads != reads || len(sent) != 1 || len(h.promptIDs()) != 1 {
		t.Fatalf("competitor touched parent reads %d->%d sends=%d prompts=%v", reads, afterReads, len(sent), h.promptIDs())
	}
	competing := h.snapshot(t, alpha.ID)
	if competing.pending.RequestID != held.pending.RequestID || competing.pending.Attempt != 1 || competing.pending.Status != "pending" || competing.policy != held.policy {
		t.Fatalf("competitor reserved another wake %#v %#v", competing.pending, competing.policy)
	}
	if r := h.reservations(t, alpha.SessionID); len(r) != 1 || r[0].RequestID != held.pending.RequestID || r[0].Phase != "reserved" {
		t.Fatalf("prompt reservations while held %#v", r)
	}
	// The lock is scoped to one root: an unrelated opted-in root proceeds.
	if err := h.step(bravo.ID); err != nil {
		t.Fatal(err)
	}
	if _, bravoSent := bravoParent.snapshot(); len(bravoSent) != 1 || h.snapshot(t, bravo.ID).pending != nil {
		t.Fatalf("independent root blocked sends=%d", len(bravoSent))
	}
	close(release)
	if err := opusWakeWait(t, first, "first controller to finish"); err != nil {
		t.Fatal(err)
	}
	alphaParent.configure("idle", true, nil)
	// Root reopened after A released: reconciliation finds nothing new to send.
	if err := h.step(alpha.ID); err != nil {
		t.Fatal(err)
	}
	done := h.snapshot(t, alpha.ID)
	if _, sent = alphaParent.snapshot(); done.pending != nil || done.policy.DeliveryCursor != held.pending.EventSequence || len(sent) != 1 {
		t.Fatalf("after release %#v %#v sends=%d", done.pending, done.policy, len(sent))
	}
	if r := h.reservations(t, alpha.SessionID); len(r) != 1 || r[0].Phase != "native_receipt_observed" {
		t.Fatalf("reconciled reservation %#v", r)
	}
	// A holder in another OS process is modeled, not executed: flock belongs to
	// an open file description, so this test-held descriptor competes exactly as
	// a separate controller process would. Closing it is what the kernel does at
	// holder exit. No process is spawned, signaled or killed.
	h.appendHook(t, alphaChild.SessionID, "completed")
	foreign, err := acquireDaemonLifecycleLockAt(h.lockPath(alpha.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.step(alpha.ID); !opusWakeLocked(err) {
		_ = foreign.Close()
		t.Fatalf("foreign holder did not exclude controller: %v", err)
	}
	if blocked := h.snapshot(t, alpha.ID); blocked.pending != nil || blocked.policy != done.policy {
		_ = foreign.Close()
		t.Fatalf("excluded controller scanned or reserved %#v %#v", blocked.pending, blocked.policy)
	}
	_ = foreign.Close()
	if err = h.step(alpha.ID); err != nil {
		t.Fatal(err)
	}
	ids := h.promptIDs()
	if _, sent = alphaParent.snapshot(); len(sent) != 2 || sent[0] == sent[1] || len(ids) != 3 || ids[2] == ids[0] {
		t.Fatalf("post-release distinct event sends=%d prompts=%v", len(sent), ids)
	}
	if final := h.snapshot(t, alpha.ID); final.pending != nil || final.policy.DeliveryCursor <= done.policy.DeliveryCursor {
		t.Fatalf("second event not delivered %#v %#v", final.pending, final.policy)
	}
}

// Case 2a. Child completion is durable before any parent wake; a busy or
// permission-waiting parent keeps the exact queued event across restarts.
func TestOpusWakeRecoveryPersistedCompletionSurvivesBusyParentRestarts(t *testing.T) {
	h := newOpusWakeHarness(t)
	db := h.open(t)
	root, child, parent := h.root(t, db, "root", "working", "completed")
	_ = db.Close()
	h.read(t, root.ID, 0, 50)
	if h.snapshot(t, root.ID).pending != nil {
		t.Fatal("conversation_read alone reserved a wake")
	}
	var event int64
	for _, e := range h.events(t, root.ID) {
		if e.ConversationID == child.ID && e.State == "completed" {
			event = e.Sequence
		}
	}
	if event == 0 {
		t.Fatal("child completion was not persisted")
	}
	for _, state := range []string{"working", "needs_input", "needs_input", "working"} {
		parent.configure(state, true, nil)
		if err := h.step(root.ID); err != nil {
			t.Fatal(err)
		}
		got := h.snapshot(t, root.ID).pending
		if got == nil || got.Status != "queued" || got.EventSequence != event || got.Attempt != 0 || got.RequestID != "" {
			t.Fatalf("parent %s lost or reserved queue %#v", state, got)
		}
	}
	if _, sent := parent.snapshot(); len(sent) != 0 || len(h.promptIDs()) != 0 || len(h.reservations(t, root.SessionID)) != 0 {
		t.Fatalf("busy parent received input sends=%d prompts=%v", len(sent), h.promptIDs())
	}
	parent.configure("idle", true, nil)
	for i := 0; i < 2; i++ {
		if err := h.step(root.ID); err != nil {
			t.Fatal(err)
		}
	}
	final := h.snapshot(t, root.ID)
	if _, sent := parent.snapshot(); len(sent) != 1 || final.pending != nil || final.policy.DeliveryCursor != event || final.conv.Revision != 0 || final.conv.DeliveredCursor != 0 {
		t.Fatalf("idle delivery sends=%d %#v %#v", len(sent), final.pending, final.conv)
	}
}

// Case 2b. Simulated interruption: runtime.Goexit unwinds the controller
// goroutine after the real prompt reservation (and transport attempt) but
// before RecordConversationWake. Deferred closes release the root lock, prompt
// lock and store as the kernel would at process exit. No process terminates.
func TestOpusWakeRecoveryInterruptedDeliveryReconcilesSavedRequest(t *testing.T) {
	for _, failpoint := range []string{"inside_transport", "after_receipt_before_outcome"} {
		t.Run(failpoint, func(t *testing.T) {
			h := newOpusWakeHarness(t)
			db := h.open(t)
			root, child, parent := h.root(t, db, "root", "completed")
			_ = db.Close()
			wantPhase := "native_receipt_observed"
			if failpoint == "inside_transport" {
				// Bytes enqueued, then interruption before transport evidence is saved.
				parent.configure("idle", false, func(context.Context, string) { goruntime.Goexit() })
				wantPhase = "reserved"
			} else {
				h.afterPrompt = func(r egg.SessionPromptResult, err error) {
					if err == nil && r.NativeReceiptObserved {
						goruntime.Goexit()
					}
				}
			}
			returned, exited := make(chan error, 1), make(chan struct{})
			go func() { defer close(exited); returned <- h.step(root.ID) }()
			opusWakeWait(t, exited, "interrupted controller")
			if len(returned) != 0 {
				t.Fatalf("failpoint did not interrupt: %v", <-returned)
			}
			w := h.snapshot(t, root.ID).pending
			if w == nil || w.Status != "pending" || w.Attempt != 1 || w.SessionID != root.SessionID || w.ProviderSessionID != "provider-root" || w.Event.ConversationID != child.ID || w.Input == "" {
				t.Fatalf("interrupted outbox %#v", w)
			}
			if r := h.reservations(t, root.SessionID); len(r) != 1 || r[0].RequestID != w.RequestID || r[0].ProviderSessionID != "provider-root" || r[0].Phase != wantPhase {
				t.Fatalf("interrupted reservation %#v", r)
			}
			// Restart with the parent busy and failpoints removed.
			h.afterPrompt = nil
			parent.configure("working", false, nil)
			if failpoint == "inside_transport" {
				parent.publish(w.Input + "\nnear miss")
				if err := h.step(root.ID); err != nil {
					t.Fatal(err)
				}
				got := h.snapshot(t, root.ID).pending
				if got == nil || got.Status != "unconfirmed" || got.RequestID != w.RequestID || got.Input != w.Input || got.SessionID != w.SessionID || got.ProviderSessionID != w.ProviderSessionID || got.Attempt != 1 {
					t.Fatalf("near-miss receipt or rebinding %#v", got)
				}
				parent.publish(w.Input)
			} else {
				// The durable reservation already holds the exact receipt evidence.
				parent.mu.Lock()
				parent.events = nil
				parent.mu.Unlock()
			}
			if err := h.step(root.ID); err != nil {
				t.Fatal(err)
			}
			final := h.snapshot(t, root.ID)
			_, sent := parent.snapshot()
			if final.pending != nil || final.policy.DeliveryCursor != w.EventSequence || final.conv.Revision != 0 || final.conv.DeliveredCursor != 0 || len(sent) != 1 || sent[0] != w.Input {
				t.Fatalf("reconcile %#v %#v %#v sends=%d", final.pending, final.policy, final.conv, len(sent))
			}
			for _, id := range h.promptIDs() {
				if id != w.RequestID {
					t.Fatalf("replacement request %s != %s", id, w.RequestID)
				}
			}
			o := h.observed(t, root.ID)
			r := h.reservations(t, root.SessionID)
			if len(o) != 1 || o[0].Request != w.RequestID || o[0].Receipt == 0 || len(r) != 1 || r[0].Phase != "native_receipt_observed" || r[0].Result.ReceiptCursor != o[0].Receipt || r[0].Result.ProviderRequestAcknowledged || r[0].Result.Causality != "unverified_without_provider_request_id" {
				t.Fatalf("receipt evidence %#v %#v", o, r)
			}
		})
	}
}

// Case 3. Duplicate imports of the same source cursor collapse; distinct
// physical journal records (including two process exits) remain distinct and
// wake once each, in order, with no skipped replay.
func TestOpusWakeRecoveryDuplicateImportsDedupeAndDistinctEventsWakeInOrder(t *testing.T) {
	h := newOpusWakeHarness(t)
	db := h.open(t)
	root, child, parent := h.root(t, db, "root", "working", "completed", "working", "completed")
	_ = db.Close()
	readAll := func(n int) {
		t.Helper()
		errs := make(chan error, n)
		var group sync.WaitGroup
		for i := 0; i < n; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				args, _ := json.Marshal(map[string]any{"conversation_id": root.ID, "limit": 1})
				_, err := (&localMCPServer{cfg: h.cfg, principal: "owner"}).toolConversationRead(context.Background(), args)
				errs <- err
			}()
		}
		group.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	childEvents := func() []store.ConversationEvent {
		t.Helper()
		seen := map[int64]bool{}
		var out []store.ConversationEvent
		for _, e := range h.events(t, root.ID) {
			if e.SessionID != child.SessionID {
				continue
			}
			if seen[e.SourceCursor] {
				t.Fatalf("duplicate import of source cursor %d", e.SourceCursor)
			}
			seen[e.SourceCursor] = true
			out = append(out, e)
		}
		return out
	}
	deliver := func() {
		t.Helper()
		for i := 0; i < 6; i++ {
			if err := h.step(root.ID); err != nil {
				t.Fatal(err)
			}
			readAll(2)
			inventory := h.open(t)
			roots, err := inventory.ConversationWakeRoots("", 4)
			tail, tailErr := inventory.ConversationWakeRoots(root.ID, 4)
			_ = inventory.Close()
			if err != nil || tailErr != nil || len(roots) != 1 || roots[0].ID != root.ID || len(tail) != 0 {
				t.Fatalf("inventory poll %v %v %v %v", roots, tail, err, tailErr)
			}
		}
	}
	readAll(4)
	if got := childEvents(); len(got) != 4 {
		t.Fatalf("concurrent import %#v", got)
	}
	// Replay an already imported page and its state summary through the store.
	db = h.open(t)
	replay := []store.ConversationEvent{{SourceCursor: 2, State: "completed", StateSource: "claude_hook", Type: "native-fixture"}, {SourceCursor: 4, State: "completed", StateSource: "claude_hook", Type: "native-fixture"}}
	if err := db.ImportConversationStates(child, replay, 4); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordConversationState(child, 4, "completed", "claude_hook"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if got := childEvents(); len(got) != 4 {
		t.Fatalf("replayed source cursor imported twice %#v", got)
	}
	deliver()
	childDir := filepath.Join(h.cfg.Dir, "eggs", child.SessionID)
	for _, reason := range []string{"fixture exit", "fixture exit recorded again"} {
		if err := egg.RecordSessionProcessEvent(childDir, "session_exit", "stopped", reason); err != nil {
			t.Fatal(err)
		}
	}
	readAll(3)
	deliver()
	var want []int64
	var states []string
	for _, e := range childEvents() {
		states = append(states, fmt.Sprintf("%d:%s:%s", e.SourceCursor, e.State, e.StateSource))
		if (e.StateSource == "claude_hook" && e.State == "completed") || (e.StateSource == "egg_process" && e.Type == "session_exit") {
			want = append(want, e.Sequence)
		}
	}
	if strings.Join(states, ",") != "1:working:claude_hook,2:completed:claude_hook,3:working:claude_hook,4:completed:claude_hook,5:stopped:egg_process,6:stopped:egg_process" || len(want) != 4 {
		t.Fatalf("imported child events %v", states)
	}
	_, sent := parent.snapshot()
	outcomes := h.observed(t, root.ID)
	if len(sent) != len(want) || len(outcomes) != len(want) {
		t.Fatalf("wakes sends=%d observed=%d want=%v", len(sent), len(outcomes), want)
	}
	requests := map[string]bool{}
	for i, text := range sent {
		var body struct {
			Cursor int64  `json:"event_cursor"`
			Child  string `json:"child_conversation_id"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal([]byte(text[strings.Index(text, "\n")+1:]), &body); err != nil {
			t.Fatal(err)
		}
		if body.Cursor != want[i] || outcomes[i].Sequence != want[i] || body.Child != child.ID || requests[outcomes[i].Request] {
			t.Fatalf("wake %d out of order or duplicated %+v %#v", i, body, outcomes[i])
		}
		requests[outcomes[i].Request] = true
	}
	final := h.snapshot(t, root.ID)
	events := h.events(t, root.ID)
	if final.pending != nil || final.policy.DeliveryCursor != want[len(want)-1] || final.policy.ScanCursor != events[len(events)-1].Sequence {
		t.Fatalf("final wake cursors %#v %#v", final.pending, final.policy)
	}
}

// Case 4. Wake receipt is not checkpoint acknowledgement. A lost checkpoint
// (uncommitted or committed-but-unacknowledged) is recovered by replayable
// read pagination and revision CAS; reads never write acknowledgement state.
func TestOpusWakeRecoveryLostCheckpointAckReplaysPagesAndRejectsStaleWriters(t *testing.T) {
	h := newOpusWakeHarness(t)
	db := h.open(t)
	root, child, parent := h.root(t, db, "root", "working", "completed", "needs_input", "working", "completed")
	_ = db.Close()
	if err := h.step(root.ID); err != nil {
		t.Fatal(err)
	}
	woke := h.observed(t, root.ID)
	if len(woke) != 1 || !strings.Contains(woke[0].Reason, "causal provider request acknowledgement remains unverified") {
		t.Fatalf("first wake %#v", woke)
	}
	if r := h.reservations(t, root.SessionID); len(r) != 1 || r[0].Result.ReceiptKind != "exact_provider_user_text_match" || r[0].Result.ProviderRequestAcknowledged || r[0].Result.Causality != "unverified_without_provider_request_id" {
		t.Fatalf("receipt overclaimed causality %#v", r)
	}
	before := h.snapshot(t, root.ID)
	if before.conv.Revision != 0 || before.conv.DeliveredCursor != 0 || before.policy.DeliveryCursor != woke[0].Sequence {
		t.Fatalf("wake receipt acknowledged checkpoint %#v %#v", before.conv, before.policy)
	}
	all, ends := h.paginate(t, root.ID, 0, 2)
	if len(all) != 6 || len(ends) != 3 {
		t.Fatalf("pages %v ends %v", all, ends)
	}
	// Restart before any checkpoint commit: replay from the persisted
	// delivered cursor returns identical handled-but-unacknowledged events.
	replayed, replayEnds := h.paginate(t, root.ID, before.conv.DeliveredCursor, 2)
	if fmt.Sprint(replayed) != fmt.Sprint(all) || fmt.Sprint(replayEnds) != fmt.Sprint(ends) || !strings.Contains(" "+strings.Trim(fmt.Sprint(all), "[]")+" ", fmt.Sprintf(" %d ", woke[0].Sequence)) {
		t.Fatalf("replay %v %v != %v %v", replayed, replayEnds, all, ends)
	}
	if got := h.snapshot(t, root.ID); !opusWakeSameAck(got.conv, before.conv) || got.policy != before.policy || got.pending != nil {
		t.Fatalf("read mutated acknowledgement or wake state %#v %#v", got.conv, got.policy)
	}
	// Partial checkpoint commits at a page boundary; its response is lost.
	mid, last := ends[1], all[len(all)-1]
	if err := h.checkpoint(root.ID, 0, mid, "handled first wake"); err != nil {
		t.Fatal(err)
	}
	if err := h.checkpoint(root.ID, 0, mid, "blind retry after lost response"); err == nil {
		t.Fatal("stale revision retry accepted")
	}
	acked := h.snapshot(t, root.ID)
	if acked.conv.Revision != 1 || acked.conv.DeliveredCursor != mid || acked.conv.Checkpoint != "handled first wake" || acked.policy != before.policy {
		t.Fatalf("lost-ack retry state %#v %#v", acked.conv, acked.policy)
	}
	rest, _ := h.paginate(t, root.ID, acked.conv.DeliveredCursor, 2)
	if fmt.Sprint(rest) != fmt.Sprint(all[4:]) {
		t.Fatalf("replay after reload %v want %v", rest, all[4:])
	}
	// Two writers race with the same reloaded revision; exactly one commits.
	results := make(chan error, 2)
	for _, after := range []int64{last, mid} {
		go func(after int64) { results <- h.checkpoint(root.ID, 1, after, fmt.Sprintf("writer %d", after)) }(after)
	}
	wins := 0
	for i := 0; i < 2; i++ {
		if opusWakeWait(t, results, "checkpoint writer") == nil {
			wins++
		}
	}
	raced := h.snapshot(t, root.ID)
	if wins != 1 || raced.conv.Revision != 2 {
		t.Fatalf("CAS race wins=%d %#v", wins, raced.conv)
	}
	if h.checkpoint(root.ID, 2, raced.conv.DeliveredCursor-1, "backwards") == nil || h.checkpoint(root.ID, 2, last+1, "past evidence") == nil {
		t.Fatal("checkpoint moved backwards or past durable evidence")
	}
	h.read(t, root.ID, raced.conv.DeliveredCursor, 2)
	if got := h.snapshot(t, root.ID); !opusWakeSameAck(got.conv, raced.conv) || got.policy != before.policy || got.pending != nil {
		t.Fatalf("rejected writers or read mutated state %#v %#v", got.conv, got.policy)
	}
	// Checkpoint acknowledgement does not consume the wake outbox either: the
	// next distinct child event still wakes once, without touching revision.
	if err := h.step(root.ID); err != nil {
		t.Fatal(err)
	}
	next := h.observed(t, root.ID)
	after := h.snapshot(t, root.ID)
	if _, sent := parent.snapshot(); len(next) != 2 || len(sent) != 2 || next[1].Sequence <= woke[0].Sequence || after.conv.Revision != 2 || after.conv.DeliveredCursor != raced.conv.DeliveredCursor {
		t.Fatalf("separate wake cursor %#v %#v sends=%d", next, after.conv, len(sent))
	}
	for _, e := range h.events(t, root.ID) {
		if e.Sequence == next[1].Sequence && (e.ConversationID != child.ID || e.State != "needs_input") {
			t.Fatalf("second wake selected %#v", e)
		}
	}
}
