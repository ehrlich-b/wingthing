package egg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func promptFixtureOptions(t *testing.T) (dir, path string, o SessionPromptOptions) {
	t.Helper()
	dir, home, cwd, path := lifecycleFixture(t)
	lifecycleHook(t, home, filepath.Base(dir), "ready", `{"session_id":"ours","hook_event_name":"SessionStart"}`)
	o = SessionPromptOptions{RequestID: "request-1", Input: "inspect fixture", Timeout: 200 * time.Millisecond,
		Read: func(ctx context.Context, after int64, limit int) (SessionView, error) {
			if ctx.Err() != nil {
				return SessionView{}, ctx.Err()
			}
			return ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, after, limit)
		},
		Send: func(context.Context, string) (PromptDelivery, error) {
			return PromptDelivery{}, errors.New("fixture sender not configured")
		},
	}
	return
}

func writeNativeUserPrompt(t *testing.T, path, input string) {
	t.Helper()
	record := map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"role": "user", "content": input}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPromptConcurrentRetrySendsAtMostOnceAndKeepsLease(t *testing.T) {
	// Keep the fixture's 200ms bound, but exclude scheduler and disk latency
	// from it: fake time advances only when all callers are durably blocked.
	synctest.Test(t, func(t *testing.T) {
		dir, path, o := promptFixtureOptions(t)
		const callers = 6
		var sends atomic.Int32
		var held atomic.Bool
		o.Send = func(ctx context.Context, input string) (PromptDelivery, error) {
			sends.Add(1)
			held.Store(true)
			writeNativeUserPrompt(t, path, input)
			return PromptDelivery{BytesEnqueued: len(input) + 1, Release: func() { held.Store(false) }}, nil
		}
		observeReceipt := make(chan struct{})
		reader := o.Read
		o.Read = func(ctx context.Context, after int64, limit int) (SessionView, error) {
			if after > 0 {
				<-observeReceipt
				if !held.Load() || sends.Load() != 1 {
					t.Error("receipt scan occurred without the sole send lease")
				}
			}
			return reader(ctx, after, limit)
		}
		var wg sync.WaitGroup
		results := make(chan SessionPromptResult, callers)
		errs := make(chan error, callers)
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := SubmitSessionPrompt(context.Background(), dir, o)
				if err != nil {
					errs <- err
					return
				}
				results <- result
			}()
		}
		// The winner is blocked on receipt observation; every retry is now
		// waiting for its session lock. The send lease must still be held.
		synctest.Wait()
		if sends.Load() != 1 || !held.Load() || len(results) != 0 || len(errs) != 0 {
			t.Errorf("before receipt: sends=%d lease=%t results=%d errors=%d", sends.Load(), held.Load(), len(results), len(errs))
		}
		close(observeReceipt)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if len(results) != callers {
			t.Errorf("successful callers=%d, want %d", len(results), callers)
		}
		retries := 0
		for r := range results {
			if !r.NativeReceiptObserved || r.ProviderRequestAcknowledged || r.ReceiptKind != "exact_provider_user_text_match" || r.Status != "native_receipt_observed" || r.Causality != "unverified_without_provider_request_id" || r.ReceiptCursor <= r.ReservedCursor {
				t.Errorf("wrong acknowledgement claims: %+v", r)
			}
			if r.Retried {
				retries++
			}
		}
		if retries != callers-1 {
			t.Errorf("retries=%d, want %d", retries, callers-1)
		}
		if sends.Load() != 1 || held.Load() {
			t.Fatalf("sends=%d lease=%t", sends.Load(), held.Load())
		}
		o.Input = "changed"
		if _, err := SubmitSessionPrompt(context.Background(), dir, o); err == nil {
			t.Fatal("same request ID accepted changed prompt")
		}
		if sends.Load() != 1 {
			t.Fatal("changed spec was resent")
		}
	})
}

