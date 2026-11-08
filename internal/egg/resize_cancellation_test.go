package egg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func blockedResizeFixture(t *testing.T, exclusive bool) (*Server, *Session, chan vtermMsg) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	dir := testsocket.Dir(t)
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "cols=80\nrows=24\n")
	queue := make(chan vtermMsg, 1)
	queue <- vtermMsg{data: []byte("pending")}
	sess := &Session{ptmx: master, vtermCh: queue, done: make(chan struct{})}
	return &Server{dir: dir, session: sess, exclusiveInput: exclusive}, sess, queue
}

func assertResizeUntouched(t *testing.T, s *Server, sess *Session, queue chan vtermMsg) {
	t.Helper()
	if msg := <-queue; string(msg.data) != "pending" {
		t.Fatalf("existing queue message lost: %+v", msg)
	}
	select {
	case msg := <-queue:
		t.Fatalf("cancelled resize reached queue: %+v", msg)
	default:
	}
	size, err := pty.GetsizeFull(sess.ptmx)
	if err != nil || size.Cols != 80 || size.Rows != 24 {
		t.Fatalf("PTY changed: %+v, %v", size, err)
	}
	meta, err := os.ReadFile(filepath.Join(s.dir, "egg.meta"))
	if err != nil || string(meta) != "cols=80\nrows=24\n" {
		t.Fatalf("metadata changed: %q, %v", meta, err)
	}
}

func receiveResizeError(t *testing.T, result <-chan error, want codes.Code) {
	t.Helper()
	select {
	case err := <-result:
		if status.Code(err) != want {
			t.Fatalf("resize returned %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("resize did not stop")
	}
}

func waitResizeGate(t *testing.T, sess *Session) {
	t.Helper()
	sess.resizeOnce.Do(func() { sess.resizeGate = make(chan struct{}, 1) })
	deadline := time.After(time.Second)
	for len(sess.resizeGate) != 1 {
		select {
		case <-deadline:
			t.Fatal("resize did not enter")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestResizeFullQueueDeadlineAndSerializedWait(t *testing.T) {
	s, sess, queue := blockedResizeFixture(t, false)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first := make(chan error, 1)
	go func() { first <- s.resizeSession(firstCtx, sess, 35, 110) }()
	// The first request owns serialization and is waiting on the full queue.
	waitResizeGate(t, sess)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Resize(ctx, &pb.ResizeRequest{Cols: 120, Rows: 40})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("serialized wait ignored deadline: %v", err)
	}
	cancelFirst()
	receiveResizeError(t, first, codes.Canceled)
	assertResizeUntouched(t, s, sess, queue)
}

func TestResizeFullQueueDeadlineAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "shutdown"}[shutdown], func(t *testing.T) {
			s, sess, queue := blockedResizeFixture(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := s.Resize(ctx, &pb.ResizeRequest{Cols: 110, Rows: 35}); result <- err }()
			want := codes.DeadlineExceeded
			if shutdown {
				waitResizeGate(t, sess)
				close(sess.done)
				want = codes.Unavailable
			}
			receiveResizeError(t, result, want)
			assertResizeUntouched(t, s, sess, queue)
		})
	}
}

