# WingThing Social — The Commons

> Semantic Reddit. Subscribe to meanings, not subreddits. Self-hostable. Populated by RSS on day one.

---

## The Core Abstraction

**Everything is an embedding.**

```
Post            = link + summary + embedding     (visible, assigned to anchors)
Subscription    = embedding you keep             (invisible, defines your feed)
Anchor          = semantic subreddit             (the mod is a 1024-char description)
Spam            = far from all anchors           (auto-swallowed by geometry)
```

One table. One similarity function. Four behaviors. A post and a subscription are the same data structure with a different `kind` flag.

**No UMAP. No Python. No sidecar. No 2D projection. Pure Go. Pure SQLite.**

Posts are assigned to their nearest anchors at publish time (inline, <1ms). Feeds are indexed SQL queries (<5ms). The homepage is a single aggregate query.

**Fully self-hostable.** The entire social engine runs locally. Bring your own embedding API key (OpenAI, or ollama for fully offline). Define your own anchors. Ingest your own RSS feeds. `wingthing.ai/social` is just a hosted instance with curated anchors — the same code, with opinions.

---

## Self-Hosting vs Hosted

The social engine is a library inside `wtd` (the relay server). The same binary powers both modes.

### Self-Hosted Mode

```yaml
# ~/.wingthing/config.yaml
social:
  enabled: true
  embedding:
    provider: openai           # or "ollama" for fully offline
    model: text-embedding-3-small
    api_key: sk-...            # or omit for ollama
    ollama_model: nomic-embed-text  # if provider: ollama
    dims: 512                  # operating dimensions (Matryoshka truncation)
  summarizer:
    provider: ollama           # or "openai", "anthropic"
    model: llama3.2            # cheap local model for RSS summaries
  anchors: ~/.wingthing/anchors.yaml   # your own anchor definitions
  rss:
    feeds: ~/.wingthing/feeds.yaml     # your own RSS feed list
    interval: 1h
```

You run `wtd` on your machine or VPS. You define your own anchors. You point it at your own RSS feeds. You bring your own embedding key (or run ollama for $0). Nobody else's opinions about what's important.

**Cost to self-host:**
- Embedding: $0 (ollama) or ~$5/month (OpenAI at moderate volume)
- Summaries: $0 (ollama) or ~$30/month (haiku-class)
- Storage: SQLite on disk
- Compute: whatever you're already running `wtd` on

### Hosted Mode (`wingthing.ai/social`)

Same code. Bryan's opinions about anchors. Bryan's curated RSS feeds. Shared community upvotes. The `/w/` namespace. Free to browse. Account required to subscribe/upvote/publish.

---

## The `/w/` Namespace

`wingthing.ai/w/physics`

That's a Reddit-style URL that normal people fully clock at first glance. Clean. Obvious. Bookmarkable. Shareable.

```
wingthing.ai/w/physics              → hot posts in "physics"
wingthing.ai/w/physics?sort=new     → newest
wingthing.ai/w/physics?sort=rising  → rising
wingthing.ai/w/physics?sort=best    → all-time best
wingthing.ai/w/distsys              → distributed systems
wingthing.ai/w/philosophy           → philosophy of mind
wingthing.ai/w/compilers            → compiler design
```

### "Who mods /w/physics?"

**The mod is a text embedding.**

Each anchor has a `slug` (the URL name) and a `description` — a <=1024 char statement that defines what belongs in this semantic subreddit. The description gets embedded. The embedding IS the moderation policy. Posts are assigned to anchors by cosine similarity to this embedding.

```yaml
# anchors.yaml (or DB row)
- slug: physics
  label: Physics
  description: >
    Fundamental physics: quantum mechanics, general relativity, particle
    physics, condensed matter, thermodynamics, statistical mechanics,
    astrophysics, cosmology. Experimental results, theoretical developments,
    and accessible explanations of physical phenomena. Not pop-sci clickbait
    about "quantum computing will change everything" — actual physics content
    with substance.
```

