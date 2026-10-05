package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestConversationWakeQueuesApprovalAndReconcilesNativeReceiptAfterRestart(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	child := fixtureConversation(t, db, cfg, "child", root.ID, "owner", "working", "needs_input", "completed")
	resumed := fixtureConversation(t, db, cfg, "resumed-child", "", "owner", "working")
	if _, err = db.DB().Exec(`DELETE FROM conversation_executions WHERE session_id=?`, resumed.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB().Exec(`DELETE FROM conversations WHERE id=?`, resumed.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.ResumeConversationExecution(child.SessionID, resumed.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s := &localMCPServer{cfg: cfg, principal: "owner"}
	state := "needs_input"
	sends := 0
	var sentID, sentText string
	var nativeEvents []egg.SessionEvent
	nativeRead := func(_ context.Context, after int64, _ int) (egg.SessionView, error) {
		view := egg.SessionView{SessionID: root.SessionID, Agent: "claude", ProviderSessionID: "provider-root", State: state, StateSource: "claude_hook", Ready: true, ProcessAlive: true, Cursor: 1, HeadCursor: 1}
		for _, event := range nativeEvents {
			view.HeadCursor = event.Sequence
			if event.Sequence > after {
				view.Events = append(view.Events, event)
				view.Cursor = event.Sequence
			}
		}
		return view, nil
	}
	runtime := conversationWakeRuntime{Read: func(ctx context.Context, _ localSession) (egg.SessionView, error) { return nativeRead(ctx, 0, 1) }, Now: time.Now,
		Prompt: func(ctx context.Context, session localSession, id, text string) (egg.SessionPromptResult, error) {
			sentID, sentText = id, text
			return egg.SubmitSessionPrompt(ctx, filepath.Join(cfg.Dir, "eggs", session.ID), egg.SessionPromptOptions{RequestID: id, Input: text, Timeout: 100 * time.Millisecond, Read: nativeRead, Send: func(context.Context, string) (egg.PromptDelivery, error) {
				sends++
				return egg.PromptDelivery{BytesEnqueued: len(text)}, nil
			}})
		},
	}
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if sends != 0 {
		t.Fatal("answered parent permission wait")
	}
	db, _ = store.Open(cfg.DBPath())
	queued, _ := db.PendingConversationWake(root.ID)
	_ = db.Close()
	if queued == nil || queued.Status != "queued" || queued.Event.State != "needs_input" {
		t.Fatalf("attention not queued %#v", queued)
	}
	state = "idle"
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || !strings.Contains(sentText, "child_provider_session_id") || !strings.Contains(sentText, "do not answer permissions automatically") {
		t.Fatalf("wake sends=%d text=%q", sends, sentText)
	}
	firstID := sentID
	db, _ = store.Open(cfg.DBPath())
	unknown, _ := db.PendingConversationWake(root.ID)
	_ = db.Close()
	if unknown.Status != "unconfirmed" {
		t.Fatalf("unknown %#v", unknown)
	}
	// Restart the controller and observe the real typed prompt reservation's
	// native transcript receipt while the parent is already doing other work.
	s = &localMCPServer{cfg: cfg, principal: "owner"}
	state = "working"
	raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": "provider-root", "message": map[string]any{"role": "user", "content": sentText}})
	nativeEvents = append(nativeEvents, egg.SessionEvent{Sequence: 2, Type: "message", Source: "claude_transcript", ProviderSessionID: "provider-root", Raw: raw})
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || sentID != firstID {
		t.Fatalf("restart duplicated wake: sends=%d %s != %s", sends, sentID, firstID)
	}
	db, _ = store.Open(cfg.DBPath())
	p, _ := db.ConversationWakePolicy(root.ID)
	saved, _ := db.GetConversation("owner", root.ID)
	_ = db.Close()
	if p.DeliveryCursor != queued.EventSequence || saved.DeliveredCursor != 0 || saved.Revision != 0 {
		t.Fatalf("receipt changed ACK %#v %#v", p, saved)
	}
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	db, _ = store.Open(cfg.DBPath())
	next, _ := db.PendingConversationWake(root.ID)
	_ = db.Close()
	if sends != 1 || next.Event.State != "completed" || next.Event.ConversationID != child.ID || next.Event.SessionID != child.SessionID {
		t.Fatalf("completion queue %#v sends=%d", next, sends)
	}
}

func TestConversationWakeFreshAttemptsRequireExplicitNoInputProof(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unproved", true: "known_not_sent"}[known], func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
			_ = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
			if err = db.SetConversationWake("owner", root.ID, true); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			now := time.Unix(100, 0)
			var ids []string
			runtime := conversationWakeRuntime{Now: func() time.Time { return now }, Read: func(_ context.Context, s localSession) (egg.SessionView, error) {
				return egg.SessionView{SessionID: s.ID, Agent: "claude", ProviderSessionID: "provider-root", State: "idle", StateSource: "claude_hook", ProcessAlive: true, Ready: true}, nil
			}, Prompt: func(_ context.Context, _ localSession, id, _ string) (egg.SessionPromptResult, error) {
				ids = append(ids, id)
				return egg.SessionPromptResult{Status: "not_sent", DefinitelyNotSent: known, TransportBytesEnqueued: 0}, nil
			}}
			s := &localMCPServer{cfg: cfg, principal: "owner"}
			for i := 0; i < 4; i++ {
				if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
					t.Fatal(err)
				}
				now = now.Add(6 * time.Second)
			}
			db, _ = store.Open(cfg.DBPath())
			w, _ := db.PendingConversationWake(root.ID)
			_ = db.Close()
			if known {
				if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] || w.Status != "blocked" {
					t.Fatalf("known retries %v %#v", ids, w)
				}
			} else {
				if len(ids) != 4 || ids[0] != ids[1] || ids[1] != ids[2] || ids[2] != ids[3] || w.Status != "unconfirmed" {
					t.Fatalf("unproved replacement %v %#v", ids, w)
				}
			}
		})
	}
}

