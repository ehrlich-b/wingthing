# WingThing v0.1 Development Plan

## Goal

Build a single-user BYOK proof of concept to validate: **Does Dreams → Morning Card actually provide value?**

**Out of scope:** Multi-user, billing, auth, social features, web UI

## Week 1: Core Infrastructure

### Project Setup
- [ ] Initialize Go module (`go mod init github.com/ehrlich-b/wingthing`)
- [ ] Set up project structure (cmd/, internal/)
- [ ] Create Makefile for build/test/run commands
- [ ] Add .gitignore (config.yaml, *.db, binaries)
- [ ] Create config.example.yaml with docs

### Database Layer (SQLite)
- [ ] Design schema: users, messages, memories, dreams
- [ ] Set up SQLite with migrations
- [ ] Implement basic CRUD for messages
- [ ] Implement memory storage (facts, relationships, boundaries)

### LLM Integration
- [ ] Create LLM provider interface
- [ ] Implement Anthropic client (Claude)
- [ ] Implement OpenAI client (GPT-4)
- [ ] Add conversation context building
- [ ] Add basic error handling and retries

### Configuration
- [ ] Load config from config.yaml
- [ ] Support env vars for secrets (ANTHROPIC_API_KEY, etc.)
- [ ] Validate required fields on startup
- [ ] Document all config options

## Week 2: Discord Bot

### Basic Bot Setup
- [ ] Discord bot registration and permissions
- [ ] Implement bot startup and connection
- [ ] Handle DM messages from your user ID (hardcoded)
- [ ] Store messages in SQLite with timestamp
- [ ] Basic message reply (echo bot test)

### Conversation Handling
- [ ] Build conversation context from message history
- [ ] Send context to LLM and get response
- [ ] Handle message threading/continuity
- [ ] Add typing indicator while processing
- [ ] Error handling and user-friendly error messages

### Commands
- [ ] `/start` - Introduction message
- [ ] `/help` - Show available commands
- [ ] `/stats` - Show message count, last Dream run
- [ ] `/dream` - Manually trigger Dreams job
- [ ] `/forget` - Clear conversation context (fresh start)

## Week 3: Dreams Pipeline

### Dreams Job
- [ ] CLI command: `wingthing dream`
- [ ] Load last 24 hours of messages from DB
- [ ] Build Dreams synthesis prompt
- [ ] Call LLM with full day context
- [ ] Parse LLM output into structured Morning Card

### Morning Card Structure
```yaml
yesterday_summary: ["bullet 1", "bullet 2", "bullet 3"]
open_loops: ["loop 1", "loop 2"]
support_plan:
  - action: "text_check_in"
    target: "person"
    draft: "message text"
  - action: "prep"
    draft: "todo item"
  - action: "boundary"
    draft: "suggestion"
```

### Morning Card Delivery
- [ ] Store generated Morning Card in DB
- [ ] Format Morning Card for Discord (nice embed)
- [ ] Schedule delivery for configured time (e.g., 7am)
- [ ] Add simple action buttons (👍 acknowledge, 📝 edit)
- [ ] Track if Morning Card was read/acknowledged

### Memory Building
- [ ] Extract key facts from Dreams synthesis
- [ ] Store in memory table (type: fact/relationship/boundary/pattern)
- [ ] Include memories in next Dreams context
- [ ] Simple memory query for context building

## Week 4: Polish & Testing

### Prompt Engineering
- [ ] Tune Dreams synthesis prompt for good output
- [ ] Tune conversation prompt for support tone
- [ ] Add system prompts for boundaries ("not therapy")
- [ ] Test with real conversations from week 3

### Cron Integration
- [ ] Document cron setup for Dreams job
- [ ] Add logging for cron runs
- [ ] Handle errors gracefully (alert on Discord?)
- [ ] Test scheduling at 2am

### User Experience
- [ ] Write good onboarding message
- [ ] Add personality/tone to bot responses
- [ ] Handle edge cases (no messages in 24h, bot offline, etc.)
- [ ] Add emoji reactions for quick interactions

### Documentation
- [ ] Write setup instructions (Discord bot creation, config, cron)
- [ ] Document config.yaml options
- [ ] Add troubleshooting guide
- [ ] Write "how to use" guide for yourself

## Configuration File Structure

```yaml
# config.yaml
discord:
  token: "your-bot-token"
  user_id: "your-discord-user-id"  # Single user for v0.1

llm:
  provider: "anthropic"  # or "openai"
  api_key: "your-api-key"
  model: "claude-3-5-sonnet-20241022"

dreams:
  schedule_time: "07:00"  # When to deliver Morning Card
  timezone: "America/New_York"

database:
  path: "./wingthing.db"

logging:
  level: "info"
  file: "./wingthing.log"
```

## Success Criteria (End of Week 4)

- [ ] Bot responds to Discord DMs with helpful, supportive messages
- [ ] Dreams runs successfully every night (via cron)
- [ ] Morning Card is delivered and actually useful
- [ ] Memories accumulate and provide better context over time
- [ ] **YOU actually use it for 2 weeks straight**

## If v0.1 Works → v0.2 Scope

- Multi-user support (still BYOK, but multiple people)
- Web dashboard for memory editing
- Better memory management UI
- Voice note support (Discord voice messages)
- Calendar integration (read-only for context)

## If v0.1 Doesn't Work

- Pivot to different features
- Or kill the project (better to know early!)

---

**Current Focus:** Week 1 - Core Infrastructure

**Next Milestone:** Working bot that can chat, then Dreams pipeline
