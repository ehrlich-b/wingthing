package wing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type browserInputController struct {
	id, publicKey, userID string
	client                *egg.Client
	lease                 *pb.AttachmentInfo
	cancel                context.CancelFunc
}

var browserInputs sync.Map  // session ID -> immutable acknowledged controller
var browserViewers sync.Map // session ID + viewer ID -> browserInputObserver

type browserInputObserver struct {
	client *egg.Client
	cancel context.CancelFunc
}

func bindBrowserViewer(sessionID, viewerID string, client *egg.Client, cancel context.CancelFunc) {
	if config.Channel() == "preview" {
		browserViewers.Store(sessionID+":"+viewerID, &browserInputObserver{client, cancel})
	}
}

func bindBrowserInput(sessionID, controllerID, publicKey, userID string, client *egg.Client, stream pb.Egg_SessionClient, cancel context.CancelFunc) {
	if config.Channel() != "preview" {
		return
	}
	browserInputs.Store(sessionID, &browserInputController{id: controllerID, publicKey: publicKey, userID: userID, client: client, lease: egg.AttachmentInfo(stream), cancel: cancel})
}

func releaseBrowserInput(sessionID, controllerID string) bool {
	if config.Channel() != "preview" || controllerID == "" {
		return false
	}
	value, ok := browserInputs.Load(sessionID)
	if !ok {
		return false
	}
	controller := value.(*browserInputController)
	if controller.id != controllerID || !browserInputs.CompareAndDelete(sessionID, controller) {
		return false
	}
	controller.cancel()
	return true
}

func releaseBrowserClient(sessionID string, client *egg.Client) {
	if config.Channel() != "preview" {
		return
	}
	if value, ok := browserInputs.Load(sessionID); ok {
		controller := value.(*browserInputController)
		if controller.client == client && browserInputs.CompareAndDelete(sessionID, controller) {
			controller.cancel()
		}
	}
	browserViewers.Range(func(key, value any) bool {
		observer := value.(*browserInputObserver)
		if observer.client == client && strings.HasPrefix(key.(string), sessionID+":") && browserViewers.CompareAndDelete(key, observer) {
			observer.cancel()
		}
		return true
	})
}

func resizeBrowserInput(ctx context.Context, sessionID, controllerID, publicKey, userID string, rows, cols uint32) error {
	value, ok := browserInputs.Load(sessionID)
	if !ok {
		return fmt.Errorf("resize requires an attached writer; attach to request control")
	}
	controller := value.(*browserInputController)
	if controllerID == "" || controller.id != controllerID || controller.publicKey != publicKey || controller.userID != userID {
		return fmt.Errorf("resize belongs to a different or expired browser attachment")
	}
	return controller.client.ResizeForAttachment(ctx, sessionID, rows, cols, controller.lease)
}

func browserDataChannelBinding(sessionID, publicKey, userID string) (*browserInputController, bool) {
	if config.Channel() != "preview" {
		return nil, true
	}
	value, ok := browserInputs.Load(sessionID)
	if !ok {
		return nil, false
	}
	controller := value.(*browserInputController)
	return controller, controller.id != "" && controller.publicKey == publicKey && controller.userID == userID
}

func currentBrowserInput(sessionID string, controller *browserInputController) bool {
	if config.Channel() != "preview" {
		return true
	}
	current, ok := browserInputs.Load(sessionID)
	return ok && current == controller
}

func resizeBrowserStream(ctx context.Context, sessionID, controllerID string, stream pb.Egg_SessionClient, rows, cols uint32) error {
	value, ok := browserInputs.Load(sessionID)
	if !ok {
		return fmt.Errorf("resize requires an attached browser writer")
	}
	controller := value.(*browserInputController)
	lease := egg.AttachmentInfo(stream)
	if controllerID == "" || controller.id != controllerID || lease == nil || controller.lease == nil || lease.AttachmentId != controller.lease.AttachmentId || lease.InputEpoch != controller.lease.InputEpoch {
		return fmt.Errorf("resize belongs to a different or expired browser attachment")
	}
	return controller.client.ResizeForAttachment(ctx, sessionID, rows, cols, controller.lease)
}

func browserStreamEnded(sessionID, controllerID string, err error, write ws.PTYWriteFunc) {
	if config.Channel() != "preview" {
		return
	}
	if err != nil && (status.Code(err) == codes.Aborted || status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.PermissionDenied) {
		ws.WritePTYMessage(write, ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, ControllerID: controllerID, Message: err.Error()})
	}
	releaseBrowserInput(sessionID, controllerID)
}

func detachBrowserAttachment(sessionID string, detach ws.PTYDetach) {
	if detach.ViewerID != "" {
		if value, ok := browserViewers.LoadAndDelete(sessionID + ":" + detach.ViewerID); ok {
			value.(*browserInputObserver).cancel()
		}
		return
	}
	releaseBrowserInput(sessionID, detach.ControllerID)
}

// A writer connection is disposable. This observer keeps lifecycle/attention
// forwarding alive while the browser detaches or another surface owns input.
func watchPreviewBrowserEgg(ctx context.Context, client *egg.Client, sessionID, agent, cwd string, idle *sessionIdleState, write ws.PTYWriteFunc, sessionCancel context.CancelFunc, services ...*wingsession.Service) error {
	sessions := &wingsession.Service{}
	if len(services) > 0 {
		sessions = services[0]
	}
	stream, err := sessions.Observe(ctx, client, sessionID, "wing:observer")
	if err != nil {
		return err
	}
	if _, err := stream.Recv(); err != nil {
		return err
	}
	go func() {
		var hadBell bool
		for {
			message, err := stream.Recv()
			if err != nil {
				return
			}
			switch payload := message.Payload.(type) {
			case *pb.SessionMsg_Output:
				idle.mu.Lock()
				idle.lastOutput = time.Now()
				idle.mu.Unlock()
				if hasBell(payload.Output) {
					if hadBell {
						checkAndSendAttention(sessionID, agent, cwd, write)
					}
					hadBell = true
				} else {
					hadBell = false
				}
			case *pb.SessionMsg_ExitCode:
				ws.WritePTYMessage(write, ws.PTYExited{Type: ws.TypePTYExited, SessionID: sessionID, ExitCode: int(payload.ExitCode)})
				clearAttentionCooldown(sessionID)
				sessionCancel()
				return
			}
		}
	}()
	return nil
}