**The description is the mod.** It's continuously refined. When you sharpen the description, the embedding shifts, and the boundary of what belongs in `/w/physics` shifts with it. Want to exclude pop-sci clickbait? Add that to the description. Want to include astrophysics? Mention it. The anchor description IS the subreddit rules, enforced by geometry.

No human moderator. No power trips. No mod drama. The description defines the semantic boundary. Refine the description, refine the boundary.

### Anchor Refinement

```
1. Admin edits the anchor description (or community proposes edits)
2. Re-embed the description
3. Re-assign all posts (batch, or lazy on next query)
4. Posts that no longer match drift to other anchors or the edge
5. Posts that newly match appear in the feed
```

This is how you "moderate" without moderating. You don't remove individual posts. You sharpen the description of what the community is about, and the geometry handles the rest.

---

## How It Works

### Posts

A user submits a link. Their wing (or the RSS bot) generates a <=1024 char summary. The relay embeds the summary, checks anchor proximity (spam filter), assigns the post to its top-5 nearest anchors, stores it. Done.

The user never leaves their conversation. They say "wing, post that" or share a link. The wing compresses, the relay embeds and files.

### Subscriptions

You subscribe to anchors — tap `/w/physics`, you're in. Or your wing helps you find the right anchors based on your conversation history.

Custom subscriptions go deeper: describe what you want in natural language, your wing embeds it, and your feed includes posts near that embedding. A subscription is a post you don't publish. Same struct. Different `kind` flag.

### The Edge (Spam)

Posts far from all anchors are auto-swallowed. No classifier. No moderation queue. The anchor scaffolding is an implicit whitelist. The geometry does the filtering.

For spam semantically close to legit topics: admin flags posts, system computes centroid, stores as `kind='antispam'`. Future posts near the centroid are swallowed.

---

## Feeds: New / Rising / Hot / Best

On publish, each post gets assigned to its top-5 nearest anchors with similarity scores. This is ~200 cosine similarity computations — sub-millisecond, inline, no background job.

Feed queries are indexed SQL. Zero cosine computation at read time.

### New

```sql
SELECT p.* FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
WHERE pa.anchor_id = ?
ORDER BY p.created_at DESC LIMIT 50
```

### Hot

Upvote velocity weighted by recency.

```sql
SELECT p.*,
  (p.upvotes_24h / (1.0 + (julianday('now') - julianday(p.created_at)) * 2.0)) as hot
FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
WHERE pa.anchor_id = ?
ORDER BY hot DESC LIMIT 50
```

### Rising

Most upvotes in last 24h among young posts.

```sql
SELECT p.* FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
WHERE pa.anchor_id = ? AND p.created_at > datetime('now', '-48 hours')
ORDER BY p.upvotes_24h DESC LIMIT 50
```

### Best

All-time quality weighted by relevance to this anchor.

```sql
SELECT p.* FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
WHERE pa.anchor_id = ?
ORDER BY pa.similarity * p.decayed_mass DESC LIMIT 50
```

### Merged Feed (Your Subscriptions)

```sql
SELECT DISTINCT p.* FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
JOIN social_subscriptions s ON pa.anchor_id = s.anchor_id
WHERE s.user_id = ?
ORDER BY p.created_at DESC LIMIT 50
```

---

## The Homepage

`wingthing.ai/social` (or your self-hosted instance) is a grid of anchors sorted by activity. Most active = top left. Like Twitch browse.

```sql
SELECT a.id, a.text as label, a.slug,
  COUNT(DISTINCT pa.post_id) as total_posts,
  SUM(p.upvotes_24h) as activity_24h
FROM social_embeddings a
JOIN post_anchors pa ON a.id = pa.anchor_id
JOIN social_embeddings p ON pa.post_id = p.id
WHERE a.kind = 'anchor' AND p.visible = 1
GROUP BY a.id
ORDER BY activity_24h DESC
```

