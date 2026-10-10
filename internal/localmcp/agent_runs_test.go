package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

type fixtureRunEgg struct {
	mu     sync.Mutex
	result egg.RunTurnResult
	done   chan struct{}
	prompt string
}
type runWingFixture struct {
	server    *Server
	service   *wingsession.Service
	mu        sync.Mutex
	eggs      map[string]*fixtureRunEgg
	submitted chan string
	spawned   chan wingsession.StartOptions
	launches  chan *wingsession.Launch
}

func newRunWingFixture(t *testing.T, s *Server) *runWingFixture {
	t.Helper()
	if s == nil {
		s = &Server{Version: "test", Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Principal: "owner", Logs: io.Discard}
	}
	s = testWingServer(t, s)
	if s.Sessions.Register == nil {
		s.Sessions.Register = func(string) error { return nil }
	}
	f := &runWingFixture{server: s, service: s.Sessions, eggs: map[string]*fixtureRunEgg{}, submitted: make(chan string, 64), spawned: make(chan wingsession.StartOptions, 64), launches: make(chan *wingsession.Launch, 64)}
	f.service.Spawn = func(l *wingsession.Launch, o wingsession.StartOptions) (*egg.Client, error) {
		dir := filepath.Join(s.Cfg.Dir, "eggs", o.SessionID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := eggclient.WriteEggOwner(dir, l.Identity.UserID, ""); err != nil {
			return nil, err
		}
		if err := eggclient.WriteSessionPrincipal(dir, o.Egg.Principal); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("kind=agent\nagent="+o.Agent+"\ncwd="+l.CWD+"\nnative_run=true\n"), 0600); err != nil {
			return nil, err
		}
		f.spawned <- o
		f.launches <- l
		return nil, nil
	}
	lookup := func(id string) (*fixtureRunEgg, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		e := f.eggs[id]
		if e == nil {
			return nil, os.ErrNotExist
		}
		return e, nil
	}
	read := func(_ context.Context, _ *config.Config, _ eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
		e, err := lookup(id)
		if err != nil {
			return egg.RunTurnResult{}, err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.result, nil
	}
	f.service.RunBackend = &wingsession.RunBackend{
		Ready: func(context.Context, *config.Config, eggclient.LocalSession) error { return nil },
		Submit: func(ctx context.Context, _ *config.Config, sess eggclient.LocalSession, r egg.RunTurnRequest) (egg.RunTurnResult, error) {
			result := egg.RunTurnResult{RunID: r.RunID, SessionID: sess.ID, Status: "running", StartedAt: time.Now().UTC(), Deadline: r.Deadline, ProviderSessionID: "provider-" + sess.ID, TurnID: "turn-" + r.RunID}
			f.mu.Lock()
			if f.eggs[r.RunID] != nil {
				f.mu.Unlock()
				return egg.RunTurnResult{}, fmt.Errorf("duplicate input for %s", r.RunID)
			}
			f.eggs[r.RunID] = &fixtureRunEgg{result: result, done: make(chan struct{}), prompt: r.Prompt}
			f.mu.Unlock()
			f.submitted <- r.RunID
			return result, nil
		}, Status: read, Result: read,
		Wait: func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
			e, err := lookup(id)
			if err != nil {
				return egg.RunTurnResult{}, err
			}
			select {
			case <-ctx.Done():
				return egg.RunTurnResult{}, ctx.Err()
			case <-e.done:
				return read(ctx, cfg, sess, id)
			}
		},
		Stop: func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
			e, err := lookup(id)
			if err != nil {
				return egg.RunTurnResult{}, err
			}
			e.mu.Lock()
			if !e.result.Terminal() {
				e.result.Status = "stopped"
				e.result.FailureKind = agent.Stopped
				e.result.Error = "Egg run turn ended: stopped."
				e.result.EndedAt = time.Now().UTC()
				close(e.done)
			}
			e.mu.Unlock()
			return read(ctx, cfg, sess, id)
		},
	}
	if err := f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.service.RunManager != nil {
			if err := f.service.RunManager.Close(); err != nil {
				t.Error(err)
			}
			f.service.RunManager = nil
		}
	})
	return f
}

