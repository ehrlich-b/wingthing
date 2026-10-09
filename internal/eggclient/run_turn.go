package eggclient

import (
	"context"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func SubmitRunTurn(ctx context.Context, cfg *config.Config, session LocalSession, request egg.RunTurnRequest) (egg.RunTurnResult, error) {
	_, client, err := OpenLocalEgg(ctx, cfg, session.ID)
	if err != nil {
		return egg.RunTurnResult{}, err
	}
	defer client.Close()
	return client.SubmitRunTurn(ctx, request)
}

func RunTurnStatus(ctx context.Context, cfg *config.Config, session LocalSession, runID string) (egg.RunTurnResult, error) {
	return observeRunTurn(ctx, cfg, session, runID, "status")
}

func WaitRunTurn(ctx context.Context, cfg *config.Config, session LocalSession, runID string) (egg.RunTurnResult, error) {
	return observeRunTurn(ctx, cfg, session, runID, "wait")
}

func ReadRunTurnResult(ctx context.Context, cfg *config.Config, session LocalSession, runID string) (egg.RunTurnResult, error) {
	return observeRunTurn(ctx, cfg, session, runID, "result")
}

func StopRunTurn(ctx context.Context, cfg *config.Config, session LocalSession, runID string) (egg.RunTurnResult, error) {
	return observeRunTurn(ctx, cfg, session, runID, "stop")
}

func observeRunTurn(ctx context.Context, cfg *config.Config, session LocalSession, runID, operation string) (egg.RunTurnResult, error) {
	if err := ValidateSessionName(session.ID); err != nil {
		return egg.RunTurnResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return egg.RunTurnResult{}, err
	}
	_, client, err := OpenLocalEgg(ctx, cfg, session.ID)
	if err != nil {
		_, alive := ReadAliveEggPID(filepath.Join(cfg.Dir, "eggs", session.ID))
		if operation == "stop" || alive {
			return egg.RunTurnResult{}, err
		}
		result, readErr := egg.ReadRunTurnResult(filepath.Join(cfg.Dir, "eggs", session.ID), runID)
		if operation != "result" {
			result.Text = ""
		}
		return result, readErr
	}
	defer client.Close()
	switch operation {
	case "status":
		return client.RunTurnStatus(ctx, runID)
	case "wait":
		return client.WaitRunTurn(ctx, runID)
	case "stop":
		return client.StopRunTurn(ctx, runID)
	default:
		return client.ReadRunTurnResult(ctx, runID)
	}
}