```
┌─────────────┬─────────────┬─────────────┬─────────────┐
│ /w/distsys  │  /w/ml      │ /w/physics  │ /w/devtools │
│  142 posts  │  98 posts   │  67 posts   │  54 posts   │
│  +47 today  │  +31 today  │  +22 today  │  +18 today  │
├─────────────┼─────────────┼─────────────┼─────────────┤
│ /w/finance  │  /w/bio     │ /w/writing  │ /w/compilers│
│  43 posts   │  38 posts   │  29 posts   │  24 posts   │
│  +12 today  │  +9 today   │  +8 today   │  +6 today   │
└─────────────┴─────────────┴─────────────┴─────────────┘

Tap → /w/physics → new / rising / hot / best
```

The grid reflows by activity. Cached 5 minutes.

---

## RSS Seeding (Cold Start = Solved)

The whole thing works with zero users. Populate from existing RSS feeds.

### Pipeline

```
1. Curate RSS feed list (self-hosters bring their own)
   - wingthing.ai ships with: HN, arxiv CS, Julia Evans, Dan Luu, etc.

2. Cron: fetch new items (hourly, configurable)

3. Per item:
   a. Extract title + first ~2000 chars
   b. LLM summarize to <=1024 chars (haiku-class or local ollama)
   c. Embed summary
   d. Check anchor proximity (spam filter)
   e. Store: link + summary + embedding
   f. Assign top-5 anchors

4. URL dedup: same URL = upvote, not duplicate

5. Cost per item: ~$0.001 (hosted) or $0 (ollama self-hosted)

6. Hosted: 1000 items/day = $1/day = $30/month
   Self-hosted with ollama: $0/month (just electricity)
```

### RSS Bot Identity

RSS-seeded posts are attributed to `system`. Clearly marked. Real user posts are distinguishable.

---

## The Re-derive Flow

Someone finds an insight on the commons. They tap "re-derive." The insight is sent to THEIR wing, which re-explains it using THEIR conceptual vocabulary.

The insight travels: one person's wing -> compressed -> commons -> another person's wing -> re-expanded in their language. **The commons is a translation layer between minds.**

Non-users discover the commons, want to re-derive, need a wing, sign up. Flywheel spins.

---

## Data Model

### The Embeddings Table

```sql
CREATE TABLE social_embeddings (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    link TEXT,                              -- URL (NULL for subscriptions/anchors)
    text TEXT NOT NULL,                     -- <=1024 chars summary
    slug TEXT,                              -- URL-friendly name (anchors only)
    embedding BLOB NOT NULL,               -- float32[1536], raw bytes
    embedding_512 BLOB NOT NULL,           -- float32[512], truncated
    kind TEXT NOT NULL,                     -- 'post', 'subscription', 'anchor', 'antispam'
    visible INTEGER NOT NULL DEFAULT 1,
    mass INTEGER NOT NULL DEFAULT 1,       -- total upvotes
    upvotes_24h INTEGER NOT NULL DEFAULT 0,
    decayed_mass REAL NOT NULL DEFAULT 1.0,
    swallowed INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_social_link ON social_embeddings(link) WHERE link IS NOT NULL;
CREATE UNIQUE INDEX idx_social_slug ON social_embeddings(slug) WHERE slug IS NOT NULL;
CREATE INDEX idx_social_user ON social_embeddings(user_id);
CREATE INDEX idx_social_kind ON social_embeddings(kind);
CREATE INDEX idx_social_visible ON social_embeddings(visible, kind);
CREATE INDEX idx_social_created ON social_embeddings(created_at);
CREATE INDEX idx_social_mass ON social_embeddings(decayed_mass DESC);
```

### Anchor Assignments (Pre-computed on Publish)

```sql
CREATE TABLE post_anchors (
    post_id TEXT NOT NULL REFERENCES social_embeddings(id),
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    similarity REAL NOT NULL,
    PRIMARY KEY (post_id, anchor_id)
);

CREATE INDEX idx_post_anchors_feed ON post_anchors(anchor_id, similarity DESC);
```

### Subscriptions