func (f *runWingFixture) finish(t *testing.T, id, text string, kind agent.ErrorKind) {
	t.Helper()
	f.mu.Lock()
	e := f.eggs[id]
	f.mu.Unlock()
	if e == nil {
		t.Fatal("egg has not received prompt")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.result.Terminal() {
		return
	}
	e.result.Status = "done"
	e.result.Text = text
	e.result.EndedAt = time.Now().UTC()
	if kind != "" {
		e.result.Status = "failed"
		if kind == agent.Timeout {
			e.result.Status = "timeout"
		}
		if kind == agent.Stopped {
			e.result.Status = "stopped"
		}
		e.result.FailureKind = kind
		e.result.Error = "Egg run turn ended: " + string(kind) + "."
	}
	close(e.done)
}
func runArgs(id string) json.RawMessage { return json.RawMessage(`{"run_id":"` + id + `"}`) }
func (f *runWingFixture) admit(t *testing.T, prompt string) string {
	t.Helper()
	wire, _ := json.Marshal(map[string]any{"prompt": prompt, "agent": "claude", "cwd": f.server.Cfg.Dir, "model": "opus"})
	data, err := f.server.toolAgentRun(wire)
	if err != nil {
		t.Fatal(err)
	}
	return data["run_id"].(string)
}
func (f *runWingFixture) wait(t *testing.T, id string) {
	t.Helper()
	if err := f.service.RunManager.Wait(t.Context(), f.server.sessionAuthority(), []string{id}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRunTimeoutContract(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout *int
		wantErr bool
	}{
		{name: "absent"},
		{name: "zero", timeout: new(0)},
		{name: "one day", timeout: new(86400)},
		{name: "minimum", timeout: new(10)},
		{name: "largest integer", timeout: new(int(^uint(0) >> 1))},
		{name: "too short", timeout: new(5), wantErr: true},
		{name: "negative", timeout: new(-1), wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newRunWingFixture(t, nil)
			ready := make(chan bool, 1)
			f.service.RunBackend.Ready = func(ctx context.Context, _ *config.Config, _ eggclient.LocalSession) error {
				_, bounded := ctx.Deadline()
				ready <- bounded
				return ctx.Err()
			}
			spawn := f.service.Spawn
			f.service.Spawn = func(l *wingsession.Launch, o wingsession.StartOptions) (*egg.Client, error) {
				client, err := spawn(l, o)
				if err != nil {
					return client, err
				}
				path := filepath.Join(f.server.Cfg.Dir, "eggs", o.SessionID, "egg.meta")
				meta, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return client, err
				}
				defer meta.Close()
				_, err = fmt.Fprintf(meta, "started_at_nanos=%d\n", time.Now().UnixNano())
				return client, err
			}
			f.restart(t)
			args := map[string]any{"prompt": "timeout contract", "agent": "claude", "cwd": f.server.Cfg.Dir}
			if tt.timeout != nil {
				args["timeout_seconds"] = *tt.timeout
			}
			wire, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			data, err := f.server.toolAgentRun(wire)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
					t.Fatalf("invalid timeout: %v %v", data, err)
				}
				select {
				case <-f.spawned:
					t.Fatal("invalid timeout spawned an egg")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			id := data["run_id"].(string)
			if got := <-f.submitted; got != id {
				t.Fatalf("submitted %q, want %q", got, id)
			}
			unbounded := tt.timeout == nil || *tt.timeout == 0
			if bounded := <-ready; bounded == unbounded {
				t.Fatalf("readiness context bounded=%t, unbounded run=%t", bounded, unbounded)
			}
			run, err := f.service.RunManager.Get(f.server.sessionAuthority(), id)
			if err != nil {
				t.Fatal(err)
			}
			wantTimeout := 0
			if tt.timeout != nil {
				wantTimeout = *tt.timeout
			}
			if run.TimeoutSeconds != wantTimeout || run.Result.Deadline.IsZero() != unbounded {
				t.Fatalf("timeout=%d deadline=%v, want timeout=%d", run.TimeoutSeconds, run.Result.Deadline, wantTimeout)
			}
			if !unbounded && !run.Result.Deadline.After(time.Now()) {
				t.Fatalf("positive timeout wrapped into the past: %v", run.Result.Deadline)
			}
			if wantTimeout == 86400 && time.Until(run.Result.Deadline) < 23*time.Hour {
				t.Fatalf("one-day deadline was capped: %v", run.Result.Deadline)
			}
			status, err := f.server.toolAgentStatus(runArgs(id))
			if err != nil || unbounded && status["deadline"] != "no deadline" || !unbounded && status["deadline"] != run.Result.Deadline.UTC().Format(time.RFC3339) {
				t.Fatalf("deadline status: %v %v", status, err)
			}
			// The persisted zero deadline survives a wing restart and remains stoppable.
			f.restart(t)
			stopped, err := f.server.toolAgentStop(runArgs(id))
			if err != nil || stopped["status"] != "stopped" || stopped["failure_kind"] != agent.Stopped {
				t.Fatalf("stop: %v %v", stopped, err)
			}
		})
	}
}

