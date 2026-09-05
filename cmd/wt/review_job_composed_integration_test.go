//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	"github.com/ehrlich-b/wingthing/internal/store"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
	"gopkg.in/yaml.v3"
)

type reviewFixtureTrace struct {
	mu          sync.Mutex
	scenario    string
	released    chan struct{}
	faulted     bool
	connections map[string]int
	runs        map[string][]string
	waits       map[string][]string
}

type reviewFixtureChannel struct {
	*pionwebrtc.DataChannel
	trace   *reviewFixtureTrace
	wing    string
	policy  *config.WingConfig
	mu      sync.Mutex
	pending map[string]control.DirectRequest
}

func (dc *reviewFixtureChannel) OnMessage(handle func(pionwebrtc.DataChannelMessage)) {
	dc.DataChannel.OnMessage(func(message pionwebrtc.DataChannelMessage) {
		var request control.DirectRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			handle(message)
			return
		}
		var args struct {
			Action string `json:"action"`
			RunID  string `json:"run_id"`
		}
		_ = json.Unmarshal(request.Arguments, &args)
		if request.Tool == "review_workspace" && args.Action == "run" {
			<-dc.trace.released
		}
		dc.mu.Lock()
		dc.pending[request.ID] = request
		dc.mu.Unlock()
		dc.trace.mu.Lock()
		fault := false
		if request.Tool == "agent_wait" {
			dc.trace.waits[dc.wing] = append(dc.trace.waits[dc.wing], args.RunID)
			if dc.wing == "222222222222222222222222" && !dc.trace.faulted && (dc.trace.scenario == "transient" || dc.trace.scenario == "revoked") {
				dc.trace.faulted, fault = true, true
			}
		}
		dc.trace.mu.Unlock()
		if fault {
			if dc.trace.scenario == "revoked" {
				wingCfgMu.Lock()
				dc.policy.Locked = true
				wingCfgMu.Unlock()
			}
			_ = dc.Close()
			return
		}
		handle(message)
	})
}

func (dc *reviewFixtureChannel) Send(data []byte) error {
	var response control.DirectResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	dc.mu.Lock()
	request := dc.pending[response.ID]
	delete(dc.pending, response.ID)
	dc.mu.Unlock()
	var args struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(request.Arguments, &args)
	drop := false
	if request.Tool == "review_workspace" && args.Action == "run" {
		id, _ := response.Result["run_id"].(string)
		dc.trace.mu.Lock()
		dc.trace.runs[dc.wing] = append(dc.trace.runs[dc.wing], id)
		if dc.trace.scenario == "ambiguous" && !dc.trace.faulted && id != "" {
			dc.trace.faulted, drop = true, true
		}
		dc.trace.mu.Unlock()
	}
	if drop {
		return dc.Close()
	}
	return dc.DataChannel.Send(data)
}

func TestReviewJobComposedNativeWorkflow(t *testing.T) {
	for _, scenario := range []string{"disconnect", "lease", "transient", "revoked", "failed-tests", "revision", "revision-limit", "ambiguous", "unavailable", "timeout"} {
		t.Run(scenario, func(t *testing.T) { testReviewJobComposedNativeWorkflow(t, scenario) })
	}
}

