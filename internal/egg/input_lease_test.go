package egg

import (
	"testing"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInputLeaseArbitratesWritersObserversAndRevokedEpochs(t *testing.T) {
	var lease inputLease
	first := lease.register(&pb.AttachOptions{Owner: "first"})
	second := lease.register(&pb.AttachOptions{Owner: "second"})
	observer := lease.register(&pb.AttachOptions{ReadOnly: true})
	writes := 0
	write := func() error { writes++; return nil }
	if err := lease.mutation(first, write); err != nil {
		t.Fatal(err)
	}
	if err := lease.mutation(second, write); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second writer bypassed lease: %v", err)
	}
	if err := lease.mutation(observer, write); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("observer wrote: %v", err)
	}
	old := lease.info(first)
	if lease.info(observer).AttachmentToken != "" {
		t.Fatal("observer received writer resize token")
	}
	if err := lease.resize(old.WriterId, old.InputEpoch, "", write); status.Code(err) != codes.FailedPrecondition {
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
	if err := lease.resize(old.WriterId, old.InputEpoch, old.AttachmentToken, write); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale resize token survived takeover: %v", err)
	}
	if err := lease.resize(current.WriterId, current.InputEpoch, current.AttachmentToken, write); err != nil {
		t.Fatal(err)
	}
	lease.release(second)
	if err := lease.mutation(first, write); status.Code(err) != codes.Aborted {
		t.Fatalf("revoked stream reclaimed after winner detached: %v", err)
	}
	if writes != 2 {
		t.Fatalf("rejected mutations reached PTY: %d", writes)
	}
	third := lease.register(nil)
	if err := lease.mutation(third, write); err != nil {
		t.Fatalf("detach did not release writer: %v", err)
	}
}
