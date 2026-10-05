package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// These journeys join the coordinator pieces end to end. Synthetic: native
// lifecycle.jsonl records, provider transcript echoes and the PTY Send
// callback. Real: the SQLite store, MCP reservation/read/checkpoint/wake
// tools, the wake controller step, the lifecycle journal reader and
// SubmitSessionPrompt's durable reservations. No provider or network starts,
// and receipts remain observational (never exactly-once delivery claims).

// opusExecutionEgg retains an execution directory for an ID that was reserved
// through MCP rather than fixtureConversation.
func opusExecutionEgg(t *testing.T, cfg *config.Config, session, provider, owner string, events ...egg.SessionEvent) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := "agent=claude\ncwd=" + cfg.Dir + "\nprovider_session_id=" + provider + "\nprovider_home=" + cfg.Dir + "\n"
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionPrincipal(dir, owner); err != nil {
		t.Fatal(err)
	}
	opusAppend(t, dir, events...)
	return dir
}

// opusAppend continues the exact journal sequence and returns the new cursors.
func opusAppend(t *testing.T, dir string, events ...egg.SessionEvent) []int64 {
	t.Helper()
	path := filepath.Join(dir, "lifecycle.jsonl")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	next := int64(bytes.Count(existing, []byte{'\n'}))
	var journal bytes.Buffer
	var sequences []int64
	for _, event := range events {
		next++
		event.Sequence = next
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		journal.Write(data)
		journal.WriteByte('\n')
		sequences = append(sequences, next)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(journal.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return sequences
}

func opusHook(eventType, state, provider string) egg.SessionEvent {
	return egg.SessionEvent{Type: eventType, Source: "claude_hook", State: state, ProviderSessionID: provider}
}

func opusTranscriptUser(provider, text string) egg.SessionEvent {
	raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": provider, "message": map[string]any{"role": "user", "content": text}})
	return egg.SessionEvent{Type: "message", Source: "claude_transcript", State: "working", Role: "user", Text: text, Raw: raw, ProviderSessionID: provider}
}

// opusNativeReader is the production journal reader for one owned execution.
func opusNativeReader(cfg *config.Config, session localSession) func(context.Context, int64, int) (egg.SessionView, error) {
	return func(ctx context.Context, after int64, limit int) (egg.SessionView, error) {
		if err := ctx.Err(); err != nil {
			return egg.SessionView{}, err
		}
		return lifecycleViewForSession(cfg, session, after, limit)
	}
}

func opusServer(cfg *config.Config, bound string) *localMCPServer {
	return &localMCPServer{cfg: cfg, principal: "owner", boundConversation: bound, logs: &bytes.Buffer{}}
}

func opusCall(t *testing.T, s *localMCPServer, tool string, args map[string]any) (map[string]any, error) {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	data, isError, protocolErr := s.callTool(context.Background(), tool, input)
	if protocolErr != nil {
		t.Fatalf("%s protocol error: %s", tool, protocolErr.Message)
	}
	if isError {
		message, _ := data["error"].(string)
		return data, errors.New(message)
	}
	return data, nil
}

// opusDrain pages conversation_read, reconnecting a new MCP server per page.
func opusDrain(t *testing.T, cfg *config.Config, root string, after int64, limit int) ([]store.ConversationEvent, int64, map[string]any) {
	t.Helper()
	var all []store.ConversationEvent
	for page := 0; page < 64; page++ {
		result, err := opusCall(t, opusServer(cfg, root), "conversation_read", map[string]any{"conversation_id": root, "after_cursor": after, "limit": limit})
		if err != nil {
			t.Fatal(err)
		}
		events := result["events"].([]store.ConversationEvent)
		next := result["next_cursor"].(int64)
		if len(events) > limit || next < after || (len(events) > 0 && next != events[len(events)-1].Sequence) {
			t.Fatalf("page after %d: next=%d events=%v", after, next, events)
		}
		all = append(all, events...)
		after = next
		if !result["has_more"].(bool) {
			return all, after, result
		}
	}
	t.Fatal("conversation_read pagination never ended")
	return nil, 0, nil
}

func opusStates(events []store.ConversationEvent, conversation string) string {
	var states []string
	for _, event := range events {
		if event.ConversationID == conversation {
			states = append(states, event.State)
		}
	}
	return strings.Join(states, ",")
}

func opusTask(t *testing.T, result map[string]any, conversation string) map[string]any {
	t.Helper()
	for _, task := range result["tasks"].([]map[string]any) {
		if task["conversation"].(*store.Conversation).ID == conversation {
			return task
		}
	}
	t.Fatalf("task %s missing from conversation_read", conversation)
	return nil
}

func opusReopen(t *testing.T, cfg *config.Config, use func(*store.Store)) {
	t.Helper()
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	use(db)
}

type opusPromptRecord struct {
	RequestID         string                  `json:"request_id"`
	ProviderSessionID string                  `json:"provider_session_id"`
	Phase             string                  `json:"phase"`
	Result            egg.SessionPromptResult `json:"result"`
}

// opusReservation inspects SubmitSessionPrompt's on-disk reservation.
func opusReservation(t *testing.T, dir, request string) (opusPromptRecord, bool) {
	t.Helper()
	key := sha256.Sum256([]byte(request))
	data, err := os.ReadFile(filepath.Join(dir, "prompt."+hex.EncodeToString(key[:])+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return opusPromptRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var record opusPromptRecord
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record, true
}

func TestOpusCoordinatorLinkedChildJourneyAcrossReconnectRestartAndCheckpoint(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	var root *store.Conversation
	opusReopen(t, cfg, func(db *store.Store) { root = fixtureConversation(t, db, cfg, "root", "", "owner", "idle") })

	// A parent-bound MCP connection reserves a child; a reconnecting connection
	// retries the same intention with a freshly generated execution ID.
	spec := map[string]any{"agent": "claude", "label": "child", "cwd": cfg.Dir}
	parent := opusServer(cfg, root.ID)
	if err := validateBoundConversation(parent); err != nil {
		t.Fatal(err)
	}
	child, created, err := parent.reserveAgentConversation("claude", cfg.Dir, "child", "child", "", "launch-child-1", "execution-child-a", spec)
	if err != nil || !created || child.ParentID != root.ID || child.RootID != root.ID || child.SessionID != "execution-child-a" {
		t.Fatalf("bound reservation %#v created=%t %v", child, created, err)
	}
	launch := conversationLaunchResult(child, false)
	if launch["launch_state"] != "starting" || launch["reconciliation_required"] != true || launch["ready"] != false {
		t.Fatalf("unreconciled launch overstated %v", launch)
	}
	reconnect := opusServer(cfg, root.ID)
	again, created, err := reconnect.reserveAgentConversation("claude", cfg.Dir, "child", "child", root.ID, "launch-child-1", "execution-child-b", spec)
	if err != nil || created || again.ID != child.ID || again.SessionID != child.SessionID {
		t.Fatalf("reconnect replay %#v created=%t %v", again, created, err)
	}
	if _, _, err = reconnect.reserveAgentConversation("claude", cfg.Dir, "child", "child", "", "launch-child-1", "execution-child-c", map[string]any{"agent": "claude", "label": "changed"}); err == nil {
		t.Fatal("reused request_id accepted a different intention")
	}
	if _, _, err = reconnect.reserveAgentConversation("claude", cfg.Dir, "peer", "parent", "", "launch-peer", "execution-peer", spec); err == nil {
		t.Fatal("bound connection created a parent")
	}
	if err = reconnect.markConversationLaunch(again, nil); err != nil {
		t.Fatal(err)
	}
	opusReopen(t, cfg, func(db *store.Store) {
		nodes, err := db.ListConversations("owner", root.ID)
		if err != nil || len(nodes) != 2 {
			t.Fatalf("tree %v %v", nodes, err)
		}
		executions, err := db.ConversationExecutions(child.ID)
		if err != nil || strings.Join(executions, ",") != child.SessionID {
			t.Fatalf("duplicate execution intention %v %v", executions, err)
		}
		if _, err = db.ConversationForSession("execution-child-b"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("retry execution linked: %v", err)
		}
	})
	started, created, err := opusServer(cfg, root.ID).reserveAgentConversation("claude", cfg.Dir, "child", "child", "", "launch-child-1", "execution-child-d", spec)
	if err != nil || created || started.SessionID != child.SessionID || started.LaunchState != "started" {
		t.Fatalf("started replay %#v %v", started, err)
	}
	if _, pending := conversationLaunchResult(started, true)["reconciliation_required"]; pending {
		t.Fatal("started launch still requests reconciliation")
	}

	// Native progress through the real journal reader. Attention and completion
	// land between reads, followed by more work: the final snapshot must not
	// collapse them.
	childDir := opusExecutionEgg(t, cfg, child.SessionID, "provider-child", "owner",
		opusHook("session_ready", "idle", "provider-child"), opusHook("prompt_submitted", "working", "provider-child"))
	first, cursor, _ := opusDrain(t, cfg, root.ID, 0, 1)
	if got := opusStates(first, child.ID); got != "idle,working" {
		t.Fatalf("initial child states %q", got)
	}
	opusAppend(t, childDir, opusHook("input_requested", "needs_input", "provider-child"), opusHook("turn_completed", "completed", "provider-child"), opusHook("prompt_submitted", "working", "provider-child"))
	progress, head, last := opusDrain(t, cfg, root.ID, cursor, 1)
	if got := opusStates(progress, child.ID); got != "needs_input,completed,working" {
		t.Fatalf("paginated progress lost transitions %q", got)
	}
	if view := opusTask(t, last, child.ID)["lifecycle"].(egg.SessionView); view.State != "working" || view.StateSource != "claude_hook" || len(view.Events) != 0 {
		t.Fatalf("child snapshot %+v", view)
	}
	replay, replayHead, _ := opusDrain(t, cfg, root.ID, 0, 2)
	if len(replay) != len(first)+len(progress) || replayHead != head || opusStates(replay, child.ID) != "idle,working,needs_input,completed,working" {
		t.Fatalf("replay after reopen %v head=%d/%d", replay, replayHead, head)
	}
	opusReopen(t, cfg, func(db *store.Store) {
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.DeliveredCursor != 0 || saved.Revision != 0 || saved.Checkpoint != "" {
			t.Fatalf("read acknowledged events %#v %v", saved, err)
		}
	})

	// Only an expected-revision checkpoint acknowledges; stale, rewinding and
	// beyond-evidence writes preserve the accepted state.
	checkpoint := func(expected, after int64, note string) error {
		_, err := opusCall(t, opusServer(cfg, root.ID), "conversation_checkpoint", map[string]any{"conversation_id": root.ID, "expected_revision": expected, "after_cursor": after, "checkpoint": note})
		return err
	}
	if err = checkpoint(0, cursor, "handled child start"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		expected, after int64
	}{{0, head}, {1, cursor - 1}, {1, head + 100}} {
		if err = checkpoint(bad.expected, bad.after, "rejected"); err == nil {
			t.Fatalf("checkpoint expected=%d after=%d accepted", bad.expected, bad.after)
		}
	}
	opusReopen(t, cfg, func(db *store.Store) {
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.Revision != 1 || saved.DeliveredCursor != cursor || saved.Checkpoint != "handled child start" {
			t.Fatalf("stale checkpoint changed state %#v %v", saved, err)
		}
	})
	if pending, _, _ := opusDrain(t, cfg, root.ID, cursor, 50); opusStates(pending, child.ID) != "needs_input,completed,working" {
		t.Fatalf("unacknowledged deliveries lost %v", pending)
	}
	if err = checkpoint(1, head, "handled attention and completion"); err != nil {
		t.Fatal(err)
	}

	// Continue the child through the real prompt reservation. Transport
	// acceptance alone stays unconfirmed.
	opusAppend(t, childDir, opusHook("turn_completed", "completed", "provider-child"))
	session, err := reconnect.resolveOwnedLifecycleSession(child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	input := "Continue from the retained checkpoint and report remaining risks."
	sends := 0
	options := egg.SessionPromptOptions{RequestID: "continue-child-1", Input: input, Timeout: 150 * time.Millisecond, Read: opusNativeReader(cfg, session),
		Send: func(_ context.Context, text string) (egg.PromptDelivery, error) {
			sends++
			return egg.PromptDelivery{BytesEnqueued: len(text) + 1}, nil
		}}
	result, err := egg.SubmitSessionPrompt(context.Background(), childDir, options)
	if err != nil || result.Status != "unconfirmed" || !result.TransportEnqueued || result.TransportBytesEnqueued != len(input)+1 || result.NativeReceiptObserved || result.DefinitelyNotSent || result.ProviderRequestAcknowledged || sends != 1 {
		t.Fatalf("enqueued bytes overstated %+v sends=%d %v", result, sends, err)
	}
	if saved, ok := opusReservation(t, childDir, options.RequestID); !ok || saved.Phase != "transport_attempted" || saved.ProviderSessionID != "provider-child" || saved.Result.Status != "unconfirmed" || saved.Result.ReservedCursor != result.ReservedCursor {
		t.Fatalf("durable reservation %+v", saved)
	}

	// Restart while the provider is busy: the exact transcript user text
	// reconciles the same request and nothing is sent again.
	receipt := opusAppend(t, childDir, opusHook("prompt_submitted", "working", "provider-child"), opusTranscriptUser("provider-child", input))[1]
	session, err = opusServer(cfg, root.ID).resolveOwnedLifecycleSession(child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	options.Read = opusNativeReader(cfg, session)
	options.Send = func(context.Context, string) (egg.PromptDelivery, error) {
		t.Error("reconciliation resent an existing request")
		return egg.PromptDelivery{}, errors.New("unexpected send")
	}
	if busy, err := options.Read(context.Background(), 0, 1); err != nil || egg.NativePromptReady(busy) {
		t.Fatalf("fixture provider is not busy %+v %v", busy, err)
	}
	result, err = egg.SubmitSessionPrompt(context.Background(), childDir, options)
	if err != nil || result.Status != "native_receipt_observed" || !result.NativeReceiptObserved || !result.Retried || result.ReceiptCursor != receipt || result.ProviderRequestAcknowledged || result.Causality != "unverified_without_provider_request_id" || sends != 1 {
		t.Fatalf("restart reconciliation %+v sends=%d %v", result, sends, err)
	}
	if saved, _ := opusReservation(t, childDir, options.RequestID); saved.Phase != "native_receipt_observed" || saved.Result.ReceiptCursor != receipt {
		t.Fatalf("receipt not persisted %+v", saved)
	}
	fresh := options
	fresh.RequestID = "continue-child-2"
	if _, err = egg.SubmitSessionPrompt(context.Background(), childDir, fresh); err == nil || !strings.Contains(err.Error(), "native foreground readiness required") {
		t.Fatalf("busy provider accepted new request: %v", err)
	}
	if _, ok := opusReservation(t, childDir, fresh.RequestID); ok {
		t.Fatal("refused request left a reservation")
	}

	// The receipt is not an acknowledgement; the tree still offers the
	// post-checkpoint native progress.
	tail, _, _ := opusDrain(t, cfg, root.ID, head, 50)
	if got := opusStates(tail, child.ID); got != "completed,working" {
		t.Fatalf("continuation progress %q", got)
	}
	opusReopen(t, cfg, func(db *store.Store) {
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.Revision != 2 || saved.DeliveredCursor != head {
			t.Fatalf("prompt receipt moved checkpoint %#v %v", saved, err)
		}
	})
}

func TestOpusCoordinatorWakeOutboxKeepsUnknownBindingAcrossParentResumeAndProofGatedRetry(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	var root, child *store.Conversation
	opusReopen(t, cfg, func(db *store.Store) {
		root = fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
		child = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "working", "needs_input", "completed")
		if err := db.SetConversationWake("owner", root.ID, true); err != nil {
			t.Fatal(err)
		}
	})
	rootDir := filepath.Join(cfg.Dir, "eggs", root.SessionID)
	opusAppend(t, rootDir, opusHook("session_ready", "idle", "provider-root"))

	type attempt struct{ session, request, input string }
	var prompts, transport []attempt
	now := time.Now()
	writerHeld, echo, noInput := false, false, 0
	runtime := conversationWakeRuntime{Read: nativeConversationWakeRuntime(cfg).Read, Now: func() time.Time { return now },
		Prompt: func(ctx context.Context, session localSession, request, text string) (egg.SessionPromptResult, error) {
			prompts = append(prompts, attempt{session.ID, request, text})
			dir := filepath.Join(cfg.Dir, "eggs", session.ID)
			return egg.SubmitSessionPrompt(ctx, dir, egg.SessionPromptOptions{RequestID: request, Input: text, Timeout: 100 * time.Millisecond, Read: opusNativeReader(cfg, session),
				Send: func(_ context.Context, input string) (egg.PromptDelivery, error) {
					if writerHeld {
						noInput++
						return egg.PromptDelivery{NoInputAttempted: true}, errors.New("synthetic writer lease held by another input owner")
					}
					transport = append(transport, attempt{session.ID, request, input})
					if echo {
						provider := readEggMetaValues(dir)["provider_session_id"]
						opusAppend(t, dir, opusHook("prompt_submitted", "working", provider), opusTranscriptUser(provider, input))
					}
					return egg.PromptDelivery{BytesEnqueued: len(input)}, nil
				}})
		}}
	step := func() {
		t.Helper()
		if err := processConversationWake(context.Background(), opusServer(cfg, ""), root.ID, runtime); err != nil {
			t.Fatal(err)
		}
	}
	pending := func() *store.ConversationWake {
		t.Helper()
		var w *store.ConversationWake
		opusReopen(t, cfg, func(db *store.Store) {
			var err error
			if w, err = db.PendingConversationWake(root.ID); err != nil {
				t.Fatal(err)
			}
		})
		return w
	}
	outbox := func(sequence int64) (status string, attempts int, receipt int64, request string) {
		t.Helper()
		opusReopen(t, cfg, func(db *store.Store) {
			if err := db.DB().QueryRow(`SELECT status,attempt,receipt_cursor,request_id FROM conversation_wake_outbox WHERE root_id=? AND event_sequence=?`, root.ID, sequence).Scan(&status, &attempts, &receipt, &request); err != nil {
				t.Fatal(err)
			}
		})
		return
	}

	// Attention wake: bytes enqueued without native receipt stays unknown.
	step()
	unknown := pending()
	if unknown == nil || unknown.Status != "unconfirmed" || unknown.Event.State != "needs_input" || unknown.Event.ConversationID != child.ID || unknown.SessionID != root.SessionID || unknown.ProviderSessionID != "provider-root" || unknown.Attempt != 1 || len(transport) != 1 {
		t.Fatalf("attention wake %#v transport=%d", unknown, len(transport))
	}
	if transport[0].request != unknown.RequestID || transport[0].input != unknown.Input || !strings.Contains(unknown.Input, `"child_provider_session_id":"provider-child"`) || !strings.Contains(unknown.Input, "do not answer permissions automatically") {
		t.Fatalf("wake payload %q", unknown.Input)
	}
	if saved, ok := opusReservation(t, rootDir, unknown.RequestID); !ok || saved.Phase != "transport_attempted" || saved.ProviderSessionID != "provider-root" {
		t.Fatalf("wake reservation %+v", saved)
	}
	retry, _ := json.Marshal(map[string]any{"conversation_id": root.ID, "retry_not_sent": true})
	if _, err := opusServer(cfg, "").toolConversationWake(retry); err == nil {
		t.Fatal("ambiguous delivery accepted an explicit not-sent retry")
	}

	// The parent resumes into a new execution. The unknown request stays bound
	// to its original execution, provider and input.
	resumedDir := opusExecutionEgg(t, cfg, "execution-root-resumed", "provider-root-resumed", "owner", opusHook("session_ready", "idle", "provider-root-resumed"))
	opusReopen(t, cfg, func(db *store.Store) {
		if err := db.ResumeConversationExecution(root.SessionID, "execution-root-resumed"); err != nil {
			t.Fatal(err)
		}
	})
	step()
	if still := pending(); still.Status != "unconfirmed" || still.RequestID != unknown.RequestID || still.SessionID != unknown.SessionID || still.ProviderSessionID != unknown.ProviderSessionID || still.Input != unknown.Input || still.Attempt != unknown.Attempt || still.EventSequence != unknown.EventSequence {
		t.Fatalf("unknown binding changed after resume %#v", still)
	}
	if len(transport) != 1 || len(prompts) != 2 || prompts[1].session != root.SessionID || prompts[1].request != unknown.RequestID {
		t.Fatalf("resume redirected or resent prompts=%v transport=%v", prompts, transport)
	}
	if _, ok := opusReservation(t, resumedDir, unknown.RequestID); ok {
		t.Fatal("unknown request reserved on resumed execution")
	}

	// The original provider is busy when its transcript shows the exact text.
	receipt := opusAppend(t, rootDir, opusHook("prompt_submitted", "working", "provider-root"), opusTranscriptUser("provider-root", unknown.Input))[1]
	step()
	if len(transport) != 1 || len(prompts) != 3 || prompts[2].request != unknown.RequestID || prompts[2].session != root.SessionID {
		t.Fatalf("busy reconciliation resent prompts=%v transport=%v", prompts, transport)
	}
	status, _, receiptCursor, _ := outbox(unknown.EventSequence)
	var policy store.ConversationWakePolicy
	opusReopen(t, cfg, func(db *store.Store) {
		var err error
		if policy, err = db.ConversationWakePolicy(root.ID); err != nil {
			t.Fatal(err)
		}
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.DeliveredCursor != 0 || saved.Revision != 0 {
			t.Fatalf("wake receipt acknowledged checkpoint %#v %v", saved, err)
		}
	})
	if status != "observed" || receiptCursor != receipt || policy.DeliveryCursor != unknown.EventSequence {
		t.Fatalf("receipt status=%s cursor=%d/%d policy=%+v", status, receiptCursor, receipt, policy)
	}

	// Checkpointing moves the coordinator acknowledgement only.
	checkpoint := map[string]any{"conversation_id": root.ID, "expected_revision": 0, "after_cursor": unknown.EventSequence, "checkpoint": "handled child attention"}
	if _, err := opusCall(t, opusServer(cfg, root.ID), "conversation_checkpoint", checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint["checkpoint"] = "stale controller"
	if _, err := opusCall(t, opusServer(cfg, root.ID), "conversation_checkpoint", checkpoint); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	opusReopen(t, cfg, func(db *store.Store) {
		after, err := db.ConversationWakePolicy(root.ID)
		if err != nil || after != policy {
			t.Fatalf("checkpoint moved wake cursors %+v != %+v %v", after, policy, err)
		}
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.Revision != 1 || saved.DeliveredCursor != unknown.EventSequence || saved.Checkpoint != "handled child attention" {
			t.Fatalf("checkpoint %#v %v", saved, err)
		}
	})
	if again, _, againReceipt, _ := outbox(unknown.EventSequence); again != "observed" || againReceipt != receipt {
		t.Fatalf("checkpoint rewrote receipt %s %d", again, againReceipt)
	}

	// Completion wake targets the resumed parent. Typed no-input proof allows
	// fresh request IDs only after cooldown, until the bounded cycle blocks.
	writerHeld = true
	step()
	first := pending()
	if first.Status != "not_sent" || first.Event.State != "completed" || first.Event.ConversationID != child.ID || first.SessionID != "execution-root-resumed" || first.ProviderSessionID != "provider-root-resumed" || first.Attempt != 1 || first.RetryAfter != now.Unix()+5 || noInput != 1 {
		t.Fatalf("not-sent proof %#v noInput=%d", first, noInput)
	}
	held := len(prompts)
	step()
	if len(prompts) != held {
		t.Fatal("not-sent retry ignored cooldown")
	}
	requests := map[string]bool{first.RequestID: true}
	for i := 0; i < 20 && pending().Status != "blocked"; i++ {
		now = now.Add(6 * time.Second)
		step()
		requests[pending().RequestID] = true
	}
	blocked := pending()
	if blocked.Status != "blocked" || len(requests) != blocked.AttemptLimit || noInput != blocked.AttemptLimit || len(transport) != 1 {
		t.Fatalf("bounded cycle %#v requests=%v noInput=%d", blocked, requests, noInput)
	}
	for request := range requests {
		if saved, ok := opusReservation(t, resumedDir, request); !ok || saved.Phase != "not_sent" || !saved.Result.DefinitelyNotSent || saved.Result.TransportBytesEnqueued != 0 {
			t.Fatalf("not-sent proof for %s: %+v", request, saved)
		}
	}
	proof, err := egg.SubmitSessionPrompt(context.Background(), resumedDir, egg.SessionPromptOptions{RequestID: first.RequestID, Input: first.Input, Timeout: 100 * time.Millisecond,
		Read: func(context.Context, int64, int) (egg.SessionView, error) {
			return egg.SessionView{}, errors.New("native reader unavailable")
		},
		Send: func(context.Context, string) (egg.PromptDelivery, error) {
			t.Error("terminal not-sent request resent")
			return egg.PromptDelivery{}, nil
		}})
	if err != nil || proof.Status != "not_sent" || !proof.DefinitelyNotSent || !proof.Retried {
		t.Fatalf("not-sent replay %+v %v", proof, err)
	}
	writerHeld = false
	now = now.Add(time.Minute)
	held = len(prompts)
	step()
	if len(prompts) != held {
		t.Fatal("blocked wake retried without a deliberate request")
	}

	// A deliberate user retry adds bounded attempts and keeps the cooldown.
	if _, err = opusServer(cfg, "").toolConversationWake(retry); err != nil {
		t.Fatal(err)
	}
	authorized := pending()
	if authorized.Status != "not_sent" || authorized.AttemptLimit != blocked.AttemptLimit+3 || authorized.Attempt != blocked.Attempt || authorized.RequestID != blocked.RequestID || authorized.RetryAfter <= time.Now().Unix() {
		t.Fatalf("deliberate retry %#v", authorized)
	}
	now = time.Unix(authorized.RetryAfter-1, 0)
	step()
	if len(prompts) != held {
		t.Fatal("deliberate retry bypassed cooldown")
	}
	echo = true
	now = time.Unix(authorized.RetryAfter+1, 0)
	step()
	if len(transport) != 2 || transport[1].session != "execution-root-resumed" || requests[transport[1].request] || transport[1].request == unknown.RequestID {
		t.Fatalf("deliberate request transport=%v", transport)
	}
	if left := pending(); left != nil {
		t.Fatalf("observed completion still pending %#v", left)
	}
	status, attempts, receiptCursor, request := outbox(blocked.EventSequence)
	if status != "observed" || attempts != blocked.Attempt+1 || request != transport[1].request || receiptCursor == 0 {
		t.Fatalf("completion outbox %s attempt=%d receipt=%d request=%s", status, attempts, receiptCursor, request)
	}
	opusReopen(t, cfg, func(db *store.Store) {
		p, err := db.ConversationWakePolicy(root.ID)
		if err != nil || p.DeliveryCursor != blocked.EventSequence {
			t.Fatalf("wake delivery cursor %+v %v", p, err)
		}
		saved, err := db.GetConversation("owner", root.ID)
		if err != nil || saved.Revision != 1 || saved.DeliveredCursor != unknown.EventSequence {
			t.Fatalf("wake receipt moved checkpoint %#v %v", saved, err)
		}
	})
	step()
	if len(transport) != 2 {
		t.Fatalf("steady state resent %v", transport)
	}
}

func TestOpusCoordinatorMissingNativeHistoryIsReportedUnavailableNotCompleted(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	var root, reaped, vanished, crashed *store.Conversation
	opusReopen(t, cfg, func(db *store.Store) {
		root = fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
		reaped = fixtureConversation(t, db, cfg, "reaped", root.ID, "owner", "working", "completed")
		vanished = fixtureConversation(t, db, cfg, "vanished", root.ID, "owner", "completed")
		crashed = fixtureConversation(t, db, cfg, "crashed", root.ID, "owner", "working")
		if err := db.SetConversationWake("owner", root.ID, true); err != nil {
			t.Fatal(err)
		}
		// The first execution's history disappears before any coordinator
		// import; a resumed execution continues the same logical child. The
		// resumed ID deliberately does not extend the retired one; prefix
		// aliasing is covered by its own test below.
		opusExecutionEgg(t, cfg, "resumed-reaped", "provider-reaped-resumed", "owner", opusHook("prompt_submitted", "working", "provider-reaped-resumed"))
		if err := db.ResumeConversationExecution(reaped.SessionID, "resumed-reaped"); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.RemoveAll(filepath.Join(cfg.Dir, "eggs", reaped.SessionID)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.Dir, "eggs", vanished.SessionID)); err != nil {
		t.Fatal(err)
	}
	crashedDir := filepath.Join(cfg.Dir, "eggs", crashed.SessionID)
	if err := os.Remove(filepath.Join(crashedDir, "egg.pid")); err != nil {
		t.Fatal(err)
	}

	result, err := opusCall(t, opusServer(cfg, root.ID), "conversation_read", map[string]any{"conversation_id": root.ID, "limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	reapedTask := opusTask(t, result, reaped.ID)
	if reapedTask["history_unavailable"] != true {
		t.Fatalf("reaped execution not reported unavailable %v", reapedTask)
	}
	if view, ok := reapedTask["lifecycle"].(egg.SessionView); !ok || view.SessionID != "resumed-reaped" || view.State != "working" {
		t.Fatalf("resumed execution view %+v", reapedTask["lifecycle"])
	}
	vanishedTask := opusTask(t, result, vanished.ID)
	if _, ok := vanishedTask["lifecycle"]; ok || vanishedTask["lifecycle_error"] == nil {
		t.Fatalf("missing current execution invented lifecycle %v", vanishedTask)
	}
	crashedView := opusTask(t, result, crashed.ID)["lifecycle"].(egg.SessionView)
	if crashedView.State != "unknown" || crashedView.StateSource != "egg_process" || crashedView.ProcessAlive || crashedView.Ready || !strings.Contains(crashedView.Reason, "without a recorded exit") {
		t.Fatalf("vanished process view %+v", crashedView)
	}
	events := result["events"].([]store.ConversationEvent)
	for _, event := range events {
		if event.ConversationID != root.ID && event.State == "completed" {
			t.Fatalf("invented completion %+v", event)
		}
	}
	if opusStates(events, reaped.ID) != "working" || opusStates(events, vanished.ID) != "" || opusStates(events, crashed.ID) != "working,unknown" {
		t.Fatalf("unavailable evidence states %v", events)
	}

	// Unavailable evidence queues no wake. The root is never prompt-ready
	// here, so a queued wake stays inspectable without any send.
	runtime := conversationWakeRuntime{Read: nativeConversationWakeRuntime(cfg).Read, Now: time.Now,
		Prompt: func(context.Context, localSession, string, string) (egg.SessionPromptResult, error) {
			t.Error("unready parent prompted")
			return egg.SessionPromptResult{}, errors.New("unexpected prompt")
		}}
	if err = processConversationWake(context.Background(), opusServer(cfg, ""), root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	var w *store.ConversationWake
	opusReopen(t, cfg, func(db *store.Store) { w, err = db.PendingConversationWake(root.ID) })
	if err != nil || w != nil {
		t.Fatalf("unavailable history queued wake %#v %v", w, err)
	}

	// A recorded exit is real evidence: it wakes as stopped, never as a
	// completed task, and the private exit reason stays out of the payload.
	if err = egg.RecordSessionProcessEvent(crashedDir, "session_exit", "stopped", "private fixture exit reason"); err != nil {
		t.Fatal(err)
	}
	if err = processConversationWake(context.Background(), opusServer(cfg, ""), root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	opusReopen(t, cfg, func(db *store.Store) { w, err = db.PendingConversationWake(root.ID) })
	if err != nil || w == nil || w.Status != "queued" || w.Event.ConversationID != crashed.ID || w.Event.State != "stopped" || w.Event.StateSource != "egg_process" || w.Event.Type != "session_exit" {
		t.Fatalf("exit wake %#v %v", w, err)
	}
	text, err := conversationWakeText(cfg, root, w)
	if err != nil || !strings.Contains(text, "child execution stopped; inspect retained evidence") || strings.Contains(text, `"state":"completed"`) || strings.Contains(text, "private fixture exit reason") {
		t.Fatalf("exit wake text %q %v", text, err)
	}
}

// A retired execution ID is an exact store key, not a user selector. Legacy
// eight-character runtime IDs can be prefixes of newer sixteen-character IDs,
// so once the retired directory is gone its history must read as unavailable
// rather than resolving to whichever surviving execution extends it.
func TestOpusCoordinatorRetiredExecutionIsNotAliasedToPrefixSibling(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	const retired, resumed = "a1b2c3d4", "a1b2c3d4e5f60718"
	var root, child *store.Conversation
	opusReopen(t, cfg, func(db *store.Store) {
		root = fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
		var err error
		child, _, err = db.ReserveConversation(store.Conversation{ID: "legacy", ParentID: root.ID, OwnerID: "owner", SessionID: retired, LaunchKey: "request-legacy", SpecDigest: "legacy", Agent: "claude", CWD: cfg.Dir, WingID: "wing-fixture"})
		if err != nil {
			t.Fatal(err)
		}
		opusExecutionEgg(t, cfg, retired, "provider-legacy", "owner", opusHook("stop", "completed", "provider-legacy"))
		opusExecutionEgg(t, cfg, resumed, "provider-legacy-resumed", "owner", opusHook("prompt_submitted", "working", "provider-legacy-resumed"))
		if err := db.ResumeConversationExecution(retired, resumed); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.RemoveAll(filepath.Join(cfg.Dir, "eggs", retired)); err != nil {
		t.Fatal(err)
	}

	result, err := opusCall(t, opusServer(cfg, root.ID), "conversation_read", map[string]any{"conversation_id": root.ID, "limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	task := opusTask(t, result, child.ID)
	if task["history_unavailable"] != true {
		t.Errorf("retired execution %s resolved to a prefix sibling instead of unavailable: %v", retired, task)
	}
	if view, ok := task["lifecycle"].(egg.SessionView); !ok || view.SessionID != resumed || view.State != "working" {
		t.Errorf("current execution view %+v", task["lifecycle"])
	}
	for _, event := range result["events"].([]store.ConversationEvent) {
		if event.SessionID == retired {
			t.Errorf("sibling evidence attributed to retired execution %+v", event)
		}
	}
	opusReopen(t, cfg, func(db *store.Store) {
		cursor, err := db.ConversationImportCursor(retired)
		if err != nil || cursor != 0 {
			t.Errorf("retired import cursor advanced from sibling journal: %d %v", cursor, err)
		}
	})
}
