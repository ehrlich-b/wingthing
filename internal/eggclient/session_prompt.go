package eggclient

import (
	"context"

	"errors"
	"fmt"
	"io"
	"path/filepath"

	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
)

func sendPromptInput(ctx context.Context, cfg *config.Config, sessionID, input, owner, expectedProviderID string, verifiedUserIDs ...string) (egg.PromptDelivery, error) {
	delivery := egg.PromptDelivery{NoInputAttempted: true}
	session, client, err := OpenLocalEgg(ctx, cfg, sessionID)
	if err != nil {
		return delivery, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	// Confirm an eager writer claim before enqueuing input. Keep the stream
	// alive through native receipt so preview arbitration excludes rival writers.
	stream, err := client.AttachSessionWithOptions(streamCtx, session.ID, egg.AttachOptions{Claim: true, Owner: owner})
	if err != nil {
		cancel()
		_ = client.Close()
		return delivery, err
	}
	delivery.Release = func() {
		_ = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Detach{Detach: true}})
		_ = stream.CloseSend()
		cancel()
		_ = client.Close()
	}
	if _, err = stream.Recv(); err != nil {
		return delivery, err
	}
	// A human may submit work between reservation and lease acquisition. Check
	// native readiness while this attachment owns input, before pasting bytes.
	view, err := LifecycleViewForSession(cfg, session, 0, 1)
	if err != nil {
		return delivery, err
	}
	if view.ProviderSessionID != expectedProviderID {
		return delivery, errors.New("provider identity changed before input")
	}
	if !egg.NativePromptReady(view) {
		return delivery, fmt.Errorf("native foreground readiness changed before input: state=%s source=%s", view.State, view.StateSource)
	}
	observeSessionController(cfg, session.ID, verifiedUserIDs)
	lost := make(chan error, 1)
	delivery.Lost = lost
	go func() {
		for {
			msg, e := stream.Recv()
			if e != nil {
				lost <- e
				return
			}
			if _, exited := msg.Payload.(*pb.SessionMsg_ExitCode); exited {
				lost <- io.EOF
				return
			}
		}
	}()
	// Bracketed paste preserves a multiline prompt as one editor submission.
	frame := []byte("\x1b[200~" + input + "\x1b[201~")
	// Even an error or crash after this point cannot prove input was unsent.
	delivery.NoInputAttempted = false
	if err = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Input{Input: frame}}); err != nil {
		return delivery, err
	}
	delivery.BytesEnqueued = len(frame)
	timer := time.NewTimer(sessionEnterDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return delivery, ctx.Err()
	case <-timer.C:
	}
	if err = stream.Send(&pb.SessionMsg{SessionId: session.ID, Payload: &pb.SessionMsg_Input{Input: []byte{'\r'}}}); err != nil {
		return delivery, err
	}
	delivery.BytesEnqueued++
	return delivery, nil
}

func PromptSession(ctx context.Context, cfg *config.Config, session LocalSession, requestID, input string, timeout time.Duration, owner string, verifiedUserIDs ...string) (egg.SessionPromptResult, error) {
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	expectedProviderID := ReadEggMetaValues(dir)["provider_session_id"]
	return egg.SubmitSessionPrompt(ctx, dir, egg.SessionPromptOptions{
		RequestID: requestID, Input: input, Timeout: timeout,
		Read: func(ctx context.Context, after int64, limit int) (egg.SessionView, error) {
			if err := ctx.Err(); err != nil {
				return egg.SessionView{}, err
			}
			return LifecycleViewForSession(cfg, session, after, limit)
		},
		Send: func(ctx context.Context, text string) (egg.PromptDelivery, error) {
			return sendPromptInput(ctx, cfg, session.ID, text, owner, expectedProviderID, verifiedUserIDs...)
		},
	})
}
