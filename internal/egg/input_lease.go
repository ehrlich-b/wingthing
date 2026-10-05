package egg

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An egg owns exactly one PTY. The lease lives here rather than in a transport
// adapter, so browser, CLI, and MCP cannot independently claim the same input.
type inputLease struct {
	mu         sync.Mutex
	sequence   uint64
	epoch      uint64
	writer     *inputAttachment
	operations chan struct{}
	cancel     context.CancelFunc
	activeDone chan struct{}
}

const inputMutationTimeout = 2 * time.Second

type inputAttachment struct {
	id         string
	token      string
	owner      string
	readOnly   bool
	epoch      uint64
	revoked    chan struct{}
	wasRevoked bool
}

func (l *inputLease) register(options *pb.AttachOptions) *inputAttachment {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	a := &inputAttachment{id: fmt.Sprintf("attachment-%d", l.sequence), token: rand.Text(), revoked: make(chan struct{})}
	if options != nil {
		a.owner, a.readOnly = options.Owner, options.ReadOnly
	}
	if a.owner == "" {
		a.owner = a.id
	}
	return a
}

func (l *inputLease) claim(a *inputAttachment, takeover bool) error {
	l.mu.Lock()
	previous := l.writer
	err := l.claimLocked(a, takeover)
	done := l.activeDone
	l.mu.Unlock()
	if err != nil || previous == nil || previous == a || done == nil {
		return err
	}
	// Acknowledge takeover only once the cancelled writer has stopped. Lease
	// metadata stays available while waiting for the serialized operation.
	timer := time.NewTimer(inputMutationTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return status.Error(codes.DeadlineExceeded, "previous terminal input operation did not stop")
	}
}

func (l *inputLease) claimLocked(a *inputAttachment, takeover bool) error {
	if a.readOnly {
		return status.Error(codes.PermissionDenied, "read-only attachment cannot write or resize")
	}
	if a.wasRevoked {
		return status.Error(codes.Aborted, "terminal attachment was taken over; attach again before requesting control")
	}
	if l.writer == a {
		return nil
	}
	if l.writer != nil {
		if !takeover {
			return status.Errorf(codes.FailedPrecondition, "terminal input owned by %s; use explicit takeover to take control", l.writer.owner)
		}
		l.writer.wasRevoked = true
		close(l.writer.revoked)
		if l.cancel != nil {
			l.cancel()
		}
	}
	l.epoch++
	a.epoch = l.epoch
	l.writer = a
	return nil
}

// Serialize operations separately from lease metadata. Takeover cancels the
// active write; the next writer waits for it to stop before touching the PTY.
func (l *inputLease) mutation(ctx context.Context, a *inputAttachment, operation func(context.Context) error) error {
	return l.mutate(ctx, func() error { return l.claimLocked(a, false) }, operation)
}

func (l *inputLease) mutate(ctx context.Context, authorize func() error, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, inputMutationTimeout)
	defer cancel()
	l.mu.Lock()
	if err := authorize(); err != nil {
		l.mu.Unlock()
		return err
	}
	if l.operations == nil {
		l.operations = make(chan struct{}, 1)
	}
	operations := l.operations
	l.mu.Unlock()
	select {
	case operations <- struct{}{}:
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
	defer func() { <-operations }()
	l.mu.Lock()
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return status.FromContextError(err).Err()
	}
	if err := authorize(); err != nil {
		l.mu.Unlock()
		return err
	}
	l.cancel = cancel
	l.activeDone = make(chan struct{})
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.cancel = nil
		close(l.activeDone)
		l.activeDone = nil
		l.mu.Unlock()
	}()
	return operation(ctx)
}

func (l *inputLease) resize(ctx context.Context, id string, epoch uint64, token string, operation func() error) error {
	return l.mutate(ctx, func() error {
		if l.writer == nil || l.writer.id != id || l.writer.epoch != epoch || l.writer.token != token || id == "" || epoch == 0 {
			return status.Error(codes.FailedPrecondition, "resize requires the current writer attachment and input epoch; resize through its attached stream")
		}
		return nil
	}, func(context.Context) error { return operation() })
}

func (l *inputLease) release(a *inputAttachment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == a {
		if l.cancel != nil {
			l.cancel()
		}
		l.writer = nil
	}
}

func (l *inputLease) info(a *inputAttachment) *pb.AttachmentInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	info := &pb.AttachmentInfo{InputEpoch: l.epoch}
	if a != nil {
		info.AttachmentId = a.id
	}
	if l.writer != nil {
		info.WriterId, info.WriterOwner = l.writer.id, l.writer.owner
		if l.writer == a {
			info.AttachmentToken = a.token
		}
	}
	return info
}
