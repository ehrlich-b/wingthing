package egg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInputLeaseArbitratesWritersObserversAndRevokedEpochs(t *testing.T) {
	var lease inputLease
	first := lease.register(&pb.AttachOptions{Owner: "first"})
	second := lease.register(&pb.AttachOptions{Owner: "second"})
	observer := lease.register(&pb.AttachOptions{ReadOnly: true})
	writes := 0
	write := func(context.Context) error { writes++; return nil }
	resize := func() error { return write(context.Background()) }
	if err := lease.mutation(context.Background(), first, write); err != nil {
		t.Fatal(err)
	}
	if err := lease.mutation(context.Background(), second, write); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second writer bypassed lease: %v", err)
	}
	if err := lease.mutation(context.Background(), observer, write); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("observer wrote: %v", err)
	}
	old := lease.info(first)
	if lease.info(observer).AttachmentToken != "" {
		t.Fatal("observer received writer resize token")
	}
	if err := lease.resize(context.Background(), old.WriterId, old.InputEpoch, "", resize); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("public status bypassed resize lease: %v", err)
	}
	if err := lease.claim(second, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.revoked:
	default:
		t.Fatal("takeover did not revoke old stream")
	}
	current := lease.info(second)
	if current.InputEpoch <= old.InputEpoch {
		t.Fatal("takeover did not advance epoch")
	}
	lease.release(first)
	if err := lease.resize(context.Background(), old.WriterId, old.InputEpoch, old.AttachmentToken, resize); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale resize token survived takeover: %v", err)
	}
	if err := lease.resize(context.Background(), current.WriterId, current.InputEpoch, current.AttachmentToken, resize); err != nil {
		t.Fatal(err)
	}
	lease.release(second)
	if err := lease.mutation(context.Background(), first, write); status.Code(err) != codes.Aborted {
		t.Fatalf("revoked stream reclaimed after winner detached: %v", err)
	}
	if writes != 2 {
		t.Fatalf("rejected mutations reached PTY: %d", writes)
	}
	third := lease.register(nil)
	if err := lease.mutation(context.Background(), third, write); err != nil {
		t.Fatalf("detach did not release writer: %v", err)
	}
}

func blockedInputPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	if _, err = term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}
	if err = preparePTYInput(master); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func TestInputLeaseBlockedPTYDoesNotBlockTakeoverObserverOrStatus(t *testing.T) {
	master, _ := blockedInputPTY(t)
	server := &Server{session: &Session{ID: "fixture", StartedAt: time.Now(), replay: newReplayBuffer("claude")}, exclusiveInput: true}
	lease := &server.inputLease
	first := lease.register(nil)
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- lease.mutation(context.Background(), first, func(ctx context.Context) error {
			close(started)
			return writePTYInput(ctx, master, []byte(strings.Repeat("x", 1<<20)))
		})
	}()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("PTY did not block: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	controlDone := make(chan error, 1)
	go func() {
		observer := lease.register(&pb.AttachOptions{ReadOnly: true})
		if lease.info(observer).AttachmentId == "" {
			controlDone <- errors.New("observer did not attach")
			return
		}
		if _, err := server.Status(context.Background(), &pb.StatusRequest{}); err != nil {
			controlDone <- err
			return
		}
		second := lease.register(nil)
		if err := lease.claim(second, true); err != nil {
			controlDone <- err
			return
		}
		controlDone <- lease.mutation(context.Background(), second, func(context.Context) error { return nil })
	}()
	select {
	case err := <-controlDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked PTY held lease metadata or serialized operation indefinitely")
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("takeover did not cancel old PTY write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old PTY write survived takeover")
	}
}

func TestInputLeaseBoundsPTYWritesAndSerializedWaits(t *testing.T) {
	master, _ := blockedInputPTY(t)
	var lease inputLease
	writer := lease.register(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := lease.mutation(ctx, writer, func(ctx context.Context) error {
		return writePTYInput(ctx, master, []byte(strings.Repeat("x", 1<<20)))
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write ignored deadline: %v", err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		completed <- lease.mutation(context.Background(), writer, func(context.Context) error {
			close(started)
			<-finish
			return nil
		})
	}()
	<-started
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := lease.mutation(ctx, writer, func(context.Context) error {
		t.Error("second operation interleaved with active write")
		return nil
	})
	close(finish)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("serialized wait ignored deadline: %v", err)
	}
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
	go func() {
		completed <- lease.mutation(context.Background(), writer, func(ctx context.Context) error {
			return writePTYInput(ctx, master, []byte(strings.Repeat("x", 1<<20)))
		})
	}()
	select {
	case err = <-completed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("write without caller deadline was not bounded: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("write without caller deadline blocked indefinitely")
	}
}

func TestInputLeaseTakeoverAcknowledgesAfterCancelledOperationStops(t *testing.T) {
	var lease inputLease
	first, second := lease.register(nil), lease.register(nil)
	started, stopping, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- lease.mutation(context.Background(), first, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopping)
			<-stopped
			return ctx.Err()
		})
	}()
	<-started
	claimed := make(chan error, 1)
	go func() { claimed <- lease.claim(second, true) }()
	select {
	case <-stopping:
	case <-time.After(time.Second):
		close(stopped)
		t.Fatal("takeover did not cancel active operation")
	}
	select {
	case err := <-claimed:
		close(stopped)
		t.Fatalf("takeover acknowledged before old operation stopped: %v", err)
	default:
	}
	// Metadata remains accessible during cancellation cleanup.
	if lease.info(nil).WriterId != second.id {
		t.Fatal("takeover failed to publish new writer")
	}
	close(stopped)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("old operation was not cancelled: %v", err)
	}
	if err := <-claimed; err != nil {
		t.Fatal(err)
	}
}

func TestInputLeasePTYResizePreservesCancellableWritesAndOutput(t *testing.T) {
	master, slave := blockedInputPTY(t)
	dir := t.TempDir()
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "cols=80\nrows=24\n")
	sess := &Session{ID: "fixture", ptmx: master, replay: newReplayBuffer("claude"), vtermCh: make(chan vtermMsg, 256)}
	server := &Server{dir: dir, session: sess, exclusiveInput: true}
	if err := server.resizeSession(sess, 30, 90); err != nil {
		t.Fatal(err)
	}
	// Fd() would restore blocking mode on Linux; inspect via SyscallConn.
	raw, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err = raw.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || flagErr != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("resize restored blocking PTY writes: flags=%d err=%v %v", flags, err, flagErr)
	}
	readDone := make(chan struct{})
	go func() { server.readPTY(sess); close(readDone) }()
	// Let Darwin's first nonblocking read reach EAGAIN before producing output.
	time.Sleep(20 * time.Millisecond)
	if _, err = slave.Write([]byte("output after resize")); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-sess.vtermCh:
		if msg.resize != nil { // the resize notification precedes PTY output
			select {
			case msg = <-sess.vtermCh:
			case <-time.After(time.Second):
				t.Fatal("nonblocking PTY output reader stopped after EAGAIN")
			}
		}
		if string(msg.data) != "output after resize" {
			t.Fatalf("PTY output changed: %q", msg.data)
		}
	case <-time.After(time.Second):
		t.Fatal("PTY output reader failed after resize")
	}
	_ = master.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("nonblocking PTY reader survived close")
	}
}
