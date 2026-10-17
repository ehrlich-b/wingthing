package main

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// Invitations can suppress relay URLs, so only a live mode query is authoritative.
func lanPairRestartGuidance() string {
	cfg, err := config.Load()
	if err != nil {
		return phonepair.RestartGuidance("", true)
	}
	return phonepair.RestartGuidance(phonepair.RunningMode(cfg.Dir), true)
}
