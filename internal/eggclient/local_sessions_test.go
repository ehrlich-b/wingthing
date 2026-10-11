package eggclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"

	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"google.golang.org/grpc"
)

type inventoryStatusFixture struct {
	pb.UnimplementedEggServer
	slow                bool
	active, peak, calls *atomic.Int32
}

func (f inventoryStatusFixture) Status(ctx context.Context, _ *pb.StatusRequest) (*pb.StatusResponse, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for old := f.peak.Load(); n > old; old = f.peak.Load() {
		if f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if f.slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pb.StatusResponse{Readers: 3, Agent: "claude", TotalWritten: 99}, nil
}

func inventorySessionsFixture(t *testing.T, slowFrom int) (*config.Config, *atomic.Int32, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	// These fixtures use short explicit IDs; validate the canonical socket
	// below rather than reserving the unrelated full runtime ID width.
	home, state, _ := testprovider.ScopedHomeDirs(t, "../../.scratch")
	home, _ = filepath.Abs(home)
	state, _ = filepath.Abs(state)
	real, alias := filepath.Join(home, "t"), filepath.Join(home, "a")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), "/bin/cat")
	child.Env = []string{"HOME=" + home, "TMPDIR=" + alias}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Wait() })
	t.Setenv("HOME", home)
	active, peak, calls := new(atomic.Int32), new(atomic.Int32), new(atomic.Int32)
	for n := range 20 {
		dir := filepath.Join(state, "eggs", fmt.Sprintf("s%02d", n))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for file, value := range map[string]string{
			"egg.pid": strconv.Itoa(child.Process.Pid), "egg.token": "fixture",
			"egg.meta":        "kind=agent\nagent=claude\nprovider_home=" + home + "\ncwd=" + alias + "\n",
			"lifecycle.jsonl": "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"working\"}\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(dir, "egg.sock")
		canonical, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range []string{path, filepath.Join(canonical, "egg.sock")} {
			if len([]byte(candidate)) > 103 {
				t.Fatalf("socket exceeds 103 bytes: %s", candidate)
			}
		}
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		rpc := grpc.NewServer()
		pb.RegisterEggServer(rpc, inventoryStatusFixture{slow: n >= slowFrom, active: active, peak: peak, calls: calls})
		done := make(chan struct{})
		go func() { defer close(done); _ = rpc.Serve(listener) }()
		t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-done })
	}
	return &config.Config{Dir: state}, active, peak, calls
}

func TestDiscoverActiveSessionsInventoryFixturePhases(t *testing.T) {
	cfg, _, peak, calls := inventorySessionsFixture(t, 12)
	start := time.Now()
	refs, err := DiscoverSessionRefs(cfg)
	metadata := time.Since(start)
	if err != nil || len(refs) != 20 {
		t.Fatalf("metadata: %d, %v", len(refs), err)
	}
	start = time.Now()
	for _, ref := range refs {
		if _, err := tryLifecycleViewForSession(cfg, ref); err != nil {
			t.Fatal(err)
		}
	}
	lifecycle := time.Since(start)
	start = time.Now()
	rows, err := DiscoverActiveSessions(t.Context(), cfg)
	if err != nil || len(rows) != 20 {
		t.Fatalf("inventory: %d, %v", len(rows), err)
	}
	for n, row := range rows {
		if row.ID != fmt.Sprintf("s%02d", n) || row.PID <= 0 {
			t.Fatalf("lost identity/sort: %+v", row)
		}
	}
	t.Logf("20-entry fixture: metadata=%s lifecycle=%s enriched_inventory=%s status_calls=%d peak_status=%d", metadata, lifecycle, time.Since(start), calls.Load(), peak.Load())
}

