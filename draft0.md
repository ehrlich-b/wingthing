# WingThing.ai: The "Go Anywhere" Support Companion

## The Synthesis

After reading all three brainstorms, here's what jumped out: you've found genuine white-space between **parasocial AI companions** (Replika/Character.AI) that trap users and **clinical therapy apps** (Wysa/Woebot) that feel medical. WingThing's superpower is being **infrastructure for human connection**, not a replacement for it.

The two killer features from all three docs:
1. **Dreams** - Nightly RAG synthesis that turns scattered venting into actionable morning briefings
2. **Two Wings** - Structured social features that help you find and maintain *real human* support

But here's the **architectural insight** that changes everything given your constraints:

## The Single Binary, Platform-Agnostic Play

**Forget mobile apps.** Build a **single Go binary that runs everywhere** and meets users where they already live:

### Architecture: One Binary, Four Modes

```bash
# Run as privileged backend server
wingthing server --port 8080

# Run as Discord bot (connects to your server)
wingthing discord --server http://localhost:8080

# Run as CLI client (for power users)
wingthing chat

# Run as web server (embedded TypeScript SPA)
wingthing web --port 3000
```

### Why This Is Brilliant

**1. Discord as the Primary Interface**
- People are *already in Discord all day* (parents have school Discords, work Discords, hobby Discords)
- DMs are perfect for the "support companion" UX
- Morning briefings arrive as Discord messages
- Vent sessions in private DMs
- Discord voice channels for future voice features
- **The "Two Wings" feature is native**: Discord communities, private 1:1 DMs, group DMs for micro-pods

**2. Embedded Web Dashboard for "Heavy" UI**
- Memory Ledger (audit/edit your persona)
- Wing Matching interface
- Settings and boundaries configuration
- Pattern visualization ("your Tuesdays are rough")
- Built as TypeScript/React SPA, compiled to static assets, embedded via `go:embed`
- Served directly from the binary - no separate deployment

**3. Text/SMS Bridge** (optional, via Twilio)
- For non-Discord users
- Morning briefings via text
- Quick check-ins

**4. CLI Mode for Hackers**
- Terminal-based interaction
- Self-hosted, local-first option
- Appeals to privacy-conscious power users

### Technical Stack

**Single Go Binary:**
```
cmd/wingthing/
  main.go              # Cobra CLI with mode flags
internal/
  server/              # HTTP API server
  discord/             # Discord bot integration
  dreams/              # Nightly RAG pipeline
  memory/              # Memory graph (SQLite/Postgres)
  llm/                 # Provider abstraction (Anthropic/OpenAI)
  wings/               # Matching engine for "Two Wings"
web/
  dist/                # Embedded TypeScript build (go:embed)
  src/                 # React/TypeScript source
```

**Why Single Binary Rocks:**
- **Deploy anywhere**: VPS, home server, Docker, fly.io
- **Local-first option**: Run server mode locally with CLI
- **Privacy story**: Self-hostable, E2EE backups
- **Development velocity**: No app store approval, instant iteration
- **Cost**: No mobile app maintenance nightmare

## The Product Pitch

### Positioning: "Emotional Infrastructure for Busy Humans"

Not therapy. Not a friend replacement. **A support system that helps you maintain the human connections that actually matter.**

### Core Loop (Discord-First UX)

**Evening (8-10pm):**
```
WingThing Bot: Hey! Quick 60-second debrief before I "sleep on it"?

You: [voice note while folding laundry]
     "Ugh, tomorrow's the parent-teacher conference and Jake still
     hasn't done his project and my partner is traveling and I'm
     just so tired of being the default parent for everything..."

WingThing: Heard. I'm on it. Sleep well.
```

**Night (Dreams RAG Process):**
- Ingests: Discord messages, voice notes, calendar (via Google integration), optional journal
- Synthesizes: patterns, open loops, support opportunities
- Generates: Morning Card with 3 actionable support moves

**Morning (7am):**
```
WingThing Bot: ☕ Morning Card

Yesterday in 3 bullets:
• Vented about parent-teacher conf stress + solo parenting
• Rescheduled workout (3rd time this month)
• Had good call with Sarah about the school fundraiser

Open loops:
• Jake's project (due Friday)
• Partner traveling (back Wed)

Your support plan for today:
1. 📱 Draft ready: "Hey [partner], I need you to video-call Jake
   tonight to help with his project. I'm stretched thin."
   → Send? [Yes] [Edit]

2. 🤝 Sarah mentioned she's free Thu - want me to suggest a
   quick coffee swap? You help with fundraiser, she helps with
   pickup? → [Draft invite]

3. 💪 You've moved workouts 3x - archive "Tue 6am gym" as
   unrealistic? → [Yes, it's not working]

You got this. 💙
```

### The "Two Wings" Discord Integration

**Wing Matching in Discord:**
- Opt-in matching based on: life stage, location, energy levels, stated needs
- Private 1:1 introductions via DM
- Suggested micro-commitments: "Sunday 8:30pm, 20-min reset call?"

