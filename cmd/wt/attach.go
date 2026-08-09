package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/config"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const attachPrefix byte = 0x02 // Ctrl+B

func attachCmd() *cobra.Command {
	var remoteTarget string
	var remoteBinary string
	var selectFlag bool
	var jsonFlag bool

	cmd := &cobra.Command{
		Use:   "attach [session]",
		Short: "Attach this terminal to a running egg session",
		Long: "Attach the current terminal directly to a persistent egg session. " +
			"Use --remote with an SSH host from ~/.ssh/config to attach without opening the web app.\n\n" +
			"Detach without stopping the session with Ctrl+B, then Q. Send a literal Ctrl+B with Ctrl+B twice.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionID string
			if len(args) == 1 {
				sessionID = args[0]
			}

			if selectFlag && jsonFlag {
				return errors.New("--select and --json are mutually exclusive")
			}
			if sessionID != "" && jsonFlag {
				return errors.New("--json lists sessions and cannot be used with a session")
			}

			if remoteTarget != "" {
				return runRemoteAttach(cmd.Context(), remoteTarget, remoteBinary, sessionID, selectFlag, jsonFlag)
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if sessionID == "" {
				if !selectFlag {
					return printActiveSessions(cmd.Context(), cfg, jsonFlag)
				}
				selected, selectErr := selectActiveSession(cmd.Context(), cfg)
				if selectErr != nil {
					return selectErr
				}
				sessionID = selected.ID
			}

			detached, err := attachLocal(cmd.Context(), cfg, sessionID)
			if detached {
				fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", sessionID)
			}
			return err
		},
	}

	cmd.Flags().StringVarP(&remoteTarget, "remote", "r", "", "SSH host or alias that owns the session")
	cmd.Flags().StringVar(&remoteBinary, "remote-binary", "wt", "wt executable or absolute path on the remote host")
	cmd.Flags().BoolVarP(&selectFlag, "select", "s", false, "choose a session interactively")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print active sessions as JSON")
	return cmd
}

func validateSessionID(sessionID string) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || filepath.Base(sessionID) != sessionID {
		return fmt.Errorf("invalid session ID %q", sessionID)
	}
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid session ID %q", sessionID)
	}
	return nil
}

func validateRemoteTarget(target string) error {
	if target == "" {
		return errors.New("missing SSH target")
	}
	if strings.HasPrefix(target, "-") {
		return errors.New("SSH target must not start with '-'")
	}
	if strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("SSH target contains invalid characters")
	}
	return nil
}

func runRemoteAttach(ctx context.Context, target, remoteBinary, sessionID string, selectSession, jsonOutput bool) error {
	if err := validateRemoteTarget(target); err != nil {
		return err
	}
	if remoteBinary == "" || strings.ContainsAny(remoteBinary, "\r\n\x00") {
		return errors.New("invalid remote binary")
	}
	if sessionID != "" {
		if err := validateSessionID(sessionID); err != nil {
			return err
		}
	}

	remoteCommand := shellQuote(remoteBinary) + " attach"
	if sessionID != "" {
		remoteCommand += " " + shellQuote(sessionID)
	} else if selectSession {
		remoteCommand += " --select"
	} else if jsonOutput {
		remoteCommand += " --json"
	}

	args := []string{"-t", target, remoteCommand}
	ssh := exec.CommandContext(ctx, "ssh", args...)
	ssh.Stdin = os.Stdin
	ssh.Stdout = os.Stdout
	ssh.Stderr = os.Stderr
	if err := ssh.Run(); err != nil {
		return fmt.Errorf("remote attach to %s: %w", target, err)
	}
	return nil
}

// shellQuote quotes one argument for the remote user's POSIX shell. OpenSSH
// joins the remote command into a string even when the local argv is safe.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type attachInputFilter struct {
	pendingPrefix bool
}

// filter returns bytes for the egg and whether the local client should detach.
// The prefix may arrive in a separate read from the following key.
func (f *attachInputFilter) filter(input []byte) ([]byte, bool) {
	output := make([]byte, 0, len(input)+1)
	for _, b := range input {
		if f.pendingPrefix {
			f.pendingPrefix = false
			switch b {
			case 'q', 'Q':
				return output, true
			case attachPrefix:
				output = append(output, attachPrefix)
			default:
				output = append(output, attachPrefix, b)
			}
			continue
		}
		if b == attachPrefix {
			f.pendingPrefix = true
			continue
		}
		output = append(output, b)
	}
	return output, false
}

type attachResult struct {
	exitCode *int
	err      error
}

func attachLocal(ctx context.Context, cfg *config.Config, sessionID string) (bool, error) {
	resolved, ec, err := openLocalEgg(ctx, cfg, sessionID)
	if err != nil {
		return false, err
	}
	defer ec.Close()
	sessionID = resolved.ID

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
			if resizeErr := ec.Resize(ctx, sessionID, uint32(rows), uint32(cols)); resizeErr != nil {
				return false, fmt.Errorf("resize session %s: %w", sessionID, resizeErr)
			}
		}
	}

	stream, err := ec.AttachSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("attach session %s: %w", sessionID, err)
	}

	if term.IsTerminal(fd) {
		oldState, rawErr := term.MakeRaw(fd)
		if rawErr != nil {
			return false, fmt.Errorf("put terminal in raw mode: %w", rawErr)
		}
		defer term.Restore(fd, oldState)
	}

	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-winchCh:
				if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
					_ = ec.Resize(ctx, sessionID, uint32(rows), uint32(cols))
				}
			}
		}
	}()

	resultCh := make(chan attachResult, 1)
	go receiveAttachedOutput(stream, resultCh)

	detachCh := make(chan struct{}, 1)
	go sendAttachedInput(stream, sessionID, detachCh)

	select {
	case <-detachCh:
		return true, nil
	case result := <-resultCh:
		if result.err != nil {
			return false, result.err
		}
		if result.exitCode != nil && *result.exitCode != 0 {
			return false, fmt.Errorf("session exited with code %d", *result.exitCode)
		}
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func receiveAttachedOutput(stream pb.Egg_SessionClient, resultCh chan<- attachResult) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
				resultCh <- attachResult{}
			} else {
				resultCh <- attachResult{err: fmt.Errorf("read session: %w", err)}
			}
			return
		}
		switch payload := msg.Payload.(type) {
		case *pb.SessionMsg_Output:
			if _, err := os.Stdout.Write(payload.Output); err != nil {
				resultCh <- attachResult{err: fmt.Errorf("write terminal output: %w", err)}
				return
			}
		case *pb.SessionMsg_ExitCode:
			code := int(payload.ExitCode)
			resultCh <- attachResult{exitCode: &code}
			return
		}
	}
}

func sendAttachedInput(stream pb.Egg_SessionClient, sessionID string, detachCh chan<- struct{}) {
	filter := &attachInputFilter{}
	buffer := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buffer)
		if n > 0 {
			output, detach := filter.filter(buffer[:n])
			if len(output) > 0 {
				if sendErr := stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Input{Input: output},
				}); sendErr != nil {
					return
				}
			}
			if detach {
				_ = stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Detach{Detach: true},
				})
				detachCh <- struct{}{}
				return
			}
		}
		if err != nil {
			return
		}
	}
}
