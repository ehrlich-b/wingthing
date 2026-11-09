package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// Use the existing broker forward and run-backend seams: no listener, provider,
// detached broker or installed wt is needed. Cancelling a broker call models
// the parent-disconnect boundary; actual parent PID polling is a built gate.
func parentLifecycleWing(t *testing.T) *runWingFixture {
	t.Helper()
	scratch := testprovider.TempDir(t)
	// Match production's physical workspace grants even when the host TMPDIR
	// is a symlink (for example, /var -> /private/var on macOS).
	home := wingpolicy.CanonicalPolicyPath(testsocket.Dir(t))
	t.Setenv("HOME", home)
	cfg := &config.Config{Dir: testprovider.ShortStateDir(t, scratch), WingID: "offline-wing", DefaultAgent: "claude"}
	wc := &config.WingConfig{WingID: cfg.WingID, Paths: config.PathList{{Path: home}}}
	if err := config.SaveWingConfig(cfg.Dir, wc); err != nil {
		t.Fatal(err)
	}
	service := &wingsession.Service{Config: cfg, Home: home, Policy: func() wingsession.Policy {
		return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()}
	}, Register: func(string) error { return nil }}
	s, err := resolveLocalWingClient("test", service, "fixture-owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	s.Logs = io.Discard
	oldProtection := conversationBrokerProtection
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return nil
	}
	t.Cleanup(func() { conversationBrokerProtection = oldProtection })
	return newRunWingFixture(t, s)
}

func parentLifecycleBroker(t *testing.T, f *runWingFixture, reg conversationBrokerRegistration) brokerFixture {
	t.Helper()
	dir := filepath.Join(reg.Workspace, reg.Mailbox)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	mailbox, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mailbox.Close() })
	epoch, err := newMailboxID()
	if err != nil {
		t.Fatal(err)
	}
	admission := NewMCPAdmissionState()
	admission.Sessions = f.service
	m := &wingMailboxes{version: "test", owner: "fixture-owner", sessions: f.service, admission: admission, remotes: &rememberedWings{wingID: f.server.Cfg.WingID}}
	b := &conversationBroker{version: "test", cfg: f.server.Cfg, reg: reg, dir: conversationBrokerDir(f.server.Cfg, reg.SessionID), epoch: epoch, provider: "fixture-parent", admission: admission, mailbox: mailbox, logs: &bytes.Buffer{}, inflight: map[string]bool{}, slots: make(chan struct{}, 2)}
	b.forward = func(ctx context.Context, call conversationMailboxCall) (localMCPResponse, error) {
		return m.call(ctx, reg, call)
	}
	b.publishReady(true, "")
	return brokerFixture{b: b, cfg: f.server.Cfg}
}

func parentLifecycleRegistration(t *testing.T, f *runWingFixture, name string) conversationBrokerRegistration {
	t.Helper()
	return scopedRegistration(t, f, map[string][]string{f.server.Cfg.WingID: {f.service.Home}}, name)
}

func parentLifecycleSubmission(t *testing.T, f *runWingFixture, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case got := <-f.submitted:
		if got != id {
			t.Fatalf("provider submitted %s, expected %s", got, id)
		}
	case <-ctx.Done():
		t.Fatal("fixture provider never received admitted input")
	}
}