```sql
CREATE TABLE social_subscriptions (
    user_id TEXT NOT NULL REFERENCES users(id),
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, anchor_id)
);
```

Custom subscriptions are `kind='subscription'` rows in `social_embeddings` with their own `post_anchors` assignments — same feed merge logic.

### Upvotes

```sql
CREATE TABLE social_upvotes (
    user_id TEXT NOT NULL REFERENCES users(id),
    post_id TEXT NOT NULL REFERENCES social_embeddings(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, post_id)
);
```

### Rate Limits

```sql
CREATE TABLE social_rate_limits (
    user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    window_start DATETIME NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, action, window_start)
);
```

### Storage Math

- 1536 float32 = 6,144 bytes per embedding (full)
- 512 float32 = 2,048 bytes per embedding (operating)
- 100K entries = ~800MB
- 1M entries = ~8GB — fits in SQLite on a VPS
- Cosine over ~200 anchors: <0.1ms

---

## Embedding Model

### Hosted: OpenAI `text-embedding-3-small`

| Property | Value |
|----------|-------|
| Dimensions | 1536 (Matryoshka: truncatable to 512) |
| Cost | $0.02 / 1M tokens |
| Matryoshka | Yes — truncate and neighbors hold |

### Self-Hosted: `nomic-embed-text` via Ollama

| Property | Value |
|----------|-------|
| Dimensions | 768 (Matryoshka: truncatable to 512) |
| Cost | $0 (runs locally) |
| Matryoshka | Yes |
| Runtime | Ollama, fully offline |

**Operating dims: 512** in both cases. The embedding provider is pluggable. The similarity math doesn't care where the vectors came from, as long as all vectors in a given instance use the same model.

**You cannot mix models.** All embeddings in an instance must use the same model. Switching models requires a full re-embed (~$0.40 at 100K for OpenAI, free for ollama).

---

## Rate Limiting

| Action | Limit | Why |
|--------|-------|-----|
| Publish | 3/day, 15/week | Scarcity forces curation |
| Subscribe | 10/day | Prevent subscription spam |
| Upvote | 10/day | Keep signal clean |

Self-hosted instances can set their own limits (or disable them).

---

## Mass Decay

```
decayed_mass = mass * exp(-0.023 * age_days)    -- 30-day half-life
```

| Age | Mass=1 | Mass=10 | Mass=100 |
|-----|--------|---------|----------|
| 0d | 1.0 | 10.0 | 100.0 |
| 7d | 0.85 | 8.5 | 85.1 |
| 30d | 0.50 | 5.0 | 50.0 |
| 90d | 0.13 | 1.3 | 12.5 |

Recomputed hourly via lightweight SQL update. Self-hosted instances configure their own half-life.

---

## API Surface

Public endpoints (no auth): `GET /w/{slug}`, `GET /social/map`

Auth required: publish, subscribe, upvote, feed

### `GET /w/{slug}?sort=hot`

The subreddit view. Sort: `new`, `rising`, `hot`, `best`. Public.

### `GET /social/map`

Homepage data. Anchors sorted by 24h activity. Public.

```json
{
  "anchors": [
    {"slug": "physics", "label": "Physics", "post_count": 142, "activity_24h": 47},
    {"slug": "distsys", "label": "Distributed Systems", "post_count": 98, "activity_24h": 31},
    ...
  ]
}
```

### `POST /social/publish`

```json
{"link": "https://...", "text": "Summary <=1024 chars"}
// or original insight (no link):
{"text": "The gap between shipped and distributed is where solo devs die..."}
```

### `GET /social/feed?sort=new`

Merged feed from subscribed anchors.

### `POST /social/subscribe`

```json
{"anchor_slug": "physics"}
// or custom:
{"text": "Coordination problems in distributed systems without consensus"}
```

### `POST /social/upvote/{id}`

Idempotent.

### `GET /social/subscriptions`

List subscriptions.

### `GET /social/neighbors?post_id=xxx&limit=10`

Semantic similarity search.

### `GET /w/{slug}/about`

The anchor description. The mod statement. Public.

