package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nativePromptTransportFixture struct {
	pb.UnimplementedEggServer
	busy     bool
	onAttach func()
	attached chan *pb.SessionMsg
	input    chan []byte
	detached chan struct{}
	calls    atomic.Int32
	released sync.Once
}

func (f *nativePromptTransportFixture) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	f.calls.Add(1)
	defer f.released.Do(func() { close(f.detached) })
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.attached <- first
	if f.busy {
		return status.Error(codes.FailedPrecondition, "another fixture attachment owns input")
	}
	if f.onAttach != nil {
		f.onAttach()
	}
	if err = stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("fixture snapshot")}}); err != nil {
		return err
	}
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if input, ok := message.Payload.(*pb.SessionMsg_Input); ok {
			f.input <- append([]byte(nil), input.Input...)
		}
		if _, ok := message.Payload.(*pb.SessionMsg_Detach); ok {
			return nil
		}
	}
}

func nativePromptTransport(t *testing.T, busy bool) (*config.Config, *nativePromptTransportFixture) {
	t.Helper()
	if config.Channel() == "preview" {
		t.Skip("preview process identity is verified by the actual egg fixture in make test-integ")
	}
	root, err := os.MkdirTemp("/tmp", "wt-prompt-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "eggs", "fixture")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"egg.pid": fmt.Sprintf("%d\n", os.Getpid()), "egg.token": "private-fixture-token",
		"egg.meta": "kind=agent\nagent=claude\ncwd=" + root + "\nprovider_home=" + root + "\nprovider_session_id=ours\n",
	} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	spool := filepath.Join(root, ".claude", "wingthing-events", "fixture")
	if err = os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(spool, "session.json"), []byte(`{"session_id":"ours","hook_event_name":"SessionStart"}`), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	fixture := &nativePromptTransportFixture{busy: busy, attached: make(chan *pb.SessionMsg, 1), input: make(chan []byte, 3), detached: make(chan struct{})}
	pb.RegisterEggServer(server, fixture)
	go func() { _ = server.Serve(listener) }()
	return &config.Config{Dir: root}, fixture
}

