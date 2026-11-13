package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/coder/websocket"
)

type ptyBrowserReply struct {
	connection *browserConnection
	id         string
	key        string
}

// Socket identities live with terminal routing, independently of controllers.
// Validate the tracking object too: reusing a socket may never reuse its ID.
func (s *Server) prunePTYBrowserReplies() {
	for conn, reply := range s.PTY.browserReplies {
		if s.browserConns[conn] != reply.connection {
			delete(s.PTY.browserReplies, conn)
		}
	}
}

func (s *Server) browserReplyID(conn *websocket.Conn) string {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.prunePTYBrowserReplies()
	tracked := s.browserConns[conn]
	if tracked == nil {
		return ""
	}
	if s.PTY.browserReplies == nil {
		s.PTY.browserReplies = make(map[*websocket.Conn]ptyBrowserReply)
	}
	reply, ok := s.PTY.browserReplies[conn]
	if !ok {
		reply = ptyBrowserReply{connection: tracked, id: newRelayRouteID(), key: newRelayRouteID() + newRelayRouteID()}
		s.PTY.browserReplies[conn] = reply
	}
	return reply.id
}

func (s *Server) browserForReplyID(id string) *websocket.Conn {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.prunePTYBrowserReplies()
	for conn, reply := range s.PTY.browserReplies {
		if reply.id == strings.SplitN(id, ".", 2)[0] {
			return conn
		}
	}
	return nil
}

func (s *Server) releasePTYBrowserReply(conn *websocket.Conn) {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	delete(s.PTY.browserReplies, conn)
}

// Sign the reply's session and wing instead of retaining a table of completed
// sessions. Only the socket identity/key survives exit, bounded by live sockets;
// closing or retracking that socket revokes every outstanding reply capability.
func ptyReplyToken(reply ptyBrowserReply, sessionID, wingID string) string {
	mac := hmac.New(sha256.New, []byte(reply.key))
	_, _ = mac.Write([]byte(sessionID + "\x00" + wingID))
	return reply.id + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) browserPTYReplyID(conn *websocket.Conn, sessionID, wingID string) string {
	if s.browserReplyID(conn) == "" {
		return ""
	}
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.prunePTYBrowserReplies()
	reply, ok := s.PTY.browserReplies[conn]
	if !ok {
		return ""
	}
	return ptyReplyToken(reply, sessionID, wingID)
}

func (s *Server) browserForPTYReplyID(id, sessionID, wingID string) *websocket.Conn {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	s.prunePTYBrowserReplies()
	for conn, reply := range s.PTY.browserReplies {
		if hmac.Equal([]byte(id), []byte(ptyReplyToken(reply, sessionID, wingID))) {
			return conn
		}
	}
	return nil
}