**Micro-Pods:**
- 3-5 person group DMs
- WingThing facilitates: agenda, timekeeping, action items
- Examples: "New parent pod," "Sunday reset," "Tuesday vent circle"

**The KPI:** H2H (Human-to-Human) minutes created, NOT time in app

### The Web Dashboard (Embedded TypeScript)

Accessible at `localhost:8080` (or your domain):

**Memory Ledger:**
- See all persona entries: relationships, boundaries, patterns
- Accept/merge/forget controls
- Red-line rules: "Never give advice before 7am," "Don't mention X"

**Wing Matching:**
- Browse potential wings (privacy-preserving)
- Manage active connections
- Track micro-commitments

**Patterns & Insights:**
- "Your Tuesdays are consistently rough - let's prep for next Tuesday"
- Energy accounting over time
- Open loops dashboard

**Settings:**
- Tone calibration: "steady co-pilot," "dry humor," "no pep talks before 7am"
- Integration toggles: Google Calendar, Todoist, voice notes
- Privacy controls: local-first mode, E2EE backups

## Differentiation Summary

| Feature | Replika/Pi | Wysa/Woebot | WingThing |
|---------|------------|-------------|-----------|
| **Goal** | Be your friend | Treat symptoms | Engineer real connection |
| **KPI** | Time in app | PHQ-9 scores | H2H minutes created |
| **Memory** | Ephemeral chat | Clinical notes | Auditable persona graph |
| **Social** | Parasocial only | None | Structured human matching |
| **Interface** | Mobile app | Mobile app | Discord/Web/CLI/Text |
| **Action Bias** | Reactive | CBT exercises | Proactive support plans |
| **Deployment** | Cloud SaaS | Cloud SaaS | Self-hostable Go binary |

## MVP Roadmap (6-8 Weeks)

**Week 1-2: Core Server**
- Go binary with SQLite persistence
- LLM provider integration (Anthropic/OpenAI)
- Basic memory graph (relationships, boundaries, facts)

**Week 3-4: Dreams Pipeline**
- Nightly RAG job (cron-based)
- Morning Card generation
- Context-aware action suggestions

**Week 5-6: Discord Integration**
- Bot setup with slash commands
- DM-based conversations
- Scheduled morning briefings
- Voice note ingestion

**Week 7-8: Web Dashboard (MVP)**
- Embedded React SPA (go:embed)
- Memory Ledger UI
- Settings/boundaries configuration

**Future (v1.1):**
- Wing matching engine
- Micro-pod facilitation
- Voice integration (Hume EVI-style)
- Google Calendar/Todoist integration

## Business Model (NEEDS MORE THOUGHT)

**Current thinking:**

**Individual:** $12/month
**Family:** $20/month (2 adults, shared family context)
**Self-hosted:** Free (BYOK - bring your own API keys)

**Later:**
- Corporate wellness (non-clinical support tool)
- "Wing Fund" - subscribers subsidize wings for those in need

**Open questions:**
- Is Discord-first limiting the addressable market?
- How do we monetize self-hosters without being predatory?
- What's the revenue model for the "Two Wings" matching features?
- Could we do a freemium model with limited Dreams runs?
- Enterprise/team pricing for small companies?
- API access for developers?
- White-label licensing?

**Key constraint:** Single binary + self-hostable means we can't rely on platform lock-in. Need to think through:
- What features justify subscription vs. one-time purchase?
- How to balance open-source ethos with sustainable business?
- Is there a "cloud-hosted convenience" tier vs. self-hosted?

## Why This Wins

1. **No app store friction** - Discord is already installed, web works everywhere
2. **Platform agnostic** - Same backend serves Discord, CLI, web, future SMS/Telegram
3. **Privacy-first narrative** - Self-hostable, local-first option, E2EE backups
4. **Developer-friendly** - Single Go binary, easy to hack/extend
5. **Lower CAC** - Discord communities are natural distribution channels
6. **Unique positioning** - "Bionic wing" that measures success by getting you *off* the platform and *onto* calls with real humans

## The Name Works Perfectly

**"WingThing.ai"** lands because:
- Whimsical, not clinical
- "Wing" = support (wingman, wing and a prayer)
- "Thing" = tool/utility, not friend replacement
- "Two wings to fly" = AI wing + human wing

---

## Next Steps

1. **Finalize business model** - resolve the monetization questions above
2. **Technical spike** - validate Go binary + embedded web + Discord bot integration
3. **Write project scaffolding** - cmd/, internal/, web/ structure
4. **First feature: Dreams pipeline** - prove out the core value prop
5. **Alpha with 5-10 users** - validate the Discord UX assumption

---

**TL;DR:** Build a single Go binary that runs as Discord bot + embedded web dashboard. Meet users where they already are (Discord), use nightly RAG to turn venting into actionable support plans, and measure success by H2H minutes created via structured "Two Wings" matching. Skip the mobile app nightmare entirely.
