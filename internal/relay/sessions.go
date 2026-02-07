package relay

import (
	"fmt"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"nhooyr.io/websocket"
)

type DaemonConn struct {
	UserID   string
	DeviceID string
	Conn     *websocket.Conn
	Send     chan *ws.Message
}

type ClientConn struct {
	UserID string
	Conn   *websocket.Conn
	Send   chan *ws.Message
}

type SessionManager struct {
	mu      sync.RWMutex
	daemons map[string]*DaemonConn  // keyed by userID+":"+deviceID
	clients map[string][]*ClientConn // keyed by userID
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		daemons: make(map[string]*DaemonConn),
		clients: make(map[string][]*ClientConn),
	}
}

func daemonKey(userID, deviceID string) string {
	return userID + ":" + deviceID
}

func (m *SessionManager) AddDaemon(userID, deviceID string, conn *websocket.Conn) *DaemonConn {
	dc := &DaemonConn{
		UserID:   userID,
		DeviceID: deviceID,
		Conn:     conn,
		Send:     make(chan *ws.Message, 256),
	}
	m.mu.Lock()
	m.daemons[daemonKey(userID, deviceID)] = dc
	m.mu.Unlock()
	return dc
}

func (m *SessionManager) RemoveDaemon(userID, deviceID string) {
	m.mu.Lock()
	delete(m.daemons, daemonKey(userID, deviceID))
	m.mu.Unlock()
}

func (m *SessionManager) AddClient(userID string, conn *websocket.Conn) *ClientConn {
	cc := &ClientConn{
		UserID: userID,
		Conn:   conn,
		Send:   make(chan *ws.Message, 256),
	}
	m.mu.Lock()
	m.clients[userID] = append(m.clients[userID], cc)
	m.mu.Unlock()
	return cc
}

func (m *SessionManager) RemoveClient(userID string, conn *websocket.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := m.clients[userID]
	for i, c := range clients {
		if c.Conn == conn {
			m.clients[userID] = append(clients[:i], clients[i+1:]...)
			return
		}
	}
}

func (m *SessionManager) RouteToUser(userID string, msg *ws.Message) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Find first available daemon for this user
	for _, dc := range m.daemons {
		if dc.UserID == userID {
			select {
			case dc.Send <- msg:
				return nil
			default:
				return fmt.Errorf("daemon send buffer full")
			}
		}
	}
	return fmt.Errorf("no daemon connected for user %s", userID)
}

func (m *SessionManager) BroadcastToClients(userID string, msg *ws.Message) error {
	m.mu.RLock()
	clients := m.clients[userID]
	m.mu.RUnlock()

	if len(clients) == 0 {
		return nil // no clients connected, not an error
	}

	for _, cc := range clients {
		select {
		case cc.Send <- msg:
		default:
			// client send buffer full, skip
		}
	}
	return nil
}

func (m *SessionManager) DaemonCount(userID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, dc := range m.daemons {
		if dc.UserID == userID {
			count++
		}
	}
	return count
}

func (m *SessionManager) ClientCount(userID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients[userID])
}
