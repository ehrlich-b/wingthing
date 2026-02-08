package orchestrator

import (
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/skill"
)

// ResolvedConfig holds the final config for a task after precedence resolution.
type ResolvedConfig struct {
	Agent     string
	Isolation string
	Timeout   time.Duration
}

// ResolveConfig applies precedence: CLI flag (taskAgent) > skill frontmatter > agent defaults > config.yaml defaults.
// Pass sk=nil for ad-hoc (non-skill) tasks. Pass taskAgent="" when no CLI override.
func ResolveConfig(sk *skill.Skill, taskAgent, agentIsolation string, cfg *config.Config) ResolvedConfig {
	rc := ResolvedConfig{
		Agent:     cfg.DefaultAgent,
		Isolation: "standard",
		Timeout:   120 * time.Second,
	}

	// Agent config defaults (isolation from agents table)
	if agentIsolation != "" {
		rc.Isolation = agentIsolation
	}

	// Skill frontmatter overrides
	if sk != nil {
		if sk.Agent != "" {
			rc.Agent = sk.Agent
		}
		if sk.Isolation != "" {
			rc.Isolation = sk.Isolation
		}
		if sk.Timeout != "" {
			if d, err := time.ParseDuration(sk.Timeout); err == nil {
				rc.Timeout = d
			}
		}
	}

	// CLI flag wins over everything
	if taskAgent != "" {
		rc.Agent = taskAgent
	}

	return rc
}