func TestSessionPromptTimeoutLostAckAndReconnectNativeReceipt(t *testing.T) {
	// Preserve the 200ms receipt bound while excluding scheduler and disk
	// latency, as in the concurrent-retry test above.
	synctest.Test(t, func(t *testing.T) {
		dir, path, o := promptFixtureOptions(t)
		var sends atomic.Int32
		lost := make(chan error, 1)
		lost <- errors.New("connection reset")
		o.Send = func(context.Context, string) (PromptDelivery, error) {
			sends.Add(1)
			return PromptDelivery{BytesEnqueued: 99, Lost: lost}, nil
		}
		r, err := SubmitSessionPrompt(context.Background(), dir, o)
		if err != nil {
			t.Fatal(err)
		}
		if r.NativeReceiptObserved || !r.TransportEnqueued || r.Status != "unconfirmed" || !strings.Contains(r.Reason, "connection") {
			t.Fatalf("lost acknowledgement lied: %+v", r)
		}
		writeNativeUserPrompt(t, path, o.Input)
		r, err = SubmitSessionPrompt(context.Background(), dir, o)
		if err != nil {
			t.Fatal(err)
		}
		if !r.NativeReceiptObserved || !r.Retried || sends.Load() != 1 {
			t.Fatalf("retry duplicated or lost receipt: %+v sends=%d", r, sends.Load())
		}
		// An identical human prompt is observational evidence; never claim the
		// provider causally acknowledged Wingthing's request ID.
		if r.ProviderRequestAcknowledged || r.Causality != "unverified_without_provider_request_id" {
			t.Fatalf("claimed request-ID linkage: %+v", r)
		}
	})
}

func TestSessionPromptCrashGapIsReservedAndNeverResent(t *testing.T) {
	dir, path, o := promptFixtureOptions(t)
	var sends atomic.Int32
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		sends.Add(1)
		panic("simulated supervising process crash after durable reservation")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("fixture didn't crash")
			}
		}()
		_, _ = SubmitSessionPrompt(context.Background(), dir, o)
	}()
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		t.Error("crash-gap retry resent input")
		return PromptDelivery{}, nil
	}
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.NativeReceiptObserved || r.TransportEnqueued || r.DefinitelyNotSent || !r.Retried || r.Status != "unconfirmed" {
		t.Fatalf("crash gap claimed delivery: %+v", r)
	}
	writeNativeUserPrompt(t, path, o.Input)
	r, err = SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || !r.NativeReceiptObserved || sends.Load() != 1 {
		t.Fatalf("crash-gap receipt lost: %+v %v", r, err)
	}
}

func TestSessionPromptNotSentEvidenceIsTerminalAndNeverResent(t *testing.T) {
	dir, path, o := promptFixtureOptions(t)
	var sends atomic.Int32
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		sends.Add(1)
		return PromptDelivery{NoInputAttempted: true}, errors.New("another writer owns input")
	}
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || r.Status != "not_sent" || !r.DefinitelyNotSent || r.TransportEnqueued || r.TransportBytesEnqueued != 0 || r.NativeReceiptObserved || !strings.Contains(r.Reason, "another writer") {
		t.Fatalf("explicit preflight evidence lost: %+v %v", r, err)
	}
	// A later identical human message cannot change an unsent request into
	// a receipt. Cached proof survives native-reader loss or replacement.
	writeNativeUserPrompt(t, path, o.Input)
	o.Read = func(context.Context, int64, int) (SessionView, error) {
		t.Error("terminal not_sent replay read the replacement provider")
		return SessionView{}, errors.New("provider disconnected")
	}
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		t.Error("terminal not_sent request was resent")
		return PromptDelivery{}, nil
	}
	retry, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || retry.Status != "not_sent" || !retry.DefinitelyNotSent || !retry.Retried || retry.Reason != r.Reason || sends.Load() != 1 {
		t.Fatalf("terminal proof was not replayed: %+v %v", retry, err)
	}
	o.Input = "changed prompt"
	if _, err := SubmitSessionPrompt(context.Background(), dir, o); err == nil {
		t.Fatal("terminal not_sent ID accepted changed caller arguments")
	}
}