func TestConversationWakeNeverRedirectsUnknownToResumedParent(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	_ = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	_ = db.SetConversationWake("owner", root.ID, true)
	s := &localMCPServer{cfg: cfg, principal: "owner"}
	var destinations, requests []string
	runtime := conversationWakeRuntime{Now: time.Now, Read: func(_ context.Context, session localSession) (egg.SessionView, error) {
		return egg.SessionView{SessionID: session.ID, Agent: "claude", ProviderSessionID: readEggMetaValues(filepath.Join(cfg.Dir, "eggs", session.ID))["provider_session_id"], State: "idle", StateSource: "claude_hook", Ready: true, ProcessAlive: true}, nil
	}, Prompt: func(_ context.Context, session localSession, id, _ string) (egg.SessionPromptResult, error) {
		destinations = append(destinations, session.ID)
		requests = append(requests, id)
		return egg.SessionPromptResult{Status: "unconfirmed"}, nil
	}}
	_ = db.Close()
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	db, _ = store.Open(cfg.DBPath())
	next := fixtureConversation(t, db, cfg, "next", "", "owner", "idle")
	_, _ = db.DB().Exec(`DELETE FROM conversation_executions WHERE session_id=?`, next.SessionID)
	_, _ = db.DB().Exec(`DELETE FROM conversations WHERE id=?`, next.ID)
	if err = db.ResumeConversationExecution(root.SessionID, next.SessionID); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 2 || destinations[0] != root.SessionID || destinations[1] != root.SessionID || requests[0] != requests[1] {
		t.Fatalf("redirected unknown %v %v", destinations, requests)
	}
	// Provider identity replacement on that original execution prevents even
	// reconciliation through the wrong provider. The pending request remains.
	metaPath := filepath.Join(cfg.Dir, "eggs", root.SessionID, "egg.meta")
	data, _ := os.ReadFile(metaPath)
	data = []byte(strings.ReplaceAll(string(data), "provider-root", "provider-replacement"))
	_ = os.WriteFile(metaPath, data, 0600)
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err == nil {
		t.Fatal("reconciled different provider")
	}
	if len(destinations) != 2 {
		t.Fatal("called changed native target")
	}
}