func TestParentLifecycleAdmissionDisconnectBoundaries(t *testing.T) {
	for _, boundary := range []string{"before_admission", "after_admission_before_ack", "after_ack"} {
		t.Run(boundary, func(t *testing.T) {
			f := parentLifecycleWing(t)
			reg := parentLifecycleRegistration(t, f, "original-parent")
			broker := parentLifecycleBroker(t, f, reg)
			// Omit the caller key: the broker must durably bind it to the one
			// mailbox request ID before dispatch, including an interrupted ack.
			payload, _ := json.Marshal(map[string]any{"method": "tools/call", "params": map[string]any{"name": "agent_run", "arguments": map[string]any{"agent": "claude", "cwd": reg.Workspace, "prompt": "ordinary child request"}}})
			id := broker.publish(t, broker.b.epoch, time.Now(), string(payload))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			forward := broker.b.forward
			reached := make(chan map[string]any, 1)
			broker.b.forward = func(ctx context.Context, call conversationMailboxCall) (localMCPResponse, error) {
				if boundary == "before_admission" {
					reached <- nil
					<-ctx.Done()
					return localMCPResponse{}, ctx.Err()
				}
				response, err := forward(ctx, call)
				result, _ := response.Result.(map[string]any)
				receipt, _ := result["structuredContent"].(map[string]any)
				reached <- receipt
				if boundary == "after_admission_before_ack" {
					<-ctx.Done()
					return localMCPResponse{}, ctx.Err()
				}
				return response, err
			}
			finished := make(chan struct{})
			go func() { defer close(finished); broker.b.accept(ctx, id) }()
			t.Cleanup(func() { cancel(); <-finished })
			var original map[string]any
			select {
			case original = <-reached:
			case <-finished:
				select {
				case original = <-reached:
				default:
					envelope, _ := broker.response(t, id)
					t.Fatalf("dispatch never reached observation barrier: %+v", envelope)
				}
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			entry, ok := broker.b.readJournal(id)
			var call conversationMailboxCall
			var params localMCPToolCallParams
			var arguments map[string]any
			if !ok || decodeMailbox(entry.Payload, &call) != nil || json.Unmarshal(call.Params, &params) != nil || json.Unmarshal(params.Arguments, &arguments) != nil || arguments["idempotency_key"] != id {
				t.Fatalf("admission journal lost original key: %+v", entry)
			}
			if boundary != "after_ack" && entry.Phase != "dispatching" {
				t.Fatalf("journal completed before dispatch barrier: %+v", entry)
			}
			if boundary != "before_admission" && (original["run_id"] == nil || original["session_id"] == nil) {
				t.Fatalf("admission did not create a job: %v", original)
			}
			if _, err := broker.b.mailbox.Lstat(mailboxRequestName(id)); !os.IsNotExist(err) {
				t.Fatalf("journaled request was not claimed: %v", err)
			}
			t.Logf("parent=%s request=%s boundary=%s journal=%s", reg.SessionID, id, boundary, entry.Phase)
			if boundary == "after_ack" {
				<-finished
				if envelope, _ := broker.response(t, id); envelope.Outcome != brokerOutcomeCompleted {
					t.Fatalf("acknowledgement: %+v", envelope)
				}
			}
			cancel()
			<-finished
			broker.b.publishReady(false, "parent execution ended")
			if _, err := readMailboxReady(broker.b.mailbox, reg.ConversationID, reg.SessionID, time.Now()); err == nil {
				t.Fatal("ended parent still advertised a ready mailbox")
			}
			if boundary != "after_ack" {
				if envelope, _ := broker.response(t, id); envelope.Outcome != brokerOutcomeUnconfirmed {
					t.Fatalf("lost acknowledgement was called safe: %+v", envelope)
				}
			}
			if boundary == "before_admission" && (len(f.spawned) != 0 || len(f.submitted) != 0) {
				t.Fatal("child existed before admission barrier")
			}
			// Drive journal recovery explicitly, independently of the real
			// broker's parent-PID readiness gate. It must reconcile, not resend.
			reconnected := parentLifecycleBroker(t, f, reg)
			reconnected.b.reconcileJournal(t.Context())
			reconnected.b.calls.Wait()
			envelope, hosted := reconnected.response(t, id)
			receipt, _ := hosted["structuredContent"].(map[string]any)
			if envelope.Outcome != brokerOutcomeCompleted || receipt["idempotency_key"] != id || receipt["wing_id"] != f.server.Cfg.WingID {
				t.Fatalf("recovery receipt: %+v %v", envelope, receipt)
			}
			run, session := receipt["run_id"].(string), receipt["session_id"].(string)
			if original != nil && (original["run_id"] != run || original["session_id"] != session) {
				t.Fatalf("recovery replaced the original job: %v -> %v", original, receipt)
			}
			parentLifecycleSubmission(t, f, run)
			if len(f.spawned) != 1 || len(f.submitted) != 0 {
				t.Fatalf("admission effects: spawns=%d extra submissions=%d", len(f.spawned), len(f.submitted))
			}
			args := map[string]any{"run_id": run}
			if _, pending := reconnected.call(t, "agent_result", args); pending["ready"] != false {
				t.Fatalf("admission was confused with completion: %v", pending)
			}
			f.finish(t, run, "durable child result Ω", "")
			f.wait(t, run)
			_, result := reconnected.call(t, "agent_result", args)
			if result["ready"] != true || result["status"] != "done" || result["output"] != "durable child result Ω" || result["run_id"] != run || result["session_id"] != session {
				t.Fatalf("completed result: %v", result)
			}
			f.restart(t)
			recovered := parentLifecycleBroker(t, f, reg)
			_, again := recovered.call(t, "agent_result", args)
			if !reflect.DeepEqual(result, again) || len(f.spawned) != 1 || len(f.submitted) != 0 {
				t.Fatalf("durable inspection changed job or resubmitted: %v", again)
			}
			t.Logf("parent=%s request=%s run=%s session=%s spawns=1 submissions=1 terminal=%v", reg.SessionID, id, run, session, again["status"])
		})
	}
}

func TestParentLifecycleTwoOriginalParentsKeepOrdinaryMessagesScoped(t *testing.T) {
	f := parentLifecycleWing(t)
	parents := []brokerFixture{}
	receipts := []map[string]any{}
	for _, name := range []string{"first-parent", "second-parent"} {
		reg := parentLifecycleRegistration(t, f, name)
		parent := parentLifecycleBroker(t, f, reg)
		parents = append(parents, parent)
		envelope, receipt := parent.call(t, "agent_run", map[string]any{"agent": "claude", "cwd": reg.Workspace, "prompt": "ordinary message from " + name, "idempotency_key": "same-caller-key"})
		if envelope.Outcome != brokerOutcomeCompleted || receipt["idempotency_key"] != "same-caller-key" {
			t.Fatalf("parent admission: %+v %v", envelope, receipt)
		}
		receipts = append(receipts, receipt)
		id := receipt["run_id"].(string)
		parentLifecycleSubmission(t, f, id)
		f.mu.Lock()
		child := f.eggs[id]
		f.mu.Unlock()
		child.mu.Lock()
		prompt := child.prompt
		child.mu.Unlock()
		if prompt != "ordinary message from "+name {
			t.Fatalf("ordinary message crossed parent jobs: %s", prompt)
		}
		f.finish(t, id, "reply for "+name, "")
		f.wait(t, id)
	}
	if receipts[0]["run_id"] == receipts[1]["run_id"] || receipts[0]["session_id"] == receipts[1]["session_id"] {
		t.Fatal("same caller key crossed original parent identities")
	}
	for i, parent := range parents {
		_, own := parent.call(t, "agent_result", map[string]any{"run_id": receipts[i]["run_id"]})
		if own["ready"] != true || own["output"] != "reply for "+[]string{"first-parent", "second-parent"}[i] {
			t.Fatalf("wrong parent's result: %v", own)
		}
		_, foreign := parent.call(t, "agent_result", map[string]any{"run_id": receipts[1-i]["run_id"]})
		if foreign["ready"] == true || foreign["error"] == nil {
			t.Fatalf("foreign parent claimed a child: %v", foreign)
		}
		children, err := mailboxChildren(f.server.Cfg, &parent.b.reg, nil)
		if err != nil || len(children) != 2 || children[childKey(f.server.Cfg.WingID, receipts[i]["run_id"].(string))].SessionID != receipts[i]["session_id"] {
			t.Fatalf("parent ledger: %v %v", children, err)
		}
	}
	if len(f.spawned) != 2 || len(f.submitted) != 0 {
		t.Fatalf("two parents produced spawns=%d extra submissions=%d", len(f.spawned), len(f.submitted))
	}
}

func TestParentLifecycleMailboxReadinessLoss(t *testing.T) {
	for _, boundary := range []string{"before_publication", "after_publication", "response_claimed"} {
		t.Run(boundary, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir, root := openTestMailbox(t)
				epoch, _ := newMailboxID()
				if boundary != "before_publication" {
					publishTestReady(t, root, "original-parent", "original-exec", epoch, true)
				}
				client := conversationMailboxClient{dir: dir, conversation: "original-parent", session: "original-exec"}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := client.exchange(ctx, conversationMailboxCall{Method: "ping"})
					done <- err
				}()
				// The client is blocked on its next protocol event. No elapsed
				// wall time or stronger timeout substitutes for this barrier.
				synctest.Wait()
				names := requestArtifacts(t, root)
				wantRequests := 1
				if boundary == "before_publication" {
					wantRequests = 0
				}
				if len(names) != wantRequests {
					t.Fatalf("publication barrier: got %v, want %d requests", names, wantRequests)
				}
				if boundary == "response_claimed" {
					data, err := mailboxRead(root, names[0], conversationMailboxRequestBytes)
					var request conversationMailboxRequest
					if err != nil || decodeMailbox(data, &request) != nil {
						t.Fatalf("published request: %s %v", data, err)
					}
					if err := root.Remove(names[0]); err != nil {
						t.Fatal(err)
					}
					response := conversationMailboxResponse{Version: 1, ID: request.ID, Epoch: epoch, ConversationID: client.conversation, SessionID: client.session, Outcome: brokerOutcomeCompleted, Dispatched: true, Payload: json.RawMessage(`{"result":{}}`)}
					if err := mailboxWrite(root, mailboxResponseName(request.ID), response, conversationMailboxResponseBytes); err != nil {
						t.Fatal(err)
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if _, err := root.Lstat(mailboxResponseName(request.ID)); !os.IsNotExist(err) {
						t.Fatalf("acknowledged response was not claimed: %v", err)
					}
					return
				}
				publishTestReady(t, root, client.conversation, client.session, epoch, false)
				cancel()
				err := <-done
				var callErr *mailboxCallError
				if !errors.As(err, &callErr) {
					t.Fatalf("lost readiness discarded dispatch state: %v", err)
				}
				wantOutcome, wantDispatched := brokerOutcomeNotDispatched, "no"
				if boundary == "after_publication" {
					wantOutcome, wantDispatched = brokerOutcomeUnconfirmed, "unknown"
				}
				if callErr.outcome != wantOutcome || callErr.dispatched != wantDispatched || !reflect.DeepEqual(names, requestArtifacts(t, root)) {
					t.Fatalf("readiness loss republished or misreported a request: %v", callErr)
				}
			})
		})
	}
}
