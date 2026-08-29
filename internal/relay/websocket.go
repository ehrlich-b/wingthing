package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
)

// writeWebSocketJSON keeps serialization and transport failures on the same
// error path. Callers decide whether a failed write is fatal or best effort.
func writeWebSocketJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal websocket message: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("write websocket message: %w", err)
	}
	return nil
}
