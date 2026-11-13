package egg

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// A finite bridge from the real reservation/receipt code and native hook
// adapters into the production JS reader. No waiting answer has a user record.
func TestSessionPromptReceiptBridge(t *testing.T) {
	traces := []map[string]any{}
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir, home, cwd, _ := lifecycleFixture(t)
				if agent == "codex" {
					dir = testsocket.Dir(t)
					cwd = ""
				}
				id := filepath.Base(dir)
				publish := func(seq, event string) {
					data := `{"session_id":"ours","hook_event_name":"` + event + `","tool_name":"Bash"}`
					if agent == "claude" {
						lifecycleHook(t, home, id, seq, data)
					} else {
						if err := os.MkdirAll(filepath.Join(home, ".codex", "wingthing-events", id), 0700); err != nil {
							t.Fatal(err)
						}
						lifecycleWrite(t, filepath.Join(home, ".codex", "wingthing-events", id, seq+".json"), data)
					}
				}
				publish("seq.00000000000000000000", "SessionStart")
				publish("seq.00000000000000000001", "PermissionRequest")
				read := func(ctx context.Context, after int64, limit int) (SessionView, error) {
					if err := ctx.Err(); err != nil {
						return SessionView{}, err
					}
					provider := "ours"
					if agent == "codex" {
						provider = ""
					}
					return ReadSessionLifecycle(dir, agent, cwd, home, provider, true, after, limit)
				}
				initial, err := read(t.Context(), 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				if initial.State != "needs_input" {
					t.Fatalf("native prompt: %+v", initial)
				}
				cursor := initial.StateCursor
				selector := SessionPromptSelector{RequestID: "bridge-answer", ProviderSessionID: "ours", AnswerToStateCursor: cursor}
				missing, err := ReadSessionPromptReceipt(t.Context(), dir, selector, read)
				if err != nil || missing.TransportEnqueued || missing.DefinitelyNotSent {
					t.Fatalf("missing reservation: %+v %v", missing, err)
				}
				sends := 0
				options := SessionPromptOptions{RequestID: selector.RequestID, Input: "Yes", AnswerToStateCursor: &cursor, Timeout: 3 * time.Second, Read: read, Send: func(context.Context, string) (PromptDelivery, error) {
					sends++
					return PromptDelivery{BytesEnqueued: 4}, nil
				}}
				start := time.Now()
				first, err := SubmitSessionPrompt(t.Context(), dir, options)
				if err != nil || first.Status != "unconfirmed" || !first.TransportEnqueued || first.NativeReceiptObserved || first.ProviderRequestAcknowledged {
					t.Fatalf("three-second response: %+v %v", first, err)
				}
				if time.Since(start) != 3*time.Second || first.EnqueuedAtMS != start.UnixMilli() || first.WaitingDeadlineMS != start.Add(15*time.Second).UnixMilli() || first.RemainingMS != 12000 {
					t.Fatalf("enqueue clocks: %+v elapsed=%s", first, time.Since(start))
				}
				observations := []SessionPromptResult{first}
				// Include each half-second observation used by the reader's polling
				// and reload traces, so a status lookup supplies a fresh server
				// duration rather than replaying an older receipt as a new reply.
				for offset := 3500 * time.Millisecond; offset <= 16*time.Second; offset += 500 * time.Millisecond {
					time.Sleep(start.Add(offset).Sub(time.Now()))
					status, e := ReadSessionPromptReceipt(t.Context(), dir, selector, read)
					if e != nil || sends != 1 || status.DefinitelyNotSent {
						t.Fatalf("status changed transport: %+v sends=%d %v", status, sends, e)
					}
					expected := "unconfirmed"
					if offset >= 15*time.Second {
						expected = "waiting_answer_expired"
					}
					if status.Status != expected || status.WaitingDeadlineMS != first.WaitingDeadlineMS || status.RemainingMS != max(0, 15000-offset.Milliseconds()) {
						t.Fatalf("fresh unchanged-prompt expiry: %+v", status)
					}
					observations = append(observations, status)
				}
				publish("seq.00000000000000000002", "PreToolUse")
				working, _ := read(t.Context(), 0, 100)
				for _, event := range working.Events {
					if event.Type == "message" || event.Type == "prompt_submitted" {
						t.Fatal("invented waiting-answer receipt")
					}
				}
				delivered, err := ReadSessionPromptReceipt(t.Context(), dir, selector, read)
				if err != nil || delivered.Status != "waiting_answer_delivered" || sends != 1 || delivered.ProviderRequestAcknowledged {
					t.Fatalf("late delivery: %+v sends=%d %v", delivered, sends, err)
				}
				replay, err := SubmitSessionPrompt(t.Context(), dir, options)
				if err != nil || replay.Status != "waiting_answer_delivered" || sends != 1 {
					t.Fatalf("replay resent: %+v sends=%d %v", replay, sends, err)
				}
				wrong := selector
				wrong.ProviderSessionID = "sibling"
				if _, e := ReadSessionPromptReceipt(t.Context(), dir, wrong, read); e == nil {
					t.Fatal("wrong provider adopted")
				}
				wrong = selector
				wrong.AnswerToStateCursor++
				if _, e := ReadSessionPromptReceipt(t.Context(), dir, wrong, read); e == nil {
					t.Fatal("wrong prompt adopted")
				}
				options.RequestID = "preflight-answer"
				rejected, e := SubmitSessionPrompt(t.Context(), dir, options)
				if e != nil || !rejected.DefinitelyNotSent || rejected.Status != "not_sent" || sends != 1 {
					t.Fatalf("preflight: %+v %v", rejected, e)
				}
				// A reservation can exist without any durable enqueue. Status cannot turn
				// an independently resolved prompt into proof of delivery for that request.
				publish("seq.00000000000000000003", "PermissionRequest")
				fresh, _ := read(t.Context(), 0, 100)
				freshCursor := fresh.StateCursor
				options.RequestID = "crash-gap"
				options.AnswerToStateCursor = &freshCursor
				options.Send = func(context.Context, string) (PromptDelivery, error) { panic("crash before transport") }
				func() {
					defer func() {
						if recover() == nil {
							t.Error("no crash")
						}
					}()
					_, _ = SubmitSessionPrompt(t.Context(), dir, options)
				}()
				publish("seq.00000000000000000004", "PreToolUse")
				crash, e := ReadSessionPromptReceipt(t.Context(), dir, SessionPromptSelector{RequestID: options.RequestID, ProviderSessionID: "ours", AnswerToStateCursor: freshCursor}, read)
				if e != nil || crash.Status != "unconfirmed" || crash.TransportEnqueued || crash.DefinitelyNotSent || sends != 1 {
					t.Fatalf("crash gap: %+v %v", crash, e)
				}
				options.Send = func(context.Context, string) (PromptDelivery, error) {
					t.Error("crash gap resent")
					return PromptDelivery{}, errors.New("forbidden")
				}
				_, _ = SubmitSessionPrompt(t.Context(), dir, options)
				traces = append(traces, map[string]any{"agent": agent, "initial": initial, "receipt": first, "statuses": observations, "working": working, "delivered": delivered, "missing": missing, "preflight": rejected, "crash": crash, "replay": replay, "sends": sends, "response_delay_ms": 3000})
				wire, _ := json.Marshal(traces[len(traces)-1])
				t.Log(string(wire))
			})
		})
	}
	if file := os.Getenv("SESSION_ANSWER_BRIDGE"); file != "" {
		wire, err := json.MarshalIndent(traces, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, append(wire, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
