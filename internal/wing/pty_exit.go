package wing

import (
	"sync"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// The relay removes a session's reply routes on pty.exited. Hold that message
// until admission has stopped and the sole input consumer has refused its queue.
// Spectator exits leave controller routing intact and can be sent immediately.
func delayPTYExit(write ws.PTYWriteFunc) (ws.PTYWriteFunc, func()) {
	var mu sync.Mutex
	var pending *ws.PTYExited
	var drained, sent bool
	ordered := func(value any) error {
		exit, ok := value.(ws.PTYExited)
		if !ok || exit.ViewerID != "" {
			return write(value)
		}
		mu.Lock()
		defer mu.Unlock()
		if sent || pending != nil {
			return nil
		}
		if !drained {
			pending = &exit
			return nil
		}
		sent = true
		return write(exit)
	}
	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		drained = true
		if pending != nil {
			sent = true
			ws.WritePTYMessage(write, *pending)
			pending = nil
		}
	}
	return ordered, flush
}
