package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

// The test binary is not wt: a real broker launch would re-run the test suite
// as a detached process. No test in this package may start one.
func refuseTestBrokerStart(*config.Config, conversationBrokerRegistration) error {
	return errors.New("test binaries never start a host broker process")
}

func init() { startConversationBroker = refuseTestBrokerStart }

// The real launcher refuses a test binary before touching state or exec. The
// path does not exist, so even a regressed guard cannot spawn a process here.
func TestConversationBrokerReceiverRefusesTestBinary(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	reg := conversationBrokerRegistration{SessionID: "parent-exec", Executable: filepath.Join(t.TempDir(), "missing", "wt.test")}
	err := defaultStartConversationBroker(cfg, reg)
	if err == nil || !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("test binary accepted as broker receiver: %v", err)
	}
	if _, statErr := os.Stat(conversationBrokerDir(cfg, reg.SessionID)); !os.IsNotExist(statErr) {
		t.Fatal("refused receiver created broker state")
	}
	if brokerReceiverRefused("/opt/wingthing/wt-preview") != nil || brokerReceiverRefused("/usr/local/bin/wt") != nil {
		t.Fatal("built wt receiver refused")
	}
}

type brokerFixture struct {
	b     *conversationBroker
	cfg   *config.Config
	db    *store.Store
	root  *store.Conversation
	child *store.Conversation
	other *store.Conversation
}

func newBrokerFixture(t *testing.T, surface control.Surface, actor string) brokerFixture {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir()}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Conversations: config.ConversationsEnabled}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	child := fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	other := fixtureConversation(t, db, cfg, "other", "", "owner", "idle")
	workspace := wingpolicy.CanonicalPolicyPath(t.TempDir())
	snapshot, err := brokerChildPolicySnapshot(egg.DefaultEggConfig(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	reg := conversationBrokerRegistration{
		Version: 1, StateDir: wingpolicy.CanonicalPolicyPath(cfg.Dir), ConversationID: root.ID, RootID: root.RootID, SessionID: root.SessionID,
		Principal: "owner", LauncherActor: actor, LauncherSurface: string(surface), Tools: conversationBrokerTools,
		MaxSessions: 8, MaxSpawnsPerHour: 60, Workspace: workspace, Mailbox: filepath.Join(".wingthing-conversations", root.ID, root.SessionID, "mailbox"),
		EggConfig: snapshot, Executable: "/usr/bin/false", RegisteredAt: time.Now().Unix(),
	}
	if err := writeConversationBrokerRegistration(cfg, reg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConversationBrokerRegistration(cfg, root.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, reg.Mailbox), 0o700); err != nil {
		t.Fatal(err)
	}
	mailbox, err := os.OpenRoot(filepath.Join(workspace, reg.Mailbox))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mailbox.Close() })
	epoch, _ := newMailboxID()
	b := &conversationBroker{version: "dev", cfg: cfg, reg: loaded, dir: conversationBrokerDir(cfg, root.SessionID), epoch: epoch, provider: "provider-root", admission: NewMCPAdmissionState(), mailbox: mailbox, logs: &bytes.Buffer{}, inflight: map[string]bool{}, slots: make(chan struct{}, 8)}
	return brokerFixture{b: b, cfg: cfg, db: db, root: root, child: child, other: other}
}

func (f brokerFixture) publish(t *testing.T, epoch string, created time.Time, payload string) string {
	t.Helper()
	id, _ := newMailboxID()
	request := conversationMailboxRequest{Version: 1, ID: id, Epoch: epoch, CreatedAt: created.Unix(), Payload: json.RawMessage(payload)}
	if err := mailboxWrite(f.b.mailbox, mailboxRequestName(id), request, conversationMailboxRequestBytes); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f brokerFixture) response(t *testing.T, id string) (conversationMailboxResponse, map[string]any) {
	t.Helper()
	data, err := mailboxRead(f.b.mailbox, mailboxResponseName(id), conversationMailboxResponseBytes)
	if err != nil {
		t.Fatal(err)
	}
	var envelope conversationMailboxResponse
	if err := decodeMailbox(data, &envelope); err != nil {
		t.Fatal(err)
	}
	var hosted struct {
		Result map[string]any `json:"result"`
	}
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &hosted); err != nil {
			t.Fatal(err)
		}
	}
	return envelope, hosted.Result
}

func (f brokerFixture) call(t *testing.T, tool string, arguments any) (conversationMailboxResponse, map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
	id := f.publish(t, f.b.epoch, time.Now(), string(encoded))
	f.b.accept(context.Background(), id)
	envelope, result := f.response(t, id)
	structured, _ := result["structuredContent"].(map[string]any)
	return envelope, structured
}

func auditLines(t *testing.T, cfg *config.Config) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.Dir, "mcp-audit.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestHostMailboxBrokerServesOnlyBridgeToolsUnderCapturedOwner(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	id := f.publish(t, f.b.epoch, time.Now(), `{"method":"tools/list"}`)
	f.b.accept(context.Background(), id)
	envelope, result := f.response(t, id)
	if !envelope.Dispatched || envelope.ConversationID != f.root.ID || envelope.SessionID != f.root.SessionID || envelope.ProviderSessionID != "provider-root" {
		t.Fatalf("response binding %+v", envelope)
	}
	var listed []string
	for _, tool := range result["tools"].([]any) {
		listed = append(listed, tool.(map[string]any)["name"].(string))
	}
	slices.Sort(listed)
	expected := slices.Clone(conversationBrokerTools)
	slices.Sort(expected)
	if !slices.Equal(listed, expected) {
		t.Fatalf("host mailbox listed %v, want exactly %v", listed, expected)
	}
	for _, forbidden := range []string{"terminal_send", "terminal_start", "prompt_run", "conversation_wake", "terminal_stop"} {
		envelope, structured := f.call(t, forbidden, map[string]any{})
		if !strings.Contains(structured["error"].(string), "not available on this connection") || !envelope.Dispatched {
			t.Fatalf("%s reachable through host mailbox: %v", forbidden, structured)
		}
	}
	_, listing := f.call(t, "conversation_list", map[string]any{})
	if len(listing["conversations"].([]any)) != 2 {
		t.Fatalf("bound listing escaped the captured tree: %v", listing)
	}
	records := auditLines(t, f.cfg)
	last := records[len(records)-1]
	if last["principal"] != "owner" || last["actor"] != "conversation:"+f.root.ID+":execution:"+f.root.SessionID || last["tool"] != "conversation_list" {
		t.Fatalf("audit attribution must name the exact parent execution: %v", last)
	}
}