func TestConversationWakeOptInOwnerAndPersonalScope(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	_ = db.Close()
	args, _ := json.Marshal(map[string]any{"conversation_id": root.ID, "enabled": true})
	s := &localMCPServer{cfg: cfg, principal: "other"}
	if _, err = s.toolConversationWake(args); err == nil {
		t.Fatal("wrong owner opted in")
	}
	s.principal = "owner"
	s.identity.SharedHost = true
	if _, err = s.toolConversationWake(args); err == nil {
		t.Fatal("shared host opted in")
	}
	s.identity.SharedHost = false
	if _, err = s.toolConversationWake(args); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cfg.Dir, "wing.yaml"), []byte("org: shared-org\n"), 0600)
	if _, err = s.toolConversationWake(args); err == nil {
		t.Fatal("organization opted in")
	}
}

func TestConversationWakeRetainedRuntimeExitAndStartupFailure(t *testing.T) {
	for _, eventType := range []string{"session_exit", "session_failed"} {
		t.Run(eventType, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
			child := fixtureConversation(t, db, cfg, "child", root.ID, "owner", "working")
			childDir := filepath.Join(cfg.Dir, "eggs", child.SessionID)
			if eventType == "session_failed" {
				meta, _ := os.ReadFile(filepath.Join(childDir, "egg.meta"))
				meta = []byte(strings.ReplaceAll(string(meta), "provider_session_id=provider-child\n", ""))
				if err = os.WriteFile(filepath.Join(childDir, "egg.meta"), meta, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = egg.RecordSessionProcessEvent(childDir, eventType, "failed", "private runtime fixture reason"); err != nil {
				t.Fatal(err)
			}
			if err = db.SetConversationWake("owner", root.ID, true); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			sends := 0
			runtime := conversationWakeRuntime{Now: time.Now,
				Read: func(_ context.Context, session localSession) (egg.SessionView, error) {
					return egg.SessionView{SessionID: session.ID, Agent: "claude", ProviderSessionID: "provider-root", State: "idle", StateSource: "claude_hook", ProcessAlive: true, Ready: true}, nil
				},
				Prompt: func(_ context.Context, _ localSession, _ string, text string) (egg.SessionPromptResult, error) {
					sends++
					if !strings.Contains(text, `"source":"egg_process"`) || strings.Contains(text, "private runtime fixture reason") {
						t.Fatalf("runtime payload %q", text)
					}
					if eventType == "session_failed" && !strings.Contains(text, `"child_provider_identity_known":false`) {
						t.Fatalf("invented startup provider %q", text)
					}
					return egg.SessionPromptResult{Status: "native_receipt_observed", NativeReceiptObserved: true, ReceiptCursor: 7}, nil
				},
			}
			s := &localMCPServer{cfg: cfg, principal: "owner"}
			if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
				t.Fatal(err)
			}
			if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
				t.Fatal(err)
			}
			if sends != 1 {
				t.Fatalf("retained runtime event sends=%d", sends)
			}
		})
	}
}

func TestConversationWakeConcurrentControllersHaveOneRootSender(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	_ = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var sends atomic.Int32
	runtime := conversationWakeRuntime{Now: time.Now,
		Read: func(_ context.Context, session localSession) (egg.SessionView, error) {
			return egg.SessionView{SessionID: session.ID, Agent: "claude", ProviderSessionID: "provider-root", State: "idle", StateSource: "claude_hook", ProcessAlive: true, Ready: true}, nil
		},
		Prompt: func(context.Context, localSession, string, string) (egg.SessionPromptResult, error) {
			sends.Add(1)
			return egg.SessionPromptResult{Status: "native_receipt_observed", NativeReceiptObserved: true, ReceiptCursor: 9}, nil
		},
	}
	var group sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			s := &localMCPServer{cfg: cfg, principal: "owner"}
			errs <- processConversationWake(context.Background(), s, root.ID, runtime)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !strings.Contains(err.Error(), "another daemon start/stop") {
			t.Fatal(err)
		}
	}
	if sends.Load() != 1 {
		t.Fatalf("concurrent root sends=%d", sends.Load())
	}
}

