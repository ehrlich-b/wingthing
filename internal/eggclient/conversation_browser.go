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
	summary := map[string]any{"state": view.State, "status": view.Status, "state_source": view.StateSource, "ready": view.Ready, "process_alive": view.ProcessAlive, "head_cursor": view.HeadCursor}
	classified := ClassifyEgg(cfg, id)
	if classified.Class == RecoveryEligible {
		summary["status"], summary["recoverable"] = "exited", true
	}
	if classified.Intent.RecoveredFrom != "" {
		summary["recovered_from"] = classified.Intent.RecoveredFrom
	}
	return summary
}
