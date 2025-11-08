# WingThing v0.1 Development Plan

## Goal

Build a single-user BYOK proof of concept to validate: **Does Dreams → Morning Card actually provide value?**

**Out of scope:** Multi-user, billing, auth, social features, web UI

---

## Week 1: Core Infrastructure ✅ COMPLETE

### Project Setup ✅
- [x] Initialize Go module
- [x] Set up project structure (cmd/, internal/)
- [x] Create Makefile for build/test/run commands
- [x] Add .gitignore (config.yaml, *.db, binaries)
- [x] Create config.example.yaml with docs

### Database Layer (SQLite) ✅
- [x] Design schema: users, messages, memories, dreams
- [x] Set up SQLite with migrations
- [x] Implement basic CRUD for messages
- [x] Implement memory storage (facts, relationships, boundaries)

### LLM Integration ✅
- [x] Create LLM provider interface
- [x] Implement OpenAI client (GPT-4)
- [x] Add conversation context building (last 10 messages)
- [x] Add basic error handling and retries
- [ ] Implement Anthropic client (Claude) - deferred

### Configuration ✅
- [x] Load config from config.yaml
- [x] Support env vars for secrets
- [x] Validate required fields on startup
- [x] Document all config options

### Discord Bot ✅
- [x] Discord bot registration and permissions
- [x] Implement bot startup and connection
- [x] Handle DM messages from hardcoded user ID
- [x] Store messages in SQLite with timestamp
- [x] Build conversation context from message history
- [x] Send context to LLM and get response
- [x] Handle message threading/continuity
- [x] Error handling and user-friendly error messages

### Commands ✅
- [x] `/start` - Introduction message
- [x] `/help` - Show available commands
- [x] `/stats` - Show message count, last Dream run
- [x] `/dream` - Manually trigger Dreams job (placeholder)
- [ ] `/forget` - Clear conversation context - TODO

**Status:** Basic bot is working! Can chat, remembers context, saves to DB.

---

## Week 2-3: Dreams Pipeline (IN PROGRESS)

### Insights from EhrlichGPT Analysis

**EhrlichGPT's brilliant pre-RAG context compression:**

1. **Progressive Memory Tiers:**
   - Short-term: Last 15 messages → summarized into bullets (rolling window)
   - Active memory: 400 token budget with 100 token low watermark
   - Long-term: Summarized from active memory, stored with embeddings
   - Retrieval: FAISS index for semantic search

2. **Intelligent Summarization:**
   - Summarize every 15 messages into concise bullets
   - When active memory > 400 tokens → compress to long-term, keep last 100 tokens
   - Different triggers: @mentioned = aggressive (300 tokens), passive = lazy (500 tokens)
   - Each summary preserves sender + key info, drops noise (reactions, exclamations)

3. **Selective Memory Retrieval:**
   - Before generating response, LLM "memory retriever" decides what to fetch
   - Can request: summarized memory, long-term memory (by query), web search
   - Token-aware: long-term retrieval capped at 500 tokens
   - Time-aware: memories tagged with "3 days ago", "2 weeks ago"

4. **Token Budgeting:**
   - Everything tracked in tokens
   - Memories have token counts
   - Retrieval has token budgets
   - GPT-4 requests force aggressive compression

**What WingThing MUST steal:**
- Progressive summarization (don't dump raw messages to Dreams)
- Token budgets for synthesis runs
- Memory retrieval agent (decide what's relevant before Dreams)
- Time-aware memory display

### Dreams Implementation Plan (Revised)

**Phase 1: Basic Dreams (This Week)**
- [ ] Implement progressive message summarization
  - [ ] Every N messages → summarize to bullets
  - [ ] Track token counts for summaries
  - [ ] Store summaries in active_memory table
- [ ] Build Dreams synthesis with token budget
  - [ ] Use summarized messages, not raw messages
  - [ ] Set token budget for synthesis (e.g., 2000 tokens input max)
  - [ ] Generate Morning Card from synthesis
- [ ] Morning Card delivery
  - [ ] Format as Discord embed
  - [ ] Send via DM
  - [ ] Store in dreams table

**Phase 2: Memory Retrieval (Next Week)**
- [ ] Implement memory retrieval agent
  - [ ] LLM decides what memories to include in Dreams
  - [ ] Can request: recent summaries, long-term patterns, specific facts
- [ ] Add embedding-based long-term memory
  - [ ] Store summaries with embeddings
  - [ ] Semantic search for relevant memories
  - [ ] Token-capped retrieval (e.g., 500 tokens max)
- [ ] Time-aware memory display
  - [ ] Tag memories with timestamps
  - [ ] Display as "3 days ago" in Dreams context

**Phase 3: Polish**
- [ ] Tune Dreams prompts
- [ ] Add Morning Card action buttons
- [ ] Track acknowledgment
- [ ] Test with real usage

### Implementation Tasks (This Week)

**Progressive Summarization:**
```go
// internal/memory/summarizer.go
- SummarizeMessages(messages []Message, windowSize int) (string, error)
- GetActiveSummary() (string, int) // returns summary + token count
- CompressToLongTerm() error
```

**Dreams Synthesis:**
```go
// internal/dreams/synthesis.go
- FetchRelevantContext(cfg, store) (Context, error)
  - Get summarized messages (not raw)
  - Query long-term memories (future)
  - Build token-budgeted context
- GenerateMorningCard(context) (Dream, error)
- DeliverMorningCard(dream, discordBot) error
```

**Database Updates:**
```sql
-- Add active_memory table
CREATE TABLE active_memory (
  id INTEGER PRIMARY KEY,
  summary TEXT,
  token_count INTEGER,
  created_at DATETIME
);

-- Add long_term_memory table (future)
CREATE TABLE long_term_memory (
  id INTEGER PRIMARY KEY,
  memory_text TEXT,
  embedding BLOB,
  unix_timestamp INTEGER
);
```

---

## Week 4: Testing & Polish

### Manual Testing
- [ ] Use bot every day for a week
- [ ] Manually trigger Dreams each night
- [ ] Evaluate Morning Card quality
- [ ] Iterate on prompts

### Cron Integration
- [ ] Document cron setup
- [ ] Test scheduling
- [ ] Error handling and logging

### Documentation
- [ ] Update SETUP.md with Dreams usage
- [ ] Document memory system architecture
- [ ] Add troubleshooting for Dreams

---

## Success Criteria (End of Week 4)

- [ ] Bot responds conversationally with context
- [ ] Dreams runs successfully (manually or cron)
- [ ] Morning Cards are actually useful and actionable
- [ ] Memory system prevents token bloat
- [ ] **YOU use it every day for 2 weeks**

---

## Lessons from EhrlichGPT

**What worked:**
- Progressive summarization kept costs down
- Memory retrieval agent prevented context dump
- Token budgeting everywhere
- Time-aware memories helped LLM understand context

**What to improve:**
- Summarization was slow (took 5-10 seconds)
  - Solution: Run Dreams async at night, no realtime pressure
- FAISS embeddings were complex
  - Solution: Start simple, add later if needed
- Web search was brittle
  - Solution: Skip for v0.1, focus on memory

**Core insight:** Don't give the LLM everything. Use an LLM to decide what to retrieve, then use another LLM to synthesize.

This is RAG before RAG had a name!

---

**Current Focus:** Implementing progressive summarization and Dreams synthesis

**Next Milestone:** First working Dreams → Morning Card loop