func TestDiscoverActiveSessionsInventoryCanceledRetainsMetadata(t *testing.T) {
	cfg, _, _, calls := inventorySessionsFixture(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := DiscoverActiveSessions(ctx, cfg)
	if err != nil || len(rows) != 20 {
		t.Fatalf("canceled inventory lost metadata: %d, %v", len(rows), err)
	}
	for _, row := range rows {
		if row.Status != "unknown" {
			t.Fatalf("canceled enrichment reported evidence: %+v", row)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("canceled inventory launched probes")
	}
}

// Exercise the real RPC separately so phase measurements do not infer health
// from lifecycle state or from an absent socket.
func TestDiscoverActiveSessionsInventoryStatusFixture(t *testing.T) {
	cfg, _, _, _ := inventorySessionsFixture(t, 20)
	dir := filepath.Join(cfg.Dir, "eggs", "s00")
	client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	st, err := client.Status(ctx)
	if err != nil || st.Readers != 3 {
		t.Fatalf("fixture status: %v, %v", st, err)
	}
}

func TestDiscoverActiveSessionsInventoryDeadlineAndWorkerCap(t *testing.T) {
	cfg, active, peak, calls := inventorySessionsFixture(t, 0)
	// Missing native evidence remains unknown even with a live process.
	for n := range 20 {
		if err := os.Remove(filepath.Join(cfg.Dir, "eggs", fmt.Sprintf("s%02d", n), "lifecycle.jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	rows, err := DiscoverActiveSessions(t.Context(), cfg)
	if err != nil || len(rows) != 20 {
		t.Fatalf("deadline dropped metadata: %d, %v", len(rows), err)
	}
	// Correctness allows scheduling slack; the 1s target is recorded separately.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("aggregate deadline unbounded: %s", elapsed)
	}
	if peak.Load() > 8 || peak.Load() < 2 || calls.Load() >= 20 {
		t.Fatalf("worker cap/admission: peak=%d calls=%d", peak.Load(), calls.Load())
	}
	for _, row := range rows {
		if row.Status != "unknown" || row.Readers != 0 {
			t.Fatalf("invented enrichment: %+v", row)
		}
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for active.Load() != 0 {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("status readers were not canceled")
		}
	}
	t.Logf("all-stalled inventory=%s peak_status=%d admitted=%d retained=%d", time.Since(start), peak.Load(), calls.Load(), len(rows))
}

func TestDiscoverActiveSessionsInventoryLegacyAgentFallback(t *testing.T) {
	cfg, _, _, _ := inventorySessionsFixture(t, 20)
	dir := filepath.Join(cfg.Dir, "eggs", "s00")
	home := filepath.Dir(cfg.Dir)
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("kind=agent\nprovider_home="+home+"\ncwd="+home+"/a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(home, ".claude", "wingthing-events", "s00")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "seq.00000000000000000001.json"), []byte(`{"session_id":"fixture","hook_event_name":"SessionStart"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "seq.00000000000000000002.json"), []byte(`{"session_id":"fixture","hook_event_name":"UserPromptSubmit"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := DiscoverActiveSessions(t.Context(), cfg)
	if err != nil || len(rows) != 20 || rows[0].Agent != "claude" || rows[0].Status != "working" {
		t.Fatalf("legacy agent fallback: %+v, %v", rows, err)
	}
}

func TestDiscoverActiveSessionsNativeAttentionMetadata(t *testing.T) {
	cfg, _, _, _ := inventorySessionsFixture(t, 20)
	home := filepath.Dir(cfg.Dir)
	dir := filepath.Join(cfg.Dir, "eggs", "s00")
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(home, ".claude", "wingthing-events", "s00")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	const private = "PRIVATE_PROMPT_TRANSCRIPT_TOKEN_ERROR"
	for n, phase := range []struct {
		hook, state, status string
		completion          bool
	}{
		{"SessionStart", "idle", "idle", false},
		{"UserPromptSubmit", "working", "working", false},
		{"PermissionRequest", "needs_input", "blocked", false},
		{"Stop", "completed", "idle", true},
		{"UserPromptSubmit", "working", "working", false},
		{"Notification", "idle", "idle", false},
		{"Stop", "idle", "idle", true},
		{"SessionEnd", "completed", "done", false},
	} {
		payload := map[string]any{"hook_event_name": phase.hook, "session_id": "fixture", "prompt": private, "error": private, "token": private}
		if phase.hook == "Notification" {
			payload["notification_type"] = "idle_prompt"
		}
		if n == 6 {
			payload["background_tasks"] = []string{"private-background"}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(spool, fmt.Sprintf("seq.%020d.json", n+1)), data, 0600); err != nil {
			t.Fatal(err)
		}
		rows, err := DiscoverActiveSessions(t.Context(), cfg)
		if err != nil || len(rows) != 20 {
			t.Fatalf("native phase %s: %v", phase.hook, err)
		}
		r := rows[0]
		if r.State != phase.state || r.Status != phase.status || r.StateSource != "claude_hook" || r.StateCursor != int64(n+1) || (r.CompletionCursor > 0) != phase.completion {
			t.Fatalf("native phase %s: %+v", phase.hook, r)
		}
		encoded, err := json.Marshal(r)
		if err != nil || strings.Contains(string(encoded), private) || strings.Contains(string(encoded), "private-background") {
			t.Fatalf("inventory exposed native content: %s %v", encoded, err)
		}
		var legacy struct{ ID, Status string }
		if err := json.Unmarshal(encoded, &legacy); err != nil || legacy.ID != "s00" || legacy.Status != phase.status {
			t.Fatalf("additive JSON broke a legacy consumer: %+v %v", legacy, err)
		}
	}
	// A live provider and a responsive status RPC supply no lifecycle evidence.
	if err := os.WriteFile(filepath.Join(cfg.Dir, "eggs", "s01", "lifecycle.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := DiscoverActiveSessions(t.Context(), cfg)
	if err != nil || rows[1].Status != "unknown" || rows[1].State != "unknown" || rows[1].CompletionCursor != 0 {
		t.Fatalf("disabled hooks inferred completion: %+v %v", rows, err)
	}
}

func TestDiscoverActiveSessionsInventoryLifecycleResidual(t *testing.T) {
	cfg, _, _, _ := inventorySessionsFixture(t, 20)
	const eventsPerSession = 4000
	var journal strings.Builder
	for n := range eventsPerSession {
		fmt.Fprintf(&journal, "{\"sequence\":%d,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"working\"}\n", n+1)
	}
	for n := range 20 {
		if err := os.WriteFile(filepath.Join(cfg.Dir, "eggs", fmt.Sprintf("s%02d", n), "lifecycle.jsonl"), []byte(journal.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	refs, err := DiscoverSessionRefs(cfg)
	metadata := time.Since(start)
	if err != nil || len(refs) != 20 {
		t.Fatalf("metadata: %d, %v", len(refs), err)
	}
	start = time.Now()
	for _, ref := range refs {
		view, err := tryLifecycleViewForSession(cfg, ref)
		if err != nil || view.HeadCursor != eventsPerSession || len(view.Events) != 1 || view.Status != "working" {
			t.Fatalf("one-event projection lost native evidence: %+v, %v", view, err)
		}
	}
	projection := time.Since(start)
	start = time.Now()
	rows, err := DiscoverActiveSessions(t.Context(), cfg)
	if err != nil || len(rows) != 20 {
		t.Fatalf("residual cost dropped metadata: %d, %v", len(rows), err)
	}
	t.Logf("RESIDUAL synchronous journal projection: sessions=20 events/session=%d bytes/session=%d metadata=%s sequential_projection=%s bounded_inventory=%s; display page=1, existing full-journal reads cannot be canceled", eventsPerSession, journal.Len(), metadata, projection, time.Since(start))
}

func TestSessionInputChunksSeparatesEnterFromPastedText(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		enter bool
		want  [][]byte
	}{
		{name: "text only", input: []byte("hello"), want: [][]byte{[]byte("hello")}},
		{name: "enter only", enter: true, want: [][]byte{{'\r'}}},
		{name: "text and enter", input: []byte("hello"), enter: true, want: [][]byte{[]byte("hello"), {'\r'}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sessionInputChunks(test.input, test.enter)
			if len(got) != len(test.want) {
				t.Fatalf("chunks = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if !bytes.Equal(got[index], test.want[index]) {
					t.Fatalf("chunk %d = %q, want %q", index, got[index], test.want[index])
				}
			}
		})
	}
}

func TestValidateSessionName(t *testing.T) {
	for _, valid := range []string{"", "work", "dev-server", "api_2", "repo.main"} {
		if err := ValidateSessionName(valid); err != nil {
			t.Errorf("validateSessionName(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"-option", ".hidden", "two words", "a/b", "x\ncommand"} {
		if err := ValidateSessionName(invalid); err == nil {
			t.Errorf("validateSessionName(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestSessionNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSessionName(dir, "api-server"); err != nil {
		t.Fatalf("writeSessionName: %v", err)
	}
	if got := ReadSessionName(dir); got != "api-server" {
		t.Fatalf("readSessionName = %q, want api-server", got)
	}
	info, err := os.Stat(filepath.Join(dir, sessionNameFile))
	if err != nil {
		t.Fatalf("stat session name: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("session name mode = %o, want 600", got)
	}
	if err := WriteSessionName(dir, ""); err != nil {
		t.Fatalf("remove session name: %v", err)
	}
	if got := ReadSessionName(dir); got != "" {
		t.Fatalf("removed session name = %q, want empty", got)
	}
}

func TestReadSessionNameRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionNameFile), []byte("bad\x1b[2Jname\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionName(dir); got != "" {
		t.Fatalf("readSessionName = %q, want invalid name ignored", got)
	}
}

func TestReadEggMetaValuesPreservesEquals(t *testing.T) {
	dir := t.TempDir()
	data := []byte("kind=command\ncommand=\"sh\" \"-c\" \"A=B echo $A\"\ncwd=/tmp/project\n")
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), data, 0600); err != nil {
		t.Fatal(err)
	}
	meta := ReadEggMetaValues(dir)
	if got, want := meta["command"], `"sh" "-c" "A=B echo $A"`; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestSessionDiscoveryDoesNotCleanDeadMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "eggs", "stale")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, "egg.pid")
	if err := os.WriteFile(pidPath, []byte("-1"), 0600); err != nil {
		t.Fatal(err)
	}
	if sessions, err := DiscoverSessionRefs(&config.Config{Dir: root}); err != nil || len(sessions) != 0 {
		t.Fatalf("discover sessions = %#v, %v", sessions, err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("read-only session discovery removed metadata: %v", err)
	}
}

func TestEggEnvironmentUsesOwnerOnlyOneShotFile(t *testing.T) {
	dir := t.TempDir()
	secret := "must-not-enter-argv"
	args, path, err := prepareEggEnvironmentTransport(dir, []string{"egg", "run", "--session-id", "session"}, map[string]string{"OPENAI_API_KEY": secret})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("environment file mode = %o, want 600", info.Mode().Perm())
	}
	for _, arg := range args {
		if strings.Contains(arg, secret) {
			t.Fatal("environment secret entered child argv")
		}
	}
	if args[len(args)-1] != "--env-file-required" {
		t.Fatalf("environment transport args = %#v", args)
	}
	environment, err := ReadEggEnvironment(path, []string{"TERM=xterm"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if environment["OPENAI_API_KEY"] != secret || environment["TERM"] != "xterm" {
		t.Fatalf("decoded environment = %#v", environment)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("environment file survived read: %v", err)
	}
}
