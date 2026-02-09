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
	Publisher   string
	SourceURL   string
	Weight      int
}

var defaultSkills = []seedSkill{
	// ── First-party (weight 100) ────────────────────────────────────
	{
		Name:        "compress",
		Description: "Summarize articles to <=1024 chars for the social feed",
		Category:    "content",
		Agent:       "claude",
		Tags:        []string{"content", "summarization"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: compress
description: Summarize articles to <=1024 chars for the social feed
agent: claude
memory: [feeds]
tags: [content, summarization]
---
Compress the following article into a concise summary of 1024 characters or fewer.
Preserve the key facts, claims, and insights. Drop filler, intros, and CTAs.
Output ONLY the summary text, no preamble.
`,
	},
	{
		Name:        "scorer",
		Description: "Score compressed articles 1-10000 for social feed ranking",
		Category:    "content",
		Agent:       "claude",
		Tags:        []string{"content", "ranking"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: scorer
description: Score compressed articles 1-10000 for social feed ranking
agent: claude
memory: []
tags: [content, ranking]
---
Score this article summary on a scale of 1 to 10000.

Factors (weighted):
- Novelty 30%: Is this genuinely new information or a rehash?
- Information density 25%: Facts-per-sentence ratio
- Practitioner signal 20%: Would someone doing the work find this useful?
- Timeliness 15%: Is this relevant right now?
- Broad appeal 10%: Interest beyond the niche

Output format: SCORE [mass] | [title] | [source] | [one-line reason]
`,
	},
	{
		Name:        "social-post",
		Description: "Format and post content to the wt social feed",
		Category:    "content",
		Agent:       "claude",
		Tags:        []string{"content", "social"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: social-post
description: Format and post content to the wt social feed
agent: claude
memory: [identity]
tags: [content, social]
---
Draft a post for the wt social feed. The post should be concise, informative, and link to the source.
Include only the essential information. No hashtags, no engagement bait.
`,
	},
	{
		Name:        "pr-review",
		Description: "Review a pull request for issues and improvements",
		Category:    "code",
		Agent:       "claude",
		Tags:        []string{"code", "review", "git"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: pr-review
description: Review a pull request for issues and improvements
agent: claude
memory: [identity]
tags: [code, review, git]
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
		Name:        "generate-tests",
		Description: "Generate test cases for a function or module",
		Category:    "code",
		Agent:       "claude",
		Tags:        []string{"code", "testing"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: generate-tests
description: Generate test cases for a function or module
agent: claude
memory: [identity]
tags: [code, testing]
---
Generate comprehensive tests for the given code.

Cover:
- Happy path (normal inputs, expected outputs)
- Edge cases (empty inputs, boundaries, zero values)
- Error cases (invalid inputs, failure modes)
- Concurrency (if applicable)

Match the existing test style in the project. Use table-driven tests for Go.
Don't test implementation details — test behavior.

Output the complete test file, ready to copy-paste.
`,
	},
	{
		Name:        "refactor",
		Description: "Analyze code for refactoring opportunities",
		Category:    "code",
		Agent:       "claude",
		Tags:        []string{"code", "refactor"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: refactor
description: Analyze code for refactoring opportunities
agent: claude
memory: [identity]
tags: [code, refactor]
---
Analyze the given code and suggest refactoring improvements.

Focus on:
- Duplicated logic that could be extracted
- Functions that are too long or do too many things
- Unclear naming
- Unnecessary complexity
- Dead code

Rules:
- Don't suggest changes just for style preferences
- Don't over-abstract — three similar lines are fine
- Preserve the existing code's conventions
- Each suggestion should have a clear benefit

Format: numbered list with current code, proposed change, and why.
`,
	},
	{
		Name:        "jira-briefing",
		Description: "Brief me on open Jira tickets and sprint status",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "productivity", "jira"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: jira-briefing
description: Brief me on open Jira tickets and sprint status
agent: claude
memory: [identity, projects]
tags: [dev, productivity, jira]
---
You are a Jira concierge. Review the current sprint and open tickets.

For each ticket, report:
- Ticket ID and summary
- Current status
- Any blockers or dependencies
- Days since last status change

Prioritize by:
1. Tickets that are blocked or stale
2. Tickets in code review
3. In-progress work
4. Upcoming work

End with a one-line recommendation on what to focus on next.
`,
	},
	{
		Name:        "deploy-check",
		Description: "Pre-deploy checklist and environment verification",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "ops", "deploy"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: deploy-check
description: Pre-deploy checklist and environment verification
agent: claude
memory: [identity]
tags: [dev, ops, deploy]
---
Run through the pre-deploy checklist.

Verify:
1. All tests pass in CI
2. No pending migrations that haven't been reviewed
3. Environment variables are set correctly
4. Feature flags are configured
5. Rollback plan exists
6. Monitoring and alerts are in place

Report status for each item. Flag any blockers.
`,
	},
	{
		Name:        "git-summary",
		Description: "Summarize recent git activity across branches",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "git", "summary"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: git-summary
description: Summarize recent git activity across branches
agent: claude
memory: [identity, projects]
tags: [dev, git, summary]
---
Summarize recent git activity across all branches.

For each active branch:
- Branch name and last commit date
- Number of commits since divergence from main
- Summary of changes
- Whether it has conflicts with main

End with a recommendation: which branches to merge, which to clean up.
`,
	},
	{
		Name:        "dependency-audit",
		Description: "Check dependencies for updates and vulnerabilities",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "security"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: dependency-audit
description: Check dependencies for updates and vulnerabilities
agent: claude
memory: [identity]
tags: [dev, security]
---
Audit the project's dependencies.

Report:
1. **Critical vulnerabilities**: CVEs with severity >= high
2. **Outdated packages**: Major version behind, with migration notes
3. **Unused dependencies**: Imported but not referenced
4. **License issues**: Incompatible or risky licenses
5. **Recommendations**: Priority-ordered update plan
`,
	},
	{
		Name:        "changelog",
		Description: "Generate a changelog from recent commits",
		Category:    "dev",
		Agent:       "claude",
		Tags:        []string{"dev", "changelog"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: changelog
description: Generate a changelog from recent commits
agent: claude
memory: [identity]
tags: [dev, changelog]
---
Given a list of recent commits or a diff, produce a human-readable changelog.

Format:
## [version] - YYYY-MM-DD

### Added / Changed / Fixed / Removed

Rules:
- Group related changes together
- Write for users, not developers
- Skip internal refactors unless they affect behavior
- Keep each entry to one line
`,
	},
	{
		Name:        "blog-draft",
		Description: "Draft a blog post from a topic or outline",
		Category:    "writing",
		Agent:       "claude",
		Tags:        []string{"writing", "blog"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: blog-draft
description: Draft a blog post from a topic or outline
agent: claude
memory: [identity]
tags: [writing, blog]
---
Draft a blog post based on the given topic or outline.

Style:
- Write in first person
- Be direct — no filler, no throat-clearing introductions
- Lead with the insight, not the backstory
- Use concrete examples over abstract claims
- Short paragraphs, punchy sentences
- End with something worth thinking about

Output the full draft in markdown with a suggested title.
`,
	},
	{
		Name:        "postmortem-writer",
		Description: "Write blameless incident postmortems",
		Category:    "writing",
		Tags:        []string{"writing", "ops"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: postmortem-writer
description: Write blameless incident postmortems
tags: [writing, ops]
---
Write a blameless postmortem for the described incident.

## Summary
## Timeline
## Root Cause
## Contributing Factors
## What Went Well
## Action Items (specific, assigned, with deadlines)
## Lessons Learned
`,
	},
	{
		Name:        "weekly-review",
		Description: "Weekly review of accomplishments and planning",
		Category:    "personal",
		Agent:       "claude",
		Tags:        []string{"personal", "productivity"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: weekly-review
description: Weekly review of accomplishments and planning
agent: claude
memory: [identity, projects]
tags: [personal, productivity]
---
Help me reflect on the past week and plan the next one.

Review:
1. What shipped
2. What's in progress
3. What got dropped — and why
4. Energy check: what energized vs. drained me

Plan:
1. Top 3 priorities for next week
2. One thing to stop doing
3. One thing to start doing
`,
	},
	{
		Name:        "server-health",
		Description: "Check server health and resource usage",
		Category:    "ops",
		Agent:       "claude",
		Tags:        []string{"ops", "monitoring"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: server-health
description: Check server health and resource usage
agent: claude
memory: [identity]
tags: [ops, monitoring]
---
Check the health of the target server or service.

Report on: CPU, memory, disk, network, running services, recent error logs, uptime.

Flag: CPU > 80%, memory > 90%, disk > 85%, services down, error spikes.

End with a one-line summary: healthy, warning, or critical.
`,
	},
	{
		Name:        "log-analysis",
		Description: "Analyze log files for patterns and anomalies",
		Category:    "ops",
		Agent:       "claude",
		Tags:        []string{"ops", "debugging"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: log-analysis
description: Analyze log files for patterns and anomalies
agent: claude
memory: [identity]
tags: [ops, debugging]
---
Analyze the given log output for patterns and anomalies.

Report:
1. **Summary**: One paragraph overview
2. **Top errors**: Most frequent error types with counts
3. **Timeline**: When issues started, peaked, resolved
4. **Root cause hypothesis**: Best guess
5. **Recommended action**: What to investigate first
`,
	},
	{
		Name:        "incident-response",
		Description: "Guide incident response and postmortem analysis",
		Category:    "ops",
		Tags:        []string{"ops", "incidents"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: incident-response
description: Guide incident response and postmortem analysis
tags: [ops, incidents]
---
Guide the incident response process.

During incident:
1. Assess: What's broken? Who's affected? Blast radius?
2. Mitigate: Quickest path to stop the bleeding
3. Communicate: Draft status updates for stakeholders
4. Resolve: Root cause and permanent fix

After: timeline, 5 whys, what went well/didn't, action items.
`,
	},
	{
		Name:        "dockerfile-builder",
		Description: "Generate optimized Dockerfiles for any application",
		Category:    "ops",
		Tags:        []string{"ops", "docker"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: dockerfile-builder
description: Generate optimized Dockerfiles for any application
tags: [ops, docker]
---
Generate an optimized Dockerfile for the described application.

Best practices: multi-stage builds, non-root user, layer caching, health checks, minimal base image, pinned versions.

Output the complete Dockerfile with comments.
`,
	},
	{
		Name:        "terraform-writer",
		Description: "Generate Terraform infrastructure-as-code configs",
		Category:    "ops",
		Tags:        []string{"ops", "terraform"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: terraform-writer
description: Generate Terraform infrastructure-as-code configs
tags: [ops, terraform]
---
Generate Terraform configuration for the described infrastructure.

Include: modules, variable definitions, outputs, data sources, naming conventions, backend configuration.

Output complete .tf files ready to apply.
`,
	},

	// ── Anthropic prompt library (weight 80) ────────────────────────
	{
		Name:        "code-consultant",
		Description: "Turn natural language into working code with best practices",
		Category:    "code",
		Tags:        []string{"code", "generation"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/code-consultant",
		Weight:      80,
		Content: `---
name: code-consultant
description: Turn natural language into working code with best practices
tags: [code, generation]
---
Convert the user's natural language description into clean, working code.

Rules:
- Choose the most appropriate language unless specified
- Follow language idioms and best practices
- Include error handling
- Add brief comments only where logic isn't obvious
- State assumptions if requirements are ambiguous
`,
	},
	{
		Name:        "code-clarifier",
		Description: "Explain complex code in plain language",
		Category:    "code",
		Tags:        []string{"code", "explanation"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/code-clarifier",
		Weight:      80,
		Content: `---
name: code-clarifier
description: Explain complex code in plain language
tags: [code, explanation]
---
Break down the given code into plain English.

For each section:
1. What it does (one sentence)
2. How it does it (step by step)
3. Why it's written this way (design choices)

Explain as if teaching a junior developer.
`,
	},
	{
		Name:        "bug-buster",
		Description: "Detect and fix bugs in code",
		Category:    "code",
		Tags:        []string{"code", "debugging"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: bug-buster
description: Detect and fix bugs in code
tags: [code, debugging]
---
Analyze the given code for bugs.

For each bug found:
1. **Location**: Line or section
2. **Bug**: What's wrong
3. **Impact**: What breaks
4. **Fix**: Corrected code

Check for: off-by-one errors, null dereferences, race conditions, resource leaks, logic errors, type mismatches, security issues.
`,
	},
	{
		Name:        "regex-generator",
		Description: "Generate and explain regular expressions",
		Category:    "code",
		Tags:        []string{"code", "regex"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: regex-generator
description: Generate and explain regular expressions
tags: [code, regex]
---
Generate a regular expression matching the described pattern.

Provide:
1. The regex
2. Plain-English explanation of each part
3. Example matches and non-matches
4. Edge cases to watch for

Default to PCRE syntax unless another flavor is specified.
`,
	},
	{
		Name:        "api-designer",
		Description: "Design RESTful API endpoints from requirements",
		Category:    "code",
		Tags:        []string{"code", "api"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: api-designer
description: Design RESTful API endpoints from requirements
tags: [code, api]
---
Design a RESTful API for the described requirements.

For each endpoint: method + path, request/response bodies, status codes, auth requirements, rate limiting.

Follow REST conventions. Use consistent naming. Document error responses.
`,
	},
	{
		Name:        "performance-profiler",
		Description: "Analyze and optimize application performance",
		Category:    "code",
		Tags:        []string{"code", "performance"},
		Publisher:   "wingthing",
		Weight:      100,
		Content: `---
name: performance-profiler
description: Analyze and optimize application performance
tags: [code, performance]
---
Analyze the given code or system for performance issues.

Check for: time complexity, memory allocation, I/O bottlenecks, caching opportunities, concurrency issues, network overhead.

For each issue: location, impact estimate, and optimized version.
`,
	},
	{
		Name:        "git-good",
		Description: "Generate clear git commit messages from diffs",
		Category:    "dev",
		Tags:        []string{"dev", "git"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/git-gud",
		Weight:      80,
		Content: `---
name: git-good
description: Generate clear git commit messages from diffs
tags: [dev, git]
---
Generate a concise git commit message for the given diff.

Rules:
- Imperative mood ("Add feature" not "Added feature")
- First line under 72 characters
- Focus on what and why, not how
- Group related changes
`,
	},
	{
		Name:        "sql-sorcerer",
		Description: "Generate and optimize SQL queries from natural language",
		Category:    "data",
		Tags:        []string{"data", "sql"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/sql-sorcerer",
		Weight:      80,
		Content: `---
name: sql-sorcerer
description: Generate and optimize SQL queries from natural language
tags: [data, sql]
---
Convert the natural language request into an optimized SQL query.

Use standard SQL unless a specific dialect is mentioned. Suggest indexes for large tables. Warn about performance issues. State assumptions if schema isn't provided.
`,
	},
	{
		Name:        "data-organizer",
		Description: "Transform and restructure data between formats",
		Category:    "data",
		Tags:        []string{"data", "transform"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/data-organizer",
		Weight:      80,
		Content: `---
name: data-organizer
description: Transform and restructure data between formats
tags: [data, transform]
---
Transform the given data into the requested format. Supported: JSON, CSV, YAML, XML, SQL, markdown tables, and more.

Preserve all data. Flag any ambiguities. Output ready to copy-paste.
`,
	},
	{
		Name:        "csv-analyzer",
		Description: "Analyze CSV data and extract insights",
		Category:    "data",
		Tags:        []string{"data", "analysis"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/spreadsheet-sorcerer",
		Weight:      80,
		Content: `---
name: csv-analyzer
description: Analyze CSV data and extract insights
tags: [data, analysis]
---
Analyze the given CSV or tabular data.

Provide:
1. Shape: rows, columns, types
2. Summary stats: min, max, mean, median for numeric columns
3. Patterns: trends, outliers, correlations
4. Issues: missing values, duplicates, inconsistencies
5. Insights: key takeaways
`,
	},
	{
		Name:        "meeting-scribe",
		Description: "Extract structured notes from meeting transcripts",
		Category:    "writing",
		Tags:        []string{"writing", "meetings"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/meeting-scribe",
		Weight:      80,
		Content: `---
name: meeting-scribe
description: Extract structured notes from meeting transcripts
tags: [writing, meetings]
---
Extract structured notes from the meeting transcript.

## Attendees
## Key Decisions
## Action Items (who, what, when)
## Open Questions
## Summary (3 sentences max)

Be specific about who said what and who owns each action item.
`,
	},
	{
		Name:        "tone-adjuster",
		Description: "Rewrite text to match a target tone or audience",
		Category:    "writing",
		Tags:        []string{"writing", "tone"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/prose-polisher",
		Weight:      80,
		Content: `---
name: tone-adjuster
description: Rewrite text to match a target tone or audience
tags: [writing, tone]
---
Rewrite the given text to match the requested tone (formal, casual, technical, friendly, etc.) while preserving the core message. Show the rewritten version only.
`,
	},
	{
		Name:        "ethical-analyzer",
		Description: "Evaluate decisions through multiple ethical frameworks",
		Category:    "research",
		Tags:        []string{"research", "ethics"},
		Publisher:   "anthropic",
		SourceURL:   "https://docs.anthropic.com/en/prompt-library/ethical-dilemma-navigator",
		Weight:      80,
		Content: `---
name: ethical-analyzer
description: Evaluate decisions through multiple ethical frameworks
tags: [research, ethics]
---
Analyze the situation through multiple ethical lenses:
1. Utilitarian: Greatest good for the greatest number
2. Deontological: Duty and rules-based
3. Virtue ethics: What would a virtuous person do?
4. Care ethics: Impact on relationships and vulnerable parties

End with a balanced recommendation acknowledging trade-offs.
`,
	},

	// ── awesome-chatgpt-prompts (weight 60) ─────────────────────────
	{
		Name:        "linux-terminal",
		Description: "Act as a Linux terminal — execute commands and show output",
		Category:    "dev",
		Tags:        []string{"dev", "linux"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: linux-terminal
description: Act as a Linux terminal
tags: [dev, linux]
---
Act as a Linux terminal. Reply with what the terminal should show inside one unique code block. Do not write explanations. Do not type commands unless instructed.
`,
	},
	{
		Name:        "javascript-console",
		Description: "Act as a JavaScript console — evaluate expressions",
		Category:    "dev",
		Tags:        []string{"dev", "javascript"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: javascript-console
description: Act as a JavaScript console
tags: [dev, javascript]
---
Act as a JavaScript console. Reply with what the console should show inside one unique code block. Do not write explanations.
`,
	},
	{
		Name:        "senior-engineer",
		Description: "Review code as a senior software engineer",
		Category:    "code",
		Tags:        []string{"code", "review"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: senior-engineer
description: Review code as a senior software engineer
tags: [code, review]
---
Act as a senior software engineer. Review the provided code for bugs, performance issues, security concerns, and maintainability. Suggest concrete improvements. Be direct and specific.
`,
	},
	{
		Name:        "architect",
		Description: "Design software architecture for a given problem",
		Category:    "code",
		Tags:        []string{"code", "architecture"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: architect
description: Design software architecture for a given problem
tags: [code, architecture]
---
Act as an IT architect. Provide a technical architecture including: component diagram, technology stack, data flow, API contracts, and deployment strategy. Justify your choices.
`,
	},
	{
		Name:        "cybersecurity-advisor",
		Description: "Analyze systems and code for security vulnerabilities",
		Category:    "ops",
		Tags:        []string{"ops", "security"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: cybersecurity-advisor
description: Analyze systems and code for security vulnerabilities
tags: [ops, security]
---
Act as a cybersecurity specialist. Analyze the provided system, code, or configuration for vulnerabilities. Classify by severity (critical, high, medium, low). Provide specific remediation steps. Reference OWASP, CWE, or CVE where applicable.
`,
	},
	{
		Name:        "devops-engineer",
		Description: "Help with CI/CD, containers, and infrastructure",
		Category:    "ops",
		Tags:        []string{"ops", "devops"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: devops-engineer
description: Help with CI/CD, containers, and infrastructure
tags: [ops, devops]
---
Act as a DevOps engineer. Help with Dockerfiles, CI/CD pipelines, Kubernetes manifests, Terraform configs, monitoring setup, and deployment strategies. Optimize for reliability and simplicity.
`,
	},
	{
		Name:        "database-admin",
		Description: "Optimize database schemas, queries, and performance",
		Category:    "data",
		Tags:        []string{"data", "database"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: database-admin
description: Optimize database schemas, queries, and performance
tags: [data, database]
---
Act as a database administrator. Analyze the provided schema or query for normalization issues, missing indexes, performance bottlenecks, and data integrity concerns. Provide optimized versions.
`,
	},
	{
		Name:        "tech-writer",
		Description: "Write clear technical documentation",
		Category:    "writing",
		Tags:        []string{"writing", "docs"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: tech-writer
description: Write clear technical documentation
tags: [writing, docs]
---
Write clear, concise technical documentation. Use short sentences. Include code examples where relevant. Structure with headers, lists, and tables. Write for someone who knows the domain but not this specific tool.
`,
	},
	{
		Name:        "editor",
		Description: "Edit and improve writing for clarity and impact",
		Category:    "writing",
		Tags:        []string{"writing", "editing"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: editor
description: Edit and improve writing for clarity and impact
tags: [writing, editing]
---
Review and improve the given text for clarity, conciseness, grammar, structure, and impact. Show the edited version with brief notes on significant changes. Preserve the author's voice.
`,
	},
	{
		Name:        "email-composer",
		Description: "Draft professional emails from bullet points",
		Category:    "writing",
		Tags:        []string{"writing", "email"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: email-composer
description: Draft professional emails from bullet points
tags: [writing, email]
---
Draft a professional email from the given bullet points. Clear subject line, get to the point in the first sentence, one ask per email, end with a specific next step.
`,
	},
	{
		Name:        "translator",
		Description: "Translate text between languages preserving tone",
		Category:    "writing",
		Tags:        []string{"writing", "translation"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: translator
description: Translate text between languages preserving tone
tags: [writing, translation]
---
Translate the given text to the requested language. Preserve tone, formality, and intent. Use cultural equivalents for idioms. Flag phrases where translation loses significant nuance.
`,
	},
	{
		Name:        "ux-designer",
		Description: "Evaluate and improve user experience designs",
		Category:    "research",
		Tags:        []string{"research", "ux"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: ux-designer
description: Evaluate and improve user experience designs
tags: [research, ux]
---
Evaluate the described interface or user flow. Identify friction points, accessibility issues, and improvement opportunities. Suggest concrete changes. Prioritize by user impact.
`,
	},
	{
		Name:        "product-manager",
		Description: "Help define product requirements and prioritize features",
		Category:    "research",
		Tags:        []string{"research", "product"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: product-manager
description: Help define product requirements and prioritize features
tags: [research, product]
---
Help define requirements for the described feature. Provide user stories, acceptance criteria, edge cases, and priority ranking (must-have, should-have, nice-to-have). Consider technical feasibility and user value.
`,
	},
	{
		Name:        "startup-advisor",
		Description: "Evaluate business ideas and provide strategic advice",
		Category:    "research",
		Tags:        []string{"research", "business"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: startup-advisor
description: Evaluate business ideas and provide strategic advice
tags: [research, business]
---
Evaluate the described business idea. Assess market size, competitive landscape, unit economics, risks, and go-to-market strategy. Be candid about weaknesses. Suggest pivots if the idea has fatal flaws.
`,
	},
	{
		Name:        "math-tutor",
		Description: "Solve and explain mathematical problems step by step",
		Category:    "research",
		Tags:        []string{"research", "math"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: math-tutor
description: Solve and explain mathematical problems step by step
tags: [research, math]
---
Solve the given problem step by step. Show your work. Explain each step as if teaching someone who understands the prerequisites but hasn't seen this type of problem.
`,
	},
	{
		Name:        "explainer",
		Description: "Explain complex topics at any expertise level",
		Category:    "research",
		Tags:        []string{"research", "explanation"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: explainer
description: Explain complex topics at any expertise level
tags: [research, explanation]
---
Explain the given topic clearly. Adjust complexity to the audience level (beginner, intermediate, expert). Use analogies for abstract concepts. Start with the core idea, then add layers of detail.
`,
	},
	{
		Name:        "debate-partner",
		Description: "Argue both sides of any issue with evidence",
		Category:    "research",
		Tags:        []string{"research", "analysis"},
		Publisher:   "awesome-chatgpt-prompts",
		SourceURL:   "https://github.com/f/awesome-chatgpt-prompts",
		Weight:      60,
		Content: `---
name: debate-partner
description: Argue both sides of any issue with evidence
tags: [research, analysis]
---
Present strong arguments for BOTH sides of the given topic. For each side: core thesis, 3 supporting arguments with evidence, strongest counterargument addressed. End with which position has stronger support and why.
`,
	},

	// ── antigravity / K-Dense (weight 50) ───────────────────────────
	{
		Name:        "scientific-analysis",
		Description: "Analyze research papers and experimental results",
		Category:    "research",
		Tags:        []string{"research", "science"},
		Publisher:   "K-Dense-AI",
		SourceURL:   "https://github.com/K-Dense-AI/claude-scientific-skills",
		Weight:      50,
		Content: `---
name: scientific-analysis
description: Analyze research papers and experimental results
tags: [research, science]
---
Analyze the provided research paper, data, or experimental results.

Evaluate: methodology, whether data support conclusions, limitations, significance, and reproducibility.

Be rigorous but fair. Distinguish between fatal flaws and minor issues.
`,
	},
	{
		Name:        "data-scientist",
		Description: "Analyze datasets and build statistical models",
		Category:    "data",
		Tags:        []string{"data", "statistics"},
		Publisher:   "K-Dense-AI",
		SourceURL:   "https://github.com/K-Dense-AI/claude-scientific-skills",
		Weight:      50,
		Content: `---
name: data-scientist
description: Analyze datasets and build statistical models
tags: [data, statistics]
---
Analyze the given dataset. Approach: understand the data, exploratory analysis, feature engineering, model selection, evaluation.

Write code in Python (pandas, scikit-learn). Explain reasoning at each step.
`,
	},
	{
		Name:        "shell-expert",
		Description: "Generate and explain shell commands and scripts",
		Category:    "dev",
		Tags:        []string{"dev", "shell"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: shell-expert
description: Generate and explain shell commands and scripts
tags: [dev, shell]
---
Generate the shell command or script for the described task. Explain each flag. Note common pitfalls. Provide a safer alternative if the command is destructive.

Default to bash. Mention platform-specific differences.
`,
	},
	{
		Name:        "api-tester",
		Description: "Generate curl commands and test plans for APIs",
		Category:    "dev",
		Tags:        []string{"dev", "api", "testing"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: api-tester
description: Generate curl commands and test plans for APIs
tags: [dev, api, testing]
---
Generate test commands for the described API endpoint. Include: curl commands, happy path, error cases (400, 401, 404, 500), edge cases, rate limiting behavior, expected responses.
`,
	},
	{
		Name:        "language-detector",
		Description: "Detect the programming or natural language of input text",
		Category:    "code",
		Tags:        []string{"code", "detection"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: language-detector
description: Detect the programming or natural language of input text
tags: [code, detection]
---
Identify the programming language (or natural language) of the given text. Output the language name and confidence level. If code, note the likely framework or library based on imports and patterns.
`,
	},
	{
		Name:        "migration-writer",
		Description: "Generate safe database migration scripts",
		Category:    "data",
		Tags:        []string{"data", "migration"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: migration-writer
description: Generate safe database migration scripts
tags: [data, migration]
---
Generate a database migration for the described schema change. Include forward and rollback scripts. Handle existing data. No downtime. Idempotent. Include validation queries.
`,
	},
	{
		Name:        "json-transformer",
		Description: "Transform, query, and restructure JSON data",
		Category:    "data",
		Tags:        []string{"data", "json"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: json-transformer
description: Transform, query, and restructure JSON data
tags: [data, json]
---
Transform the given JSON according to instructions. Support: flatten, nest, rename, filter, map, reduce, merge, split, type conversion. Output valid JSON. Provide the jq expression too if applicable.
`,
	},
	{
		Name:        "readme-generator",
		Description: "Generate a comprehensive README from project context",
		Category:    "writing",
		Tags:        []string{"writing", "docs"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: readme-generator
description: Generate a comprehensive README from project context
tags: [writing, docs]
---
Generate a README.md. Include: project name, what problem it solves, quick start, usage examples, configuration, contributing, license. Keep it scannable. Code over prose.
`,
	},
	{
		Name:        "adr-writer",
		Description: "Write Architecture Decision Records",
		Category:    "writing",
		Tags:        []string{"writing", "architecture"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: adr-writer
description: Write Architecture Decision Records
tags: [writing, architecture]
---
Write an ADR for the described decision. Include: Status, Context, Decision, Consequences. Be specific about alternatives considered and why they were rejected.
`,
	},
	{
		Name:        "commit-review",
		Description: "Review a series of commits for quality and consistency",
		Category:    "dev",
		Tags:        []string{"dev", "git"},
		Publisher:   "antigravity-skills",
		SourceURL:   "https://github.com/sickn33/antigravity-awesome-skills",
		Weight:      50,
		Content: `---
name: commit-review
description: Review a series of commits for quality and consistency
tags: [dev, git]
---
Review the commit history. Are commits atomic? Are messages clear? Is the order logical? Should any be squashed or split? Provide specific suggestions.
`,
	},
}

func SeedDefaultSkills(store *RelayStore) error {
	// Build set of curated skill names
	keep := make(map[string]bool, len(defaultSkills))
	for _, sk := range defaultSkills {
		keep[sk.Name] = true
	}

	// Remove old skills not in the curated list
	existing, err := store.ListSkills("")
	if err != nil {
		return fmt.Errorf("list existing skills: %w", err)
	}
	for _, sk := range existing {
		if !keep[sk.Name] {
			if err := store.DeleteSkill(sk.Name); err != nil {
				return fmt.Errorf("delete old skill %s: %w", sk.Name, err)
			}
		}
	}

	// Upsert curated skills
	for _, sk := range defaultSkills {
		tags, err := json.Marshal(sk.Tags)
		if err != nil {
			return fmt.Errorf("marshal tags for %s: %w", sk.Name, err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(sk.Content)))

		// Only insert if not already present with same hash
		ex, err := store.GetSkill(sk.Name)
		if err != nil {
			return fmt.Errorf("check existing skill %s: %w", sk.Name, err)
		}
		if ex != nil && ex.SHA256 == hash {
			continue
		}

		publisher := sk.Publisher
		if publisher == "" {
			publisher = "wingthing"
		}

		if err := store.CreateSkill(sk.Name, sk.Description, sk.Category, sk.Agent, string(tags), sk.Content, hash, publisher, sk.SourceURL, sk.Weight); err != nil {
			return fmt.Errorf("seed skill %s: %w", sk.Name, err)
		}
	}
	return nil
}
