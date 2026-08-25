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
	// Timeout 0 means "no timeout" -- the runner only arms a deadline when
	// timeout > 0. Agent tasks are long-running by nature, so an unrequested
	// wall-clock cap here silently kills real work: a 120s default meant every
	// ad-hoc `wt run` was SIGKILLed at two minutes with no way to raise it,
	// since only skill frontmatter could override. Callers that want a bound
	// pass one (`wt run --timeout`, skill frontmatter, or task.TimeoutSeconds).
	rc := ResolvedConfig{
		Agent:     cfg.DefaultAgent,
		Isolation: "standard",
		Timeout:   0,
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
