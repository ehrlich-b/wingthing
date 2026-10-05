package egg

import (
	"crypto/rand"
	"fmt"
	"sync"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An egg owns exactly one PTY. The lease lives here rather than in a transport
// adapter, so browser, CLI, and MCP cannot independently claim the same input.
type inputLease struct {
	mu       sync.Mutex
	sequence uint64
	epoch    uint64
	writer   *inputAttachment
}

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
	defer l.mu.Unlock()
	return l.claimLocked(a, takeover)
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
	}
	l.epoch++
	a.epoch = l.epoch
	l.writer = a
	return nil
}

// mutation holds the lease lock through the PTY operation. Takeover cannot
// acknowledge a new epoch while an older connection still writes a frame.
func (l *inputLease) mutation(a *inputAttachment, operation func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.claimLocked(a, false); err != nil {
		return err
	}
	return operation()
}

func (l *inputLease) resize(id string, epoch uint64, token string, operation func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil || l.writer.id != id || l.writer.epoch != epoch || l.writer.token != token || id == "" || epoch == 0 {
		return status.Error(codes.FailedPrecondition, "resize requires the current writer attachment and input epoch; resize through its attached stream")
	}
	return operation()
}

func (l *inputLease) release(a *inputAttachment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == a {
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
