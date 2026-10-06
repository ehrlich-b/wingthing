package localmcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
)

const (
	wakeExactLegacy   = "a1b2c3d4"         // legacy 8-hex execution ID
	wakeExactNative   = "a1b2c3d4e5f60718" // longer ID beginning with the legacy one
	wakeExactProvider = "provider-root"
)

// wakeExactSpy records every native read, prompt and transport send made
// through the wake runtime. Prompts use the real reservation primitive, so a
// misdirected attempt leaves a prompt.*.json file in the wrong egg directory.
type wakeExactSpy struct {
	cfg     *config.Config
	mu      sync.Mutex
	receipt bool
	view    map[string]string // native reader provider override per execution
	events  map[string][]egg.SessionEvent
	reads   []string
	prompts []string
	sends   []string
}

func newWakeExactSpy(cfg *config.Config) *wakeExactSpy {
	return &wakeExactSpy{cfg: cfg, view: map[string]string{}, events: map[string][]egg.SessionEvent{}}
}

func (w *wakeExactSpy) nativeView(id string, after int64, limit int) egg.SessionView {
	w.mu.Lock()
	defer w.mu.Unlock()
	provider, ok := w.view[id]
	if !ok {
		provider = eggclient.ReadEggMetaValues(filepath.Join(w.cfg.Dir, "eggs", id))["provider_session_id"]
	}
	v := egg.SessionView{SessionID: id, Agent: "claude", ProviderSessionID: provider, State: "idle", StateSource: "claude_hook", Ready: true, ProcessAlive: true, Cursor: after, HeadCursor: 1, Events: []egg.SessionEvent{}}
	for _, e := range w.events[id] {
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
	return v
}

// publishLocked appends an exact native user-text record to one execution.
func (w *wakeExactSpy) publishLocked(id, text string) {
	provider := eggclient.ReadEggMetaValues(filepath.Join(w.cfg.Dir, "eggs", id))["provider_session_id"]
	raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": provider, "message": map[string]any{"role": "user", "content": text}})
	w.events[id] = append(w.events[id], egg.SessionEvent{Sequence: int64(len(w.events[id])) + 2, Type: "message", Source: "claude_transcript", ProviderSessionID: provider, Raw: raw})
}

func (w *wakeExactSpy) publish(id, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.publishLocked(id, text)
}

func (w *wakeExactSpy) configure(receipt bool, view map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.receipt, w.view = receipt, view
}

func (w *wakeExactSpy) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads, w.prompts, w.sends = nil, nil, nil
}

func (w *wakeExactSpy) calls() (reads, prompts, sends []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.reads...), append([]string(nil), w.prompts...), append([]string(nil), w.sends...)
}

func (w *wakeExactSpy) runtime() conversationWakeRuntime {
	return conversationWakeRuntime{Now: time.Now,
		Read: func(_ context.Context, s eggclient.LocalSession) (egg.SessionView, error) {
			w.mu.Lock()
			w.reads = append(w.reads, s.ID)
			w.mu.Unlock()
			return w.nativeView(s.ID, 0, 1), nil
		},
		Prompt: func(ctx context.Context, s eggclient.LocalSession, id, text string) (egg.SessionPromptResult, error) {
			w.mu.Lock()
			w.prompts = append(w.prompts, s.ID+":"+id)
			w.mu.Unlock()
			return egg.SubmitSessionPrompt(ctx, filepath.Join(w.cfg.Dir, "eggs", s.ID), egg.SessionPromptOptions{RequestID: id, Input: text, Timeout: 100 * time.Millisecond,
				Read: func(_ context.Context, after int64, limit int) (egg.SessionView, error) {
					return w.nativeView(s.ID, after, limit), nil
				},
				Send: func(context.Context, string) (egg.PromptDelivery, error) {
					w.mu.Lock()
					defer w.mu.Unlock()
					w.sends = append(w.sends, s.ID)
					if w.receipt {
						w.publishLocked(s.ID, text)
					}
					return egg.PromptDelivery{BytesEnqueued: len(text)}, nil
				}})
		}}
}

