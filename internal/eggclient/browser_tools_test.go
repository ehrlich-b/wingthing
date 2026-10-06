package eggclient

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
)

type browserToolsSessionFixture struct {
	pb.UnimplementedEggServer
	attached chan *pb.SessionMsg
	exit     chan struct{}
}

func (f *browserToolsSessionFixture) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.attached <- first
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("snapshot")}}); err != nil {
		return err
	}
	select {
	case <-f.exit:
		return stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_ExitCode{ExitCode: 0}})
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

func TestBrowserForkToolsLiveUntilSessionExit(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wt-tool-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cfg := &config.Config{Dir: root}
	opts := SpawnEggOpts{}
	listener, err := PrepareBrowserTools(cfg, "fork", []*config.ToolConfig{{Name: "tool", Run: "true"}}, &opts)
	if err != nil || listener == nil {
		t.Fatalf("prepare tools: %v", err)
	}
	dir := filepath.Join(root, "eggs", "fork")
	if err := os.WriteFile(filepath.Join(dir, "egg.token"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	fixture := &browserToolsSessionFixture{attached: make(chan *pb.SessionMsg, 1), exit: make(chan struct{})}
	pb.RegisterEggServer(server, fixture)
	go func() { _ = server.Serve(socket) }()
	client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		serveBrowserSessionTools(client, listener, "fork")
		close(done)
	}()
	select {
	case first := <-fixture.attached:
		if !first.AttachOptions.GetReadOnly() || first.AttachOptions.GetClaim() || first.AttachOptions.GetTakeover() {
			t.Fatalf("tool lifetime watcher claimed PTY input: %v", first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool lifetime watcher did not attach")
	}
	if conn, err := net.Dial("unix", opts.ToolSocketPath); err != nil {
		t.Fatalf("tools closed before fork exit: %v", err)
	} else {
		_ = conn.Close()
	}
	close(fixture.exit)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("tools did not close after fork exit")
	}
	if conn, err := net.Dial("unix", opts.ToolSocketPath); err == nil {
		_ = conn.Close()
		t.Fatal("tool socket still accepts calls after fork exit")
	}
}

func TestPrepareBrowserToolsWithoutTools(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	opts := SpawnEggOpts{}
	listener, err := PrepareBrowserTools(cfg, "fork", nil, &opts)
	if err != nil || listener != nil || opts.ToolSocketPath != "" || len(opts.ToolNames) != 0 {
		t.Fatalf("empty tools created a listener: %#v, %v", opts, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "eggs")); !os.IsNotExist(err) {
		t.Fatalf("empty tools created session artifacts: %v", err)
	}
}
