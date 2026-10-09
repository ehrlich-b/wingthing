package egg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/agent"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type runFixture struct {
	runtime *runTurnRuntime
	path    string
	home    string
	request RunTurnRequest
	sent    chan struct{}
	scanned chan turnEvidence
	sends   atomic.Int32
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	dir, path, options := promptFixtureOptions(t)
	fixture := &runFixture{path: path, sent: make(chan struct{}), scanned: make(chan turnEvidence, 64), request: RunTurnRequest{RunID: "run-1", Prompt: options.Input, Deadline: time.Now().Add(time.Hour)}}
	// The original fixture reader has exactly bound provider home and CWD.
	home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	fixture.home = home
	backend := runTurnBackend{Agent: "claude", Read: options.Read}
	backend.Send = func(ctx context.Context, input string) (PromptDelivery, error) {
		fixture.sends.Add(1)
		writeNativeUserPrompt(t, path, input)
		close(fixture.sent)
		return PromptDelivery{BytesEnqueued: len(input) + 1}, nil
	}
	backend.Prepare = func(prompt, id string) (func() (turnEvidence, error), error) {
		scan, err := claudeRunScanner(home, "/fixture/shared-workspace", id, prompt, options.Read)
		return func() (turnEvidence, error) {
			evidence, err := scan()
			select {
			case fixture.scanned <- evidence:
			default:
			}
			return evidence, err
		}, err
	}
	fixture.runtime = newRunTurnRuntime(dir, backend)
	t.Cleanup(func() { fixture.runtime.stopActive() })
	return fixture
}

