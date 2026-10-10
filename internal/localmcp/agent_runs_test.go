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
		s = &Server{Cfg: &config.Config{Dir: t.TempDir(), DefaultAgent: "claude"}, Principal: "owner", Logs: io.Discard}
	}
	s = testWingServer(t, s)
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

func TestAgentRunResultShapeParity(t *testing.T) {
	for _, kind := range []agent.ErrorKind{"", agent.AuthFailed, agent.RateLimited, agent.UnknownOutcome} {
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
			stranger := *f.server
			stranger.Principal = "foreign"
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

func TestMCPStartedAgentSurvivesHostExit(t *testing.T) {
	f := newRunWingFixture(t, nil)
	// Keep socket paths short without changing TMPDIR or touching any live state.
	root, err := filepath.Abs("../../.scratch")
	if err != nil {
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