func TestAgentRunResultShapeParity(t *testing.T) {
	for _, kind := range []agent.ErrorKind{"", agent.AuthFailed, agent.RateLimited, agent.UnknownOutcome, agent.Timeout, agent.Stopped} {
		t.Run(string(kind), func(t *testing.T) {
			f := newRunWingFixture(t, nil)
			id := f.admit(t, "fixture request")
			if <-f.submitted != id {
				t.Fatal("wrong run")
			}
			before, err := f.server.toolAgentResult(runArgs(id))
			if err != nil || before["ready"] != false {
				t.Fatalf("before: %v %v", before, err)
			}
			text := strings.Repeat("AΩ🙂漢", 60000)
			f.finish(t, id, text, kind)
			f.wait(t, id)
			data, err := f.server.toolAgentResult(json.RawMessage(`{"run_id":"` + id + `","max_chars":3}`))
			if err != nil {
				t.Fatal(err)
			}
			r, err := f.service.RunManager.Get(f.server.sessionAuthority(), id)
			if err != nil {
				t.Fatal(err)
			}
			task := &store.Task{ID: id, Status: r.Result.Status, Agent: r.Agent, Model: r.Model, CWD: r.CWD, Isolation: r.Isolation, TimeoutSeconds: r.TimeoutSeconds, CreatedAt: r.CreatedAt, StartedAt: &r.Result.StartedAt, FinishedAt: &r.Result.EndedAt}
			want := agentRunStatusData(task)
			want["ready"] = true
			want["output"] = "AΩ🙂"
			want["truncated"] = true
			want["total_chars"] = 240000
			if kind != "" {
				want["error"] = r.Result.Error
			}
			for k, v := range want {
				if !reflect.DeepEqual(data[k], v) {
					t.Fatalf("field %s: %#v want %#v", k, data[k], v)
				}
			}
			if r.Result.Text != text || data["session_id"] != r.SessionID || data["turn_id"] == "" {
				t.Fatal("lost untruncated artifact or correlation")
			}
			if _, err := f.server.toolAgentResult(json.RawMessage(`{"run_id":"` + id + `","max_chars":200001}`)); err == nil {
				t.Fatal("unbounded result")
			}
			stranger := &Server{Version: f.server.Version, Cfg: f.server.Cfg, Sessions: f.service, Principal: "foreign", identity: f.server.identity, sessionRole: f.server.sessionRole}
			if _, err := stranger.toolAgentResult(runArgs(id)); err == nil {
				t.Fatal("foreign result disclosed")
			}
		})
	}
}

func TestWingRunAdmissionRetryKeepsEggAndModel(t *testing.T) {
	f := newRunWingFixture(t, nil)
	wire, _ := json.Marshal(map[string]any{"prompt": "request", "agent": "claude", "model": "opus", "cwd": f.server.Cfg.Dir, "idempotency_key": "retry"})
	a, err := f.server.toolAgentRun(wire)
	if err != nil {
		t.Fatal(err)
	}
	id := <-f.submitted
	b, err := f.server.toolAgentRun(wire)
	if err != nil || a["run_id"] != b["run_id"] || a["session_id"] != b["session_id"] {
		t.Fatalf("retry: %v %v", b, err)
	}
	opts := <-f.spawned
	if !reflect.DeepEqual(opts.Egg.AgentArgs, []string{"--model", "opus"}) {
		t.Fatalf("model argv: %v", opts.Egg.AgentArgs)
	}
	f.finish(t, id, "done", "")
	f.wait(t, id)
}