func TestSessionPromptZeroBytesErrorDoesNotProveNotSent(t *testing.T) {
	for _, delivery := range []PromptDelivery{{}, {NoInputAttempted: true, BytesEnqueued: 1}} {
		dir, _, o := promptFixtureOptions(t)
		sends := 0
		o.Send = func(context.Context, string) (PromptDelivery, error) {
			sends++
			return delivery, errors.New("input Send failed; acknowledgement is unknown")
		}
		r, err := SubmitSessionPrompt(context.Background(), dir, o)
		if err != nil || r.Status != "unconfirmed" || r.DefinitelyNotSent || r.NativeReceiptObserved {
			t.Fatalf("ambiguous error inferred not_sent: %+v %v", r, err)
		}
		r, err = SubmitSessionPrompt(context.Background(), dir, o)
		if err != nil || r.Status != "unconfirmed" || r.DefinitelyNotSent || !r.Retried || sends != 1 {
			t.Fatalf("ambiguous retry resent or became proven unsent: %+v sends=%d %v", r, sends, err)
		}
	}
}

func TestSessionPromptNotSentReasonIsBoundedAndActionable(t *testing.T) {
	dir, _, o := promptFixtureOptions(t)
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		return PromptDelivery{NoInputAttempted: true}, errors.New("lease busy\n\x1b[31m " + strings.Repeat("界", 1000))
	}
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || !r.DefinitelyNotSent || r.Status != "not_sent" || len(r.Reason) > 700 || strings.ContainsAny(r.Reason, "\n\x1b") || !strings.Contains(r.Reason, "lease busy") || !strings.Contains(r.Reason, "new request_id") {
		t.Fatalf("unbounded or non-actionable preflight reason: %+v %v", r, err)
	}
}

