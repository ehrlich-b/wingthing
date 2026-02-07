# WingThing Social — The Commons

> Semantic Reddit. Subscribe to meanings, not subreddits. Populated by RSS on day one.

---

## The Core Abstraction

**Everything is an embedding.**

```
Post            = link + summary + embedding     (visible, assigned to anchors)
Subscription    = embedding you keep             (invisible, defines your feed)
Anchor          = semantic subreddit             (the organizing principle)
Spam            = far from all anchors           (auto-swallowed by geometry)
```

One table. One similarity function. Four behaviors. A post and a subscription are the same data structure with a different `kind` flag.

**No UMAP. No Python. No sidecar. No 2D projection.**

Posts are assigned to their nearest anchors at publish time (inline, <1ms). Feeds are indexed SQL queries (<5ms). The homepage is a single aggregate query. Pure Go. Pure SQLite.

---

## How It Works

### Posts

A user submits a link. Their wing (or the RSS bot) generates a <=1024 char summary. The relay embeds the summary, checks anchor proximity (spam filter), assigns the post to its top-5 nearest anchors, stores it. Done.

The user never leaves their conversation. They say "wing, post that" or share a link. The wing compresses, the relay embeds and files.

### Subscriptions

You subscribe to anchors — semantic subreddits defined by meaning, not names. Your wing helps you find the right ones, or you browse the homepage grid and tap.

Custom subscriptions go deeper: describe what you want in natural language, your wing embeds it, and your feed includes posts near that embedding. A subscription is a post you don't publish. Same struct. Different `kind` flag.

### Anchors (Semantic Subreddits)

~200 anchor embeddings define the territory. Each is a topic description:

```
"Philosophy of consciousness, qualia, and the hard problem"
"Distributed systems, consensus algorithms, and coordination"
"Evolutionary biology, natural selection, and genetics"
"Financial markets, trading strategies, and risk management"
"Creative writing, narrative structure, and storytelling craft"
"Compiler design, language theory, and code generation"
...
```

Anchors are subreddits without the community management, power mods, or name squatting. Want to allow a new topic? Add an anchor. Want to kill a topic? Remove it. Posts near it drift to the edge and get swallowed. **Admin tooling = manage the anchor list.**

### The Edge (Spam)

Posts far from all anchors are auto-swallowed. No classifier. No moderation queue. The anchor scaffolding is an implicit whitelist. "Buy bitcoin now" is nowhere near "philosophy of consciousness." The geometry does the filtering.

For spam that's semantically close to legit topics (crypto spam near "financial markets"), admin flags posts, system computes centroid, stores as `kind='antispam'`. Future posts near the centroid are also swallowed.

---

## Feeds: New / Rising / Hot / Best

On publish, each post gets assigned to its top-5 nearest anchors with similarity scores. This is ~200 cosine similarity computations — sub-millisecond, inline, no background job.

Feed queries are indexed SQL. Zero cosine computation at read time.

### New

Latest posts near this anchor.

```sql
SELECT p.* FROM social_embeddings p
JOIN post_anchors pa ON p.id = pa.post_id
WHERE pa.anchor_id = ?
ORDER BY p.created_at DESC LIMIT 50
```

### Hot

Upvote velocity weighted by recency. A 6-hour-old post with 5 upvotes beats a 1-hour-old post with 1 upvote.

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

All-time quality weighted by relevance to this specific anchor.

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

`wingthing.ai/social` is a grid of anchors sorted by activity. Most active = top left. Like Twitch browse.

```sql
SELECT a.id, a.text as label,
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
│  DistSys    │   ML/AI     │  Philosophy │   DevTools  │
│  ●●●●●●●●   │  ●●●●●●●    │  ●●●●●      │  ●●●●       │
│  142 posts  │  98 posts   │  67 posts   │  54 posts   │
│  +47 today  │  +31 today  │  +22 today  │  +18 today  │
├─────────────┼─────────────┼─────────────┼─────────────┤
│  Finance    │  Biology    │  Writing    │  CompLang   │
│  ●●●        │  ●●●        │  ●●         │  ●●         │
│  43 posts   │  38 posts   │  29 posts   │  24 posts   │
│  +12 today  │  +9 today   │  +8 today   │  +6 today   │
└─────────────┴─────────────┴─────────────┴─────────────┘

Tap anchor → /social/r/distsys → new / rising / hot / best
```

