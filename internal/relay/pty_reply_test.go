package relay

import (
	"testing"

	"github.com/coder/websocket"
)

func TestPTYBrowserReplyLifecycle(t *testing.T) {
	s := &Server{PTY: NewPTYRoutes(), browserConns: make(map[*websocket.Conn]*browserConnection)}
	conn := &websocket.Conn{}
	s.trackBrowser(conn, "alice")
	first := s.browserReplyID(conn)
	if first == "" || s.browserReplyID(conn) != first || s.browserForReplyID(first) != conn {
		t.Fatal("socket origin did not remain stable")
	}
	s.untrackBrowser(conn)
	if s.browserForReplyID(first) != nil || len(s.PTY.browserReplies) != 0 {
		t.Fatal("closed socket origin retained")
	}
	s.trackBrowser(conn, "alice")
	second := s.browserReplyID(conn)
	if second == first || s.browserForReplyID(first) != nil || s.browserForReplyID(second) != conn {
		t.Fatal("retracked socket borrowed an old origin")
	}
	// Replacing the tracking object without a lookup in between must also fence
	// the old ID. A new input prunes retired entries before storing its origin.
	s.untrackBrowser(conn)
	s.trackBrowser(conn, "alice")
	third := s.browserReplyID(conn)
	if third == second || s.browserForReplyID(second) != nil || len(s.PTY.browserReplies) != 1 {
		t.Fatal("replaced socket retained an old origin")
	}
	s.releasePTYBrowserReply(conn)
	if s.browserForReplyID(third) != nil || len(s.PTY.browserReplies) != 0 {
		t.Fatal("terminal socket close did not release its route")
	}
	s.untrackBrowser(conn)
}
