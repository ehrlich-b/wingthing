package relay

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const (
	browserOutboundMessages = 64
	browserOutboundBytes    = 1 << 20
	relayWriteTimeout       = 5 * time.Second
)

type browserOutboundJob struct {
	bytes int
	write func(context.Context)
}

// One writer per browser keeps wing readers independent of browser transport
// and rate-limit waits. Both the frame count and retained payload bytes are capped.
type browserOutbound struct {
	mu        sync.Mutex
	jobs      []browserOutboundJob
	bytes     int
	slow      bool
	detached  []browserDetachRoute
	sessionID string
	wake      chan struct{}
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
}

func (s *Server) enqueueBrowser(conn *websocket.Conn, size int, sessionID string, write func(context.Context)) {
	s.browserMu.Lock()
	tracked := s.browserConns[conn]
	if tracked == nil {
		s.browserMu.Unlock()
		return
	}
	q := tracked.outbound
	if q == nil {
		ctx, cancel := context.WithCancel(context.Background())
		q = &browserOutbound{wake: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel}
		tracked.outbound = q
		go s.runBrowserWriter(conn, q)
	}
	s.browserMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.slow || q.ctx.Err() != nil {
		return
	}
	if len(q.jobs) >= browserOutboundMessages || size > browserOutboundBytes-q.bytes {
		q.slow = true
		q.sessionID = sessionID
		q.detached = s.PTY.browserDetachRoutes(conn, "")
		s.PTY.ClearBrowser(conn)
		s.clearTunnelRequests(conn)
		clear(q.jobs)
		q.jobs = nil
		q.bytes = 0
	} else {
		q.jobs = append(q.jobs, browserOutboundJob{size, write})
		q.bytes += size
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (s *Server) runBrowserWriter(conn *websocket.Conn, q *browserOutbound) {
	defer close(q.done)
	defer func() {
		// Overflow already cleared the browser's routes. Deliver their saved
		// detaches even if disconnect cancels an in-flight write before the
		// slow-connection branch runs, and finish before untrackBrowser returns.
		q.mu.Lock()
		detached := q.detached
		q.detached = nil
		q.mu.Unlock()
		s.forwardDetachedRoutes(detached)
	}()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		for {
			if q.ctx.Err() != nil {
				return
			}
			q.mu.Lock()
			if q.slow {
				sessionID := q.sessionID
				q.mu.Unlock()
				// A genuinely dead transport cannot receive an error. Try once with a
				// deadline, then close it; reconnecting browsers can attach for replay.
				ctx, cancel := context.WithTimeout(q.ctx, relayWriteTimeout)
				_ = writeWebSocketJSON(ctx, conn, ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, Message: "detached: slow connection"})
				cancel()
				_ = conn.CloseNow()
				return
			}
			if len(q.jobs) == 0 {
				q.mu.Unlock()
				break
			}
			job := q.jobs[0]
			q.jobs[0] = browserOutboundJob{}
			q.jobs = q.jobs[1:]
			q.bytes -= job.bytes
			q.mu.Unlock()
			// Rate limiting uses the browser lifetime. Each socket write starts
			// its own deadline after the shared limiter has admitted the frame.
			job.write(q.ctx)
		}
	}
}

func writeWingPayload(ctx context.Context, conn *websocket.Conn, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func writeWingJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	ctx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return writeWebSocketJSON(ctx, conn, value)
}

func writeBrowserPayload(ctx context.Context, conn *websocket.Conn, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func writeBrowserJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	ctx, cancel := context.WithTimeout(ctx, relayWriteTimeout)
	defer cancel()
	return writeWebSocketJSON(ctx, conn, value)
}