func testReviewJobComposedNativeWorkflow(t *testing.T, scenario string) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(name, "")
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"value.txt": "baseline\n", "egg.yaml": "fs:\n  - rw:./\nnetwork: none\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-C", source}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %s: %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("add", "value.txt", "egg.yaml")
	git("commit", "--quiet", "-m", "Pinned fixture baseline")
	base := git("rev-parse", "HEAD")

	relayStore, err := relay.OpenRelay(filepath.Join(root, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relayStore.Close() })
	server := relay.NewServer(relayStore, relay.ServerConfig{})
	key, _, err := relay.GenerateECKey()
	if err != nil {
		t.Fatal(err)
	}
	server.SetJWTKey(key)
	var requests sync.WaitGroup
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer requests.Done()
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		httpServer.Close()
		done := make(chan struct{})
		go func() { requests.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("fixture relay retained a websocket handler")
		}
	})
	for _, user := range []string{"alice", "bob"} {
		if err := relayStore.CreateUser(user); err != nil {
			t.Fatal(err)
		}
		if _, err := relayStore.DB().Exec("UPDATE users SET email = ? WHERE id = ?", user+"@example.test", user); err != nil {
			t.Fatal(err)
		}
	}
	newConfig := func(name, user string) *config.Config {
		t.Helper()
		cfg := &config.Config{Dir: filepath.Join(root, name), RoostURL: httpServer.URL, DefaultAgent: "codex", WingID: name}
		if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
			t.Fatal(err)
		}
		expiry := time.Now().UTC().Add(time.Hour)
		token := "fixture-" + name
		if err := relayStore.CreateDeviceToken(token, user, name, &expiry); err != nil {
			t.Fatal(err)
		}
		if err := auth.NewTokenStore(cfg.Dir).Save(&auth.DeviceToken{Token: token, ExpiresAt: expiry.Unix(), DeviceID: name}); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	trace := &reviewFixtureTrace{scenario: scenario, released: make(chan struct{}), connections: map[string]int{}, runs: map[string][]string{}, waits: map[string][]string{}}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(trace.released) }) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	startWing := func(id string) *config.Config {
		t.Helper()
		cfg := newConfig(id, "alice")
		workspaceRoot := filepath.Join(root, "workspaces-"+id)
		if err := os.MkdirAll(workspaceRoot, 0700); err != nil {
			t.Fatal(err)
		}
		policy := &config.WingConfig{WingID: id, HostedRelay: config.HostedRelayDeny, Paths: config.PathList{{Path: root}}}
		if err := config.SaveWingConfig(cfg.Dir, policy); err != nil {
			t.Fatal(err)
		}
		data, err := yaml.Marshal(reviewJobPolicy{OwnerPrincipal: roostSessionPrincipal("alice"), WorkspaceRoot: workspaceRoot, Sources: []string{source}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Dir, "review-jobs.yaml"), data, 0600); err != nil {
			t.Fatal(err)
		}
		public, err := auth.EnsureKeyPair(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		private, err := auth.LoadPrivateKey(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		manager := webrtcpkg.NewPeerManager(nil)
		t.Cleanup(manager.Close)
		admission := newMCPAdmissionState()
		manager.OnDC(func(_ string, _ string, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
			trace.mu.Lock()
			trace.connections[id]++
			lease := directMCPIdentityLease
			if scenario == "lease" && id == "222222222222222222222222" && trace.connections[id] == 1 {
				lease = 3 * time.Second
			}
			trace.mu.Unlock()
			channel := &reviewFixtureChannel{DataChannel: dc, trace: trace, wing: id, policy: policy, pending: map[string]control.DirectRequest{}}
			serveDirectMCPChannelWithPolicySourceAndLease(cfg, home, false, admission, identity, channel, func() (*config.WingConfig, []config.AllowKey) {
				wingCfgMu.Lock()
				defer wingCfgMu.Unlock()
				return policy.Clone(), nil
			}, lease)
		})
		client := &ws.Client{RoostURL: "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/wing", Token: "fixture-" + id, WingID: id, PublicKey: public, DirectMCP: true, HostedRelay: ws.HostedRelayDeny, Hostname: id, Platform: "fixture"}
		var allowed []config.AllowKey
		var eggMu sync.Mutex
		var eggConfig *egg.EggConfig
		var sessions sync.Map
		client.OnTunnel = func(ctx context.Context, request ws.TunnelRequest, write ws.PTYWriteFunc) {
			handleTunnelRequest(ctx, cfg, policy, request, write, &allowed, nil, nil, auth.PasskeyPolicy{}, private, home, &eggMu, &eggConfig, false, false, client, manager, &sessions)
		}
		ready := make(chan struct{}, 1)
		client.OnRegistered = func(ws.RegisteredMsg) {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
		wingCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		var runErr error
		go func() {
			runErr = client.Run(wingCtx)
			close(done)
		}()
		t.Cleanup(func() {
			stop()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fixture wing did not stop")
			}
		})
		select {
		case <-ready:
		case <-done:
			t.Fatalf("fixture wing exited: %v", runErr)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return cfg
	}
	coordinator := startWing("111111111111111111111111")
	implementer := startWing("222222222222222222222222")
	connect := func(name, owner string) *connectMCPServer {
		t.Helper()
		connector, err := newReviewConnector(newConfig(name, owner), name, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(connector.close)
		return connector
	}
	call := func(client *connectMCPServer, tool string, args map[string]any) map[string]any {
		t.Helper()
		args["wing_id"] = coordinator.WingID
		data, _ := json.Marshal(args)
		result, bad, err := client.callTool(ctx, tool, data)
		if err != nil || bad {
			t.Fatalf("%s failed: %v %v", tool, result, err)
		}
		return result
	}
	spec := reviewjob.Spec{RequestID: "fixture-" + scenario, Prompt: "WT_REVIEW_FIXTURE=" + scenario, BaseCommit: base, Implementer: reviewjob.Target{WingID: implementer.WingID, Source: source, Model: "fixture-terra"}, Reviewer: reviewjob.Target{WingID: coordinator.WingID, Source: source, Model: "fixture-sol"}, AllowedPaths: []string{"value.txt"}, TestArgv: []string{"/bin/sh", "-c", "cat value.txt; test \"$(cat value.txt)\" = good"}, MaxRevisions: 0, RunSeconds: 20, TestSeconds: 5, TimeoutSeconds: 60}
	if scenario == "revision" || scenario == "revision-limit" {
		spec.MaxRevisions = 2
		spec.TestArgv = []string{"/bin/sh", "-c", "cat value.txt; test -s value.txt"}
	}
	if scenario == "timeout" {
		spec.RunSeconds, spec.TimeoutSeconds = 10, 10
	}
	if scenario == "unavailable" {
		spec.Implementer.WingID = "333333333333333333333333"
	}
	encoded, _ := json.Marshal(spec)
	var args map[string]any
	_ = json.Unmarshal(encoded, &args)
	parent := connect("submitting-client", "alice")
	admitted := call(parent, "review_job_submit", args)
	if admitted["ready"] != false {
		t.Fatalf("submission did not acknowledge unfinished durable job: %v", admitted)
	}
	parent.close()
	release.Do(func() { close(trace.released) })
	observer := connect("fresh-client", "alice")
	var row map[string]any
	for {
		listed := call(observer, "review_job_list", map[string]any{})
		rows := listed["jobs"].([]any)
		if len(rows) != 1 {
			t.Fatalf("fresh discovery: %v", listed)
		}
		row = rows[0].(map[string]any)
		if row["ready"] == true {
			break
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("job did not terminate: %v", row)
		}
	}
	id := row["job_id"].(string)
	if id != admitted["job_id"] || row["deadline"] != admitted["deadline"] {
		t.Fatalf("job identity or deadline changed: %v -> %v", admitted, row)
	}
	job, err := (reviewjob.Engine{Dir: filepath.Join(coordinator.Dir, "review-jobs")}).Get(roostSessionPrincipal("alice"), id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Deadline.Sub(job.CreatedAt) != time.Duration(spec.TimeoutSeconds)*time.Second {
		t.Fatalf("job deadline changed: %+v", job)
	}
	wantStatus, wantTerra, wantSol := "succeeded", 1, 1
	if scenario == "revoked" || scenario == "ambiguous" || scenario == "failed-tests" {
		wantStatus, wantSol = "failed", 0
	}
	if scenario == "revision" {
		wantTerra, wantSol = 2, 2
	}
	if scenario == "revision-limit" {
		wantStatus, wantTerra, wantSol = "failed", 3, 3
	}
	if scenario == "unavailable" {
		wantStatus, wantTerra, wantSol = "failed", 0, 0
	}
	if scenario == "timeout" {
		wantStatus, wantSol = "timed_out", 0
	}
	if job.Status != wantStatus {
		t.Fatalf("expected %s, got %s: %s; rounds=%+v", wantStatus, job.Status, job.Error, job.Rounds)
	}
	trace.mu.Lock()
	if len(trace.runs[implementer.WingID]) != wantTerra || len(trace.runs[coordinator.WingID]) != wantSol {
		t.Errorf("incorrect child count: %+v", trace.runs)
	}
	if scenario == "lease" || scenario == "transient" {
		if trace.connections[implementer.WingID] != 2 || len(trace.waits[implementer.WingID]) < 2 {
			t.Errorf("observation did not reconnect: connections=%v waits=%v", trace.connections, trace.waits)
		}
		for _, run := range trace.waits[implementer.WingID] {
			if run != job.Rounds[0].ImplementerRun {
				t.Errorf("recovery observed another child: %s", run)
			}
		}
	}
	trace.mu.Unlock()
	for _, cfg := range []*config.Config{coordinator, implementer} {
		taskStore, err := store.Open(cfg.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = taskStore.Close() })
		var tasks []*store.Task
		for {
			tasks, err = taskStore.ListRecent(100)
			if err != nil {
				t.Fatal(err)
			}
			active := false
			for _, task := range tasks {
				active = active || task.Status == "pending" || task.Status == "running"
			}
			if !active {
				break
			}
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("fixture left a child active beyond its deadline")
			}
		}
		want := wantTerra
		if cfg == coordinator {
			want = wantSol
		}
		if err != nil || len(tasks) != want {
			t.Fatalf("durable child count on %s: %d want %d: %v", cfg.WingID, len(tasks), want, err)
		}
		trace.mu.Lock()
		for _, task := range tasks {
			found := false
			for _, id := range trace.runs[cfg.WingID] {
				found = found || id == task.ID
			}
			if !found || task.TimeoutSeconds != spec.RunSeconds || task.RetryCount != 0 || task.Principal != roostSessionPrincipal("alice") {
				t.Errorf("child identity, owner, or original run bound changed: %+v", task)
			}
		}
		trace.mu.Unlock()
		for _, task := range tasks {
			if running, ok := activeMCPAgentRuns.Load(cfg.DBPath() + "\x00" + task.ID); ok {
				select {
				case <-running.(activeMCPAgentRun).done:
				case <-ctx.Done():
					t.Fatal("fixture supervisor did not finish after its terminal task record")
				}
			}
		}
	}
	if scenario == "ambiguous" && (!strings.Contains(job.Error, "submission unconfirmed; not retried") || job.Rounds[0].ImplementerRun != "") {
		t.Fatalf("ambiguous launch was replayed or invented: %+v", job)
	}
	if scenario == "failed-tests" && (job.Rounds[0].ImplementerTest == nil || job.Rounds[0].ImplementerTest.ExitCode == 0) {
		t.Fatalf("failed tests lost: %+v", job.Rounds)
	}
	if scenario == "revision" && (len(job.Rounds) != 2 || job.Rounds[0].Review.Verdict != "changes_requested") {
		t.Fatalf("revision evidence lost: %+v", job.Rounds)
	}
	if scenario == "revision-limit" && (len(job.Rounds) != 3 || job.Rounds[2].Review.Verdict != "changes_requested" || !strings.Contains(job.Error, "revision limit reached")) {
		t.Fatalf("revision bound not enforced: %+v", job)
	}
	if job.Status == "succeeded" {
		patch := call(observer, "review_job_result", map[string]any{"job_id": id, "artifact": "patch"})
		review := call(observer, "review_job_result", map[string]any{"job_id": id, "artifact": "review"})
		tests := call(observer, "review_job_result", map[string]any{"job_id": id, "artifact": "tests"})
		digest := sha256.Sum256([]byte(patch["patch"].(string)))
		sha := hex.EncodeToString(digest[:])
		if row["final_evidence_available"] != true || patch["sha256"] != sha || patch["base_commit"] != base || review["review"].(map[string]any)["patch_sha256"] != sha {
			t.Fatalf("candidate evidence mismatch: %v %v", patch, review)
		}
		left, right := tests["implementation"].(map[string]any), tests["review"].(map[string]any)
		if left["workspace_id"] == right["workspace_id"] || left["exit_code"] != float64(0) || right["exit_code"] != float64(0) {
			t.Fatalf("test evidence not independent and passing: %v", tests)
		}
		for _, record := range []map[string]any{left, right} {
			digest := sha256.Sum256([]byte(record["output"].(string)))
			argv, _ := json.Marshal(record["argv"])
			var actual []string
			_ = json.Unmarshal(argv, &actual)
			if record["sha256"] != hex.EncodeToString(digest[:]) || !reflect.DeepEqual(actual, spec.TestArgv) {
				t.Fatalf("test evidence mismatch: %v", record)
			}
		}
	}
	other := connect("other-owner", "bob")
	for _, tool := range []string{"review_job_list", "review_job_result"} {
		args := map[string]any{"wing_id": coordinator.WingID}
		if tool == "review_job_result" {
			args["job_id"], args["artifact"] = id, "patch"
		}
		data, _ := json.Marshal(args)
		result, bad, err := other.callTool(ctx, tool, data)
		if err == nil && !bad {
			t.Fatalf("cross-owner %s leaked: %v", tool, result)
		}
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("pinned source was modified")
	}
	t.Logf("%s: status=%s job=%s children=%d/%d deadline=%s", scenario, job.Status, id, wantTerra, wantSol, job.Deadline.Format(time.RFC3339Nano))
}
