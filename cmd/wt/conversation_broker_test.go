package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ehrlich-b/wingthing/internal/store"
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
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	child := fixtureConversation(t, db, cfg, "child", root.ID, "owner", "completed")
	other := fixtureConversation(t, db, cfg, "other", "", "owner", "idle")
	workspace := canonicalPolicyPath(t.TempDir())
	snapshot, err := brokerChildPolicySnapshot(egg.DefaultEggConfig(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	reg := conversationBrokerRegistration{
		Version: 1, StateDir: canonicalPolicyPath(cfg.Dir), ConversationID: root.ID, RootID: root.RootID, SessionID: root.SessionID,
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
	b := &conversationBroker{cfg: cfg, reg: loaded, dir: conversationBrokerDir(cfg, root.SessionID), epoch: epoch, provider: "provider-root", admission: newMCPAdmissionState(), mailbox: mailbox, logs: &bytes.Buffer{}, inflight: map[string]bool{}, slots: make(chan struct{}, 8)}
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
	direct := &localMCPServer{cfg: f.cfg, principal: "owner", boundConversation: f.root.ID}
	if err := direct.checkBoundSessionTarget("session_status", json.RawMessage(`{"session":"`+f.other.SessionID+`"}`)); err != nil {
		t.Fatalf("direct bound connection gained broker tree rules: %v", err)
	}
	broker, _, err := f.b.reg.server(f.cfg, f.b.admission)
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
	first, _, err := f.b.reg.server(f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	resumed := f.b.reg
	resumed.SessionID = f.root.SessionID + "-resumed"
	second, _, err := resumed.server(f.cfg, f.b.admission)
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

func TestHostMailboxReintersectsCurrentWingPathsPerCall(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceHTTPMCP, "browser")
	workspace := f.b.reg.Workspace
	f.b.reg.AllowedPaths, f.b.reg.EnforcePathBounds = []string{workspace}, true
	server, _, err := f.b.reg.server(f.cfg, f.b.admission)
	if err != nil || !server.enforcePathBounds || !slices.Equal(server.allowedPaths, []string{workspace}) {
		t.Fatalf("captured bound %v %v", server.allowedPaths, err)
	}
	narrow := filepath.Join(workspace, "project")
	if err := os.WriteFile(filepath.Join(f.cfg.Dir, "wing.yaml"), []byte("paths:\n  - "+narrow+"\n  - "+t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if server, _, err = f.b.reg.server(f.cfg, f.b.admission); err != nil || !slices.Equal(server.allowedPaths, []string{narrow}) {
		t.Fatalf("current wing paths did not narrow the captured bound: %v %v", server.allowedPaths, err)
	}
	f.b.reg.AllowedPaths, f.b.reg.EnforcePathBounds = nil, false
	if server, _, err = f.b.reg.server(f.cfg, f.b.admission); err != nil || !server.enforcePathBounds || len(server.allowedPaths) != 2 {
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
	if err := writeSessionPrincipal(unlinked, "owner"); err != nil {
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
		"stale epoch":    func() string { other, _ := newMailboxID(); return f.publish(t, other, time.Now(), `{"method":"ping"}`) },
		"expired":        func() string { return f.publish(t, f.b.epoch, time.Now().Add(-time.Hour), `{"method":"ping"}`) },
		"forged owner":   func() string { return f.publish(t, f.b.epoch, time.Now(), `{"method":"ping","principal":"someone-else"}`) },
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

func TestHostMailboxPolicyIntersectsCurrentClientsAndWing(t *testing.T) {
	f := newBrokerFixture(t, control.SurfaceLocalMCP, "codex")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.cfg.Dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server, _, err := f.b.reg.server(f.cfg, f.b.admission)
	if err != nil || len(server.tools) != len(conversationBrokerTools) || server.maxSessions != 8 || server.maxSpawnsPerHour != 60 {
		t.Fatalf("captured ceiling %+v %v", server, err)
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, capabilities.read]\n    bounds:\n      max_sessions: 2\n")
	server, _, err = f.b.reg.server(f.cfg, f.b.admission)
	if err != nil || server.tools["agent_start"] || server.tools["session_prompt"] || server.tools["conversation_checkpoint"] || !server.tools["session_read"] || server.maxSessions != 2 || server.maxSpawnsPerHour != 60 {
		t.Fatalf("narrowed policy not applied: %+v %v", server.tools, err)
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, terminal.send, terminal.start, capabilities.read, agent.run]\n    bounds:\n      max_sessions: 100\n")
	if server, _, _ = f.b.reg.server(f.cfg, f.b.admission); server.maxSessions != 8 || len(server.tools) != len(conversationBrokerTools) {
		t.Fatalf("current policy widened the captured ceiling: %d %v", server.maxSessions, server.tools)
	}
	for name, content := range map[string]string{
		"revoked launcher": "clients:\n  someone:\n    owner: owner\n",
		"changed owner":    "clients:\n  codex:\n    owner: another-owner\n",
	} {
		write("clients.yaml", content)
		if _, _, err := f.b.reg.server(f.cfg, f.b.admission); err == nil {
			t.Fatalf("%s still dispatches", name)
		}
	}
	write("clients.yaml", "clients:\n  codex:\n    owner: owner\n    grants: [terminal.read, terminal.send, terminal.start, capabilities.read]\n")
	write("wing.yaml", "locked: true\n")
	envelope, _ := f.call(t, "conversation_checkpoint", map[string]any{"conversation_id": f.root.ID, "expected_revision": 0, "after_cursor": 0, "checkpoint": "locked"})
	if envelope.Dispatched || !strings.Contains(envelope.Error, "locked") {
		t.Fatalf("locked wing accepted a mutation: %+v", envelope)
	}
	if _, structured := f.call(t, "conversation_list", map[string]any{}); structured["conversations"] == nil {
		t.Fatalf("locked wing blocked a read: %v", structured)
	}
	write("wing.yaml", "org: team\n")
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
	server, _, err := f.b.reg.server(f.cfg, f.b.admission)
	if err != nil {
		t.Fatal(err)
	}
	refused := false
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, EggIdentity, []string) error {
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
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, EggIdentity, []string) error { return nil }
	t.Cleanup(func() { conversationBrokerProtection = defaultConversationBrokerProtection })
	f.b.reg.MaxSpawnsPerHour = 3
	server, _, err := f.b.reg.server(f.cfg, newMCPAdmissionState())
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
	workspace := canonicalPolicyPath(t.TempDir())
	direct := &localMCPServer{cfg: &config.Config{Dir: filepath.Join(workspace, "state")}, principal: "owner"}
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
	exposed := &localMCPServer{cfg: &config.Config{Dir: t.TempDir()}, principal: "owner"}
	var protectedTargets []string
	conversationBrokerProtection = func(_ *config.Config, _ *egg.EggConfig, _, _, _ string, _ EggIdentity, targets []string) error {
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
	stableRefusal := `parent MCP cannot write isolated Wingthing state "` + exposed.cfg.Dir + `" under the existing sandbox policy; use an already writable workspace containing that state directory (no mounts or grants were changed)`
	if _, err := exposed.prepareBoundParentMCP(c, narrow(), nil); err == nil || err.Error() != stableRefusal || protectedTargets != nil {
		t.Fatalf("stable layout changed: %v", err)
	}
	if err := runConversationBroker(context.Background(), exposed.cfg, c.SessionID, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "preview channel") {
		t.Fatalf("stable binary served a broker: %v", err)
	}
	config.ReleaseChannel = "preview"
	if _, err := exposed.prepareBoundParentMCP(c, narrow(), nil); err == nil || !strings.Contains(err.Error(), "host mailbox unavailable: modeled provider-writable state") {
		t.Fatalf("exposed state was not refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exposed.cfg.Dir, conversationBrokersDir)); !os.IsNotExist(err) || len(started) != 0 {
		t.Fatal("refused layout left a registration or started a broker")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The whole canonical state directory and the resolved controller, not a
	// list of individual state files.
	if want := []string{canonicalPolicyPath(exposed.cfg.Dir), canonicalPolicyPath(executable)}; !slices.Equal(protectedTargets, want) {
		t.Fatalf("protected targets %v, want %v", protectedTargets, want)
	}
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, EggIdentity, []string) error { return nil }
	// A resumed browser parent cannot carry the launch contract and never
	// registers a broker.
	resumed := &localMCPServer{cfg: exposed.cfg, principal: "owner", hostMailboxUnavailable: "resume refused"}
	if _, err := resumed.prepareBoundParentMCP(c, narrow(), nil); err == nil || !strings.Contains(err.Error(), "host mailbox unavailable: resume refused") || len(started) != 0 {
		t.Fatalf("resume selected the host mailbox: %v", err)
	}
	launcher := &localMCPServer{cfg: exposed.cfg, principal: roostSessionPrincipal("user"), actor: "browser", surface: control.SurfaceHTTPMCP, identity: EggIdentity{UserID: "user", Email: "user@example.invalid"}}
	args, managed, err := launcher.prepareBoundParentLaunch(c, narrow(), []string{"--model", "selected"})
	if err != nil {
		t.Fatal(err)
	}
	opts := managed.launchOpts(exposed.cfg, spawnEggOpts{Label: "parent", Kind: "agent"})
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
	if reg.Principal != launcher.principal || reg.LauncherActor != "browser" || reg.UserID != "user" || reg.MaxSessions != defaultDirectMCPMaxSessions || reg.MaxSpawnsPerHour != defaultDirectMCPMaxSpawnsPerHour || !slices.Equal(reg.Tools, conversationBrokerTools) {
		t.Fatalf("nil-grant launcher must receive the finite bridge ceiling: %+v", reg)
	}
	loaded, err := loadConversationBrokerRegistration(exposed.cfg, c.SessionID)
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
	limited := &localMCPServer{cfg: exposed.cfg, principal: "owner", grants: grantSet([]string{"terminal.read"}), maxSessions: 2, maxSpawnsPerHour: 5}
	second := &store.Conversation{ID: "parent", RootID: "parent", SessionID: "parent-exec-2", CWD: workspace, Agent: "claude"}
	if _, err := limited.prepareBoundParentMCP(second, narrow(), nil); err != nil {
		t.Fatal(err)
	}
	if reg := started[len(started)-1]; slices.Contains(reg.Tools, "agent_start") || !slices.Contains(reg.Tools, "session_read") || reg.MaxSessions != 2 || reg.MaxSpawnsPerHour != 5 {
		t.Fatalf("launcher limits were not preserved: %+v", reg)
	}
	if err := os.WriteFile(filepath.Join(exposed.cfg.Dir, "wing.yaml"), []byte("org: team\n"), 0o600); err != nil {
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
	base := canonicalPolicyPath(t.TempDir())
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
	model, err := modelProviderWrites(cfg, egg.DefaultEggConfig(), "claude", workspace, "parent-exec", EggIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]bool{
		filepath.Join(home, ".wingthing", "wt.db"):                    false,
		filepath.Join(home, ".wingthing", "conversation-brokers"):     false,
		filepath.Join(home, ".claude", "projects", "x.jsonl"):          true,
		filepath.Join(home, ".claude.json"):                           true,
		filepath.Join(home, ".cache", "go-build"):                     true,
		filepath.Join(home, "Library", "Keychains", "login.keychain"): true,
		filepath.Join(tmp, "anything"):                                true,
		filepath.Join(workspace, ".wingthing-conversations"):          true,
		// Broker-managed eggs omit the browser bridge mount.
		filepath.Join(home, ".wingthing", "eggs", "parent-exec", "browser-requests"): false,
		"/private/tmp/x":   true,
		"/usr/local/x":     true,
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
	if _, err := modelProviderWrites(cfg, egg.UnsandboxedEggConfig(), "claude", workspace, "parent-exec", EggIdentity{}); err == nil {
		t.Fatal("outer-boundary session modeled as protected")
	}
}

func TestHostMailboxRequiresProviderDataHomeOutsideState(t *testing.T) {
	state := canonicalPolicyPath(t.TempDir())
	// The default preview provider home lies inside the state directory.
	inside := &config.Config{Dir: state}
	if err := brokerProviderHomeOutsideState(inside); err == nil || !strings.Contains(err.Error(), "overlaps protected state") {
		t.Fatalf("provider data home inside state accepted: %v", err)
	}
	if err := defaultConversationBrokerProtection(inside, egg.DefaultEggConfig(), "claude", state, "parent-exec", EggIdentity{}, nil); err == nil || !strings.Contains(err.Error(), "overlaps protected state") {
		t.Fatalf("protection preflight ignored the provider data home: %v", err)
	}
}

func TestHostMailboxChildLaunchCarriesContract(t *testing.T) {
	cfg := &config.Config{Dir: canonicalPolicyPath(t.TempDir())}
	reg := &conversationBrokerRegistration{SessionID: "parent-exec", Executable: "/opt/wt/bin/wt"}
	opts := reg.launchOpts(cfg, spawnEggOpts{Kind: "agent", Principal: "owner"})
	if !opts.OmitBrowserBridge || !slices.Equal(opts.ProtectedWriteTargets, []string{cfg.Dir, canonicalPolicyPath(reg.Executable)}) || opts.Principal != "owner" {
		t.Fatalf("child launch options %+v", opts)
	}
	args, err := protectedWriteTargetArgs(opts.ProtectedWriteTargets)
	if err != nil || len(args) != 2 {
		t.Fatalf("protected argv %v %v", args, err)
	}
	cmd := eggRunCmd()
	if err := cmd.ParseFlags(append([]string{"--session-id", "s1", "--" + omitBrowserBridgeArg}, args...)); err != nil {
		t.Fatal(err)
	}
	if omitted, err := cmd.Flags().GetBool(omitBrowserBridgeArg); err != nil || !omitted {
		t.Fatalf("omit-browser-bridge did not round trip: %v", err)
	}
	if flag := cmd.Flags().Lookup(omitBrowserBridgeArg); flag == nil || !flag.Hidden {
		t.Fatal("omit-browser-bridge must be a hidden internal flag")
	}
	plain := eggRunCmd()
	if err := plain.ParseFlags([]string{"--session-id", "s1"}); err != nil {
		t.Fatal(err)
	}
	if omitted, _ := plain.Flags().GetBool(omitBrowserBridgeArg); omitted {
		t.Fatal("ordinary egg runs must keep the browser bridge")
	}
}