func appendNative(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func assistantRecord(text, stop string) []byte {
	wire, _ := json.Marshal(map[string]any{"type": "assistant", "sessionId": "ours", "message": map[string]any{"id": "message-1", "role": "assistant", "content": []any{map[string]string{"type": "text", "text": text}}, "stop_reason": stop}})
	return append(wire, '\n')
}

func waitRunFixture(t *testing.T, fixture *runFixture) RunTurnResult {
	t.Helper()
	if _, err := fixture.runtime.wait(context.Background(), fixture.request.RunID); err != nil {
		t.Fatal(err)
	}
	result, err := ReadRunTurnResult(fixture.runtime.dir, fixture.request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestExactTurnResultAfterTranscriptFlush(t *testing.T) {
	fixture := newRunFixture(t)
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	text := strings.Repeat("complete Ω result\n", 90000) // larger than native lifecycle's record limit
	stop, _ := json.Marshal(map[string]any{"session_id": "ours", "hook_event_name": "Stop", "last_assistant_message": text})
	lifecycleHook(t, fixture.home, filepath.Base(fixture.runtime.dir), "stop", string(stop))
	line := assistantRecord(text, "end_turn")
	appendNative(t, fixture.path, line[:len(line)/2])
	for evidence := range fixture.scanned {
		if evidence.Receipt && !evidence.Complete {
			break
		}
	}
	if result, err := fixture.runtime.get(fixture.request.RunID, true); err != nil || result.Terminal() {
		t.Fatalf("Stop beat transcript flush: %s %v", result.Status, err)
	}
	appendNative(t, fixture.path, line[len(line)/2:])
	result := waitRunFixture(t, fixture)
	if result.Status != "done" || result.Text != text || result.ProviderSessionID != "ours" || result.StartedAt.IsZero() || result.EndedAt.IsZero() {
		t.Fatalf("incorrect durable result: status=%s bytes=%d", result.Status, len(result.Text))
	}
}

func TestRunTurnIdempotentOnRunID(t *testing.T) {
	fixture := newRunFixture(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fixture.runtime.submit(fixture.request); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	<-fixture.sent
	appendNative(t, fixture.path, assistantRecord("final", "end_turn"))
	if result := waitRunFixture(t, fixture); result.Status != "done" {
		t.Fatal(result)
	}
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	if fixture.sends.Load() != 1 {
		t.Fatalf("sent %d prompts", fixture.sends.Load())
	}
	request := fixture.request
	request.Prompt = "changed"
	if _, err := fixture.runtime.submit(request); err == nil {
		t.Fatal("changed request reused run ID")
	}
	// Disk admission survives losing in-memory state and must never resend.
	reopened := newRunTurnRuntime(fixture.runtime.dir, fixture.runtime.backend)
	if result, err := reopened.submit(fixture.request); err != nil || result.Status != "done" || fixture.sends.Load() != 1 {
		t.Fatalf("retry after reconnect: %s %v", result.Status, err)
	}
}

func TestRunTurnSurvivesClientDisconnect(t *testing.T) {
	fixture := newRunFixture(t)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	RegisterRunTurnRPC(server, &Server{runTurns: fixture.runtime})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connect := func() *Client {
		conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
		if err != nil {
			t.Fatal(err)
		}
		return &Client{conn: conn}
	}
	client := connect()
	if _, err := client.SubmitRunTurn(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.WaitRunTurn(ctx, fixture.request.RunID); err == nil {
		t.Fatal("cancelled observation returned success")
	}
	client.Close()
	appendNative(t, fixture.path, assistantRecord(strings.Repeat("Ω", 3<<20), "end_turn"))
	client = connect()
	defer client.Close()
	if result, err := client.WaitRunTurn(context.Background(), fixture.request.RunID); err != nil || result.Status != "done" || result.Text != "" {
		t.Fatalf("reconnected wait: status=%s %v", result.Status, err)
	}
	if result, err := client.ReadRunTurnResult(context.Background(), fixture.request.RunID); err != nil || len(result.Text) != 6<<20 {
		t.Fatalf("RPC truncated result: bytes=%d %v", len(result.Text), err)
	}
	if fixture.sends.Load() != 1 {
		t.Fatal("disconnect resent prompt")
	}
}

func TestRunTurnHumanInputAndNativeConflictFail(t *testing.T) {
	for _, rawInput := range []bool{true, false} {
		t.Run(fmt.Sprint(rawInput), func(t *testing.T) {
			fixture := newRunFixture(t)
			if _, err := fixture.runtime.submit(fixture.request); err != nil {
				t.Fatal(err)
			}
			<-fixture.sent
			if rawInput {
				fixture.runtime.inputConflict()
			} else {
				writeNativeUserPrompt(t, fixture.path, "human prompt")
			}
			appendNative(t, fixture.path, assistantRecord("another turn's success", "end_turn"))
			result := waitRunFixture(t, fixture)
			if result.Status != "failed" || result.FailureKind != agent.InputConflict || result.Text != "" {
				t.Fatal(result)
			}
		})
	}
}

func TestRunTurnDeadlineAndStopAreEggOwned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := newRunFixture(t)
		kills := make(chan struct{}, 1)
		fixture.runtime.backend.Kill = func() ([]RunDescendant, error) { kills <- struct{}{}; return []RunDescendant{{PID: 123}}, nil }
		fixture.request.Deadline = time.Now().Add(time.Minute)
		if _, err := fixture.runtime.submit(fixture.request); err != nil {
			t.Fatal(err)
		}
		<-fixture.sent
		result := waitRunFixture(t, fixture) // synthetic clock reaches the stored deadline
		<-kills
		if result.Status != "timeout" || result.FailureKind != agent.Timeout || len(result.SurvivingDescendants) != 1 {
			t.Fatal(result)
		}
		if stopped, err := fixture.runtime.stop(fixture.request.RunID); err != nil || stopped.Status != "timeout" {
			t.Fatalf("stop overwrote deadline: %s %v", stopped.Status, err)
		}
	})
}

func TestRunTurnFailureNeverPersistsProviderDiagnostics(t *testing.T) {
	fixture := newRunFixture(t)
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	appendNative(t, fixture.path, []byte(`{"type":"assistant","sessionId":"ours","error":"authentication_failed","message":{"role":"assistant","content":[{"type":"text","text":"Invalid API key private-provider-canary"}]}}`+"\n"))
	result := waitRunFixture(t, fixture)
	data, err := os.ReadFile(runTurnPath(fixture.runtime.dir, fixture.request.RunID))
	if err != nil {
		t.Fatal(err)
	}
	if result.FailureKind != agent.AuthFailed || result.Text != "" || strings.Contains(string(data), "private-provider-canary") {
		t.Fatalf("provider diagnostic escaped: %s %s", result.Status, result.FailureKind)
	}
}

func TestRunTurnStopIsIdempotentAndDisarmsDeadline(t *testing.T) {
	fixture := newRunFixture(t)
	var kills atomic.Int32
	fixture.runtime.backend.Kill = func() ([]RunDescendant, error) { kills.Add(1); return nil, nil }
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	first, err := fixture.runtime.stop(fixture.request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.runtime.stop(fixture.request.RunID)
	if err != nil || second.Status != "stopped" || second.FailureKind != agent.Stopped || !first.EndedAt.Equal(second.EndedAt) || kills.Load() != 1 {
		t.Fatalf("repeated stop: %s %v kills=%d", second.Status, err, kills.Load())
	}
}

func TestClaudeStopRequiresMatchingFlushedAssistantText(t *testing.T) {
	fixture := newRunFixture(t)
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	lifecycleHook(t, fixture.home, filepath.Base(fixture.runtime.dir), "stop", `{"session_id":"ours","hook_event_name":"Stop","last_assistant_message":"native final"}`)
	for evidence := range fixture.scanned {
		if evidence.Receipt && !evidence.Complete {
			break
		}
	}
	line := assistantRecord("native final", "")
	appendNative(t, fixture.path, line[:len(line)-1])
	for evidence := range fixture.scanned {
		if evidence.Receipt && !evidence.Complete {
			break
		}
	}
	if result, _ := fixture.runtime.get(fixture.request.RunID, true); result.Terminal() {
		t.Fatal("unterminated assistant record completed the run")
	}
	appendNative(t, fixture.path, []byte{'\n'})
	if result := waitRunFixture(t, fixture); result.Status != "done" || result.Text != "native final" {
		t.Fatal(result)
	}
}

func TestRunTurnProviderExitIsFailureWithoutNativeCompletion(t *testing.T) {
	fixture := newRunFixture(t)
	done := make(chan struct{})
	fixture.runtime.backend.Done = done
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	appendNative(t, fixture.path, assistantRecord("unfinished", ""))
	close(done)
	if result := waitRunFixture(t, fixture); result.Status != "failed" || result.FailureKind != agent.ProviderExit || result.Text != "" {
		t.Fatal(result)
	}
}

func TestEggRunPromptUsesAttachablePTYAndExactEditorFrames(t *testing.T) {
	fixture := newRunFixture(t)
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if _, err = term.MakeRaw(int(terminal.Fd())); err != nil {
		t.Fatal(err)
	}
	if err = preparePTYInput(master); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: filepath.Base(fixture.runtime.dir), Kind: "agent", Agent: "claude", CWD: "/fixture/shared-workspace", ptmx: master, done: make(chan struct{})}
	server := &Server{dir: fixture.runtime.dir, session: sess}
	runtime := server.sessionRunTurns(sess, fixture.home, "ours")
	server.runTurns = runtime
	t.Cleanup(runtime.stopActive)
	want := "\x1b[200~" + fixture.request.Prompt + "\x1b[201~\r"
	input := make(chan string, 1)
	go func() {
		bytes := make([]byte, len(want))
		_, err := io.ReadFull(terminal, bytes)
		if err != nil {
			input <- "read failed"
			return
		}
		input <- string(bytes)
		writeNativeUserPrompt(t, fixture.path, fixture.request.Prompt)
		appendNative(t, fixture.path, assistantRecord("native PTY result", "end_turn"))
	}()
	if _, err = runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	if got := <-input; got != want {
		t.Fatalf("PTY input=%q, want %q", got, want)
	}
	if _, err = runtime.wait(context.Background(), fixture.request.RunID); err != nil {
		t.Fatal(err)
	}
	if result, _ := runtime.get(fixture.request.RunID, true); result.Status != "done" || result.Text != "native PTY result" {
		t.Fatal(result)
	}
}

func TestRunProcessHelper(t *testing.T) {
	stage := os.Getenv("WT_RUN_PROCESS_FIXTURE")
	if stage == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Printf("%s %d\n", stage, os.Getpid())
	if stage != "escape" {
		next := "child"
		if stage == "child" {
			next = "escape"
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRunProcessHelper$")
		cmd.Env = append(os.Environ(), "WT_RUN_PROCESS_FIXTURE="+next)
		cmd.Stdout = os.Stdout
		if next == "escape" {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := cmd.Start(); err != nil {
			panic(err)
		}
	}
	wait := make(chan os.Signal, 1)
	signal.Notify(wait, syscall.SIGUSR1)
	<-wait
	os.Exit(0)
}

func TestDeadlineKillsProcessGroupAndReportsSurvivors(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunProcessHelper$")
	cmd.Env = append(os.Environ(), "WT_RUN_PROCESS_FIXTURE=root")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pids := make(map[string]int)
	scanner := bufio.NewScanner(stdout)
	for len(pids) < 3 && scanner.Scan() {
		var stage string
		var pid int
		if _, err := fmt.Sscanf(scanner.Text(), "%s %d", &stage, &pid); err != nil {
			t.Fatal(err)
		}
		pids[stage] = pid
	}
	if len(pids) != 3 {
		t.Fatal("provider descendants did not signal readiness")
	}
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	sess := &Session{PID: cmd.Process.Pid, processGroupID: cmd.Process.Pid, cmd: cmd, done: done}
	tree := &runProcessTree{root: sess.PID}
	// The real run timer triggers the same process-group containment path. A
	// synthetic past deadline avoids a correctness dependency on sleeping.
	fixture := newRunFixture(t)
	fixture.runtime.backend.Kill = func() ([]RunDescendant, error) { return tree.kill(sess) }
	fixture.request.Deadline = time.Now().Add(-time.Second)
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	result := waitRunFixture(t, fixture)
	if result.Status != "timeout" || len(result.SurvivingDescendants) != 1 || result.SurvivingDescendants[0].PID != pids["escape"] {
		t.Fatalf("containment result: %+v", result)
	}
	<-done
	if err := syscall.Kill(pids["escape"], 0); err != nil {
		t.Fatalf("setsid survivor not alive: %v", err)
	}
	snapshot, err := processSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, alive := snapshot[pids["root"]]; alive {
		t.Fatal("root survived process-group kill")
	}
	if _, alive := snapshot[pids["child"]]; alive {
		t.Fatal("child survived process-group kill")
	}
}