// wakeExactEgg writes one synthetic execution directory. An empty provider
// omits provider_session_id from egg.meta.
func wakeExactEgg(t *testing.T, cfg *config.Config, id, owner, provider string) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	meta := "agent=claude\ncwd=" + cfg.Dir + "\nprovider_home=" + cfg.Dir + "\n"
	if provider != "" {
		meta += "provider_session_id=" + provider + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(dir, owner); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(egg.SessionEvent{Sequence: 1, Type: "native-fixture", Source: "claude_hook", State: "idle", ProviderSessionID: provider})
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func wakeExactOpen(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// wakeExactTree reserves an opted-in root whose current execution is session,
// plus one child (execution-child, no shared prefix) with a durable completion.
func wakeExactTree(t *testing.T, cfg *config.Config, session string) *store.Conversation {
	t.Helper()
	db := wakeExactOpen(t, cfg)
	defer func() { _ = db.Close() }()
	root, _, err := db.ReserveConversation(store.Conversation{ID: "root", OwnerID: "owner", SessionID: session, LaunchKey: "request-root", SpecDigest: "root", Agent: "claude", CWD: cfg.Dir, WingID: "wing-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	wakeExactEgg(t, cfg, session, "owner", wakeExactProvider)
	_ = fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	return root
}

func wakeExactStep(cfg *config.Config, spy *wakeExactSpy) error {
	return processConversationWake(context.Background(), &Server{Version: "dev", Cfg: cfg, Principal: "owner"}, "root", spy.runtime())
}

type wakeExactReservation struct {
	RequestID         string `json:"request_id"`
	ProviderSessionID string `json:"provider_session_id"`
	Phase             string `json:"phase"`
}

type wakeExactRow struct {
	Sequence, Receipt, RetryAfter                     int64
	Attempt, Limit                                    int
	Request, Session, Provider, Input, Status, Reason string
}

func wakeExactOutbox(t *testing.T, cfg *config.Config) []wakeExactRow {
	t.Helper()
	db := wakeExactOpen(t, cfg)
	defer func() { _ = db.Close() }()
	rows, err := db.DB().Query(`SELECT event_sequence,receipt_cursor,retry_after,attempt,attempt_limit,request_id,session_id,provider_session_id,input,status,reason FROM conversation_wake_outbox WHERE root_id='root' ORDER BY event_sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []wakeExactRow
	for rows.Next() {
		var r wakeExactRow
		if err = rows.Scan(&r.Sequence, &r.Receipt, &r.RetryAfter, &r.Attempt, &r.Limit, &r.Request, &r.Session, &r.Provider, &r.Input, &r.Status, &r.Reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wakeExactReservations(t *testing.T, cfg *config.Config, id string) []wakeExactReservation {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(cfg.Dir, "eggs", id, "prompt.*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []wakeExactReservation
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r wakeExactReservation
		if err = json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// wakeExactUnconfirmed delivers one wake to the root's current execution with
// no native receipt, leaving an immutable unconfirmed binding plus its saved
// before-send reservation.
func wakeExactUnconfirmed(t *testing.T, cfg *config.Config, spy *wakeExactSpy, session string) wakeExactRow {
	t.Helper()
	spy.configure(false, map[string]string{})
	if err := wakeExactStep(cfg, spy); err != nil {
		t.Fatal(err)
	}
	rows := wakeExactOutbox(t, cfg)
	if len(rows) != 1 {
		t.Fatalf("outbox %#v", rows)
	}
	w := rows[0]
	if w.Status != "unconfirmed" || w.Attempt != 1 || w.Session != session || w.Provider != wakeExactProvider || w.Request == "" || w.Input == "" {
		t.Fatalf("unconfirmed binding %#v", w)
	}
	if _, _, sends := spy.calls(); len(sends) != 1 || sends[0] != session {
		t.Fatalf("setup sends %v", sends)
	}
	if r := wakeExactReservations(t, cfg, session); len(r) != 1 || r[0].RequestID != w.Request || r[0].ProviderSessionID != wakeExactProvider {
		t.Fatalf("setup reservation %#v", r)
	}
	spy.reset()
	return w
}

// wakeExactAlias removes the legacy execution directory and installs a
// same-owner, same-provider execution that a human selector would resolve for
// "a1b2c3d4". It returns the directory that must stay untouched.
func wakeExactAlias(t *testing.T, cfg *config.Config, kind string) string {
	t.Helper()
	legacy := filepath.Join(cfg.Dir, "eggs", wakeExactLegacy)
	if err := os.RemoveAll(legacy); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "prefix_sibling":
		wakeExactEgg(t, cfg, wakeExactNative, "owner", wakeExactProvider)
		return wakeExactNative
	case "resumed_prefix_sibling":
		// The surviving sibling is even a recorded execution of this root.
		wakeExactEgg(t, cfg, wakeExactNative, "owner", wakeExactProvider)
		db := wakeExactOpen(t, cfg)
		defer func() { _ = db.Close() }()
		if err := db.ResumeConversationExecution(wakeExactLegacy, wakeExactNative); err != nil {
			t.Fatal(err)
		}
		return wakeExactNative
	case "name_alias":
		const other = "b0b0b0b0b0b0b0b0"
		dir := wakeExactEgg(t, cfg, other, "owner", wakeExactProvider)
		if err := eggclient.WriteSessionName(dir, wakeExactLegacy); err != nil {
			t.Fatal(err)
		}
		return other
	case "symlinked_execution":
		// The exact path exists but is a symlink to a sibling. Metadata reads
		// follow it, so only an lstat-based resolver keeps the sibling out.
		dir := wakeExactEgg(t, cfg, wakeExactNative, "owner", wakeExactProvider)
		if err := os.Symlink(dir, legacy); err != nil {
			t.Fatal(err)
		}
		return wakeExactNative
	}
	t.Fatalf("unknown alias %s", kind)
	return ""
}

func wakeExactUntouched(t *testing.T, cfg *config.Config, spy *wakeExactSpy, sibling string) {
	t.Helper()
	reads, prompts, sends := spy.calls()
	if len(reads) != 0 || len(prompts) != 0 || len(sends) != 0 {
		t.Fatalf("wake runtime touched an execution reads=%v prompts=%v sends=%v", reads, prompts, sends)
	}
	if r := wakeExactReservations(t, cfg, sibling); len(r) != 0 {
		t.Fatalf("sibling %s received a reservation %#v", sibling, r)
	}
}

// The reviewed gap: an unconfirmed wake bound to a retired legacy execution
// must never be read, reserved or sent through an alias. The original request,
// execution, provider and input stay immutable across controller restarts.
func TestWakeExactUnconfirmedLegacyTargetNeverAliasesSibling(t *testing.T) {
	for _, kind := range []string{"prefix_sibling", "resumed_prefix_sibling", "name_alias", "symlinked_execution"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, wakeExactLegacy)
			spy := newWakeExactSpy(cfg)
			before := wakeExactUnconfirmed(t, cfg, spy, wakeExactLegacy)
			sibling := wakeExactAlias(t, cfg, kind)
			// A misdirected send would even observe a receipt on the sibling.
			spy.configure(true, map[string]string{})
			for restart := 0; restart < 2; restart++ {
				if err := wakeExactStep(cfg, spy); err == nil {
					t.Fatalf("restart %d accepted a missing exact wake target", restart)
				}
			}
			wakeExactUntouched(t, cfg, spy, sibling)
			if after := wakeExactOutbox(t, cfg); len(after) != 1 || after[0] != before {
				t.Fatalf("unconfirmed binding changed %#v -> %#v", before, after)
			}
			db := wakeExactOpen(t, cfg)
			p, err := db.ConversationWakePolicy("root")
			_ = db.Close()
			if err != nil || p.DeliveryCursor != 0 {
				t.Fatalf("delivery advanced without receipt %#v %v", p, err)
			}
		})
	}
}

// A fresh wake binds only the root's exact current execution. If that
// directory is gone, nothing is bound, read or sent; the event stays queued.
func TestWakeExactQueuedWakeNeverBindsAliasOfMissingCurrentExecution(t *testing.T) {
	for _, kind := range []string{"prefix_sibling", "name_alias", "symlinked_execution"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, wakeExactLegacy)
			sibling := wakeExactAlias(t, cfg, kind)
			spy := newWakeExactSpy(cfg)
			spy.configure(true, map[string]string{})
			for restart := 0; restart < 2; restart++ {
				if err := wakeExactStep(cfg, spy); err == nil {
					t.Fatalf("restart %d accepted a missing current execution", restart)
				}
			}
			wakeExactUntouched(t, cfg, spy, sibling)
			rows := wakeExactOutbox(t, cfg)
			if len(rows) != 1 || rows[0].Status != "queued" || rows[0].Attempt != 0 || rows[0].Request != "" || rows[0].Session != "" || rows[0].Provider != "" || rows[0].Input != "" {
				t.Fatalf("queued wake was bound %#v", rows)
			}
		})
	}
}

// Ownership checks are retained, and a durable binding must name a recorded
// execution of the same root. None of these reach the native reader.
func TestWakeExactForeignOrUnrecordedExecutionFailsClosed(t *testing.T) {
	for _, kind := range []string{"foreign_principal", "foreign_roost_owner", "unrecorded_execution"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, wakeExactLegacy)
			spy := newWakeExactSpy(cfg)
			before := wakeExactUnconfirmed(t, cfg, spy, wakeExactLegacy)
			legacy := filepath.Join(cfg.Dir, "eggs", wakeExactLegacy)
			untouched := wakeExactLegacy
			switch kind {
			case "foreign_principal":
				if err := eggclient.WriteSessionPrincipal(legacy, "other"); err != nil {
					t.Fatal(err)
				}
			case "foreign_roost_owner":
				if err := os.WriteFile(filepath.Join(legacy, "egg.owner"), []byte("someone-else\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unrecorded_execution":
				// An owned, same-provider egg that is not an execution of root.
				untouched = "c0ffee00"
				wakeExactEgg(t, cfg, untouched, "owner", wakeExactProvider)
				db := wakeExactOpen(t, cfg)
				_, err := db.DB().Exec(`UPDATE conversation_wake_outbox SET session_id=? WHERE root_id='root'`, untouched)
				_ = db.Close()
				if err != nil {
					t.Fatal(err)
				}
				before.Session = untouched
			}
			spy.configure(true, map[string]string{})
			if err := wakeExactStep(cfg, spy); err == nil {
				t.Fatal("foreign or unrecorded wake target accepted")
			}
			reads, prompts, sends := spy.calls()
			if len(reads) != 0 || len(prompts) != 0 || len(sends) != 0 {
				t.Fatalf("touched reads=%v prompts=%v sends=%v", reads, prompts, sends)
			}
			if kind == "unrecorded_execution" {
				if r := wakeExactReservations(t, cfg, untouched); len(r) != 0 {
					t.Fatalf("unrecorded execution reserved %#v", r)
				}
			}
			if after := wakeExactOutbox(t, cfg); len(after) != 1 || after[0] != before {
				t.Fatalf("binding changed %#v -> %#v", before, after)
			}
		})
	}
}

// Changed or missing provider identity on the exact original execution keeps
// the wake unconfirmed with no prompt. Restoring identity reconciles only the
// original request from its saved reservation, without a second send.
func TestWakeExactProviderIdentityBranchesStayUnconfirmed(t *testing.T) {
	for _, kind := range []string{"meta_provider_replaced", "meta_provider_missing", "meta_file_missing", "reader_provider_changed", "reader_provider_missing"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, wakeExactLegacy)
			spy := newWakeExactSpy(cfg)
			before := wakeExactUnconfirmed(t, cfg, spy, wakeExactLegacy)
			metaPath := filepath.Join(cfg.Dir, "eggs", wakeExactLegacy, "egg.meta")
			original, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			view := map[string]string{}
			switch kind {
			case "meta_provider_replaced":
				err = os.WriteFile(metaPath, []byte(strings.ReplaceAll(string(original), wakeExactProvider, "provider-replacement")), 0600)
			case "meta_provider_missing":
				err = os.WriteFile(metaPath, []byte(strings.ReplaceAll(string(original), "provider_session_id="+wakeExactProvider+"\n", "")), 0600)
			case "meta_file_missing":
				err = os.Remove(metaPath)
			case "reader_provider_changed":
				view[wakeExactLegacy] = "provider-other"
			case "reader_provider_missing":
				view[wakeExactLegacy] = ""
			}
			if err != nil {
				t.Fatal(err)
			}
			spy.configure(true, view)
			for restart := 0; restart < 2; restart++ {
				if err = wakeExactStep(cfg, spy); err == nil {
					t.Fatalf("restart %d reconciled without exact provider identity", restart)
				}
			}
			if _, prompts, sends := spy.calls(); len(prompts) != 0 || len(sends) != 0 {
				t.Fatalf("prompted changed identity prompts=%v sends=%v", prompts, sends)
			}
			if after := wakeExactOutbox(t, cfg); len(after) != 1 || after[0] != before {
				t.Fatalf("binding changed %#v -> %#v", before, after)
			}
			if r := wakeExactReservations(t, cfg, wakeExactLegacy); len(r) != 1 || r[0].RequestID != before.Request || r[0].ProviderSessionID != wakeExactProvider {
				t.Fatalf("reservation changed %#v", r)
			}
			// Restore identity; the original input then appears natively.
			if err = os.WriteFile(metaPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			spy.configure(false, map[string]string{})
			spy.publish(wakeExactLegacy, before.Input)
			if err = wakeExactStep(cfg, spy); err != nil {
				t.Fatal(err)
			}
			_, prompts, sends := spy.calls()
			if len(sends) != 0 || len(prompts) != 1 || prompts[0] != wakeExactLegacy+":"+before.Request {
				t.Fatalf("restored reconcile prompts=%v sends=%v", prompts, sends)
			}
			rows := wakeExactOutbox(t, cfg)
			if len(rows) != 1 || rows[0].Status != "observed" || rows[0].Request != before.Request || rows[0].Session != wakeExactLegacy || rows[0].Receipt == 0 {
				t.Fatalf("restored outcome %#v", rows)
			}
		})
	}
}

// A queued wake with missing or changed provider identity is never bound.
func TestWakeExactQueuedProviderIdentityBranchesNeverBind(t *testing.T) {
	for _, kind := range []string{"meta_provider_missing", "reader_provider_changed", "reader_provider_missing"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, wakeExactLegacy)
			spy := newWakeExactSpy(cfg)
			view := map[string]string{}
			switch kind {
			case "meta_provider_missing":
				wakeExactEgg(t, cfg, wakeExactLegacy, "owner", "")
			case "reader_provider_changed":
				view[wakeExactLegacy] = "provider-other"
			case "reader_provider_missing":
				view[wakeExactLegacy] = ""
			}
			spy.configure(true, view)
			if err := wakeExactStep(cfg, spy); err == nil {
				t.Fatal("bound a wake without exact provider identity")
			}
			if _, prompts, sends := spy.calls(); len(prompts) != 0 || len(sends) != 0 {
				t.Fatalf("prompts=%v sends=%v", prompts, sends)
			}
			rows := wakeExactOutbox(t, cfg)
			if len(rows) != 1 || rows[0].Status != "queued" || rows[0].Attempt != 0 || rows[0].Request != "" {
				t.Fatalf("queued wake was bound %#v", rows)
			}
		})
	}
}

// Pins the documented binding-before-reservation limit without killing a
// process: a store-level bind with no prompt reservation, then the bound
// execution retires. The row stays pending and wedges this root; it is not
// rebound to an alias, not resent, and not retryable as proven not_sent.
func TestWakeExactBindingWithoutReservationOnRetiredTargetStaysPending(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	wakeExactTree(t, cfg, wakeExactLegacy)
	spy := newWakeExactSpy(cfg)
	spy.configure(true, map[string]string{})
	db := wakeExactOpen(t, cfg)
	args, _ := json.Marshal(map[string]any{"conversation_id": "root", "limit": 1})
	if _, err := (&Server{Version: "dev", Cfg: cfg, Principal: "owner"}).ToolConversationRead(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	w, err := db.QueueConversationWake("root")
	if err != nil || w == nil || w.Status != "queued" {
		t.Fatalf("queue %#v %v", w, err)
	}
	if err = db.BindConversationWake(w, wakeExactLegacy, wakeExactProvider, "synthetic bound wake input", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before := wakeExactOutbox(t, cfg)
	if len(before) != 1 || before[0].Status != "pending" || len(wakeExactReservations(t, cfg, wakeExactLegacy)) != 0 {
		t.Fatalf("bound without reservation %#v", before)
	}
	sibling := wakeExactAlias(t, cfg, "resumed_prefix_sibling")
	for restart := 0; restart < 2; restart++ {
		if err = wakeExactStep(cfg, spy); err == nil {
			t.Fatalf("restart %d accepted a retired pending target", restart)
		}
	}
	wakeExactUntouched(t, cfg, spy, sibling)
	if after := wakeExactOutbox(t, cfg); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("pending binding changed %#v -> %#v", before, after)
	}
	db = wakeExactOpen(t, cfg)
	defer func() { _ = db.Close() }()
	if err = db.RetryNotSentConversationWake("owner", "root", time.Now().Unix()); err == nil {
		t.Fatal("pending binding without typed no-input proof became retryable")
	}
}

// The resolver accepts only a recorded execution ID; human selectors that the
// public lifecycle resolver accepts for the same egg are refused here.
func TestWakeExactResolverRejectsHumanSelectorsAndUnsafeIDs(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	root := wakeExactTree(t, cfg, wakeExactLegacy)
	if err := eggclient.WriteSessionName(filepath.Join(cfg.Dir, "eggs", wakeExactLegacy), "parent"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Version: "dev", Cfg: cfg, Principal: "owner"}
	for _, ref := range []string{"a1b2", "parent"} {
		if got, err := s.resolveOwnedLifecycleSession(ref); err != nil || got.ID != wakeExactLegacy {
			t.Fatalf("public selector %q no longer resolves: %#v %v", ref, got, err)
		}
	}
	db := wakeExactOpen(t, cfg)
	defer func() { _ = db.Close() }()
	for _, ref := range []string{"", ".", "..", "a1b2", "parent", "../eggs/" + wakeExactLegacy, wakeExactLegacy + "/", "execution-child"} {
		if got, err := s.resolveExactWakeTarget(db, root, ref); err == nil {
			t.Fatalf("exact wake resolver accepted %q as %#v", ref, got)
		}
	}
	got, err := s.resolveExactWakeTarget(db, root, wakeExactLegacy)
	if err != nil || got.ID != wakeExactLegacy || got.Principal != "owner" || got.Agent != "claude" || got.CWD != cfg.Dir || got.Name != "parent" {
		t.Fatalf("exact execution %#v %v", got, err)
	}
	bounded := &Server{Version: "dev", Cfg: cfg, Principal: "owner", enforcePathBounds: true, allowedPaths: []string{filepath.Join(cfg.Dir, "elsewhere")}}
	if _, err = bounded.resolveExactWakeTarget(db, root, wakeExactLegacy); err == nil {
		t.Fatal("exact wake resolver ignored path bounds")
	}
}

// Valid exact legacy and native executions still deliver, even when another
// owned execution shares a prefix in either direction.
func TestWakeExactValidLegacyAndNativeExecutionsStillDeliver(t *testing.T) {
	for _, tc := range []struct{ target, other string }{{wakeExactLegacy, wakeExactNative}, {wakeExactNative, wakeExactLegacy}} {
		t.Run(tc.target, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			wakeExactTree(t, cfg, tc.target)
			wakeExactEgg(t, cfg, tc.other, "owner", wakeExactProvider)
			spy := newWakeExactSpy(cfg)
			spy.configure(true, map[string]string{})
			for i := 0; i < 2; i++ {
				if err := wakeExactStep(cfg, spy); err != nil {
					t.Fatal(err)
				}
			}
			reads, _, sends := spy.calls()
			for _, id := range reads {
				if id != tc.target {
					t.Fatalf("read %s instead of %s", id, tc.target)
				}
			}
			if len(sends) != 1 || sends[0] != tc.target || len(wakeExactReservations(t, cfg, tc.other)) != 0 {
				t.Fatalf("exact delivery sends=%v", sends)
			}
			rows := wakeExactOutbox(t, cfg)
			if len(rows) != 1 || rows[0].Status != "observed" || rows[0].Session != tc.target || rows[0].Provider != wakeExactProvider {
				t.Fatalf("exact outcome %#v", rows)
			}
		})
	}
	t.Run("unconfirmed_legacy_reconciles_with_sibling_present", func(t *testing.T) {
		cfg := &config.Config{Dir: t.TempDir()}
		wakeExactTree(t, cfg, wakeExactLegacy)
		spy := newWakeExactSpy(cfg)
		before := wakeExactUnconfirmed(t, cfg, spy, wakeExactLegacy)
		wakeExactEgg(t, cfg, wakeExactNative, "owner", wakeExactProvider)
		spy.publish(wakeExactLegacy, before.Input)
		if err := wakeExactStep(cfg, spy); err != nil {
			t.Fatal(err)
		}
		reads, prompts, sends := spy.calls()
		if len(sends) != 0 || len(prompts) != 1 || prompts[0] != wakeExactLegacy+":"+before.Request || len(reads) != 1 || reads[0] != wakeExactLegacy {
			t.Fatalf("reconcile reads=%v prompts=%v sends=%v", reads, prompts, sends)
		}
		rows := wakeExactOutbox(t, cfg)
		if len(rows) != 1 || rows[0].Status != "observed" || rows[0].Request != before.Request || len(wakeExactReservations(t, cfg, wakeExactNative)) != 0 {
			t.Fatalf("reconcile outcome %#v", rows)
		}
	})
}
