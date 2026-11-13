package wing

import (
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/google/uuid"
	pion "github.com/pion/webrtc/v4"
)

// This envelope exists only inside the wing's input queue. Relay ingress strips
// unknown fields; data-channel ingress always replaces all routing metadata.
type queuedBrowserInput struct {
	ws.PTYInput
	Origin string `json:"wing_input_origin,omitempty"`
}

type dataChannelInputOrigin struct {
	session    string
	controller *browserInputController
	dc         *pion.DataChannel
	timer      *time.Timer
}

var inputOrigins = struct {
	sync.Mutex
	items map[string]*dataChannelInputOrigin
}{items: make(map[string]*dataChannelInputOrigin)}

const inputOriginLimit = 4096
const inputOriginPerChannelLimit = 128
const inputOriginLifetime = 30 * time.Second

func retainInputOrigin(session string, controller *browserInputController, dc *pion.DataChannel) string {
	inputOrigins.Lock()
	defer inputOrigins.Unlock()
	count := 0
	for _, origin := range inputOrigins.items {
		if origin.dc == dc {
			count++
		}
	}
	if len(inputOrigins.items) >= inputOriginLimit || count >= inputOriginPerChannelLimit {
		return ""
	}
	token := uuid.NewString()
	origin := &dataChannelInputOrigin{session: session, controller: controller, dc: dc}
	inputOrigins.items[token] = origin
	origin.timer = time.AfterFunc(inputOriginLifetime, func() { expireInputOrigin(token) })
	return token
}

func takeInputOrigin(token string) *dataChannelInputOrigin {
	inputOrigins.Lock()
	defer inputOrigins.Unlock()
	origin := inputOrigins.items[token]
	delete(inputOrigins.items, token)
	if origin != nil {
		origin.timer.Stop()
	}
	return origin
}

func expireInputOrigin(token string) {
	if origin := takeInputOrigin(token); origin != nil {
		refuseDataChannelInput(origin.dc, origin.session, "session_input_timeout", "terminal refused input; delivery could not be confirmed")
		// Receipt is unknown. Closing also invokes the browser's visible disconnect
		// path and prevents a late consumer from borrowing another transport.
		_ = origin.dc.Close()
	}
}

func releaseDataChannelInputOrigins(dc *pion.DataChannel) {
	inputOrigins.Lock()
	defer inputOrigins.Unlock()
	for token, origin := range inputOrigins.items {
		if origin.dc == dc {
			origin.timer.Stop()
			delete(inputOrigins.items, token)
		}
	}
}