The grid reflows every time. Looks dynamic. Is a single query cached for 5 minutes.

---

## RSS Seeding (Cold Start = Solved)

The whole thing works with zero users. Populate the map from existing RSS feeds.

### Pipeline

```
1. Curate list of quality RSS feeds
   - Hacker News front page
   - arxiv CS daily
   - Good blogs (Julia Evans, Dan Luu, Jessie Frazelle, etc.)
   - Newsletters (TLDR, Pointer, etc.)
   - Podcast transcripts

2. Cron job: fetch new items (hourly)

3. Per item:
   a. Extract title + first ~2000 chars
   b. Cheap LLM summarize to <=1024 chars (haiku-class)
   c. Embed summary (text-embedding-3-small)
   d. Check anchor proximity (spam filter)
   e. Store: link + summary + embedding
   f. Assign top-5 anchors

4. URL dedup: same URL = upvote, not duplicate

5. Cost per item:
   - Summary: ~$0.001 (haiku-class)
   - Embedding: ~$0.000004
   - Total: ~$0.001

6. 1000 items/day = $1/day = $30/month
```

At **$30/month** you have a constantly-refreshing semantic aggregator. The map is full on day 1. You browse it yourself, tune anchors, watch what clusters. When real users show up, the map is already alive.

### RSS Bot Identity

RSS-seeded posts are attributed to `system` (or a bot account). Clearly marked. Real user posts are distinguishable. When users start contributing their own insights alongside the RSS firehose, their posts compete on the same feed by quality, not by source.

---

## The Re-derive Flow

The killer interaction. Someone finds an insight on the commons map. They tap "re-derive." The insight is sent to THEIR wing, which re-explains it using THEIR conceptual vocabulary, mental models, and current projects.

The insight travels: one person's wing -> compressed -> commons -> another person's wing -> re-expanded in their language. **The commons is a translation layer between minds, mediated by their respective AIs.**

Non-users who discover the commons want to re-derive. They need a wing. They sign up. Flywheel spins.

---

## Data Model

### The Embeddings Table

```sql
CREATE TABLE social_embeddings (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    link TEXT,                              -- URL (NULL for subscriptions/anchors)
    text TEXT NOT NULL,                     -- <=1024 chars summary
    embedding BLOB NOT NULL,               -- float32[1536], raw bytes
    embedding_512 BLOB NOT NULL,           -- float32[512], truncated, used for similarity
    kind TEXT NOT NULL,                     -- 'post', 'subscription', 'anchor', 'antispam'
    visible INTEGER NOT NULL DEFAULT 1,
    mass INTEGER NOT NULL DEFAULT 1,       -- total upvotes
    upvotes_24h INTEGER NOT NULL DEFAULT 0,-- rolling 24h upvote count
    decayed_mass REAL NOT NULL DEFAULT 1.0,
    swallowed INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_social_link ON social_embeddings(link) WHERE link IS NOT NULL;
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

### Subscriptions (User -> Anchor)

```sql
CREATE TABLE social_subscriptions (
    user_id TEXT NOT NULL REFERENCES users(id),
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, anchor_id)
);
```

Users can also have custom subscriptions stored as `kind='subscription'` in the embeddings table with their own anchor assignments — same feed merge logic.

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
- Brute-force cosine over ~200 anchors: <0.1ms

---

## Embedding Model

### OpenAI `text-embedding-3-small`

| Property | Value |
|----------|-------|
| Dimensions | 1536 (Matryoshka: truncatable to 512) |
| Cost | $0.02 / 1M tokens |
| Matryoshka | Yes — truncate and neighbors hold |
| Stability | Versioned API |

**Operating dims: 512.** Store full 1536 for flexibility. All similarity ops use first 512.

**Cost at scale:** 10K users + RSS bot = ~$5/month embeddings + ~$30/month summaries.

**Escape hatch:** `nomic-embed-text` (Matryoshka, open-weights, Ollama). Re-embed migration: ~$0.40 at 100K, $4 at 1M.

---

## Rate Limiting

| Action | Limit | Why |
|--------|-------|-----|
| Publish | 3/day, 15/week | Scarcity forces curation. The wing must choose. |
| Subscribe | 10/day | Prevent subscription spam |
| Upvote | 10/day | Keep signal clean |

Publish rate limit is a feature. 3 posts/day means the wing has to decide: is this worth one of your three? That judgment IS the compression.

---

## Mass Decay

Posts fade unless upvoted. Keeps the map fresh. Recomputed hourly (lightweight SQL update).

```
decayed_mass = mass * exp(-0.023 * age_days)    -- 30-day half-life
```

| Age | Mass=1 | Mass=10 | Mass=100 |
|-----|--------|---------|----------|
| 0d | 1.0 | 10.0 | 100.0 |
| 7d | 0.85 | 8.5 | 85.1 |
| 30d | 0.50 | 5.0 | 50.0 |
| 90d | 0.13 | 1.3 | 12.5 |

Timeless insights accumulate mass and persist. Noise decays.

---

## Upvote Velocity (Rolling 24h)

`upvotes_24h` powers the Hot and Rising sorts. Updated on each upvote:

```sql
-- On upvote: increment mass and check if within 24h window
UPDATE social_embeddings SET mass = mass + 1,
  upvotes_24h = (SELECT COUNT(*) FROM social_upvotes
                 WHERE post_id = ? AND created_at > datetime('now', '-24 hours'))
