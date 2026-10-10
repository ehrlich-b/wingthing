package localmcp

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func bindWingAttachment(version string, sessions *wingsession.Service, owner string, admission *AdmissionState) controlsocket.AttachBind {
	return func(ctx context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		a := hello.Attach
		if a == nil || a.Session == "" || hello.Execution != "" || hello.Conversation != "" || hello.Aggregate || a.ReadOnly && (a.Takeover || a.Rows != 0 || a.Cols != 0) {
			return controlsocket.Welcome{}, nil, errors.New("invalid wing attachment request")
		}
		check := func(write, archived bool) (*Server, eggclient.LocalSession, error) {
			s, err := resolveLocalWingClient(version, sessions, owner, admission, hello)
			if err != nil {
				return nil, eggclient.LocalSession{}, err
			}
			if !s.toolAllowed("terminal_read") || write && !s.toolAllowed("terminal_send") {
				return nil, eggclient.LocalSession{}, errors.New("terminal attachment is not granted")
			}
			ref, err := sessions.Resolve(ctx, s.sessionAuthority(), a.Session, archived)
			return s, ref, err
		}
		s, ref, err := check(!a.ReadOnly, false)
		if err != nil {
			return controlsocket.Welcome{}, nil, err
		}
		handler := func(ctx context.Context, client *controlsocket.SessionStream) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			_, ec, err := eggclient.OpenLocalEgg(ctx, sessions.Config, ref.ID)
			if err != nil {
				return err
			}
			defer ec.Close()
			stream, err := sessions.Attach(ctx, s.sessionAuthority(), ec, ref.ID, egg.AttachOptions{ReadOnly: a.ReadOnly, Takeover: a.Takeover, Claim: !a.ReadOnly, Owner: s.clientActor(), Rows: a.Rows, Cols: a.Cols}, nil)
			if err != nil {
				return err
			}
			done := make(chan error, 2)
			var detached atomic.Bool
			go func() {
				for {
					msg, err := stream.Recv()
					if err != nil {
						if errors.Is(err, io.EOF) && detached.Load() {
							err = nil
						}
						done <- err
						return
					}
					if _, _, err := check(false, true); err != nil {
						done <- err
						return
					}
					// Writer capabilities remain in the wing.
					msg.AttachmentInfo = nil
					if err := client.Send(msg); err != nil {
						done <- err
						return
					}
					if _, exited := msg.Payload.(*pb.SessionMsg_ExitCode); exited {
						done <- nil
						return
					}
				}
			}()
			go func() {
				for {
					msg, err := client.Recv()
					if err != nil {
						done <- err
						return
					}
					if msg.SessionId != "" && msg.SessionId != ref.ID && msg.SessionId != a.Session || msg.AttachmentInfo != nil || msg.AttachOptions != nil {
						done <- errors.New("invalid attachment frame")
						return
					}
					write := true
					switch p := msg.Payload.(type) {
					case *pb.SessionMsg_Detach:
						if !p.Detach {
							done <- errors.New("invalid detach frame")
							return
						}
						write = false
						detached.Store(true)
					case *pb.SessionMsg_Input, *pb.SessionMsg_Resize:
						if a.ReadOnly {
							done <- errors.New("read-only attachment cannot write or resize")
							return
						}
					default:
						done <- errors.New("unsupported attachment frame")
						return
					}
					if _, _, err := check(write, true); err != nil {
						done <- err
						return
					}
					msg.SessionId = ref.ID
					if err := stream.Send(msg); err != nil {
						done <- err
						return
					}
					if detached.Load() {
						return
					}
				}
			}()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return controlsocket.Welcome{Principal: s.clientPrincipal(), Actor: s.clientActor()}, handler, nil
	}
}

// AttachWingIO only presents a stream; the wing resolves identity and sessions.
func AttachWingIO(ctx context.Context, dir, clientName, session string, input io.Reader, output io.Writer, options egg.AttachOptions) (bool, error) {
	return eggclient.AttachStreamIO(ctx, session, input, output, options, func(ctx context.Context, options egg.AttachOptions) (eggclient.SessionStream, error) {
		stream, _, err := controlsocket.DialAttachment(ctx, dir, controlsocket.Hello{Client: clientName, Attach: &controlsocket.Attachment{Session: session, ReadOnly: options.ReadOnly, Takeover: options.Takeover, Rows: options.Rows, Cols: options.Cols}})
		return stream, err
	})
}
