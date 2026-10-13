package localmcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/fleet"
)

func TestPhoneFleetAttachmentSingleReadinessBudget(t *testing.T) {
	s := inventoryFixture(t, "origin", "fixture-machine")
	owner, err := config.EnsureLocalOwner(s.Cfg.Dir, "fixture-user")
	if err != nil {
		t.Fatal(err)
	}
	// The receiving protocol intentionally delays welcome and egg ack by finite
	// amounts whose combined duration exceeds the one readiness deadline.
	listener, err := controlsocket.Listen(t.Context(), s.Cfg.Dir, "origin", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, nil, nil
	}, func(ctx context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return controlsocket.Welcome{}, nil, ctx.Err()
		}
		return controlsocket.Welcome{}, func(ctx context.Context, stream *controlsocket.SessionStream) error {
			select {
			case <-time.After(4 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			return stream.Send(&pb.SessionMsg{SessionId: hello.Attach.Session, Payload: &pb.SessionMsg_Output{Output: []byte("delayed ack")}})
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Resolve archived metadata with exact ownership; no fake alive PID or native
	// provider/sandbox claim is needed to exercise the control readiness phases.
	if err := os.WriteFile(filepath.Join(s.Cfg.Dir, "eggs", "same-short-id", "lifecycle.jsonl"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	bridge, err := OwnerFleetAttach(t.Context(), s.Sessions, owner.ID, fleet.Identity{OriginWingID: "origin", SessionID: "same-short-id"}, true, false)
	if bridge != nil {
		_ = bridge.Close()
		t.Fatal("attachment succeeded after cumulative readiness deadline")
	}
	if err == nil {
		t.Fatal("delayed acknowledgement was accepted")
	}
	elapsed := time.Since(started)
	if elapsed < 4500*time.Millisecond || elapsed > 5800*time.Millisecond {
		t.Fatalf("readiness did not use one five-second budget: %s, %v", elapsed, err)
	}
}