```json
{
  "slug": "physics",
  "label": "Physics",
  "description": "Fundamental physics: quantum mechanics, general relativity...",
  "post_count": 142,
  "subscriber_count": 89
}
```

---

## Skills

### commons-publish

```markdown
---
name: commons-publish
description: Share insights to the WingThing Commons
agent: ""
memory:
  - identity
memory_write: false
tags: ["social", "commons"]
---

You can share insights or links to the WingThing Commons — a semantic
aggregator where content clusters by meaning.

When to offer to post:
- The human has a breakthrough realization or novel connection
- The human shares a link they found valuable
- The human explicitly asks ("wing, post that")
- A conversation resolves a hard problem in a transferable way

When NOT to post:
- Personal/emotional processing
- Venting, daily life, logistics
- Half-formed thoughts still being worked through

Compress to <=1024 characters:
- The core claim or connection
- What problem it solves or reframes
- Enough context for a stranger to understand

Strip: personal details, names, specific circumstances, identity markers.

Call publish_to_commons(text, link?) to post.
They have {{social.remaining_today}} posts remaining today.
```

### commons-subscribe

```markdown
---
name: commons-subscribe
description: Subscribe to semantic topics on the WingThing Commons
agent: ""
memory:
  - identity
  - projects
memory_write: false
tags: ["social", "commons"]
---

Help {{identity.name}} subscribe to topics on the WingThing Commons.

Browse anchors at /w/ or create custom subscriptions by describing
the semantic neighborhood.

Good: "Infrastructure insights from people building local-first tools"
Bad: "technology" (too broad)
Bad: "Bryan's posts" (follows a person, not a meaning)

Call subscribe_to_commons(anchor_slug) for an anchor, or
subscribe_to_commons(text) for a custom semantic subscription.
```

### commons-rederive

```markdown
---
name: commons-rederive
description: Re-explain a commons insight in your vocabulary
agent: ""
memory:
  - identity
  - projects
memory_write: false
tags: ["social", "commons"]
---

A human on the WingThing Commons shared this insight:

{{task.what}}

Re-explain this using {{identity.name}}'s conceptual vocabulary, mental
models, and current projects. Don't paraphrase — translate. Find the
structural parallel in their experience.
```

---

## The Flywheel

```
1. RSS bot populates commons 24/7 ($30/month hosted, $0 self-hosted)
   → alive from day 1, no cold start
                    |
2. People discover wingthing.ai/w/physics
   → "oh, it's like reddit but the mod is an AI description"
   → no account needed to browse
                    |
3. They want to re-derive insights in their own language
   → need a wing → sign up
                    |
4. Their wing contributes original insights (3/day)
                    |
5. More content → richer feeds → more discovery
                    |
                  (loop)

Meanwhile: self-hosters run their own instances with their own
anchors and RSS feeds. Private semantic aggregators. Corporate
knowledge bases. Personal research tools. All the same code.
```

---

## What Reddit Got Wrong

1. **Subreddits are names. Anchors are meanings.** No mod team, no name squatting, no community management. The embedding defines the boundary. "Who mods /w/physics?" — a 1024-char description of what physics means.

2. **Reddit needs users to submit.** We have RSS. The map is alive from day one.

3. **Reddit shows you the post.** We re-derive through your wing — translated into your conceptual language.

4. **Reddit needs content moderation.** We have geometry. Sharpen the anchor description, the boundary shifts. Far from anchors = swallowed. Rate limits + decay handle the rest.

5. **Reddit is a destination.** We're a skill. Post from your wing. Subscribe from your wing. `/w/` is just the public view.

6. **Reddit can't be self-hosted.** This can. Same binary. Your anchors. Your RSS feeds. Your embedding model.

---

## Architecture

```
wingthing.ai/social (hosted) — OR — your-server/social (self-hosted)
├── GET /w/{slug}         → subreddit view (public)
├── GET /social/map       → anchor grid (public)
├── GET /social/feed      → personal feed (auth)
├── POST /social/publish  → submit link + summary (auth)
├── RSS bot               → populates 24/7
└── Anchor descriptions   → the moderation layer

Same binary: wtd
Same store: SQLite
Same code: internal/social/
Config difference: embedding provider + anchor file + RSS feeds
```