func TestConversationWakeExplicitRetryAfterWriterReleaseUsesFreshNativeRequest(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	_ = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	now := time.Unix(100, 0)
	writerBusy := true
	noInputAttempts, transportSends := 0, 0
	var ids []string
	var events []egg.SessionEvent
	read := func(_ context.Context, after int64, _ int) (egg.SessionView, error) {
		v := egg.SessionView{SessionID: root.SessionID, Agent: "claude", ProviderSessionID: "provider-root", State: "idle", StateSource: "claude_hook", Ready: true, ProcessAlive: true, Cursor: 1, HeadCursor: 1}
		for _, e := range events {
			v.HeadCursor = e.Sequence
			if e.Sequence > after {
				v.Events = append(v.Events, e)
				v.Cursor = e.Sequence
			}
		}
		return v, nil
	}
	runtime := conversationWakeRuntime{Now: func() time.Time { return now }, Read: func(ctx context.Context, _ localSession) (egg.SessionView, error) { return read(ctx, 0, 1) }, Prompt: func(ctx context.Context, session localSession, id, text string) (egg.SessionPromptResult, error) {
		ids = append(ids, id)
		return egg.SubmitSessionPrompt(ctx, filepath.Join(cfg.Dir, "eggs", session.ID), egg.SessionPromptOptions{RequestID: id, Input: text, Timeout: 100 * time.Millisecond, Read: read, Send: func(context.Context, string) (egg.PromptDelivery, error) {
			if writerBusy {
				noInputAttempts++
				return egg.PromptDelivery{NoInputAttempted: true}, errors.New("synthetic writer lease is held")
			}
			transportSends++
			raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": "provider-root", "message": map[string]any{"role": "user", "content": text}})
			events = append(events, egg.SessionEvent{Sequence: 2, Type: "message", Source: "claude_transcript", ProviderSessionID: "provider-root", Raw: raw})
			return egg.PromptDelivery{BytesEnqueued: len(text)}, nil
		}})
	}}
	s := &localMCPServer{cfg: cfg, principal: "owner"}
	for i := 0; i < 3; i++ {
		if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
			t.Fatal(err)
		}
		now = now.Add(6 * time.Second)
	}
	db, _ = store.Open(cfg.DBPath())
	blocked, _ := db.PendingConversationWake(root.ID)
	_ = db.Close()
	if blocked.Status != "blocked" || noInputAttempts != 3 || transportSends != 0 {
		t.Fatalf("writer conflict %#v noinput=%d sent=%d", blocked, noInputAttempts, transportSends)
	}
	writerBusy = false
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if transportSends != 0 {
		t.Fatal("automatically retried beyond known-no-input cycle")
	}
	// This is the explicit user tool action. It keeps all old native request
	// reservations and starts no provider call until readiness and cooldown.
	args, _ := json.Marshal(map[string]any{"conversation_id": root.ID, "retry_not_sent": true})
	if _, err = s.toolConversationWake(args); err != nil {
		t.Fatal(err)
	}
	now = time.Now().Add(6 * time.Second)
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if transportSends != 1 || len(ids) != 4 || ids[3] == ids[2] {
		t.Fatalf("explicit recovery IDs=%v sent=%d", ids, transportSends)
	}
	if err = processConversationWake(context.Background(), s, root.ID, runtime); err != nil {
		t.Fatal(err)
	}
	if transportSends != 1 {
		t.Fatal("duplicated recovered wake")
	}
	db, _ = store.Open(cfg.DBPath())
	p, _ := db.ConversationWakePolicy(root.ID)
	saved, _ := db.GetConversation("owner", root.ID)
	_ = db.Close()
	if p.DeliveryCursor == 0 || saved.DeliveredCursor != 0 || saved.Revision != 0 {
		t.Fatalf("retry altered ACK %#v %#v", p, saved)
	}
}