func TestRunAdmissionRetryCannotCrossOwnerBinding(t *testing.T) {
	f := newRunWingFixture(t, nil)
	wire, _ := json.Marshal(map[string]any{"prompt": "private request", "agent": "claude", "cwd": f.server.Cfg.Dir, "idempotency_key": "private-key"})
	if _, err := f.server.toolAgentRun(wire); err != nil {
		t.Fatal(err)
	}
	id := <-f.submitted
	other := &Server{Version: f.server.Version, Cfg: f.server.Cfg, Sessions: f.service, Principal: f.server.Principal, identity: f.server.identity, sessionRole: f.server.sessionRole}
	other.identity.UserID = "another-user"
	if _, err := other.toolAgentResult(runArgs(id)); err == nil {
		t.Fatal("changed web owner disclosed prior run")
	}
	if _, err := other.toolAgentRun(wire); err == nil {
		t.Fatal("retry key disclosed a run from the prior owner binding")
	}
	f.finish(t, id, "private result", "")
	f.wait(t, id)
}

func TestLostSubmitAcknowledgementKeepsCompleteTerminalArtifact(t *testing.T) {
	f := newRunWingFixture(t, nil)
	submit := f.service.RunBackend.Submit
	status := f.service.RunBackend.Status
	f.service.RunBackend.Submit = func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, request egg.RunTurnRequest) (egg.RunTurnResult, error) {
		if _, err := submit(ctx, cfg, sess, request); err != nil {
			return egg.RunTurnResult{}, err
		}
		f.finish(t, request.RunID, "complete Ω🙂 result", "")
		return egg.RunTurnResult{}, fmt.Errorf("lost submit acknowledgement")
	}
	f.service.RunBackend.Status = func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
		r, err := status(ctx, cfg, sess, id)
		r.Text = ""
		return r, err
	}
	f.restart(t)
	id := f.admit(t, "request")
	<-f.submitted
	f.wait(t, id)
	result, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || result["ready"] != true || result["output"] != "complete Ω🙂 result" {
		t.Fatalf("published an incomplete terminal artifact: %v, %v", result, err)
	}
}

func TestRunRejectsMismatchedNativeResultIdentity(t *testing.T) {
	f := newRunWingFixture(t, nil)
	submit := f.service.RunBackend.Submit
	f.service.RunBackend.Submit = func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, request egg.RunTurnRequest) (egg.RunTurnResult, error) {
		r, err := submit(ctx, cfg, sess, request)
		r.SessionID = "another-session"
		return r, err
	}
	f.restart(t)
	id := f.admit(t, "request")
	<-f.submitted
	f.wait(t, id)
	result, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || result["status"] != "failed" || result["failure_kind"] != agent.UnknownOutcome {
		t.Fatalf("mismatched native result: %v, %v", result, err)
	}
}

