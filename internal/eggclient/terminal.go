package eggclient

import (
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func SessionIsolationLabel(cfg *egg.EggConfig) string {
	if egg.RequiresSandbox(cfg, "") {
		return "wingthing-sandbox"
	}
	return "outer-boundary"
}
