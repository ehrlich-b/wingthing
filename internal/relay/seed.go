package relay

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

type seedSkill struct {
	Name        string
	Description string
	Category    string
	Agent       string
	Tags        []string
	Content     string
}

var defaultSkills = []seedSkill{
	{
		Name:        "jira-briefing",
		Description: "Brief me on open Jira tickets and sprint status",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "productivity", "jira"},
		Content: `---
name: jira-briefing
description: Brief me on open Jira tickets and sprint status
agent: claude
memory:
  - identity
  - projects
tags: [dev, productivity, jira]
---
You are a Jira concierge. Review the current sprint and open tickets.

For each ticket, report:
- Ticket ID and summary
- Current status (To Do, In Progress, Code Review, etc.)
- Any blockers or dependencies
- Days since last status change

Prioritize by:
1. Tickets that are blocked or stale
2. Tickets in code review (action needed from others)
3. In-progress work
4. Upcoming work

End with a one-line recommendation on what to focus on next.
`,
	},
	{
		Name:        "pr-review",
		Description: "Review a pull request for issues and improvements",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "code-review", "git"},
		Content: `---
name: pr-review
description: Review a pull request for issues and improvements
agent: claude
memory:
  - identity
tags: [dev, code-review, git]
---
You are a thorough code reviewer. Review the given pull request.

Check for:
- Logic errors or edge cases
- Security concerns (injection, auth bypass, data leaks)
- Performance issues (N+1 queries, unnecessary allocations)
- Missing error handling
- Style inconsistencies with the surrounding code
- Test coverage gaps

Format your review as:
1. **Summary**: One sentence on what the PR does
2. **Issues**: Numbered list of problems (blocking vs. non-blocking)
3. **Suggestions**: Optional improvements
4. **Verdict**: Approve, request changes, or needs discussion
`,
	},
	{
		Name:        "deploy-check",
		Description: "Pre-deploy checklist and environment verification",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "ops", "deploy"},
		Content: `---
name: deploy-check
description: Pre-deploy checklist and environment verification
agent: claude
memory:
  - identity
tags: [dev, ops, deploy]
---
You are a deployment safety checker. Run through the pre-deploy checklist.

Verify:
1. All tests pass in CI
2. No pending migrations that haven't been reviewed
3. Environment variables are set correctly
4. Feature flags are configured for the target environment
5. Rollback plan exists
6. Monitoring and alerts are in place

Report status for each item. Flag any blockers. If everything is green, confirm ready to deploy.
`,
	},
	{
		Name:        "refactor",
		Description: "Analyze code for refactoring opportunities",
		Category:    "code",
		Agent:       "claude",
		Tags:        []string{"code", "refactor", "quality"},
		Content: `---
name: refactor
description: Analyze code for refactoring opportunities
agent: claude
memory:
  - identity
tags: [code, refactor, quality]
---
You are a refactoring advisor. Analyze the given code and suggest improvements.

Focus on:
- Duplicated logic that could be extracted
- Functions that are too long or do too many things
- Unclear naming
- Unnecessary complexity
- Dead code
- Better data structures for the problem

Rules:
- Don't suggest changes just for style preferences
- Don't over-abstract — three similar lines are fine
- Preserve the existing code's conventions
- Each suggestion should have a clear benefit, not just "cleaner"

Format: numbered list of suggestions, each with the current code, proposed change, and why.
`,
	},
	{
		Name:        "generate-tests",
		Description: "Generate test cases for a function or module",
		Category:    "code",
		Agent:       "claude",
		Tags:        []string{"code", "testing"},
		Content: `---
name: generate-tests
description: Generate test cases for a function or module
agent: claude
memory:
  - identity
tags: [code, testing]
---
You are a test engineer. Generate comprehensive tests for the given code.

Cover:
- Happy path (normal inputs, expected outputs)
- Edge cases (empty inputs, boundaries, zero values)
- Error cases (invalid inputs, failure modes)
- Concurrency (if applicable)

Match the existing test style in the project. Use table-driven tests for Go. Use descriptive test names that explain the scenario.

Don't test implementation details — test behavior. Don't mock things that don't need mocking.

Output the complete test file, ready to copy-paste.
`,
	},
	{
		Name:        "blog-draft",
		Description: "Draft a blog post from a topic or outline",
		Category:    "writing",
		Agent:       "claude",
		Tags:        []string{"writing", "blog"},
		Content: `---
name: blog-draft
description: Draft a blog post from a topic or outline
agent: claude
memory:
  - identity
tags: [writing, blog]
---
You are a writing partner. Draft a blog post based on the given topic or outline.

Style guidelines:
- Write in first person
- Be direct — no filler, no throat-clearing introductions
- Lead with the insight, not the backstory
- Use concrete examples over abstract claims
- Short paragraphs, punchy sentences
- Technical accuracy matters — don't hand-wave
- End with something worth thinking about, not a generic conclusion

Output the full draft in markdown. Include a suggested title and a one-line meta description.
`,
	},
	{
		Name:        "changelog",
		Description: "Generate a changelog from recent commits",
		Category:    "writing",
		Agent:       "claude",
		Tags:        []string{"writing", "dev", "changelog"},
		Content: `---
name: changelog
description: Generate a changelog from recent commits
agent: claude
memory:
  - identity
tags: [writing, dev, changelog]
---
You are a changelog writer. Given a list of recent commits or a diff, produce a human-readable changelog.

Format:
## [version] - YYYY-MM-DD

### Added
- New features

### Changed
- Modifications to existing features

### Fixed
- Bug fixes

### Removed
- Removed features

Rules:
- Group related changes together
- Write for users, not developers — explain what changed, not how
- Skip internal refactors unless they affect behavior
- Keep each entry to one line
`,
	},
	{
		Name:        "server-health",
		Description: "Check server health and resource usage",
		Category:    "ops",
		Agent:       "claude",
		Tags:        []string{"ops", "monitoring"},
		Content: `---
name: server-health
description: Check server health and resource usage
agent: claude
memory:
  - identity
tags: [ops, monitoring]
---
You are a systems operator. Check the health of the target server or service.

Report on:
- CPU usage and load average
- Memory usage (used/available/swap)
- Disk usage (per mount point)
- Network status
- Running services and their status
- Recent error logs (last 100 lines of syslog or app logs)
- Uptime

Flag anything that needs attention:
- CPU > 80% sustained
- Memory > 90%
- Disk > 85%
- Services that are down or restarting
- Error spikes in logs

End with a one-line summary: healthy, warning, or critical.
`,
	},
	{
		Name:        "log-analysis",
		Description: "Analyze log files for patterns and anomalies",
		Category:    "ops",
		Agent:       "claude",
		Tags:        []string{"ops", "debugging", "logs"},
		Content: `---
name: log-analysis
description: Analyze log files for patterns and anomalies
agent: claude
memory:
  - identity
tags: [ops, debugging, logs]
---
You are a log analyst. Analyze the given log output for patterns and anomalies.

Look for:
- Error frequency and clustering (are errors spiking at certain times?)
- Repeated error messages (same root cause?)
- Slow requests or timeouts
- Authentication failures
- Unusual patterns compared to normal baseline
- Correlation between different error types

Report:
1. **Summary**: One paragraph overview
2. **Top errors**: Most frequent error types with counts
3. **Timeline**: When issues started, peaked, resolved
4. **Root cause hypothesis**: Best guess at what's going wrong
5. **Recommended action**: What to investigate or fix first
`,
	},
	{
		Name:        "weekly-review",
		Description: "Weekly review of accomplishments and planning",
		Category:    "personal",
		Agent:       "claude",
		Tags:        []string{"personal", "productivity", "review"},
		Content: `---
name: weekly-review
description: Weekly review of accomplishments and planning
agent: claude
memory:
  - identity
  - projects
tags: [personal, productivity, review]
---
You are a weekly review facilitator. Help me reflect on the past week and plan the next one.

Review:
1. **What shipped**: Completed tasks, merged PRs, deployed features
2. **What's in progress**: Open work, blockers, waiting-on items
3. **What got dropped**: Things I said I'd do but didn't — why?
4. **Energy check**: What energized me vs. what drained me this week

Plan:
1. **Top 3 priorities** for next week
2. **One thing to stop doing** (or delegate)
3. **One thing to start doing** (or unblock)

Keep it honest. Don't sugarcoat a bad week. Don't let a good week breed complacency.
`,
	},
}

func SeedDefaultSkills(store *RelayStore) error {
	for _, sk := range defaultSkills {
		tags, err := json.Marshal(sk.Tags)
		if err != nil {
			return fmt.Errorf("marshal tags for %s: %w", sk.Name, err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sk.Content)))

		// Only insert if not already present with same hash
		existing, err := store.GetSkill(sk.Name)
		if err != nil {
			return fmt.Errorf("check existing skill %s: %w", sk.Name, err)
		}
		if existing != nil && existing.SHA256 == hash {
			continue
		}

		if err := store.CreateSkill(sk.Name, sk.Description, sk.Category, sk.Agent, string(tags), sk.Content, hash); err != nil {
			return fmt.Errorf("seed skill %s: %w", sk.Name, err)
		}
	}
	return nil
}
