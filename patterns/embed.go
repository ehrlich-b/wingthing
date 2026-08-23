package patterns

import "embed"

// Files contains the agent skill and the human-readable setup recipes served by
// the Wingthing site.
//
//go:embed SKILL.md */INSTRUCTIONS.md
var Files embed.FS
