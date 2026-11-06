package eggclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

type reconnectEvents struct{ events chan string }

func (w reconnectEvents) Write(p []byte) (int, error) { w.events <- string(p); return len(p), nil }
func reconnectEvent(t *testing.T, ch <-chan string, want string) string {
	t.Helper()
	for {
		select {
		case event := <-ch:
			if strings.Contains(event, want) {
				return event
			}
		case <-t.Context().Done():
			t.Fatal("event wait canceled")
			return ""
		}
	}
}

type reconnectTick struct {
	delay   time.Duration
	release chan struct{}
}
type reconnectClock struct {
	ticks       chan reconnectTick
	realBackoff bool
}

func (c reconnectClock) wait(ctx context.Context, delay time.Duration) error {
	tick := reconnectTick{delay: delay, release: make(chan struct{})}
	select {
	case c.ticks <- tick:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-tick.release:
		if c.realBackoff {
			return reconnectWait(ctx, delay)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type reconnectProvider struct {
	mu        sync.Mutex
	process   *exec.Cmd
	input     io.WriteCloser
	output    *bufio.Reader
	markers   map[string]int
	screen    string
	wrong     atomic.Bool
	exit      atomic.Int32
	ids       []string
	takeovers []bool
}

func (p *reconnectProvider) exchange(line string) (string, error) {
	if _, err := io.WriteString(p.input, line); err != nil {
		return "", err
	}
	return p.output.ReadString('\n')
}
func reconnectFixture(t *testing.T) (*testssh.Harness, config.Remote, *reconnectProvider) {
	t.Helper()
	return reconnectFixtureWithHandshake(t, func(bool) {})
}

func reconnectFixtureWithHandshake(t *testing.T, handshake func(bool)) (*testssh.Harness, config.Remote, *reconnectProvider) {
	t.Helper()
	h := testssh.New(t)
	dir := filepath.Join(h.Root, "wing")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/cat")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &reconnectProvider{process: cmd, input: in, output: bufio.NewReader(out), markers: map[string]int{}, screen: "current screen\n"}
	t.Cleanup(func() { in.Close(); cmd.Wait() })
	server, err := controlsocket.Listen(t.Context(), dir, "fixture-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		handshake(false)
		return controlsocket.Welcome{}, func(_ context.Context, request control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: request.ID}
		}, nil
	}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		handshake(true)
		a := hello.Attach
		if a.Session != "full-session-id" && (a.Exact || a.Session != "friendly") {
			return controlsocket.Welcome{}, nil, errors.New("exact session ID required")
		}
		p.mu.Lock()
		p.ids = append(p.ids, a.Session)
		p.takeovers = append(p.takeovers, a.Takeover)
		p.mu.Unlock()
		return controlsocket.Welcome{}, func(ctx context.Context, stream *controlsocket.SessionStream) error {
			id := "full-session-id"
			if p.wrong.Swap(false) {
				id = "different-session-id"
			}
			if code := p.exit.Load(); code != 0 {
				return stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_ExitCode{ExitCode: code}})
			}
			p.mu.Lock()
			snapshot := p.screen
			p.mu.Unlock()
			if err := stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Output{Output: []byte(snapshot)}}); err != nil {
				return err
			}
			for {
				msg, err := stream.Recv()
				if err != nil {
					return err
				}
				if msg.GetDetach() {
					return nil
				}
				if msg.SessionId != "full-session-id" {
					return errors.New("input changed identity")
				}
				if data := msg.GetInput(); len(data) > 0 {
					p.mu.Lock()
					reply, err := p.exchange(string(data))
					if err == nil {
						p.markers[strings.TrimSpace(reply)]++
						p.screen = "current screen: " + reply
					}
					p.mu.Unlock()
					if err != nil {
						return err
					}
					if err := stream.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Output{Output: []byte(reply)}}); err != nil {
						return err
					}
				}
			}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	meta, err := sshcontrol.InspectLocal(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "fixture-peer", meta)
	return h, config.Remote{SSHTarget: "fixture-peer", WingthingDir: dir, WingID: meta.WingID, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}, p
}
func reconnectStart(t *testing.T, h *testssh.Harness, remote config.Remote, clock reconnectClock) (*os.File, chan string, chan string, chan attachResult) {
	t.Helper()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); writer.Close() })
	screen, diagnostic := make(chan string, 128), make(chan string, 128)
	done := make(chan attachResult, 1)
	go func() {
		detached, err := attachRemoteIO(t.Context(), remote, "friendly", input, reconnectEvents{screen}, reconnectEvents{diagnostic}, egg.AttachOptions{Takeover: true}, sshcontrol.Transport{SSHPath: h.SSHPath, SocketDir: h.Root}, clock.wait)
		done <- attachResult{detached: detached, err: err}
	}()
	return writer, screen, diagnostic, done
}

