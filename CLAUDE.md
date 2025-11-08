# WingThing - Claude Code Guidance

## Project Overview

WingThing is a Go-based Discord bot that provides personal support through:
1. Daily conversation via Discord DMs
2. Nightly "Dreams" RAG synthesis
3. Morning Card with actionable support moves

**v0.1 Scope:** Single-user BYOK proof of concept (no multi-user, no billing, no social features)

## Architecture

### Core Components

**Main Binary (`cmd/wingthing/main.go`)**
- Cobra CLI with commands: `bot`, `dream`
- Bot mode: runs Discord bot continuously
- Dream mode: one-shot RAG synthesis job (for cron)

**Discord Layer (`internal/discord/`)**
- Bot connection and authentication
- DM message handling (single hardcoded user ID)
- Message storage to database
- Command handlers (/start, /help, /dream, etc.)
- Morning Card formatting and delivery

**Dreams System (`internal/dreams/`)**
- RAG synthesis pipeline
- Morning Card generation
- Memory extraction and storage
- Pattern recognition

**Memory Layer (`internal/memory/`)**
- SQLite database interface
- Message storage and retrieval
- Memory CRUD (facts, relationships, boundaries, patterns)
- Context building for LLM calls

**LLM Integration (`internal/llm/`)**
- Provider interface (Anthropic, OpenAI)
- Conversation context management
- Token usage tracking
- Error handling and retries

**Configuration (`internal/config/`)**
- YAML config loading
- Environment variable support for secrets
- Settings validation

## Key Design Principles

### 1. Single User First
- Hardcode user ID in config.yaml for v0.1
- No auth, no multi-tenancy, no complexity
- Focus: Does the core loop work?

### 2. BYOK (Bring Your Own Key)
- User provides their own LLM API keys
- No token costs to worry about initially
- Simpler to iterate and test

### 3. Local-First
- SQLite database (single file)
- Run locally on your machine or VPS
- No external dependencies except Discord API and LLM API

### 4. Simple Deployment
- Single binary
- Cron for scheduling Dreams
- No containers or orchestration needed (yet)

## Development Commands

```bash
# Build
make build

# Run bot (stays running)
./wingthing bot

# Run Dreams manually
./wingthing dream

# Run tests
make test

# Format code
make fmt
```

## Database Schema (SQLite)

```sql
-- messages: all Discord DM messages
CREATE TABLE messages (
  id INTEGER PRIMARY KEY,
  discord_id TEXT UNIQUE,
  user_id TEXT,  -- Discord user ID
  content TEXT,
  timestamp DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- memories: extracted facts/patterns/boundaries
CREATE TABLE memories (
  id INTEGER PRIMARY KEY,
  type TEXT,  -- 'fact', 'relationship', 'boundary', 'pattern'
  content TEXT,
  metadata JSON,  -- flexible storage for additional context
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- dreams: generated Morning Cards
CREATE TABLE dreams (
  id INTEGER PRIMARY KEY,
  date DATE UNIQUE,
  yesterday_summary JSON,
  open_loops JSON,
  support_plan JSON,
  delivered_at DATETIME,
  acknowledged BOOLEAN DEFAULT FALSE,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

## Configuration File

```yaml
# config.yaml (not checked into git)
discord:
  token: "YOUR_DISCORD_BOT_TOKEN"
  user_id: "YOUR_DISCORD_USER_ID"

llm:
  provider: "anthropic"  # or "openai"
  api_key: "YOUR_API_KEY"
  model: "claude-3-5-sonnet-20241022"

dreams:
  schedule_time: "07:00"
  timezone: "America/New_York"

database:
  path: "./wingthing.db"

logging:
  level: "info"
  file: "./wingthing.log"
```

## Code Style Standards

- Standard Go formatting (gofmt)
- Error handling: always wrap errors with context
- Logging: use structured logging (slog)
- Tests: co-located with source files
- Comments: explain *why*, not *what*

## Dependencies

- `github.com/spf13/cobra` - CLI framework
- `github.com/bwmarrin/discordgo` - Discord API
- `github.com/anthropics/anthropic-sdk-go` - Anthropic client
- `github.com/sashabaranov/go-openai` - OpenAI client (optional)
- `modernc.org/sqlite` - Pure Go SQLite
- `gopkg.in/yaml.v3` - YAML config parsing

## Development Workflow

1. **Start with Discord bot** - Get basic conversation working
2. **Add Dreams manually** - Run `wingthing dream` by hand, iterate on prompts
3. **Polish output** - Make Morning Cards actually useful
4. **Add cron** - Automate the Dreams → delivery loop
5. **Use it yourself** - 2 weeks of real usage to validate

## Testing Strategy

### Unit Tests
- Memory storage/retrieval
- Config loading and validation
- LLM client mocking

### Integration Tests
- Discord message handling (with mock Discord API)
- Dreams pipeline end-to-end
- Morning Card generation

### Manual Testing
- Run bot locally and chat with it
- Manually trigger Dreams and review output
- Validate Morning Cards are useful

## Current State

**Status:** Initial setup phase
**Next:** Project scaffolding, Discord bot basics, LLM integration

See [TODO.md](./TODO.md) for detailed development plan.

## Notes for Claude Code

- This is a solo project, move fast
- Prefer simple solutions over clever ones
- Don't optimize prematurely - make it work first
- The success metric is: "Do I actually use this every day?"
- If something isn't working, pivot quickly
