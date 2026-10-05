package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/spf13/cobra"
)

func sendPromptInput(ctx context.Context, cfg *config.Config, sessionID, input, owner, expectedProviderID string) (egg.PromptDelivery, error) {
	delivery := egg.PromptDelivery{NoInputAttempted: true}
	session, client, err := openLocalEgg(ctx, cfg, sessionID)
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
	view, err := lifecycleViewForSession(cfg, session, 0, 1)
	if err != nil {
		return delivery, err
	}
	if view.ProviderSessionID != expectedProviderID {
		return delivery, errors.New("provider identity changed before input")
	}
	if !egg.NativePromptReady(view) {
		return delivery, fmt.Errorf("native foreground readiness changed before input: state=%s source=%s", view.State, view.StateSource)
	}
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

func promptSession(ctx context.Context, cfg *config.Config, session localSession, requestID, input string, timeout time.Duration, owner string) (egg.SessionPromptResult, error) {
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	expectedProviderID := readEggMetaValues(dir)["provider_session_id"]
	return egg.SubmitSessionPrompt(ctx, dir, egg.SessionPromptOptions{
		RequestID: requestID, Input: input, Timeout: timeout,
		Read: func(ctx context.Context, after int64, limit int) (egg.SessionView, error) {
			if err := ctx.Err(); err != nil {
				return egg.SessionView{}, err
			}
			return lifecycleViewForSession(cfg, session, after, limit)
		},
		Send: func(ctx context.Context, text string) (egg.PromptDelivery, error) {
			return sendPromptInput(ctx, cfg, session.ID, text, owner, expectedProviderID)
		},
	})
}

func (s *localMCPServer) toolSessionPrompt(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session        string  `json:"session"`
		RequestID      string  `json:"request_id"`
		Input          string  `json:"input"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 15
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 60 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 60")
	}
	session, err := s.resolveOwnedLifecycleSession(args.Session)
	if err != nil {
		return nil, err
	}
	result, err := promptSession(ctx, s.cfg, session, args.RequestID, args.Input, durationSeconds(args.TimeoutSeconds), "mcp:"+s.clientActor())
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "receipt": result}, nil
}

func sessionPromptCmd() *cobra.Command {
	var requestID string
	var jsonFlag, stdinFlag bool
	var timeout time.Duration
	cmd := &cobra.Command{Use: "prompt <session> [text]", Short: "Submit a retry-safe prompt and observe its exact native transcript receipt", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if stdinFlag && len(args) > 1 {
			return errors.New("provide text arguments or --stdin, not both")
		}
		input := strings.Join(args[1:], " ")
		if stdinFlag {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), egg.MaxSessionPromptBytes+1))
			if err != nil {
				return err
			}
			input = string(data)
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		session, err := resolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		result, err := promptSession(cmd.Context(), cfg, session, requestID, input, timeout, "cli:session-prompt")
		if err != nil {
			return err
		}
		if jsonFlag {
			return writeSessionJSON(map[string]any{"session": session.ID, "receipt": result})
		}
		fmt.Printf("%s: %s, request=%s, transport_enqueued=%t, native_receipt_observed=%t\n", session.ID, result.Status, result.RequestID, result.TransportEnqueued, result.NativeReceiptObserved)
		if result.Reason != "" {
			fmt.Println(result.Reason)
		}
		return nil
	}}
	cmd.Flags().StringVar(&requestID, "request-id", "", "caller-chosen retry ID; reuse identical arguments after reconnect")
	cmd.Flags().BoolVar(&stdinFlag, "stdin", false, "read prompt from stdin (up to 65536 bytes)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "maximum receipt wait (100ms-60s)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