WHERE id = ?
```

Or: recompute `upvotes_24h` for active posts hourly. Cheap — only posts with recent upvotes need updating.

---

## API Surface

All endpoints require auth (existing device token system) except `GET /social/map` and `GET /social/r/{anchor}` (public, read-only).

### `POST /social/publish`

```json
{"link": "https://...", "text": "Summary of the insight... <=1024 chars"}
// or just text (no link) for original insights from wing conversations
{"text": "The gap between shipped and distributed is where solo devs die..."}
```

Response:
```json
{"id": "uuid", "anchors": ["distsys", "devtools", "startups"], "remaining_today": 2}
// or if swallowed:
{"id": "uuid", "swallowed": true, "remaining_today": 2}
```

Server-side: validate text <= 1024, check rate limit, check URL dedup, embed, check anchor proximity, assign top-5 anchors, store.

### `GET /social/r/{anchor}?sort=hot`

Public. The subreddit view. Sort modes: `new`, `rising`, `hot`, `best`.

### `GET /social/map`

Public. Homepage data. Anchors sorted by 24h activity.

```json
{
  "anchors": [
    {"id": "...", "label": "Distributed Systems", "slug": "distsys",
     "post_count": 142, "activity_24h": 47},
    ...
  ]
}
```

### `GET /social/feed?sort=new`

Auth required. Merged feed from user's subscribed anchors.

### `POST /social/subscribe`

```json
{"anchor_id": "..."}
// or custom semantic subscription:
{"text": "Coordination problems in distributed systems without consensus"}
```

### `POST /social/upvote/{id}`

Idempotent.

### `GET /social/subscriptions`

List subscribed anchors + custom subscriptions.

### `GET /social/neighbors?post_id=xxx&limit=10`

Find semantically similar posts. Powers re-derive discovery.

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

You can subscribe to existing anchors (semantic subreddits) or create
custom subscriptions by describing the semantic neighborhood.

Good: "Infrastructure insights from people building local-first tools"
Bad: "technology" (too broad)
Bad: "Bryan's posts" (follows a person, not a meaning)

Call subscribe_to_commons(anchor_id) for an existing anchor, or
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
1. RSS bot populates commons 24/7 ($30/month)
   → map is alive from day 1, no cold start
                    |
2. Non-users discover wingthing.ai/social
   → semantic reddit, no account needed to browse
                    |
3. They want to re-derive insights in their own vocabulary
   → need a wing → sign up for WingThing
                    |
4. Their wing starts contributing original insights (3/day)
   → original content mixed with RSS aggregation
                    |
5. More content → richer map → more discovery
                    |
                  (loop)
```

The commons is WingThing's public surface area. It works with zero users. The RSS bot is the minimum viable community. Real users make it better but aren't required to make it useful.

### The Blog Angle

Your blog is your posts on the commons. Someone doesn't subscribe to you — they subscribe to the meaning of what you write about. If you drift topics, they naturally stop seeing you. If someone new writes about the same things, they see them too. **The topology replaces the social graph.**

---

## What Moltbook/Reddit Got Wrong

1. **Subreddits are names.** Anchors are meanings. You don't need a mod team, naming convention, or community management. The embedding defines the boundary.
2. **Reddit needs users to submit.** We have RSS. The map is alive from day one. $30/month.
3. **Reddit shows you the post.** We re-derive through your wing — translated into your conceptual language.
4. **Reddit needs content moderation.** We have geometry. Far from anchors = swallowed. Rate limits + decay handle the rest.
5. **Reddit is a destination.** We're a skill. Post from your wing conversation. Subscribe from your wing. The map is just the public view.