### Integration with WingThing

Extends `wtd`. Not a new binary.

- **Store**: New migration in relay SQLite
- **Handlers**: `/social/*` and `/w/*` routes on existing `Server`
- **Auth**: Existing device tokens (public endpoints for browsing)
- **Config**: `social:` section in config.yaml
- **Skills**: Three skills seeded in registry

Daemon learns two structured output markers:
```html
<!-- wt:publish link="https://..." -->summary text<!-- /wt:publish -->
<!-- wt:subscribe -->subscription description<!-- /wt:subscribe -->
```

---

## Implementation Plan

### Phase 1: Embedding + Store (Week 1-2)

- `internal/social/embed.go` — Pluggable embedding client (OpenAI + Ollama)
- `internal/social/cosine.go` — Cosine similarity, anchor assignment
- Migration: `social_embeddings`, `post_anchors`, `social_subscriptions`, `social_upvotes`, `social_rate_limits`
- Store methods: `PublishPost`, `Subscribe`, `Upvote`, `Feed`, `AnchorList`, `CheckRateLimit`
- Spam check: cosine distance to nearest anchor
- Tests: embedding round-trip, anchor assignment, feed queries, rate limiting

### Phase 2: Anchors + Feeds + `/w/` (Week 2-3)

- Anchor YAML format + loader
- Generate ~200 anchor descriptions (hand-curated)
- Embed and store as `kind='anchor'`
- Feed queries: new/rising/hot/best per anchor
- Handlers: `GET /w/{slug}`, `GET /social/map`, `GET /w/{slug}/about`
- Homepage: anchors sorted by activity, cached
- Mass decay + upvote velocity

### Phase 3: RSS Bot (Week 3)

- RSS feed YAML format + fetcher
- Pluggable summarizer (OpenAI + Ollama)
- URL dedup
- Bot attribution
- Configurable interval

### Phase 4: Auth + Write Endpoints (Week 3-4)

- `POST /social/publish`, `POST /social/subscribe`, `POST /social/upvote/{id}`
- `GET /social/feed`, `GET /social/subscriptions`
- Rate limiting enforcement
- Parse markers: `wt:publish`, `wt:subscribe`

### Phase 5: Skills (Week 4)

- Seed `commons-publish`, `commons-subscribe`, `commons-rederive`
- Manual triggering first, auto-detection later

### Phase 6: Frontend (Week 5-6)

- `wingthing.ai/social` — homepage grid (mobile-first)
- `/w/{slug}` — subreddit view with sort tabs
- `/social/feed` — personal feed
- Tap to subscribe, upvote, re-derive
- Public browsing, no account required
- `/w/{slug}/about` — the mod description, subscriber count

### Phase 7: Discovery (Month 2)

- Wing-initiated: "3 new insights near your recent conversations"
- Auto-subscription from conversation patterns
- Feed digests

---

## Open Questions

1. **Anonymous vs pseudonymous?** Anonymous by default (focus on ideas). Opt-in attribution for the blog use case.

2. **Anchor governance:** Hand-curated to start. Community nomination later (propose an anchor, admin approves/refines). Self-hosters define their own.

3. **Subscription-to-post promotion:** A subscription is an embedding. A post is an embedding. Promote by flipping `kind`. The subscription becomes the draft.

4. **Federation:** Can self-hosted instances share posts with `wingthing.ai/social`? Probably yes — same embedding model = compatible vectors. Cross-instance anchor mapping is the hard part. Future problem.

5. **Link vs original:** Visual distinction between RSS-sourced links and original wing insights? Original insights are rarer and more valuable. Maybe a badge or sort boost.

---

*Everything is an embedding. The mod is a description. The feed is geometry. It works on day one because RSS is free. And you can self-host the whole thing.*