// The task-tree target rules belong to host mailbox connections only. A direct
// `--conversation` stdio connection keeps the deployed terminal contract.
func TestBoundTreeScopeRulesApplyOnlyToBrokerConnections(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	direct := &Server{Version: "dev", Cfg: f.cfg, Principal: "owner", BoundConversation: f.root.ID}
	if err := direct.checkBoundSessionTarget("session_status", json.RawMessage(`{"session":"`+f.other.SessionID+`"}`)); err != nil {
		t.Fatalf("direct bound connection gained broker tree rules: %v", err)
	}
	broker, _, err := f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.checkBoundSessionTarget("session_status", json.RawMessage(`{"session":"`+f.other.SessionID+`"}`)); err == nil {
		t.Fatal("broker connection reached another task tree")
	}
	// The broker's fixed tool subset and exact-ID resolution stay off the
	// direct server: no tool filter, no broker registration.
	if direct.tools != nil || direct.broker != nil || !direct.toolAllowed("terminal_send") {
		t.Fatal("direct bound connection inherited the host mailbox tool subset")
	}
	if broker.toolAllowed("terminal_send") || !broker.toolAllowed("session_prompt") {
		t.Fatal("broker tool subset not enforced")
	}
}

// Audit attribution names the exact execution, not the conversation or the
// launcher: a resumed execution of the same conversation is a distinct actor.
func TestHostMailboxActorIsExecutionSpecific(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	first, _, err := f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	resumed := f.b.reg
	resumed.SessionID = f.root.SessionID + "-resumed"
	second, _, err := resumed.server("dev", f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	if first.clientActor() != brokerActor(f.root.ID, f.root.SessionID) || second.clientActor() != brokerActor(f.root.ID, resumed.SessionID) {
		t.Fatalf("actors %q %q", first.clientActor(), second.clientActor())
	}
	if first.clientActor() == second.clientActor() || first.clientActor() == f.b.reg.LauncherActor || first.clientPrincipal() != f.b.reg.Principal {
		t.Fatalf("actor %q must name one execution under principal %q", first.clientActor(), f.b.reg.Principal)
	}
}

// forwardMailboxCallError returns the structured MCP error data the stdio
// client derives from a broker error envelope, via the production mapping.
func forwardMailboxCallError(t *testing.T, envelope conversationMailboxResponse) map[string]any {
	t.Helper()
	if envelope.Error == "" {
		t.Fatal("expected a broker error envelope")
	}
	var state *mailboxCallError
	if !errors.As(mailboxEnvelopeError(envelope), &state) {
		t.Fatal("broker error envelope did not map to dispatch state")
	}
	return map[string]any{"dispatched": state.dispatched, "outcome": state.outcome, "retry_safe": state.dispatched == "no"}
}

// A mutation interrupted mid-dispatch is reported as dispatched with an
// unconfirmed outcome and stays journaled for bounded recovery; it is never
// reported as safe to resend.
func TestHostMailboxInterruptedMutationIsStructuredUnconfirmed(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := f.publish(t, f.b.epoch, time.Now(), `{"method":"tools/call","params":{"name":"conversation_checkpoint","arguments":{"conversation_id":"`+f.root.ID+`","expected_revision":0,"after_cursor":0,"checkpoint":"interrupted"}}}`)
	f.b.accept(ctx, id)
	envelope, _ := f.response(t, id)
	if !envelope.Dispatched || envelope.Outcome != brokerOutcomeUnconfirmed || !strings.Contains(envelope.Error, "outcome is unconfirmed") {
		t.Fatalf("interrupted mutation %+v", envelope)
	}
	if entry, ok := f.b.readJournal(id); !ok || entry.Phase != "dispatching" {
		t.Fatalf("interrupted mutation left no dispatching journal entry: %+v", entry)
	}
	response := forwardMailboxCallError(t, envelope)
	if response["dispatched"] != "unknown" || response["outcome"] != brokerOutcomeUnconfirmed || response["retry_safe"] != false {
		t.Fatalf("caller-visible state %v", response)
	}
}

func TestHostMailboxRecoveryRefusalKeepsPriorMutationUnconfirmed(t *testing.T) {
	for _, tool := range []string{"agent_start", "session_prompt"} {
		for _, refusal := range []string{"locked", "policy_changed"} {
			t.Run(tool+"/"+refusal, func(t *testing.T) {
				f := newBrokerFixture(t, control.SurfaceLocalMCP, "codex")
				arguments := map[string]any{"agent": "claude", "cwd": f.cfg.Dir, "request_id": f.child.LaunchKey}
				if tool == "agent_start" {
					if err := (&Server{Version: "dev", Cfg: f.cfg}).markConversationLaunch(f.child, nil); err != nil {
						t.Fatal(err)
					}
				} else {
					arguments = map[string]any{"session": f.child.SessionID, "request_id": "interrupted-prompt", "input": "hello"}
					sends := 0
					result, err := egg.SubmitSessionPrompt(context.Background(), filepath.Join(f.cfg.Dir, "eggs", f.child.SessionID), egg.SessionPromptOptions{
						RequestID: "interrupted-prompt", Input: "hello", Timeout: 100 * time.Millisecond,
						Read: func(context.Context, int64, int) (egg.SessionView, error) {
							return egg.SessionView{SessionID: f.child.SessionID, Agent: "claude", ProviderSessionID: "provider-child", State: "idle", StateSource: "claude_hook", ProcessAlive: true, Ready: true}, nil
						},
						Send: func(context.Context, string) (egg.PromptDelivery, error) {
							sends++
							return egg.PromptDelivery{BytesEnqueued: 5}, nil
						},
					})
					if err != nil || sends != 1 || !result.TransportEnqueued {
						t.Fatalf("prior prompt effect: %+v sends=%d err=%v", result, sends, err)
					}
				}
				// The effect survived, but the broker crashed before saving its response.
				payload, _ := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
				id, _ := newMailboxID()
				entry := conversationBrokerJournal{Version: 1, ID: id, Method: "tools/call", Tool: tool, Payload: payload, AcceptedAt: time.Now().Unix(), Phase: "dispatching"}
				if err := f.b.writeJournal(entry); err != nil {
					t.Fatal(err)
				}
				name, content := "wing.yaml", "conversations: enabled\nlocked: true\n"
				if refusal == "policy_changed" {
					name, content = "clients.yaml", "clients:\n  codex:\n    owner: different-owner\n"
				}
				if err := os.WriteFile(filepath.Join(f.cfg.Dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				f.b.reconcileJournal(context.Background())
				f.b.calls.Wait()
				envelope, _ := f.response(t, id)
				response := forwardMailboxCallError(t, envelope)
				if response["dispatched"] != "unknown" || response["outcome"] != brokerOutcomeUnconfirmed || response["retry_safe"] != false {
					t.Fatalf("recovery forgot the earlier effect: %v", response)
				}
				if saved, ok := f.b.readJournal(id); !ok || saved.Outcome != brokerOutcomeUnconfirmed {
					t.Fatalf("recovery persisted an unsafe outcome: %+v", saved)
				}
			})
		}
	}
}

func TestHostMailboxReintersectsCurrentWingPathsPerCall(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	workspace := f.b.reg.Workspace
	f.b.reg.AllowedPaths, f.b.reg.EnforcePathBounds = []string{workspace}, true
	server, _, err := f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil || !server.enforcePathBounds || !slices.Equal(server.allowedPaths, []string{workspace}) {
		t.Fatalf("captured bound %v %v", server.allowedPaths, err)
	}
	narrow := filepath.Join(workspace, "project")
	if err := os.WriteFile(filepath.Join(f.cfg.Dir, "wing.yaml"), []byte("conversations: enabled\npaths:\n  - "+narrow+"\n  - "+t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if server, _, err = f.b.reg.server("dev", f.cfg, f.b.admission); err != nil || !slices.Equal(server.allowedPaths, []string{narrow}) {
		t.Fatalf("current wing paths did not narrow the captured bound: %v %v", server.allowedPaths, err)
	}
	f.b.reg.AllowedPaths, f.b.reg.EnforcePathBounds = nil, false
	if server, _, err = f.b.reg.server("dev", f.cfg, f.b.admission); err != nil || !server.enforcePathBounds || len(server.allowedPaths) != 2 {
		t.Fatalf("an unbounded capture must still honor the current wing ACL: %v %v", server.allowedPaths, err)
	}
	if paths, enforced := intersectBrokerPaths([]string{workspace}, true, []string{t.TempDir()}); !enforced || len(paths) != 0 {
		t.Fatalf("disjoint ACL widened access: %v", paths)
	}
}

func TestHostMailboxBrokerKeepsEverySessionTargetInsideCapturedRoot(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	resumed := fixtureConversation(t, f.db, f.cfg, "resumed", "", "owner", "idle")
	if _, err := f.db.DB().Exec(`DELETE FROM conversation_executions WHERE session_id = ?`, resumed.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.DB().Exec(`DELETE FROM conversations WHERE id = ?`, resumed.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ResumeConversationExecution(f.child.SessionID, resumed.SessionID); err != nil {
		t.Fatal(err)
	}
	unlinked := filepath.Join(f.cfg.Dir, "eggs", "plain-terminal")
	if err := os.MkdirAll(unlinked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(unlinked, "owner"); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"other root":        f.other.SessionID,
		"unlinked terminal": "plain-terminal",
		"prefix":            f.child.SessionID[:len(f.child.SessionID)-2],
		"missing":           "execution-missing",
	} {
		_, structured := f.call(t, "session_status", map[string]any{"session": target})
		if !strings.Contains(structured["error"].(string), "outside this MCP connection's bound task tree") {
			t.Fatalf("%s target accepted: %v", name, structured)
		}
	}
	for name, target := range map[string]string{"child": f.child.SessionID, "archived child execution": f.child.SessionID, "resumed child execution": resumed.SessionID, "parent": f.root.SessionID} {
		_, structured := f.call(t, "session_status", map[string]any{"session": target})
		if message, _ := structured["error"].(string); strings.Contains(message, "bound task tree") {
			t.Fatalf("%s rejected: %v", name, message)
		}
	}
	_, structured := f.call(t, "conversation_read", map[string]any{"conversation_id": f.other.ID})
	if !strings.Contains(structured["error"].(string), "bound task tree") {
		t.Fatalf("other conversation readable: %v", structured)
	}
}

func TestHostMailboxBrokerJournalNeverRedispatchesAnEffect(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	ctx := context.Background()
	applied := `{"method":"tools/call","params":{"name":"conversation_checkpoint","arguments":{"conversation_id":"` + f.root.ID + `","expected_revision":0,"after_cursor":0,"checkpoint":"first"}}}`
	id := f.publish(t, f.b.epoch, time.Now(), applied)
	f.b.accept(ctx, id)
	first, _ := f.response(t, id)
	if first.Outcome != brokerOutcomeCompleted {
		t.Fatalf("mutation outcome %+v", first)
	}
	audited := len(auditLines(t, f.cfg))
	// A replayed artifact with the same request ID republishes the journaled
	// response; it is not a second dispatch.
	request := conversationMailboxRequest{Version: 1, ID: id, Epoch: f.b.epoch, CreatedAt: time.Now().Unix(), Payload: json.RawMessage(applied)}
	if err := mailboxWrite(f.b.mailbox, mailboxRequestName(id), request, conversationMailboxRequestBytes); err != nil {
		t.Fatal(err)
	}
	f.b.accept(ctx, id)
	again, _ := f.response(t, id)
	if string(again.Payload) != string(first.Payload) || len(auditLines(t, f.cfg)) != audited {
		t.Fatal("replayed request ID was dispatched again")
	}
	before, _ := f.db.GetConversation("owner", f.root.ID)
	// Crash after journaling a checkpoint: outcome unknown, never replayed.
	checkpoint, _ := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{"name": "conversation_checkpoint", "arguments": map[string]any{"conversation_id": f.root.ID, "expected_revision": before.Revision, "after_cursor": 0, "checkpoint": "interrupted"}}})
	crashed, _ := newMailboxID()
	if err := f.b.writeJournal(conversationBrokerJournal{Version: 1, ID: crashed, Method: "tools/call", Tool: "conversation_checkpoint", Payload: checkpoint, AcceptedAt: time.Now().Unix(), Phase: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	// Crash after journaling a request_id mutation: reconciled by its durable ID.
	prompt := `{"method":"tools/call","params":{"name":"session_prompt","arguments":{"session":"` + f.other.SessionID + `","request_id":"r1","input":"hello"}}}`
	replayed, _ := newMailboxID()
	if err := f.b.writeJournal(conversationBrokerJournal{Version: 1, ID: replayed, Method: "tools/call", Tool: "session_prompt", Payload: json.RawMessage(prompt), AcceptedAt: time.Now().Unix(), Phase: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	// An entry older than the request age bound is never replayed.
	expired, _ := newMailboxID()
	if err := f.b.writeJournal(conversationBrokerJournal{Version: 1, ID: expired, Method: "tools/call", Tool: "session_prompt", Payload: json.RawMessage(prompt), AcceptedAt: time.Now().Add(-time.Hour).Unix(), Phase: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	f.b.reconcileJournal(ctx)
	f.b.calls.Wait()
	unconfirmed, _ := f.response(t, crashed)
	if !unconfirmed.Dispatched || unconfirmed.Outcome != brokerOutcomeUnconfirmed || !strings.Contains(unconfirmed.Error, "outcome is unconfirmed") {
		t.Fatalf("interrupted checkpoint %+v", unconfirmed)
	}
	if c, err := f.db.GetConversation("owner", f.root.ID); err != nil || c.Revision != before.Revision || c.Checkpoint != before.Checkpoint {
		t.Fatalf("interrupted checkpoint was replayed: %+v %v", c, err)
	}
	if envelope, result := f.response(t, replayed); envelope.Outcome != brokerOutcomeCompleted || result == nil {
		t.Fatalf("request_id mutation was not reconciled: %+v", envelope)
	}
	if envelope, _ := f.response(t, expired); envelope.Outcome != brokerOutcomeUnconfirmed {
		t.Fatalf("expired journal entry was replayed: %+v", envelope)
	}
	for name, publish := range map[string]func() string{
		"stale epoch": func() string { other, _ := newMailboxID(); return f.publish(t, other, time.Now(), `{"method":"ping"}`) },
		"expired":     func() string { return f.publish(t, f.b.epoch, time.Now().Add(-time.Hour), `{"method":"ping"}`) },
		"forged owner": func() string {
			return f.publish(t, f.b.epoch, time.Now(), `{"method":"ping","principal":"someone-else"}`)
		},
		"raw method":     func() string { return f.publish(t, f.b.epoch, time.Now(), `{"method":"resources/read"}`) },
		"future request": func() string { return f.publish(t, f.b.epoch, time.Now().Add(time.Hour), `{"method":"ping"}`) },
	} {
		before := len(auditLines(t, f.cfg))
		id := publish()
		f.b.accept(ctx, id)
		envelope, _ := f.response(t, id)
		if envelope.Dispatched || !strings.Contains(envelope.Error, "not dispatched") || len(auditLines(t, f.cfg)) != before {
			t.Fatalf("%s: %+v", name, envelope)
		}
		if _, err := os.Stat(f.b.journalPath(id)); !os.IsNotExist(err) {
			t.Fatalf("%s was journaled as dispatched", name)
		}
	}
}

// Polling and protocol calls are never journaled, so they cannot exhaust the
// mutation journal; a full journal refuses only new mutations, undispatched.
func TestHostMailboxReadsNeverConsumeTheMutationJournal(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	for _, payload := range []string{`{"method":"initialize","params":{}}`, `{"method":"tools/list"}`, `{"method":"tools/call","params":{"name":"conversation_list","arguments":{}}}`, `{"method":"tools/call","params":{"name":"session_read","arguments":{"session":"` + f.root.SessionID + `"}}}`} {
		id := f.publish(t, f.b.epoch, time.Now(), payload)
		f.b.accept(context.Background(), id)
		if envelope, _ := f.response(t, id); envelope.Outcome != brokerOutcomeCompleted {
			t.Fatalf("%s: %+v", payload, envelope)
		}
		if _, err := os.Stat(f.b.journalPath(id)); !os.IsNotExist(err) {
			t.Fatalf("non-mutation %s was journaled", payload)
		}
	}
	for range conversationBrokerJournalLimit {
		id, _ := newMailboxID()
		if err := f.b.writeJournal(conversationBrokerJournal{Version: 1, ID: id, Method: "tools/call", Tool: "session_prompt", AcceptedAt: time.Now().Unix(), Phase: "completed", Outcome: brokerOutcomeCompleted}); err != nil {
			t.Fatal(err)
		}
	}
	if envelope, structured := f.call(t, "conversation_list", map[string]any{}); envelope.Outcome != brokerOutcomeCompleted || structured["conversations"] == nil {
		t.Fatalf("full journal blocked a read: %+v", envelope)
	}
	envelope, _ := f.call(t, "conversation_checkpoint", map[string]any{"conversation_id": f.root.ID, "expected_revision": 0, "after_cursor": 0, "checkpoint": "full"})
	if envelope.Dispatched || envelope.Outcome != brokerOutcomeNotDispatched || !strings.Contains(envelope.Error, "journal is full") {
		t.Fatalf("full journal accepted a mutation: %+v", envelope)
	}
}

func TestHostMailboxSweepExpiresOrphanResponsesBeyondEntryLimit(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	old := time.Now().Add(-conversationMailboxRequestMaxAge - time.Minute)
	var abandoned []string
	for i := 0; i <= conversationMailboxEntryLimit; i++ {
		payload := `{"method":"ping"}`
		if i%2 == 0 {
			payload = `{"method":"tools/call","params":{"name":"conversation_list","arguments":{}}}`
		}
		id := f.publish(t, f.b.epoch, time.Now(), payload)
		f.b.accept(context.Background(), id)
		path := filepath.Join(f.b.reg.Workspace, f.b.reg.Mailbox, mailboxResponseName(id))
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		abandoned = append(abandoned, id)
	}
	if f.b.journalCount() != 0 {
		t.Fatal("read/protocol calls consumed the mutation journal")
	}
	fresh := f.publish(t, f.b.epoch, time.Now(), `{"method":"ping"}`)
	f.b.accept(context.Background(), fresh)
	pending := f.publish(t, f.b.epoch, time.Now(), `{"method":"ping"}`)
	if _, err := mailboxEntries(f.b.mailbox, conversationMailboxEntryLimit); err == nil {
		t.Fatal("fixture did not exceed the mailbox dispatch limit")
	}
	f.b.sweep()
	for _, id := range abandoned {
		if _, err := f.b.mailbox.Lstat(mailboxResponseName(id)); !os.IsNotExist(err) {
			t.Fatalf("orphan response survived retention: %s: %v", id, err)
		}
	}
	if _, err := f.b.mailbox.Lstat(mailboxResponseName(fresh)); err != nil {
		t.Fatalf("fresh response was swept: %v", err)
	}
	if _, err := f.b.mailbox.Lstat(mailboxRequestName(pending)); err != nil {
		t.Fatalf("pending request was swept: %v", err)
	}
	f.b.scan(context.Background())
	f.b.calls.Wait()
	if envelope, _ := f.response(t, pending); envelope.Outcome != brokerOutcomeCompleted {
		t.Fatalf("dispatch did not resume after sweeping: %+v", envelope)
	}
}

func TestHostMailboxPolicyIntersectsCurrentClientsAndWing(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceLocalMCP, "codex")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.cfg.Dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server, _, err := f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil || len(server.tools) != len(conversationBrokerTools) || server.MaxSessions != 8 || server.MaxSpawnsPerHour != 60 {
		t.Fatalf("captured ceiling %+v %v", server, err)
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, capabilities.read]\n    bounds:\n      max_sessions: 2\n")
	server, _, err = f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil || server.tools["agent_start"] || server.tools["session_prompt"] || server.tools["conversation_checkpoint"] || !server.tools["session_read"] || server.MaxSessions != 2 || server.MaxSpawnsPerHour != 60 {
		t.Fatalf("narrowed policy not applied: %+v %v", server.tools, err)
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, terminal.send, terminal.start, capabilities.read, agent.run]\n    bounds:\n      max_sessions: 100\n")
	if server, _, _ = f.b.reg.server("dev", f.cfg, f.b.admission); server.MaxSessions != 8 || len(server.tools) != len(conversationBrokerTools) {
		t.Fatalf("current policy widened the captured ceiling: %d %v", server.MaxSessions, server.tools)
	}
	for name, content := range map[string]string{
		"revoked launcher": "clients:\n  someone:\n    owner: owner\n",
		"changed owner":    "clients:\n  codex:\n    owner: another-owner\n",
	} {
		write("clients.yaml", content)
		if _, _, err := f.b.reg.server("dev", f.cfg, f.b.admission); err == nil {
			t.Fatalf("%s still dispatches", name)
		}
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, terminal.send, terminal.start, capabilities.read]\n")
	write("wing.yaml", "conversations: enabled\nlocked: true\n")
	envelope, _ := f.call(t, "conversation_checkpoint", map[string]any{"conversation_id": f.root.ID, "expected_revision": 0, "after_cursor": 0, "checkpoint": "locked"})
	if envelope.Dispatched || !strings.Contains(envelope.Error, "locked") {
		t.Fatalf("locked wing accepted a mutation: %+v", envelope)
	}
	if _, structured := f.call(t, "conversation_list", map[string]any{}); structured["conversations"] == nil {
		t.Fatalf("locked wing blocked a read: %v", structured)
	}
	write("wing.yaml", "conversations: enabled\norg: team\n")
	if envelope, _ := f.call(t, "conversation_list", map[string]any{}); envelope.Dispatched {
		t.Fatal("organization wing dispatched through a personal host mailbox")
	}
}

func TestHostMailboxChildrenUseCapturedParentPolicyNotWorkspaceFiles(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	workspace := f.b.reg.Workspace
	if err := os.WriteFile(filepath.Join(workspace, "egg.yaml"), []byte("base:\n  name: none\nfs: [\"rw:/\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	child, err := f.b.reg.childEggConfig(filepath.Join(workspace, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if !egg.RequiresSandbox(child, "claude") || child.DangerouslySkipPermissions || !slices.Contains(child.FS, "rw:"+workspace) || slices.Contains(child.FS, "rw:/") {
		t.Fatalf("child policy came from a provider-writable file: %v", child.FS)
	}
	if !slices.Contains(child.FS, "deny-write:"+filepath.Join(workspace, "egg.yaml")) || !slices.Contains(child.FS, "deny:~/.ssh") {
		t.Fatalf("relative rules must be anchored to the parent workspace, ~ rules kept: %v", child.FS)
	}
	if _, err := f.b.reg.childEggConfig(t.TempDir()); err == nil {
		t.Fatal("child outside the parent workspace accepted")
	}
	server, _, err := f.b.reg.server("dev", f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	refused := false
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		refused = true
		return os.ErrPermission
	}
	t.Cleanup(func() { conversationBrokerProtection = defaultConversationBrokerProtection })
	if err := server.preflightBrokerChild(child, "claude", workspace, "child-session"); err == nil || !refused {
		t.Fatal("child spawn skipped the protection preflight")
	}
}

func TestHostMailboxDurableSpawnRateSurvivesBrokerRestart(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return nil
	}
	t.Cleanup(func() { conversationBrokerProtection = defaultConversationBrokerProtection })
	f.b.reg.MaxSpawnsPerHour = 3
	server, _, err := f.b.reg.server("dev", f.cfg, NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	// Three reservations already exist for this owner (root, child, other).
	if err := server.preflightBrokerChild(egg.DefaultEggConfig(), "claude", f.b.reg.Workspace, "next"); err != nil {
		t.Fatalf("launch within durable bound refused: %v", err)
	}
	fixtureConversation(t, f.db, f.cfg, "fourth", f.root.ID, "owner", "idle")
	if err := server.preflightBrokerChild(egg.DefaultEggConfig(), "claude", f.b.reg.Workspace, "next"); err == nil || !strings.Contains(err.Error(), "durable max_spawns_per_hour") {
		t.Fatalf("fresh process admission ignored durable launches: %v", err)
	}
}

func TestHostMailboxActivationKeepsDirectTransportAndCapturesFiniteAuthority(t *testing.T) {
	workspace := wingpolicy.CanonicalPolicyPath(t.TempDir())
	direct := &Server{Version: "dev", Cfg: &config.Config{Dir: filepath.Join(workspace, "state")}, Principal: "owner"}
	c := &store.Conversation{ID: "parent", RootID: "parent", SessionID: "parent-exec", CWD: workspace, Agent: "claude"}
	args, err := direct.prepareBoundParentMCP(c, egg.DefaultEggConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(args[1])
	if bytes.Contains(data, []byte("--host-mailbox")) || !bytes.Contains(data, []byte("WINGTHING_DIR")) {
		t.Fatalf("writable-state layout changed transport: %s", data)
	}
	// Only the workspace is writable, so state elsewhere selects the mailbox.
	narrow := func() *egg.EggConfig { return &egg.EggConfig{FS: []string{"ro:/", "rw:./"}} }
	exposed := &Server{Version: "dev", Cfg: &config.Config{Dir: t.TempDir()}, Principal: "owner"}
	var protectedTargets []string
	conversationBrokerProtection = func(_ *config.Config, _ *egg.EggConfig, _, _, _ string, _ eggclient.EggIdentity, targets []string) error {
		protectedTargets = targets
		return errors.New("modeled provider-writable state")
	}
	var started []conversationBrokerRegistration
	startConversationBroker = func(_ *config.Config, reg conversationBrokerRegistration) error {
		started = append(started, reg)
		return nil
	}
	oldChannel := config.ReleaseChannel
	t.Cleanup(func() {
		conversationBrokerProtection = defaultConversationBrokerProtection
		startConversationBroker = refuseTestBrokerStart
		config.ReleaseChannel = oldChannel
	})
	// Stable keeps its deployed refusal byte for byte and never registers.
	config.ReleaseChannel = "stable"
	stableRefusal := `parent MCP cannot write isolated Wingthing state "` + exposed.Cfg.Dir + `" under the existing sandbox policy; use an already writable workspace containing that state directory (no mounts or grants were changed)`
	if _, err := exposed.prepareBoundParentMCP(c, narrow(), nil); err == nil || err.Error() != stableRefusal || protectedTargets != nil {
		t.Fatalf("stable layout changed: %v", err)
	}
	if err := RunConversationBroker("dev", context.Background(), exposed.Cfg, c.SessionID, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "preview channel") {
		t.Fatalf("stable binary served a broker: %v", err)
	}
	config.ReleaseChannel = "preview"
	if _, err := exposed.prepareBoundParentMCP(c, narrow(), nil); err == nil || !strings.Contains(err.Error(), "host mailbox unavailable: modeled provider-writable state") {
		t.Fatalf("exposed state was not refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exposed.Cfg.Dir, conversationBrokersDir)); !os.IsNotExist(err) || len(started) != 0 {
		t.Fatal("refused layout left a registration or started a broker")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The whole canonical state directory and the resolved controller, not a
	// list of individual state files.
	if want := []string{wingpolicy.CanonicalPolicyPath(exposed.Cfg.Dir), wingpolicy.CanonicalPolicyPath(executable)}; !slices.Equal(protectedTargets, want) {
		t.Fatalf("protected targets %v, want %v", protectedTargets, want)
	}
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return nil
	}
	// A resumed browser parent cannot carry the launch contract and never
	// registers a broker.
	resumed := &Server{Version: "dev", Cfg: exposed.Cfg, Principal: "owner", hostMailboxUnavailable: "resume refused"}
	if _, err := resumed.prepareBoundParentMCP(c, narrow(), nil); err == nil || !strings.Contains(err.Error(), "host mailbox unavailable: resume refused") || len(started) != 0 {
		t.Fatalf("resume selected the host mailbox: %v", err)
	}
	launcher := &Server{Version: "dev", Cfg: exposed.Cfg, Principal: roostSessionPrincipal("user"), Actor: "browser", Surface: control.SurfaceHTTPMCP, identity: eggclient.EggIdentity{UserID: "user", Email: "user@example.invalid"}}
	args, managed, err := launcher.prepareBoundParentLaunch(c, narrow(), []string{"--model", "selected"})
	if err != nil {
		t.Fatal(err)
	}
	opts := managed.launchOpts(exposed.Cfg, eggclient.SpawnEggOpts{Label: "parent", Kind: "agent"})
	if !opts.OmitBrowserBridge || !slices.Equal(opts.ProtectedWriteTargets, protectedTargets) || opts.Label != "parent" {
		t.Fatalf("broker-managed parent launch options %+v", opts)
	}
	if _, selected, err := direct.prepareBoundParentLaunch(c, egg.DefaultEggConfig(), nil); err != nil || selected != nil {
		t.Fatalf("direct transport became broker-managed: %v", err)
	}
	if len(started) != 1 || len(args) != 6 || args[0] != "--model" || args[2] != "--mcp-config" || args[4] != "--append-system-prompt" {
		t.Fatalf("broker activation args %v started %d", args, len(started))
	}
	reg := started[0]
	if reg.Principal != launcher.Principal || reg.LauncherActor != "browser" || reg.UserID != "user" || reg.MaxSessions != defaultDirectMCPMaxSessions || reg.MaxSpawnsPerHour != defaultDirectMCPMaxSpawnsPerHour || !slices.Equal(reg.Tools, conversationBrokerTools) {
		t.Fatalf("nil-grant launcher must receive the finite bridge ceiling: %+v", reg)
	}
	loaded, err := loadConversationBrokerRegistration(exposed.Cfg, c.SessionID)
	if err != nil || loaded.Workspace != workspace || loaded.SessionID != c.SessionID {
		t.Fatalf("protected registration %+v %v", loaded, err)
	}
	data, err = os.ReadFile(args[3])
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &configuration); err != nil {
		t.Fatal(err)
	}
	server := configuration.MCPServers["wingthing"]
	mailbox := filepath.Join(workspace, reg.Mailbox)
	if !slices.Equal(server.Args, []string{"mcp", "stdio", "--client", reg.Principal, "--conversation", c.ID, "--execution", c.SessionID, "--host-mailbox", mailbox}) || len(server.Env) != 0 {
		t.Fatalf("parent mailbox configuration %+v", server)
	}
	if info, err := os.Stat(mailbox); err != nil || !info.IsDir() {
		t.Fatalf("mailbox directory %v", err)
	}
	if _, err := launcher.prepareBoundParentMCP(c, narrow(), nil); err == nil {
		t.Fatal("one execution registered twice")
	}
	limited := &Server{Version: "dev", Cfg: exposed.Cfg, Principal: "owner", Grants: GrantSet([]string{"terminal.read"}), MaxSessions: 2, MaxSpawnsPerHour: 5}
	second := &store.Conversation{ID: "parent", RootID: "parent", SessionID: "parent-exec-2", CWD: workspace, Agent: "claude"}
	if _, err := limited.prepareBoundParentMCP(second, narrow(), nil); err != nil {
		t.Fatal(err)
	}
	if reg := started[len(started)-1]; slices.Contains(reg.Tools, "agent_start") || !slices.Contains(reg.Tools, "session_read") || reg.MaxSessions != 2 || reg.MaxSpawnsPerHour != 5 {
		t.Fatalf("launcher limits were not preserved: %+v", reg)
	}
	if err := os.WriteFile(filepath.Join(exposed.Cfg.Dir, "wing.yaml"), []byte("org: team\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third := &store.Conversation{ID: "parent", RootID: "parent", SessionID: "parent-exec-3", CWD: workspace, Agent: "claude"}
	if _, err := launcher.prepareBoundParentMCP(third, narrow(), nil); err == nil {
		t.Fatal("organization wing captured a personal host mailbox")
	}
}

func TestProviderWriteModelFollowsSeatbeltWriteRules(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the provider write model describes the macOS Seatbelt profile")
	}
	base := wingpolicy.CanonicalPolicyPath(t.TempDir())
	tmp, workspace := filepath.Join(base, "tmp"), filepath.Join(base, "workspace")
	for _, dir := range []string{tmp, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The modeled HOME must not lie under /private/tmp, which the profile
	// always reopens; t.TempDir may. It is never created: path checks are
	// lexical below an existing root-owned ancestor.
	home := filepath.Join("/Users", "wt-provider-model-"+filepath.Base(base))
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	cfg := &config.Config{Dir: filepath.Join(home, ".wingthing")}
	model, err := modelProviderWrites(cfg, egg.DefaultEggConfig(), "claude", workspace, "parent-exec", eggclient.EggIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]bool{
		filepath.Join(home, ".wingthing", "wt.db"):                    false,
		filepath.Join(home, ".wingthing", "conversation-brokers"):     false,
		filepath.Join(home, ".claude", "projects", "x.jsonl"):         true,
		filepath.Join(home, ".claude.json"):                           true,
		filepath.Join(home, ".cache", "go-build"):                     true,
		filepath.Join(home, "Library", "Keychains", "login.keychain"): true,
		filepath.Join(tmp, "anything"):                                true,
		filepath.Join(workspace, ".wingthing-conversations"):          true,
		// Broker-managed eggs omit the browser bridge mount.
		filepath.Join(home, ".wingthing", "eggs", "parent-exec", "browser-requests"): false,
		"/private/tmp/x":    true,
		"/usr/local/x":      true,
		filepath.Join(home): false,
	} {
		if _, writable := model.writable(path); writable != expected {
			t.Fatalf("%s writable=%v, want %v", path, writable, expected)
		}
	}
	if err := verifyAncestorsNotRenamable(workspace); err == nil || !strings.Contains(err.Error(), "ancestor") {
		t.Fatalf("a user-writable ancestor of HOME must be refused: %v", err)
	}
	if err := model.verifyProtected(filepath.Join(workspace, "state"), nil); err == nil || !strings.Contains(err.Error(), "provider-writable") {
		t.Fatalf("workspace state accepted: %v", err)
	}
	if err := model.verifyProtected(filepath.Join(home, ".claude-state"), nil); err == nil {
		t.Fatal("state matched by the agent configuration prefix accepted")
	}
	if err := verifyAncestorsNotRenamable("/usr"); err != nil && os.Getuid() != 0 {
		t.Fatalf("root-owned ancestors refused: %v", err)
	}
	if _, err := modelProviderWrites(cfg, egg.UnsandboxedEggConfig(), "claude", workspace, "parent-exec", eggclient.EggIdentity{}); err == nil {
		t.Fatal("outer-boundary session modeled as protected")
	}
}

func TestHostMailboxRequiresProviderDataHomeOutsideState(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	state := wingpolicy.CanonicalPolicyPath(t.TempDir())
	// The default preview provider home lies inside the state directory.
	inside := &config.Config{Dir: state}
	if err := brokerProviderHomeOutsideState(inside); err == nil || !strings.Contains(err.Error(), "overlaps protected state") {
		t.Fatalf("provider data home inside state accepted: %v", err)
	}
	if err := defaultConversationBrokerProtection(inside, egg.DefaultEggConfig(), "claude", state, "parent-exec", eggclient.EggIdentity{}, nil); err == nil || !strings.Contains(err.Error(), "overlaps protected state") {
		t.Fatalf("protection preflight ignored the provider data home: %v", err)
	}
}

func TestStableConversationHostMailboxOptIn(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	oldProtection, oldStart := conversationBrokerProtection, startConversationBroker
	t.Cleanup(func() {
		config.ReleaseChannel = oldChannel
		conversationBrokerProtection, startConversationBroker = oldProtection, oldStart
	})
	cfg := &config.Config{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "wing.yaml"), []byte("conversations: enabled\nroost: https://wingthing.ai\nhosted_relay: allow\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := wingpolicy.CanonicalPolicyPath(t.TempDir())
	c := &store.Conversation{ID: "parent", RootID: "parent", SessionID: "parent-exec", CWD: workspace, Agent: "claude"}
	launcher := &Server{Version: "dev", Cfg: cfg, Principal: roostSessionPrincipal("user"), Actor: "browser", Surface: control.SurfaceHTTPMCP, identity: eggclient.EggIdentity{UserID: "user"}}
	policy := &egg.EggConfig{FS: []string{"ro:/", "rw:./"}}
	before, _ := policy.YAML()
	var protected int
	conversationBrokerProtection = func(got *config.Config, gotPolicy *egg.EggConfig, agent, cwd, session string, identity eggclient.EggIdentity, targets []string) error {
		protected++
		if got != cfg || gotPolicy != policy || agent != "claude" || cwd != workspace || session != c.SessionID || identity.UserID != "user" || len(targets) != 2 || targets[0] != wingpolicy.CanonicalPolicyPath(cfg.Dir) {
			t.Fatalf("lost launch protection: cfg=%+v identity=%+v targets=%v", got, identity, targets)
		}
		return nil
	}
	var started []conversationBrokerRegistration
	startConversationBroker = func(_ *config.Config, reg conversationBrokerRegistration) error {
		started = append(started, reg)
		return nil
	}
	args, managed, err := launcher.prepareBoundParentLaunch(c, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if managed == nil || protected != 1 || len(started) != 1 || managed.Principal != launcher.Principal || managed.UserID != "user" || managed.MaxSessions != defaultDirectMCPMaxSessions || managed.MaxSpawnsPerHour != defaultDirectMCPMaxSpawnsPerHour || !slices.Equal(managed.Tools, conversationBrokerTools) {
		t.Fatalf("stable opted-in launch did not capture its owner: managed=%+v protected=%d started=%d", managed, protected, len(started))
	}
	data, err := os.ReadFile(args[1])
	if err != nil || !bytes.Contains(data, []byte("--host-mailbox")) || bytes.Contains(data, []byte("WINGTHING_DIR")) {
		t.Fatalf("parent must use the mailbox without reopening state: %s %v", data, err)
	}
	after, _ := policy.YAML()
	opts := managed.launchOpts(cfg, eggclient.SpawnEggOpts{})
	if before != after || !opts.OmitBrowserBridge || !slices.Equal(opts.ProtectedWriteTargets, managed.protectedTargets(cfg)) {
		t.Fatalf("sandbox contract changed: before=%s after=%s opts=%+v", before, after, opts)
	}
	// The subprocess must admit stable opt-in too, then recheck protection.
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return errors.New("startup protection checked")
	}
	if err := RunConversationBroker("dev", context.Background(), cfg, c.SessionID, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "startup protection checked") {
		t.Fatalf("stable broker did not reach its protection check: %v", err)
	}
	// Revocation is checked by the dispatcher on every call, including reads.
	if err := os.WriteFile(filepath.Join(cfg.Dir, "wing.yaml"), []byte("conversations: disabled\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managed.server("dev", cfg, NewMCPAdmissionState()); err == nil {
		t.Fatal("stable broker survived revocation of the opt-in")
	}
	// A disabled stable launch retains the historical refusal exactly.
	want := fmt.Sprintf("parent MCP cannot write isolated Wingthing state %q under the existing sandbox policy; use an already writable workspace containing that state directory (no mounts or grants were changed)", cfg.Dir)
	if _, _, err := launcher.prepareBoundParentLaunch(c, policy, nil); err == nil || err.Error() != want || len(started) != 1 {
		t.Fatalf("disabled stable behavior changed: %v", err)
	}
	if err := os.Remove(filepath.Join(cfg.Dir, "wing.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := launcher.prepareBoundParentLaunch(c, policy, nil); err == nil || err.Error() != want || len(started) != 1 {
		t.Fatalf("default stable behavior changed: %v", err)
	}
	// A writable-state launch keeps identical direct configuration and argv,
	// whether or not stable has opted in.
	direct := &store.Conversation{ID: "direct", CWD: filepath.Dir(cfg.Dir), Agent: "claude"}
	directArgs, reg, err := launcher.prepareBoundParentLaunch(direct, policy, nil)
	if err != nil || reg != nil {
		t.Fatalf("default direct launch: %+v %v", reg, err)
	}
	directData, err := os.ReadFile(directArgs[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Conversations: config.ConversationsEnabled}); err != nil {
		t.Fatal(err)
	}
	optedArgs, reg, err := launcher.prepareBoundParentLaunch(direct, policy, nil)
	if err != nil || reg != nil || !slices.Equal(directArgs, optedArgs) || len(started) != 1 {
		t.Fatalf("opt-in changed direct launch: args=%v reg=%+v err=%v", optedArgs, reg, err)
	}
	optedData, err := os.ReadFile(optedArgs[1])
	if err != nil || !bytes.Equal(directData, optedData) {
		t.Fatalf("opt-in changed direct configuration: %s %v", optedData, err)
	}
	for _, identity := range []eggclient.EggIdentity{{UserID: "user", OrgWing: true}, {UserID: "user", SharedHost: true}} {
		launcher.identity = identity
		if _, _, err := launcher.prepareBoundParentLaunch(c, policy, nil); err == nil || !strings.Contains(err.Error(), "only for personal wings") || len(started) != 1 {
			t.Fatalf("nonpersonal stable wing admitted: identity=%+v err=%v", identity, err)
		}
	}
}

func TestStableHostMailboxUsesNormalProviderHomeWriteProtection(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	// Stable has no preview provider-home binding: its ordinary HOME remains
	// write-denied except for the provider profile and configured workspace.
	// Model HOME beside TMPDIR so a caller's scratch HOME does not become an
	// intentional temporary-directory write grant in this protection test.
	base := t.TempDir()
	home, tmp := filepath.Join(base, "home"), filepath.Join(base, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	cfg := &config.Config{Dir: filepath.Join(home, ".wingthing-phone")}
	if err := brokerProviderHomeOutsideState(cfg); err != nil {
		t.Fatalf("stable incorrectly checked the preview data home: %v", err)
	}
	if runtime.GOOS != "darwin" {
		t.Skip("write protection is modeled only for macOS")
	}
	workspace := wingpolicy.CanonicalPolicyPath(t.TempDir())
	model, err := modelProviderWrites(cfg, &egg.EggConfig{FS: []string{"ro:/", "rw:./"}}, "claude", workspace, "parent-exec", eggclient.EggIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if _, writable := model.writable(cfg.Dir); writable {
		t.Fatal("stable state became writable")
	}
	for _, exposed := range []string{workspace, filepath.Join(home, ".claude-phone-state"), os.TempDir()} {
		if err := model.verifyProtected(exposed, nil); err == nil || !strings.Contains(err.Error(), "provider-writable") {
			t.Fatalf("writable stable state %s accepted: %v", exposed, err)
		}
	}
}

func TestHostMailboxChildLaunchCarriesContract(t *testing.T) {
	cfg := &config.Config{Dir: wingpolicy.CanonicalPolicyPath(t.TempDir())}
	reg := &conversationBrokerRegistration{SessionID: "parent-exec", Executable: "/opt/wt/bin/wt"}
	opts := reg.launchOpts(cfg, eggclient.SpawnEggOpts{Kind: "agent", Principal: "owner"})
	if !opts.OmitBrowserBridge || !slices.Equal(opts.ProtectedWriteTargets, []string{cfg.Dir, wingpolicy.CanonicalPolicyPath(reg.Executable)}) || opts.Principal != "owner" {
		t.Fatalf("child launch options %+v", opts)
	}
	args, err := eggclient.ProtectedWriteTargetArgs(opts.ProtectedWriteTargets)
	if err != nil || len(args) != 2 {
		t.Fatalf("protected argv %v %v", args, err)
	}
}
