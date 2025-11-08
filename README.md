# WingThing.ai

**Emotional infrastructure for busy humans.**

WingThing is a support companion that helps you maintain the human connections that actually matter. Not therapy. Not a friend replacement. A system that turns scattered venting into actionable support plans.

## The Core Idea

**Dreams** - Nightly RAG synthesis that processes your day and generates a Morning Card with 3 actionable support moves.

**The experiment:** Does a nightly synthesis + morning briefing actually provide value? Let's find out.

## v0.1 Scope

**Single-user proof of concept:**
- You run it locally with your own API keys (BYOK)
- Discord bot that DMs you throughout the day
- Nightly Dreams job that synthesizes your conversations
- Morning Card delivered via Discord DM

**No billing. No auth. No social features. No web UI.**

Just: "Does this help me?"

## How It Works

**Evening:** Chat with WingThing bot via Discord DM while doing dishes
**Night (2am):** WingThing runs Dreams job (nightly RAG synthesis)
**Morning (7am):** Wake up to Morning Card with 3 specific support actions
**Day:** Continue chatting as needed

## Architecture (v0.1)

```
cmd/wingthing/main.go          # Single binary
internal/
  discord/                     # Discord bot (single user hardcoded)
  dreams/                      # Nightly RAG job
  memory/                      # SQLite storage
  llm/                         # Anthropic/OpenAI client
config.yaml                    # Your API keys + settings
```

**Run it:**
```bash
# Set up config
cp config.example.yaml config.yaml
# Edit config.yaml with your keys

# Run the bot
./wingthing bot

# Run Dreams manually (testing)
./wingthing dream

# Schedule Dreams (cron)
0 2 * * * /path/to/wingthing dream
```

## Tech Stack

- **Backend:** Go 1.24+
- **Database:** SQLite (local file)
- **LLM:** Anthropic Claude (or OpenAI)
- **Discord:** discordgo library

## Current Status

**Phase:** v0.1 - Single-user BYOK proof of concept
**Goal:** Validate that Dreams → Morning Card provides value

See [TODO.md](./TODO.md) for development plan.
See [CLAUDE.md](./CLAUDE.md) for development guidance.
See [draft0.md](./draft0.md) for long-term vision.

## Future (if v0.1 works)

- Multi-user SaaS with billing
- Web dashboard for memory management
- Two Wings social features
- Voice integration

But first: **Does the core loop work?**

---

**License:** TBD
**Status:** Proof of concept