func TestResizeFullQueueLeaseTakeoverAndTimeout(t *testing.T) {
	for _, takeover := range []bool{true, false} {
		t.Run(map[bool]string{true: "takeover", false: "lease_timeout"}[takeover], func(t *testing.T) {
			s, sess, queue := blockedResizeFixture(t, true)
			first := s.inputLease.register(&pb.AttachOptions{Owner: "first"})
			if err := s.inputLease.claim(first, false); err != nil {
				t.Fatal(err)
			}
			info := s.inputLease.info(first)
			result := make(chan error, 1)
			go func() {
				_, err := s.Resize(context.Background(), &pb.ResizeRequest{AttachmentId: info.AttachmentId, AttachmentToken: info.AttachmentToken, InputEpoch: info.InputEpoch, Cols: 110, Rows: 35})
				result <- err
			}()
			deadline := time.After(time.Second)
			for {
				s.inputLease.mu.Lock()
				active := s.inputLease.activeDone != nil
				s.inputLease.mu.Unlock()
				if active {
					break
				}
				select {
				case <-deadline:
					t.Fatal("resize did not become active")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if takeover {
				second := s.inputLease.register(&pb.AttachOptions{Owner: "second"})
				if err := s.inputLease.claim(second, true); err != nil {
					t.Fatalf("takeover failed: %v", err)
				}
				receiveResizeError(t, result, codes.Canceled)
			} else {
				select {
				case err := <-result:
					if status.Code(err) != codes.DeadlineExceeded {
						t.Fatalf("lease timeout: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("lease timeout did not stop resize")
				}
			}
			assertResizeUntouched(t, s, sess, queue)
		})
	}
}

type resizeTestStream struct {
	pb.Egg_SessionServer
	ctx      context.Context
	messages chan *pb.SessionMsg
}

func (s *resizeTestStream) Context() context.Context  { return s.ctx }
func (s *resizeTestStream) Send(*pb.SessionMsg) error { return nil }
func (s *resizeTestStream) Recv() (*pb.SessionMsg, error) {
	select {
	case msg := <-s.messages:
		return msg, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func TestSessionResizeFullQueueCancellation(t *testing.T) {
	for _, initial := range []bool{true, false} {
		for _, mode := range []string{"stable-cancel", "preview-cancel", "preview-takeover"} {
			name := "stream"
			if initial {
				name = "initial"
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				exclusive := mode != "stable-cancel"
				s, sess, queue := blockedResizeFixture(t, exclusive)
				sess.replay = newReplayBuffer("")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stream := &resizeTestStream{ctx: ctx, messages: make(chan *pb.SessionMsg, 2)}
				options := &pb.AttachOptions{Owner: "first", Claim: true}
				if initial {
					options.Cols, options.Rows = 110, 35
				}
				stream.messages <- &pb.SessionMsg{AttachOptions: options, Payload: &pb.SessionMsg_Attach{Attach: true}}
				if !initial {
					stream.messages <- &pb.SessionMsg{Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Cols: 110, Rows: 35}}}
				}
				result := make(chan error, 1)
				go func() { result <- s.Session(stream) }()
				waitResizeGate(t, sess)
				if mode == "preview-takeover" {
					second := s.inputLease.register(&pb.AttachOptions{Owner: "second"})
					if err := s.inputLease.claim(second, true); err != nil {
						t.Fatalf("takeover failed: %v", err)
					}
				} else {
					cancel()
				}
				receiveResizeError(t, result, codes.Canceled)
				assertResizeUntouched(t, s, sess, queue)
			})
		}
	}
}

// Cancellation racing a ready send can leave a resize message in the queue.
// Its decision must preserve subsequent output and the emulator's dimensions.
func TestVTermCancelledResizeDecisionPreservesQueue(t *testing.T) {
	h := newAsyncVTermHarness(80, 24, 4)
	defer h.close()
	decision := make(chan bool, 1)
	h.vtermCh <- vtermMsg{resize: &vtermResize{110, 35}, resizeDecision: decision}
	decision <- false
	h.vtermCh <- vtermMsg{data: []byte("after"), offset: 5}
	fence := h.fence()
	h.vterm.mu.Lock()
	cols, rows := h.vterm.cols, h.vterm.rows
	h.vterm.mu.Unlock()
	if cols != 80 || rows != 24 || fence.Offset != 5 || !strings.Contains(string(fence.Snapshot), "after") {
		t.Fatalf("cancelled resize or subsequent data was mishandled: %dx%d offset=%d", cols, rows, fence.Offset)
	}
}

func TestResizePTYFailurePreservesVTermAndMetadata(t *testing.T) {
	for _, mode := range []string{"stable", "exclusive", "nonblocking"} {
		for _, failure := range []string{"closed", "not-a-pty"} {
			for _, entry := range []string{"rpc", "initial", "stream"} {
				t.Run(mode+"/"+failure+"/"+entry, func(t *testing.T) {
					s, sess, queue := blockedResizeFixture(t, mode == "exclusive")
					<-queue
					if err := sess.ptmx.Close(); err != nil {
						t.Fatal(err)
					}
					if failure == "not-a-pty" {
						file, err := os.OpenFile(filepath.Join(s.dir, "ordinary-file"), os.O_CREATE|os.O_RDWR, 0600)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { file.Close() })
						sess.ptmx = file
					}
					h := newAsyncVTermHarness(80, 24, 4)
					defer h.close()
					sess.vtermCh, sess.replay = h.vtermCh, h.replay
					sess.nonblockInput = mode == "nonblocking"
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					result := make(chan error, 1)
					go func() {
						if entry == "rpc" {
							req := &pb.ResizeRequest{Cols: 110, Rows: 35}
							if s.exclusiveInput {
								attachment := s.inputLease.register(&pb.AttachOptions{Owner: "first"})
								if err := s.inputLease.claim(attachment, false); err != nil {
									result <- err
									return
								}
								info := s.inputLease.info(attachment)
								req.AttachmentId, req.AttachmentToken, req.InputEpoch = info.AttachmentId, info.AttachmentToken, info.InputEpoch
							}
							_, err := s.Resize(ctx, req)
							result <- err
							return
						}
						stream := &resizeTestStream{ctx: ctx, messages: make(chan *pb.SessionMsg, 2)}
						options := &pb.AttachOptions{Owner: "first", Claim: true}
						if entry == "initial" {
							options.Cols, options.Rows = 110, 35
						}
						stream.messages <- &pb.SessionMsg{AttachOptions: options, Payload: &pb.SessionMsg_Attach{Attach: true}}
						if entry == "stream" {
							stream.messages <- &pb.SessionMsg{Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Cols: 110, Rows: 35}}}
						}
						result <- s.Session(stream)
					}()
					receiveResizeError(t, result, codes.Internal)
					// A fence observes the decision and any output queued behind it.
					h.vtermCh <- vtermMsg{data: []byte("after"), offset: 5}
					fence := h.fence()
					h.vterm.mu.Lock()
					cols, rows := h.vterm.cols, h.vterm.rows
					h.vterm.mu.Unlock()
					if cols != 80 || rows != 24 || fence.Offset != 5 || !strings.Contains(string(fence.Snapshot), "after") {
						t.Fatalf("failed PTY resize changed emulator or lost output: %dx%d offset=%d", cols, rows, fence.Offset)
					}
					meta, err := os.ReadFile(filepath.Join(s.dir, "egg.meta"))
					if err != nil || string(meta) != "cols=80\nrows=24\n" {
						t.Fatalf("failed PTY resize changed metadata: %q, %v", meta, err)
					}
				})
			}
		}
	}
}
