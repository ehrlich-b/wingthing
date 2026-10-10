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
	archived := func() (egg.RunTurnResult, error) {
		result, err := egg.ReadRunTurnResult(filepath.Join(cfg.Dir, "eggs", session.ID), runID)
		if operation != "result" {
			result.Text = ""
		}
		return result, err
	}
	_, client, err := OpenLocalEgg(ctx, cfg, session.ID)
	if err != nil {
		result, readErr := archived()
		if readErr == nil && result.Terminal() {
			return result, nil
		}
		_, alive := ReadAliveEggPID(filepath.Join(cfg.Dir, "eggs", session.ID))
		if operation == "stop" || alive {
			return egg.RunTurnResult{}, err
		}
		return result, readErr
	}
	defer client.Close()
	var result egg.RunTurnResult
	switch operation {
	case "status":
		result, err = client.RunTurnStatus(ctx, runID)
	case "wait":
		result, err = client.WaitRunTurn(ctx, runID)
	case "stop":
		result, err = client.StopRunTurn(ctx, runID)
	default:
		result, err = client.ReadRunTurnResult(ctx, runID)
	}
	if err != nil && ctx.Err() == nil {
		if saved, readErr := archived(); readErr == nil && saved.Terminal() {
			return saved, nil
		}
	}
	return result, err
}

func ReserveRunTurn(ctx context.Context, cfg *config.Config, session LocalSession, request egg.RunTurnRequest) (egg.RunTurnResult, error) {
	_, client, err := OpenLocalEgg(ctx, cfg, session.ID)
	if err != nil {
		return egg.RunTurnResult{}, err
	}
	defer client.Close()
	return client.ReserveRunTurn(ctx, request)
}
