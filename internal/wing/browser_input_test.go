package wing

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"google.golang.org/grpc"
)

type browserResizeRecorder struct {
	pb.UnimplementedEggServer
	mu       sync.Mutex
	requests []*pb.ResizeRequest
}

func (r *browserResizeRecorder) Resize(_ context.Context, request *pb.ResizeRequest) (*pb.ResizeResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return &pb.ResizeResponse{}, nil
}

func TestPreviewBrowserInputKeepsConnectionAndEpochBound(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	root, err := os.MkdirTemp("/tmp", "wt-browser-lease-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket, token := filepath.Join(root, "egg.sock"), filepath.Join(root, "egg.token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	recorder := &browserResizeRecorder{}
	pb.RegisterEggServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := egg.Dial(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstCanceled := false
	first := &browserInputController{id: "first-connection", publicKey: "tab-key", userID: "user", client: client, lease: &pb.AttachmentInfo{AttachmentId: "stream-1", InputEpoch: 1, AttachmentToken: "private-1"}, cancel: func() { firstCanceled = true }}
	sessionID := "fixture-session"
	browserInputs.Store(sessionID, first)
	t.Cleanup(func() { browserInputs.Delete(sessionID) })
	for _, args := range [][3]string{{"wrong", "tab-key", "user"}, {first.id, "other-tab", "user"}, {first.id, "tab-key", "other-user"}, {"", "tab-key", "user"}} {
		if err := resizeBrowserInput(ctx, sessionID, args[0], args[1], args[2], 30, 100); err == nil {
			t.Fatalf("foreign resize accepted: %v", args)
		}
	}
	if err := resizeBrowserInput(ctx, sessionID, first.id, first.publicKey, first.userID, 30, 100); err != nil {
		t.Fatal(err)
	}
	bound, ok := browserDataChannelBinding(sessionID, "tab-key", "user")
	if !ok || bound != first {
		t.Fatal("current DC rejected")
	}
	if _, ok := browserDataChannelBinding(sessionID, "foreign-key", "user"); ok {
		t.Fatal("foreign DC admitted")
	}
	secondCanceled := false
	second := &browserInputController{id: "replacement", publicKey: "tab-key", userID: "user", client: client, lease: &pb.AttachmentInfo{AttachmentId: "stream-2", InputEpoch: 2, AttachmentToken: "private-2"}, cancel: func() { secondCanceled = true }}
	browserInputs.Store(sessionID, second)
	if currentBrowserInput(sessionID, bound) {
		t.Fatal("old DC adopted replacement despite same tab key")
	}
	if err := resizeBrowserInput(ctx, sessionID, first.id, first.publicKey, first.userID, 40, 120); err == nil {
		t.Fatal("old tab resized replacement")
	}
	if err := resizeBrowserStream(ctx, sessionID, second.id, nil, 40, 120); err == nil {
		t.Fatal("unacknowledged stream resized")
	}
	if releaseBrowserInput(sessionID, first.id) || firstCanceled || secondCanceled {
		t.Fatal("late old detach displaced current controller")
	}
	if err := resizeBrowserInput(ctx, sessionID, second.id, second.publicKey, second.userID, 40, 120); err != nil {
		t.Fatal(err)
	}
	if !releaseBrowserInput(sessionID, second.id) || !secondCanceled {
		t.Fatal("current detach did not release")
	}
	if err := resizeBrowserInput(ctx, sessionID, second.id, second.publicKey, second.userID, 50, 130); err == nil {
		t.Fatal("detached controller resized")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 2 {
		t.Fatalf("rejected resize reached RPC: %d", len(recorder.requests))
	}
	if got := recorder.requests[0]; got.AttachmentId != "stream-1" || got.InputEpoch != 1 || got.AttachmentToken != "private-1" {
		t.Fatalf("first resize borrowed lease: %v", got)
	}
	if got := recorder.requests[1]; got.AttachmentId != "stream-2" || got.InputEpoch != 2 || got.AttachmentToken != "private-2" {
		t.Fatalf("second resize wrong lease: %v", got)
	}
}

func TestPendingBrowserDetachCannotCancelDifferentAttempt(t *testing.T) {
	pending := newPendingReattachAuths()
	defer pending.close()
	pending.put(ws.PTYAttach{ControllerID: "controller"}, []byte("c"), "controller", time.Minute)
	pending.put(ws.PTYAttach{Spectate: true, ViewerID: "viewer"}, []byte("v"), "viewer", time.Minute)
	pending.detach(ws.PTYDetach{ControllerID: "stale"})
	if len(pending.byViewer) != 2 {
		t.Fatal("stale detach removed current attempts")
	}
	pending.detach(ws.PTYDetach{ControllerID: "controller"})
	if _, ok := pending.take(""); ok {
		t.Fatal("canceled controller can authenticate later")
	}
	if _, ok := pending.take("viewer"); !ok {
		t.Fatal("controller detach canceled spectator")
	}
}