func TestQueuedLegacyFollowUpSurvivesWingRestart(t *testing.T) {
	f := newRunWingFixture(t, nil)
	id := f.admit(t, "launch template")
	<-f.submitted
	f.finish(t, id, "template outcome", "")
	f.wait(t, id)
	r, err := f.service.RunManager.Get(f.server.sessionAuthority(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	prior := "legacy Ω result"
	parent := &store.Task{ID: "legacy-parent", Type: "agent_run", What: "legacy request", Agent: "claude", Model: "opus", CWD: f.server.Cfg.Dir, Principal: f.server.Principal, Status: "done", Output: &prior, RunAt: time.Now().UTC()}
	if err := db.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("UPDATE tasks SET output=? WHERE id=?", prior, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec("INSERT INTO task_log (task_id,event,detail) VALUES (?,'failed','legacy-provider-diagnostic-canary')", parent.ID); err != nil {
		t.Fatal(err)
	}
	r.ID, r.SessionID = "legacy-child", "legacy-child-egg"
	r.ParentID, r.Direction, r.Phase = parent.ID, "new direction", "queued"
	r.QueueExpiresAt = time.Now().UTC().Add(time.Hour)
	r.Launch.Options.SessionID = r.SessionID
	r.Result = egg.RunTurnResult{RunID: r.ID, SessionID: r.SessionID, Status: "pending"}
	wire, _ := json.Marshal(r)
	if _, _, err := db.AdmitAgentRun(&store.AgentRun{ID: r.ID, SessionID: r.SessionID, Principal: f.server.Principal, SpecHash: r.SpecHash, Record: wire}, &store.Task{ID: r.ID, What: r.Prompt, Agent: r.Agent, CWD: r.CWD, Principal: f.server.Principal, Status: "pending", RunAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := <-f.submitted; got != r.ID {
		t.Fatalf("reconciled %s, want %s", got, r.ID)
	}
	f.mu.Lock()
	child := f.eggs[r.ID]
	f.mu.Unlock()
	child.mu.Lock()
	prompt := child.prompt
	child.mu.Unlock()
	if !strings.Contains(prompt, "Prior request:\nlegacy request") || !strings.Contains(prompt, "Prior result:\n"+prior) || !strings.Contains(prompt, "New direction:\nnew direction") {
		t.Fatalf("restarted legacy follow-up lost context: %q", prompt)
	}
	f.finish(t, r.ID, "continued", "")
	f.wait(t, r.ID)
	stopped, err := f.server.toolAgentStop(runArgs(parent.ID))
	if err != nil || stopped["status"] != "done" {
		t.Fatalf("terminal legacy stop changed history: %v, %v", stopped, err)
	}
	events, err := f.server.toolAgentEvents(runArgs(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "legacy-provider-diagnostic-canary") {
		t.Fatalf("legacy stop exposed archived provider diagnostics: %s", encoded)
	}
}

func TestMCPStartedAgentSurvivesHostExit(t *testing.T) { exerciseMCPRunClientExit(t) }

func exerciseMCPRunClientExit(t *testing.T) {
	t.Helper()
	f := newRunWingFixture(t, nil)
	// Keep socket paths short without changing TMPDIR or touching any live state.
	root, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(root, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(f.server.Cfg.Dir), alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(alias) })
	socket, err := controlsocket.Listen(t.Context(), filepath.Join(alias, filepath.Base(f.server.Cfg.Dir)), "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: f.server.Principal}, f.server.handleDirectRequest, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeLocalWingClient(t.Context(), "test", filepath.Join(alias, filepath.Base(f.server.Cfg.Dir)), "", "", false, input, output)
	}()
	defer receive.Close()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_run","arguments":{"agent":"claude","prompt":"durable request"}}}` + "\n"
	if _, err := io.WriteString(send, request); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			IsError bool           `json:"isError"`
			Data    map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(receive).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.IsError {
		t.Fatalf("admission: %+v", response)
	}
	id := <-f.submitted
	if response.Result.Data["run_id"] != id {
		t.Fatal("wrong admission")
	}
	send.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.finish(t, id, "survived client exit", "")
	f.wait(t, id)
	c, err := controlsocket.Dial(t.Context(), filepath.Join(alias, filepath.Base(f.server.Cfg.Dir)), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	data, denied, err := c.Call(t.Context(), "agent_result", runArgs(id))
	if err != nil || denied || data["output"] != "survived client exit" {
		t.Fatalf("reconnected: %v %v", data, err)
	}
}

func TestStopAndQueuedSteerSurviveReconnect(t *testing.T) {
	f := newRunWingFixture(t, nil)
	parent := f.admit(t, "original request")
	<-f.submitted
	queued, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"new direction","model":"sonnet"}`))
	if err != nil {
		t.Fatal(err)
	}
	child := queued["run_id"].(string)
	reconnected := &Server{Version: f.server.Version, Cfg: f.server.Cfg, Sessions: f.service, Principal: f.server.Principal, identity: f.server.identity, sessionRole: f.server.sessionRole}
	stopped, err := reconnected.toolAgentStop(runArgs(parent))
	if err != nil || stopped["status"] != "stopped" || stopped["stopped"] != true {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	for _, id := range []string{parent, child} {
		result, err := reconnected.toolAgentResult(runArgs(id))
		if err != nil || result["status"] != "stopped" || result["ready"] != true {
			t.Fatalf("reconnect result: %v %v", result, err)
		}
	}
	repeated, err := reconnected.toolAgentStop(runArgs(parent))
	if err != nil || repeated["finished_at"] != stopped["finished_at"] {
		t.Fatalf("non-idempotent stop: %v %v", repeated, err)
	}
	select {
	case id := <-f.submitted:
		t.Fatalf("cancelled follow-up executed: %s", id)
	default:
	}
	stranger := &Server{Version: reconnected.Version, Cfg: reconnected.Cfg, Sessions: reconnected.Sessions, Principal: "stranger", identity: reconnected.identity, sessionRole: reconnected.sessionRole}
	if _, err := stranger.toolAgentStop(runArgs(parent)); err == nil {
		t.Fatal("foreign stop accepted")
	}
}

func TestQueuedSteerReceivesParentOutcomeAndModelOverride(t *testing.T) {
	f := newRunWingFixture(t, nil)
	parent := f.admit(t, "original request")
	<-f.submitted
	follow, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"fix remaining issue","model":"sonnet"}`))
	if err != nil {
		t.Fatal(err)
	}
	child := follow["run_id"].(string)
	select {
	case id := <-f.submitted:
		t.Fatalf("active parent did not gate followup: %s", id)
	default:
	}
	f.finish(t, parent, "partial Ω result", agent.ProviderError)
	if id := <-f.submitted; id != child {
		t.Fatalf("submitted wrong followup: %s", id)
	}
	f.mu.Lock()
	e := f.eggs[child]
	f.mu.Unlock()
	e.mu.Lock()
	prompt := e.prompt
	e.mu.Unlock()
	if !strings.Contains(prompt, "Prior request:\noriginal request") || !strings.Contains(prompt, "Prior result:\npartial Ω result") || !strings.Contains(prompt, "New direction:\nfix remaining issue") || !strings.Contains(prompt, "Prior error:\nEgg run turn ended: provider_error.") {
		t.Fatalf("followup context: %q", prompt)
	}
	<-f.spawned
	opts := <-f.spawned
	if !reflect.DeepEqual(opts.Egg.AgentArgs, []string{"--model", "sonnet"}) || opts.SessionID != follow["session_id"] {
		t.Fatalf("followup model: %+v", opts)
	}
	f.finish(t, child, "fixed", "")
	f.wait(t, child)
	events, err := f.server.toolAgentEvents(json.RawMessage(`{"run_id":"` + child + `","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	page := events["events"].([]map[string]any)
	if len(page) != 2 || page[0]["event"] != "done" {
		t.Fatalf("events order: %v", events)
	}
	cursor := events["next_cursor"].(int64)
	wire, _ := json.Marshal(map[string]any{"run_id": child, "limit": 2, "cursor": cursor})
	older, err := f.server.toolAgentEvents(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range older["events"].([]map[string]any) {
		if entry["cursor"].(int64) >= cursor {
			t.Fatal("cursor replay repeated newer event")
		}
	}
}

func TestWingWaitAnyPreservesFinishedPendingAndHiddenErrors(t *testing.T) {
	f := newRunWingFixture(t, nil)
	a := f.admit(t, "a")
	b := f.admit(t, "b")
	<-f.submitted
	<-f.submitted
	f.finish(t, b, "done", "")
	f.wait(t, b)
	wire, _ := json.Marshal(map[string]any{"run_ids": []string{a, b, "missing", b}})
	data, err := f.server.toolAgentWaitAny(t.Context(), wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data["finished"], []map[string]any{{"run_id": b, "status": "done"}}) || !reflect.DeepEqual(data["pending"], []string{a}) || len(data["errors"].([]map[string]any)) != 1 {
		t.Fatalf("wait-any: %v", data)
	}
	f.finish(t, a, "done", "")
	f.wait(t, a)
}

func (f *runWingFixture) restart(t *testing.T) {
	t.Helper()
	old := f.service
	if old.RunManager != nil {
		if err := old.RunManager.Close(); err != nil {
			t.Fatal(err)
		}
		old.RunManager = nil
	}
	next := &wingsession.Service{Config: old.Config, Home: old.Home, SharedHost: old.SharedHost, Policy: old.Policy, Register: old.Register, Spawn: old.Spawn, RunBackend: old.RunBackend}
	if err := next.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.service = next
	f.server.Sessions = next
}

func TestAgentRunSurvivesWingRestart(t *testing.T) {
	f := newRunWingFixture(t, nil)
	id := f.admit(t, "durable request")
	<-f.submitted
	f.restart(t)
	f.finish(t, id, "complete after wing restart", "")
	f.wait(t, id)
	result, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || result["output"] != "complete after wing restart" || result["provider_session_id"] == "" {
		t.Fatalf("restart: %v %v", result, err)
	}
	select {
	case duplicate := <-f.submitted:
		t.Fatalf("restart resent input: %s", duplicate)
	default:
	}
	f.restart(t)
	again, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatalf("terminal restart changed result: %v %v", again, err)
	}
}

func TestLostRunSubmissionAcknowledgementDoesNotResendAfterRestart(t *testing.T) {
	f := newRunWingFixture(t, nil)
	original := f.service.RunBackend.Submit
	accepted := make(chan struct{})
	f.service.RunBackend.Submit = func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, r egg.RunTurnRequest) (egg.RunTurnResult, error) {
		_, err := original(ctx, cfg, sess, r)
		if err != nil {
			return egg.RunTurnResult{}, err
		}
		close(accepted)
		<-ctx.Done()
		return egg.RunTurnResult{}, ctx.Err()
	}
	// Backend is captured by StartRuns, so recreate the idle service before use.
	f.restart(t)
	id := f.admit(t, "only once")
	<-accepted
	<-f.submitted
	f.service.RunBackend.Submit = original
	f.restart(t)
	f.finish(t, id, "one accepted prompt", "")
	f.wait(t, id)
	result, err := f.server.toolAgentResult(runArgs(id))
	if err != nil || result["status"] != "done" {
		t.Fatalf("lost acknowledgement: %v %v", result, err)
	}
	select {
	case duplicate := <-f.submitted:
		t.Fatalf("ambiguous input resent: %s", duplicate)
	default:
	}
}

func TestStopIntentAndQueuedCancellationSurviveWingRestart(t *testing.T) {
	f := newRunWingFixture(t, nil)
	original := f.service.RunBackend.Stop
	stopping := make(chan struct{})
	f.service.RunBackend.Stop = func(ctx context.Context, _ *config.Config, _ eggclient.LocalSession, _ string) (egg.RunTurnResult, error) {
		close(stopping)
		<-ctx.Done()
		return egg.RunTurnResult{}, ctx.Err()
	}
	f.restart(t)
	parent := f.admit(t, "parent")
	<-f.submitted
	childData, err := f.server.toolAgentSteer(json.RawMessage(`{"run_id":"` + parent + `","prompt":"queued"}`))
	if err != nil {
		t.Fatal(err)
	}
	child := childData["run_id"].(string)
	stopped := make(chan error, 1)
	go func() { _, err := f.server.toolAgentStop(runArgs(parent)); stopped <- err }()
	<-stopping
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	<-stopped
	f.service.RunBackend.Stop = original
	f.restart(t)
	f.wait(t, parent)
	for _, id := range []string{parent, child} {
		result, err := f.server.toolAgentResult(runArgs(id))
		if err != nil || result["status"] != "stopped" {
			t.Fatalf("stop restart: %v %v", result, err)
		}
	}
	select {
	case duplicate := <-f.submitted:
		t.Fatalf("cancelled queue executed: %s", duplicate)
	default:
	}
}

func agentRunStatusData(task *store.Task) map[string]any {
	data := map[string]any{
		"run_id": task.ID, "status": task.Status, "agent": task.Agent,
		"model": task.Model, "cwd": task.CWD, "isolation": task.Isolation,
		"timeout_seconds": task.TimeoutSeconds,
		"created_at":      task.CreatedAt.UTC().Format(time.RFC3339),
	}
	if task.StartedAt != nil {
		data["started_at"] = task.StartedAt.UTC().Format(time.RFC3339)
	}
	if task.FinishedAt != nil {
		data["finished_at"] = task.FinishedAt.UTC().Format(time.RFC3339)
	}
	return data
}