func TestRemoteReconnectHandshakeDrop(t *testing.T) {
	for _, phase := range []string{"control", "attachment"} {
		t.Run(phase, func(t *testing.T) {
			var interrupt atomic.Bool
			started, release := make(chan struct{}), make(chan struct{})
			h, remote, provider := reconnectFixtureWithHandshake(t, func(attachment bool) {
				if attachment == (phase == "attachment") && interrupt.Swap(false) {
					close(started)
					select {
					case <-release:
					case <-t.Context().Done():
					}
				}
			})
			clock := reconnectClock{ticks: make(chan reconnectTick, 8)}
			writer, screen, diagnostic, done := reconnectStart(t, h, remote, clock)
			forward := <-h.Started
			reconnectEvent(t, screen, "current screen")
			forward.Drop()
			tick := <-clock.ticks
			reconnectEvent(t, diagnostic, "connection lost")
			interrupt.Store(true)
			close(tick.release)
			broken := <-h.Started
			<-started // The server has read Hello but has not sent Welcome.
			broken.Drop()
			select {
			case tick = <-clock.ticks:
			case result := <-done:
				t.Fatalf("handshake loss ended attachment: %+v", result)
			}
			close(release)
			if tick.delay != 500*time.Millisecond {
				t.Fatalf("handshake retry backoff = %s", tick.delay)
			}
			close(tick.release)
			<-h.Started
			reconnectEvent(t, screen, "current screen")
			if _, err := io.WriteString(writer, "after-handshake-drop\n"); err != nil {
				t.Fatal(err)
			}
			reconnectEvent(t, screen, "after-handshake-drop")
			if _, err := writer.Write([]byte{2, 'q'}); err != nil {
				t.Fatal(err)
			}
			if result := <-done; result.err != nil || !result.detached {
				t.Fatalf("detach after recovery: %+v", result)
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if provider.markers["after-handshake-drop"] != 1 {
				t.Fatalf("provider markers = %v", provider.markers)
			}
			if len(provider.ids) < 2 || provider.ids[0] != "friendly" {
				t.Fatalf("handshake retry changed identity: %v", provider.ids)
			}
			for _, id := range provider.ids[1:] {
				if id != "full-session-id" {
					t.Fatalf("handshake retry changed identity: %v", provider.ids)
				}
			}
			select {
			case event := <-diagnostic:
				t.Fatalf("repeated outage status: %q", event)
			default:
			}
		})
	}
}

func TestRemoteReconnectTwentyTransportDrops(t *testing.T) {
	h, remote, provider := reconnectFixture(t)
	clock := reconnectClock{ticks: make(chan reconnectTick, 64), realBackoff: true}
	writer, screen, diagnostic, done := reconnectStart(t, h, remote, clock)
	forward := <-h.Started
	reconnectEvent(t, screen, "current screen")
	durations := make([]float64, 0, 20)
	for n := range 20 {
		started := time.Now()
		forward.Drop()
		tick := <-clock.ticks
		if tick.delay != 250*time.Millisecond {
			t.Fatalf("initial backoff = %s", tick.delay)
		}
		reconnectEvent(t, diagnostic, "connection lost")
		if _, err := io.WriteString(writer, "outage-input\n"); err != nil {
			t.Fatal(err)
		}
		reconnectEvent(t, diagnostic, "offline input dropped")
		close(tick.release)
		forward = <-h.Started
		snapshot := reconnectEvent(t, screen, "current screen")
		if n > 0 && !strings.Contains(snapshot, fmt.Sprintf("marker-%02d", n-1)) {
			t.Fatalf("stale snapshot: %q", snapshot)
		}
		durations = append(durations, float64(time.Since(started))/float64(time.Millisecond))
		marker := fmt.Sprintf("marker-%02d", n)
		if _, err := io.WriteString(writer, marker+"\n"); err != nil {
			t.Fatal(err)
		}
		reconnectEvent(t, screen, marker)
		if !strings.Contains(strings.Join(forward.Args, " "), "BatchMode=yes") || !strings.Contains(strings.Join(forward.Args, " "), "StrictHostKeyChecking=yes") {
			t.Fatalf("unsafe SSH args: %v", forward.Args)
		}
	}
	// A successful SSH connection to a different egg must never be adopted.
	provider.wrong.Store(true)
	forward.Drop()
	tick := <-clock.ticks
	close(tick.release)
	<-h.Started
	result := <-done
	if result.err == nil || !strings.Contains(result.err.Error(), "different session ID") {
		t.Fatalf("wrong-ID control: %+v", result)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.markers) != 20 {
		t.Fatalf("markers (including outage input) = %v", provider.markers)
	}
	for n := range 20 {
		if provider.markers[fmt.Sprintf("marker-%02d", n)] != 1 {
			t.Fatalf("marker count = %v", provider.markers)
		}
	}
	for n, id := range provider.ids {
		if n == 0 {
			if id != "friendly" {
				t.Fatal(id)
			}
		} else if id != "full-session-id" || provider.takeovers[n] {
			t.Fatalf("retry changed ID or repeated takeover: %v %v", provider.ids, provider.takeovers)
		}
	}
	if reply, err := provider.exchange("still-alive\n"); err != nil || reply != "still-alive\n" {
		t.Fatalf("provider died: %q %v", reply, err)
	}
	sort.Float64s(durations)
	evidence := map[string]any{"transport_kills": 20, "successful_reattachments": 20, "same_session_id": "full-session-id", "eggs_created": 1, "provider_pid_unchanged": true, "markers": provider.markers, "wrong_id_refused": true, "time_to_reattach_ms": map[string]any{"p50": durations[9], "p95": durations[18], "samples": durations}, "clock": "production backoff after event barrier; elapsed includes fixture outage/input barriers"}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("WT_RECONNECT_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoteReconnectDetachDuringOutage(t *testing.T) {
	h, remote, provider := reconnectFixture(t)
	clock := reconnectClock{ticks: make(chan reconnectTick, 8)}
	writer, screen, diagnostic, done := reconnectStart(t, h, remote, clock)
	forward := <-h.Started
	reconnectEvent(t, screen, "current screen")
	forward.Drop()
	<-clock.ticks
	reconnectEvent(t, diagnostic, "connection lost")
	if _, err := writer.Write([]byte{2, 'q'}); err != nil {
		t.Fatal(err)
	}
	if result := <-done; result.err != nil || !result.detached {
		t.Fatalf("outage detach: %+v", result)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if reply, err := provider.exchange("alive\n"); err != nil || reply != "alive\n" {
		t.Fatalf("detach stopped provider: %q %v", reply, err)
	}
	if len(provider.ids) != 1 {
		t.Fatalf("detach reattached: %v", provider.ids)
	}
}

func TestRemoteReconnectStopsForSecurityAndExit(t *testing.T) {
	for _, outcome := range []string{"host-key", "auth", "exit"} {
		t.Run(outcome, func(t *testing.T) {
			h, remote, provider := reconnectFixture(t)
			clock := reconnectClock{ticks: make(chan reconnectTick, 8)}
			_, screen, _, done := reconnectStart(t, h, remote, clock)
			forward := <-h.Started
			reconnectEvent(t, screen, "current screen")
			switch outcome {
			case "host-key":
				if err := os.WriteFile(filepath.Join(h.Root, "fixture-peer.changed-key"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "auth":
				if err := os.WriteFile(filepath.Join(h.Root, "fixture-peer.auth-failed"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "exit":
				provider.exit.Store(17)
			}
			forward.Drop()
			tick := <-clock.ticks
			close(tick.release)
			result := <-done
			if result.err == nil || result.detached {
				t.Fatalf("terminal outcome = %+v", result)
			}
			switch outcome {
			case "host-key":
				if !strings.Contains(result.err.Error(), "HOST IDENTIFICATION") {
					t.Fatal(result.err)
				}
			case "auth":
				if !strings.Contains(result.err.Error(), "Permission denied") {
					t.Fatal(result.err)
				}
			case "exit":
				var exit *SessionExitError
				if !errors.As(result.err, &exit) || exit.Code != 17 {
					t.Fatal(result.err)
				}
			}
			select {
			case tick := <-clock.ticks:
				t.Fatalf("terminal outcome retried: %+v", tick)
			default:
			}
		})
	}
}

func TestRemoteReconnectBackoffAndExitJournal(t *testing.T) {
	for _, tc := range []struct {
		input string
		code  int
		ok    bool
	}{
		{`{"type":"session_exit","source":"egg_process","exit_code":0}`, 0, true},
		{`{"type":"session_exit","source":"egg_process","exit_code":17}`, 17, true},
		{`{"type":"session_exit","source":"claude_hook","exit_code":0}`, 0, false},
		{`{"type":"session_exit","source":"egg_process"}`, 0, false},
	} {
		code, ok := ConfirmedSessionExit([]byte(tc.input))
		if code != tc.code || ok != tc.ok {
			t.Fatalf("journal %s: %d %t", tc.input, code, ok)
		}
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	var delays []time.Duration
	opens := 0
	_, err = attachReconnectingIO(t.Context(), "full-id", input, io.Discard, io.Discard, egg.AttachOptions{}, func(ctx context.Context, id string, exact bool, opts egg.AttachOptions) (SessionStream, error) {
		opens++
		if opens == 1 {
			return &attachTestStream{messages: []*pb.SessionMsg{{SessionId: "full-id", Payload: &pb.SessionMsg_Output{Output: []byte("screen")}}}}, nil
		}
		if opens < 8 {
			return nil, io.EOF
		}
		return &attachTestStream{messages: []*pb.SessionMsg{{SessionId: "full-id", Payload: &pb.SessionMsg_ExitCode{ExitCode: 17}}}}, nil
	}, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	var exit *SessionExitError
	if !errors.As(err, &exit) || exit.Code != 17 {
		t.Fatal(err)
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	if !reflect.DeepEqual(delays, want) {
		t.Fatalf("backoff = %v", delays)
	}
	// A terminal error never becomes a retryable transport failure.
	for _, message := range []string{"Permission denied (publickey)", "Host key verification failed", "REMOTE HOST IDENTIFICATION HAS CHANGED!"} {
		if retryAttachment(errors.New("SSH forward closed: " + message)) {
			t.Fatal(message)
		}
	}
	for _, message := range []string{"pinned wing identity mismatch (wing ID)", "incompatible local wing handshake", "incompatible wing attachment handshake"} {
		if retryAttachment(fmt.Errorf("verify forwarded wing: %w", errors.New(message))) {
			t.Fatal(message)
		}
	}
}

type reconnectGenerationStream struct {
	attachTestStream
	generation uint64
}

func (s *reconnectGenerationStream) InputGeneration() uint64 { return s.generation }

type reconnectChunkReader struct {
	stream *reconnectGenerationStream
	chunks [][]byte
}

func (r *reconnectChunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	r.stream.generation++
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(p, chunk), nil
}
func TestRemoteReconnectDropsPartialOutageChord(t *testing.T) {
	stream := &reconnectGenerationStream{}
	input := &reconnectChunkReader{stream: stream, chunks: [][]byte{{2}, {'x'}}}
	done := make(chan attachInputResult, 1)
	sendAttachedInput(&serializedSessionStream{SessionStream: stream}, "full-id", input, done)
	if result := <-done; result.err != nil || !result.detached {
		t.Fatal(result)
	}
	if len(stream.sent) != 2 || string(stream.sent[0].GetInput()) != "x" || !stream.sent[1].GetDetach() {
		t.Fatalf("partial outage chord replayed: %v", stream.sent)
	}
}

func TestRemoteReconnectStdioPeer(t *testing.T) {
	if os.Getenv("WT_RECONNECT_STDIO_PEER") != "1" {
		return
	}
	err := ServeAttachmentIO(context.Background(), os.Stdin, os.Stdout, func(_ context.Context, request AttachmentRequest) (SessionStream, error) {
		data, _ := json.Marshal(request)
		return &attachTestStream{messages: []*pb.SessionMsg{
			{SessionId: "full-session-id", Payload: &pb.SessionMsg_Output{Output: data}},
			{SessionId: "full-session-id", Payload: &pb.SessionMsg_ExitCode{ExitCode: 17}},
		}}, nil
	})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func TestRemoteReconnectStdioSavedRoute(t *testing.T) {
	t.Setenv("WT_RECONNECT_STDIO_PEER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ssh := filepath.Join(root, "ssh")
	log := filepath.Join(root, "route")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + quote(log) + "\nexec " + quote(executable) + " -test.run=^TestRemoteReconnectStdioPeer$\n"
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	var output, diagnostic strings.Builder
	detached, err := AttachRemoteIO(t.Context(), config.Remote{SSHTarget: "saved-peer", WTBinary: "/custom wt/wt", WingthingDir: "/saved state"}, "friendly", input, &output, &diagnostic, egg.AttachOptions{Takeover: true}, sshcontrol.Transport{SSHPath: ssh})
	var exit *SessionExitError
	if detached || !errors.As(err, &exit) || exit.Code != 17 {
		t.Fatalf("stdio result: %t %v", detached, err)
	}
	var request AttachmentRequest
	if err := json.Unmarshal([]byte(output.String()), &request); err != nil || request.Session != "friendly" || !request.Options.Takeover {
		t.Fatalf("request = %s: %v", output.String(), err)
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "UpdateHostKeys=no", "saved-peer", "'/custom wt/wt'", "'/saved state'", "'attach' '--attachment-stream'"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("missing %s in %s", want, args)
		}
	}
	if !strings.Contains(diagnostic.String(), "session exited with code 17") {
		t.Fatal(diagnostic.String())
	}
}

func TestRemoteReconnectTruncatedSSHFrame(t *testing.T) {
	root := t.TempDir()
	ssh, requests, received := filepath.Join(root, "ssh"), filepath.Join(root, "requests"), filepath.Join(root, "received")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	frame := func(msg *pb.SessionMsg) string {
		var output strings.Builder
		if err := newAttachmentWire(strings.NewReader(""), &output).Send(msg); err != nil {
			t.Fatal(err)
		}
		return quote(output.String())
	}
	screenFrame := func(screen string) string {
		return frame(&pb.SessionMsg{SessionId: "full-session-id", Payload: &pb.SessionMsg_Output{Output: []byte(screen)}})
	}
	// The first SSH process loses its transport in the middle of a frame.
	// The second process waits for input, so recovery is ordered by events.
	script := "#!/bin/sh\n" +
		"IFS= read -r request || exit 1\n" +
		"if [ ! -e " + quote(requests) + " ]; then\n" +
		"  printf '%s\\n' \"$request\" > " + quote(requests) + "\n" +
		"  printf '%s' " + screenFrame("initial screen") + "\n" +
		"  printf '%s' '{\"message\":{\"sessionId\":\"full-session-id\",\"output\":\"'\n" +
		"  exit 255\nfi\n" +
		"printf '%s\\n' \"$request\" >> " + quote(requests) + "\n" +
		"printf '%s' " + screenFrame("current screen") + "\n" +
		"IFS= read -r input || exit 1\n" +
		"printf '%s\\n' \"$input\" > " + quote(received) + "\n" +
		"printf '%s' " + frame(&pb.SessionMsg{SessionId: "full-session-id", Payload: &pb.SessionMsg_ExitCode{ExitCode: 17}}) + "\n"
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	clock := reconnectClock{ticks: make(chan reconnectTick, 8)}
	writer, screen, diagnostic, done := reconnectStart(t, &testssh.Harness{SSHPath: ssh, Root: root}, config.Remote{SSHTarget: "saved-peer"}, clock)
	reconnectEvent(t, screen, "initial screen")
	select {
	case tick := <-clock.ticks:
		if tick.delay != 250*time.Millisecond {
			t.Fatalf("initial backoff = %s", tick.delay)
		}
		reconnectEvent(t, diagnostic, "connection lost")
		close(tick.release)
	case result := <-done:
		t.Fatalf("truncated SSH frame ended attachment: detached=%t error=%v", result.detached, result.err)
	}
	reconnectEvent(t, screen, "current screen")
	if _, err := io.WriteString(writer, "after-truncation\n"); err != nil {
		t.Fatal(err)
	}
	result := <-done
	var exit *SessionExitError
	if result.detached || !errors.As(result.err, &exit) || exit.Code != 17 {
		t.Fatalf("recovered attachment result: %+v", result)
	}
	data, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("SSH attempts = %d: %s", len(lines), data)
	}
	for i, line := range lines {
		var request AttachmentRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		if i == 0 && (request.Session != "friendly" || request.Exact) || i == 1 && (request.Session != "full-session-id" || !request.Exact || request.Options.Takeover) {
			t.Fatalf("request %d changed session identity: %+v", i, request)
		}
	}
	data, err = os.ReadFile(received)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := newAttachmentWire(strings.NewReader(string(data)), io.Discard).Recv()
	if err != nil || msg.SessionId != "full-session-id" || string(msg.GetInput()) != "after-truncation\n" {
		t.Fatalf("input after recovery = %s: %v", data, err)
	}
	t.Log("truncated SSH transport recovered in two attempts with the same session ID, current screen, and post-recovery input")
}