func TestNativePromptTransportClaimsBeforeInputAndHoldsUntilRelease(t *testing.T) {
	cfg, fixture := nativePromptTransport(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := "first line\nsecond line\twith tab"
	delivery, err := sendPromptInput(ctx, cfg, "fixture", input, "mcp:fixture", "ours")
	if delivery.Release != nil {
		defer delivery.Release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if delivery.NoInputAttempted {
		t.Fatal("completed input transport still reported no input attempt")
	}
	first := <-fixture.attached
	if !first.GetAttach() || !first.AttachOptions.GetClaim() || first.AttachOptions.GetOwner() != "mcp:fixture" {
		t.Fatalf("prompt input preceded an eager writer claim: %v", first)
	}
	var frames [][]byte
	for len(frames) < 2 {
		select {
		case data := <-fixture.input:
			frames = append(frames, data)
		case <-ctx.Done():
			t.Fatal("prompt transport did not deliver both editor frames")
		}
	}
	if string(frames[0]) != "\x1b[200~"+input+"\x1b[201~" || string(frames[1]) != "\r" || delivery.BytesEnqueued != len(frames[0])+len(frames[1]) {
		t.Fatalf("multiline prompt split or transport evidence changed: %q bytes=%d", frames, delivery.BytesEnqueued)
	}
	select {
	case <-fixture.detached:
		t.Fatal("writer attachment released before receipt observation")
	default:
	}
	delivery.Release()
	select {
	case <-fixture.detached:
	case <-ctx.Done():
		t.Fatal("writer attachment did not detach after receipt observation")
	}
}

func TestNativePromptTransportBusyDoesNotEnqueue(t *testing.T) {
	cfg, fixture := nativePromptTransport(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	delivery, err := sendPromptInput(ctx, cfg, "fixture", "must remain unsent", "mcp:second", "ours")
	if status.Code(err) != codes.FailedPrecondition || !delivery.NoInputAttempted || delivery.BytesEnqueued != 0 || delivery.Release != nil {
		t.Fatalf("busy transport reported optimistic delivery: %+v %v", delivery, err)
	}
	select {
	case data := <-fixture.input:
		t.Fatalf("busy attachment enqueued input %q", data)
	default:
	}
}

func TestNativePromptTransportRechecksForegroundReadinessAfterClaim(t *testing.T) {
	cfg, fixture := nativePromptTransport(t, false)
	fixture.onAttach = func() {
		path := filepath.Join(cfg.Dir, ".claude", "wingthing-events", "fixture", "session.json")
		// Same published file is first imported only after the claim; it now
		// reports the human submission that raced the reservation read.
		if err := os.WriteFile(path, []byte(`{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"human work"}`), 0600); err != nil {
			t.Error(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	delivery, err := sendPromptInput(ctx, cfg, "fixture", "must remain unsent", "mcp:raced", "ours")
	if delivery.Release != nil {
		defer delivery.Release()
	}
	if err == nil || !delivery.NoInputAttempted || delivery.BytesEnqueued != 0 {
		t.Fatalf("readiness changed after claim but input was enqueued: %+v %v", delivery, err)
	}
	select {
	case data := <-fixture.input:
		t.Fatalf("working provider received prompt %q", data)
	default:
	}
}

func TestNativePromptTransportRejectsChangedExactProviderIdentity(t *testing.T) {
	cfg, fixture := nativePromptTransport(t, false)
	fixture.onAttach = func() {
		meta := "kind=agent\nagent=claude\ncwd=" + cfg.Dir + "\nprovider_home=" + cfg.Dir + "\nprovider_session_id=replacement\n"
		if err := os.WriteFile(filepath.Join(cfg.Dir, "eggs", "fixture", "egg.meta"), []byte(meta), 0600); err != nil {
			t.Error(err)
		}
		path := filepath.Join(cfg.Dir, ".claude", "wingthing-events", "fixture", "session.json")
		if err := os.WriteFile(path, []byte(`{"session_id":"replacement","hook_event_name":"SessionStart"}`), 0600); err != nil {
			t.Error(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	delivery, err := sendPromptInput(ctx, cfg, "fixture", "must remain unsent", "mcp:bound", "ours")
	if delivery.Release != nil {
		defer delivery.Release()
	}
	if err == nil || !delivery.NoInputAttempted || delivery.BytesEnqueued != 0 {
		t.Fatalf("replacement provider received reserved input: %+v %v", delivery, err)
	}
	select {
	case data := <-fixture.input:
		t.Fatalf("changed provider received prompt %q", data)
	default:
	}
}

func TestNativePromptPreflightNotSentIsDurable(t *testing.T) {
	for _, condition := range []string{"busy", "readiness", "identity"} {
		t.Run(condition, func(t *testing.T) {
			cfg, fixture := nativePromptTransport(t, condition == "busy")
			if condition != "busy" {
				fixture.onAttach = func() {
					payload := `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"human work"}`
					if condition == "identity" {
						meta := "kind=agent\nagent=claude\ncwd=" + cfg.Dir + "\nprovider_home=" + cfg.Dir + "\nprovider_session_id=replacement\n"
						if err := os.WriteFile(filepath.Join(cfg.Dir, "eggs", "fixture", "egg.meta"), []byte(meta), 0600); err != nil {
							t.Error(err)
						}
						payload = `{"session_id":"replacement","hook_event_name":"SessionStart"}`
					}
					path := filepath.Join(cfg.Dir, ".claude", "wingthing-events", "fixture", "late.json")
					if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
						t.Error(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			session, err := resolveLifecycleSession(cfg, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			result, err := promptSession(ctx, cfg, session, "proof-"+condition, "retain this draft", time.Second, "mcp:proof")
			if err != nil || result.Status != "not_sent" || !result.DefinitelyNotSent || result.TransportEnqueued || result.TransportBytesEnqueued != 0 || result.NativeReceiptObserved {
				t.Fatalf("preflight failure did not become typed not_sent: %+v %v", result, err)
			}
			wantReason := map[string]string{"busy": "another fixture attachment", "readiness": "readiness changed", "identity": "provider identity changed"}[condition]
			if !strings.Contains(result.Reason, wantReason) {
				t.Fatalf("actionable preflight reason lost: %q", result.Reason)
			}
			// The condition may resolve or the provider may be replaced. This
			// request remains terminal and cannot claim a second attachment.
			fixture.busy = false
			retry, err := promptSession(ctx, cfg, session, "proof-"+condition, "retain this draft", time.Second, "mcp:proof")
			if err != nil || retry.Status != "not_sent" || !retry.DefinitelyNotSent || !retry.Retried || fixture.calls.Load() != 1 {
				t.Fatalf("not_sent replay resent or lost its proof: %+v calls=%d %v", retry, fixture.calls.Load(), err)
			}
			select {
			case data := <-fixture.input:
				t.Fatalf("preflight failure sent prompt bytes %q", data)
			default:
			}
		})
	}
}