func TestSessionPromptRejectsHooksSyntheticMetaToolAndOldReceipts(t *testing.T) {
	base := SessionEvent{Source: "claude_transcript", Type: "message", ProviderSessionID: "ours"}
	for _, record := range []string{
		`{"type":"user","sessionId":"ours","isMeta":true,"message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"ours","isSynthetic":true,"message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"ours","isCompactSummary":true,"message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"ours","isSidechain":true,"message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"ours","isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"foreign","message":{"role":"user","content":"x"}}`,
		`{"type":"user","sessionId":"ours","message":{"role":"user","content":[{"type":"tool_result","text":"x"}]}}`,
		`{"type":"assistant","sessionId":"ours","message":{"role":"assistant","content":"x"}}`,
	} {
		event := base
		event.Raw = json.RawMessage(record)
		if nativeHumanPromptReceipt(event, "ours", "x") {
			t.Fatalf("accepted non-human receipt: %s", record)
		}
	}
	dir, path, o := promptFixtureOptions(t)
	writeNativeUserPrompt(t, path, o.Input)
	reader := o.Read
	initial, err := reader(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	o.Read = func(ctx context.Context, after int64, limit int) (SessionView, error) {
		v, err := reader(ctx, after, limit)
		v.State = "idle"
		v.StateSource = "claude_hook"
		return v, err
	}
	o.Send = func(context.Context, string) (PromptDelivery, error) { return PromptDelivery{BytesEnqueued: 10}, nil }
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.NativeReceiptObserved || r.ReservedCursor != initial.HeadCursor {
		t.Fatalf("old matching text satisfied new prompt: %+v", r)
	}
	// Hook UserPromptSubmit by itself is not provider transcript acceptance.
	o.RequestID = "hooks-only"
	o.Read = func(ctx context.Context, after int64, limit int) (SessionView, error) {
		v, err := reader(ctx, after, limit)
		v.State = "idle"
		v.StateSource = "claude_hook"
		if after > 0 {
			v.Events = []SessionEvent{{Sequence: after + 1, Type: "prompt_submitted", Source: "claude_hook", Text: o.Input}}
			v.Cursor = after + 1
			v.HeadCursor = v.Cursor
		}
		return v, err
	}
	r, err = SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || r.NativeReceiptObserved {
		t.Fatalf("hook became acceptance: %+v %v", r, err)
	}
}

func TestSessionPromptValidationReadinessAndCancellation(t *testing.T) {
	dir, _, o := promptFixtureOptions(t)
	var sends atomic.Int32
	o.Send = func(context.Context, string) (PromptDelivery, error) { sends.Add(1); return PromptDelivery{}, nil }
	for _, state := range []string{"working", "needs_input", "unknown"} {
		t.Run(state, func(t *testing.T) {
			next := o
			next.Read = func(context.Context, int64, int) (SessionView, error) {
				return SessionView{Agent: "claude", ProviderSessionID: "ours", Ready: true, ProcessAlive: true, State: state, StateSource: "claude_hook"}, nil
			}
			if _, err := SubmitSessionPrompt(context.Background(), dir, next); err == nil {
				t.Fatal("sent without foreground readiness")
			}
		})
	}
	for _, input := range []string{"", strings.Repeat("x", MaxSessionPromptBytes+1), "x\x1b[31m", "x\r\ny", "x\u0085y"} {
		next := o
		next.Input = input
		if _, err := SubmitSessionPrompt(context.Background(), dir, next); err == nil {
			t.Fatalf("accepted invalid input %q", input[:min(len(input), 20)])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SubmitSessionPrompt(ctx, dir, o); err != context.Canceled {
		t.Fatalf("cancelled caller sent: %v", err)
	}
	if sends.Load() != 0 {
		t.Fatalf("rejected sends=%d", sends.Load())
	}
	badRequest := o
	badRequest.RequestID = "r\x1b[2J"
	if _, err := SubmitSessionPrompt(context.Background(), dir, badRequest); err == nil {
		t.Fatal("request ID accepted terminal control bytes")
	}
	// Retry bounds belong to the reserved spec, including the timeout.
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil || r.Status != "unconfirmed" {
		t.Fatalf("timeout receipt: %+v %v", r, err)
	}
	o.Timeout = time.Second
	if _, err = SubmitSessionPrompt(context.Background(), dir, o); err == nil {
		t.Fatal("changed wait bound accepted as same request")
	}
}

func TestSessionPromptCompetingRequestsAreSerialized(t *testing.T) {
	dir, path, o := promptFixtureOptions(t)
	var active, maxActive atomic.Int32
	o.Timeout = time.Second
	o.Send = func(ctx context.Context, input string) (PromptDelivery, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		time.Sleep(30 * time.Millisecond)
		writeNativeUserPrompt(t, path, input)
		return PromptDelivery{Release: func() { active.Add(-1) }}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		next := o
		next.RequestID = fmt.Sprintf("r-%d", i)
		next.Input = fmt.Sprintf("prompt-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := SubmitSessionPrompt(context.Background(), dir, next)
			if err != nil {
				errs <- err
			} else if !r.NativeReceiptObserved {
				errs <- errors.New("receipt not observed")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("parallel prompt writers=%d", maxActive.Load())
	}
}

func TestSessionPromptReservationDrainsOldNativeHistory(t *testing.T) {
	dir, path, o := promptFixtureOptions(t)
	o.Timeout = 10 * time.Second
	var rows strings.Builder
	for i := 0; i < 505; i++ {
		text := fmt.Sprintf("old-%d", i)
		if i == 504 {
			text = o.Input
		}
		record, _ := json.Marshal(map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"role": "user", "content": text}})
		rows.Write(record)
		rows.WriteByte('\n')
	}
	lifecycleWrite(t, path, rows.String())
	lost := make(chan error, 1)
	lost <- errors.New("fixture disconnect after input")
	o.Send = func(context.Context, string) (PromptDelivery, error) {
		return PromptDelivery{BytesEnqueued: 1, Lost: lost}, nil
	}
	r, err := SubmitSessionPrompt(context.Background(), dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.NativeReceiptObserved || r.ReservedCursor != 506 {
		t.Fatalf("old matching row beyond import page acknowledged new input: %+v", r)
	}
}
