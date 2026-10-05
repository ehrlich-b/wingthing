package eggclient

import (
	"context"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func SessionLifecycleSummary(ctx context.Context, cfg *config.Config, id string) map[string]any {
	view, err := ReadSessionLifecycleView(ctx, cfg, id, 0, 1)
	if err != nil {
		return nil
	}
	return map[string]any{"state": view.State, "status": view.Status, "state_source": view.StateSource, "ready": view.Ready, "process_alive": view.ProcessAlive, "head_cursor": view.HeadCursor}
}