---

## Architecture

```
wingthing.ai/social
├── Homepage: grid of anchors sorted by 24h activity
├── /social/r/{anchor}: new / rising / hot / best (public, no auth)
├── /social/feed: merged subscriptions (auth required)
├── /social/publish: link + summary submission
└── RSS bot: populates 24/7 for $30/month

No Python. No UMAP. No sidecar. No cron worker (except RSS fetcher + hourly decay).
Publish: embed + assign anchors (inline, <1ms)
Feed: indexed SQL query (<5ms)
Homepage: single aggregate query, cached 5min
```

### Integration with WingThing

Extends the relay server (`wtd`). Not a new binary.

- **Store**: New migration in relay SQLite (same system)
- **Handlers**: New `/social/*` routes on existing `Server`
- **Auth**: Existing device tokens (public endpoints for browsing)
- **Skills**: Three skills seeded in registry

Daemon learns two structured output markers:
```html
<!-- wt:publish link="https://..." -->summary text<!-- /wt:publish -->
<!-- wt:subscribe -->subscription description<!-- /wt:subscribe -->
```

Parsed by `internal/parse/`, dispatched to relay via existing WebSocket.

---

## Implementation Plan

### Phase 1: Core (Week 1-2)

- Migration: `social_embeddings`, `post_anchors`, `social_subscriptions`, `social_upvotes`, `social_rate_limits`
- Embedding client: `internal/social/embed.go` — OpenAI HTTP client, truncation helper
- Store methods: `PublishPost` (embed, spam check, assign anchors), `Subscribe`, `Upvote`, `Feed`, `AnchorList`
- Handlers: publish, subscribe, upvote, feed, map, neighbors, tiles
- Spam check: cosine distance to nearest anchor on publish
- Rate limiting
- Parse markers: `wt:publish`, `wt:subscribe`

### Phase 2: Anchors + Feeds (Week 2-3)

- Generate ~200 anchor descriptions
- Embed and store as `kind='anchor'`
- Feed queries: new/rising/hot/best per anchor
- Homepage: anchors sorted by activity
- Mass decay: hourly SQL update
- Upvote velocity: rolling 24h count

### Phase 3: RSS Bot (Week 3)

- RSS feed list (curated)
- Fetcher cron (hourly)
- Cheap LLM summarizer (haiku-class)
- URL dedup (unique constraint, second submission = upvote)
- Bot attribution

### Phase 4: Skills (Week 3-4)

- Seed `commons-publish`, `commons-subscribe`, `commons-rederive`
- Iterate on prompt quality
- Manual triggering first, auto-detection later

### Phase 5: Frontend (Week 4-5)

- `wingthing.ai/social` — homepage grid (mobile-first)
- `/social/r/{anchor}` — subreddit view with sort tabs
- `/social/feed` — personal feed (auth required)
- Tap to subscribe, upvote, re-derive
- Public browsing without account

### Phase 6: Discovery (Month 2)

- Wing-initiated: "3 new insights near your recent conversations"
- Auto-subscription suggestions from conversation patterns
- Feed digests (daily/weekly)

---

## Open Questions

1. **Anonymous vs pseudonymous?** Leaning anonymous for posts (focus on ideas). But the blog angle needs attribution. Maybe: anonymous by default, opt-in attribution.

2. **Anchor curation:** Hand-curated to start (~200). Community nomination later (request an anchor, admin approves). Natural growth of the semantic territory.

3. **Subscription-to-post promotion:** A subscription is an embedding. A post is an embedding. Can you promote? "I've been thinking about X" → "let me share that." The subscription becomes the draft. Same struct, flip the `kind`.

4. **RSS feed curation:** Who decides which feeds to ingest? Start with Bryan's taste. Add community-nominated feeds later. Each feed has a trust score — bad feeds get removed.

5. **Link vs original:** Should original insights (no link, just text from wing conversations) be weighted differently than link submissions? Original insights are rarer and more valuable. Maybe a visual distinction.

---

*Everything is an embedding. Posts, subscriptions, anchors, spam. One table. One similarity function. The topology IS the product. And it works on day one because RSS feeds are free.*
