# RSS Feed Discovery & Curation Plan

## The Vibe

Each of our 159 spaces is a mini Hacker News for its topic. That's the product. So the question for every feed we add is: **what does the Hacker News of sourdough look like?**

It looks like someone who spent six months testing autolyse times and wrote up what they found. A materials scientist explaining gluten network formation. A bakery owner's honest postmortem on scaling from home oven to deck oven. Someone reverse-engineering a 100-year-old starter culture. Not "10 Easy Sourdough Recipes" from a food content mill.

HN has corporate blogs on it — and that's fine. Google Research publishes genuinely interesting work. Cloudflare's blog is legitimately good. The HN immune system doesn't reject corporate content because it's corporate. It rejects content that's boring, obvious, or exists to sell you something. The community can smell marketing through any veneer.

That's our filter. Not "who wrote this" but "is this interesting?"

Apply that lens to every space: the Hacker News of mycology, the Hacker News of typography, the Hacker News of chess. For each one, there are people out there writing genuinely interesting stuff and exposing it via RSS. Our job is to find them.

## Why This Matters

This is the cold start for wingthing.ai/social. If someone visits and the content is mediocre, they never come back. If they land on the sourdough space and find five blogs they've never seen before that are genuinely fascinating, they think "what else is on here?" and start exploring.

The feeds we seed now define the editorial voice of the entire platform. We're making taste decisions. That's the product.

## Bring Back RSS

RSS/Atom is the original open social protocol. Millions of people still publish to it. We reward that: find people still maintaining feeds, surface their best work, show them an audience exists.

Source priority (not a hard rule, just a lean):
1. **Self-hosted blogs** — someone runs their own site with an RSS feed. This is the open web we're championing.
2. **Substack** — every Substack has `{name}.substack.com/feed`. Quality varies wildly but the gems are real.
3. **Ghost/WordPress/Hugo/Jekyll** — independent publishers on open platforms, feeds are standard.
4. **Medium** — `medium.com/feed/@{user}` or `medium.com/feed/{pub}`. Same deal, find the good ones.
5. **Institutional with substance** — university labs, open-source projects, research groups. Fine if the content is good.
6. **Community aggregators** — Lobste.rs, Tildes, niche community sites with feeds.

Platform doesn't matter. Substance matters.

## Feed URL Patterns

| Platform | Feed URL pattern | Notes |
|----------|-----------------|-------|
| Substack | `{name}.substack.com/feed` | Always works |
| Medium user | `medium.com/feed/@{username}` | Personal accounts |
| Medium pub | `medium.com/feed/{publication}` | Publications |
| Ghost | `{site}/rss/` | Standard Ghost path |
| WordPress | `{site}/feed/` | Standard WP path |
| Hugo/Jekyll | `{site}/index.xml` or `{site}/feed.xml` | Varies by theme |
| Blogger | `{site}/feeds/posts/default` | Legacy but active |
| YouTube | `youtube.com/feeds/videos.xml?channel_id={id}` | Yes, YouTube has RSS |
| Reddit | `reddit.com/r/{sub}/.rss` | Subreddit feeds |
| GitHub | `github.com/{user}/{repo}/releases.atom` | Release feeds |

## The Quality Bar

For each candidate feed, imagine you're the moderator of "the Hacker News of [this topic]." Ask:

**Would the regulars upvote this?** Not the mass audience — the people who actually care about this topic. The sourdough nerds. The typography obsessives. The reverse engineering community. Would *they* find this interesting?

More concretely, look at the best 2-3 posts from the last year:
- Does it teach you something you didn't know?
- Does it show original work, thinking, or experimentation?
- Would domain experts share it?
- Would it spark discussion?

**Reject signals** (not hard rules, but strong signals):
- Content that exists to sell something
- Listicles and "ultimate guides" with no depth
- Obvious AI-generated text
- Aggregation without original insight
- Dead (no posts in 6+ months)
- Paywalled feeds that just tease headlines

**Accept signals:**
- Original research, experiments, or builds
- Deep technical content aimed at practitioners
- First-person experience and honest retrospectives
- Niche expertise (the kind of person who's been doing this for 20 years)
- Active community engagement (comments, responses, updates)

## Space Coverage Strategy

159 spaces. Not all spaces have equal blog ecosystems. Plan accordingly.

### Tier 1: Deep coverage (20+ feeds each)
Rich existing blog ecosystems, lots of people writing:
- Programming languages: `python`, `rust`, `golang`, `javascript`, `typescript`
- AI/ML: `machine-learning`, `llm-engineering`, `ai-agents` (heavy filter needed — 90% is slop)
- Infrastructure: `linux`, `self-hosted`, `homelab`, `databases`, `distributed-systems`
- Security: `security`, `pentesting`, `reverse-engineering`
- Academics: `mathematics`, `physics`, `astrophysics`
- Humanities: `history`, `philosophy`, `economics`
- Games: `game-dev`, `indie-games`
- Web: `webdev`

### Tier 2: Good coverage (10-20 feeds each)
Solid but smaller blog ecosystems:
- CS deep cuts: `compilers`, `operating-systems`, `computer-architecture`, `systems-programming`
- Creative: `design`, `typography`, `photography`, `data-visualization`
- Arts: `music-theory`, `film`, `writing`, `sci-fi`
- Science: `climate-science`, `ecology`, `genetics`, `neuroscience`
- Technical: `cryptography`, `quantum-computing`, `formal-verification`
- Craft: `woodworking`, `fermentation`, `sourdough`, `espresso`

### Tier 3: Niche coverage (3-10 feeds each)
Blogs exist but are harder to find — this is where the curation really matters:
- Esoteric CS: `category-theory`, `type-theory`, `number-theory`, `topology`
- Retro/hardware: `retrocomputing`, `vintage-hardware`, `fpga`, `ham-radio`
- Niche languages: `zig`, `elixir`, `nix`, `forth`, `haskell`, `lisp`
- Niche hobbies: `mycology`, `birding`, `lock-picking`, `urban-exploration`
- Creative tech: `pixel-art`, `generative-art`, `creative-coding`, `demoscene`
- Philosophy deep cuts: `semiotics`, `epistemology`, `philosophy-of-mind`

### Tier 4: Long tail (1-3 feeds each)
Even one good feed per space is a win here:
- `aquariums`, `cartography`, `conlangs`, `cellular-automata`, `fractals`
- `mechanism-design`, `information-theory`
- `standup-comedy`, `true-crime`, `horror`
- `vinyl`, `synths`, `electronic-music`
- `manga`, `worldbuilding`, `tabletop-rpg`

## Execution: Multi-Agent Discovery Pipeline

### Phase 1: Finder Bots (10 parallel haiku agents)

Cheap, fast, wide net. Each bot gets a cluster of related spaces. Their job: search the web, mine curated lists, follow blogrolls, and output raw candidate feeds. We expect noise — that's what Phase 2 is for.

**Bot 1: Systems & Infrastructure**
Spaces: `linux`, `operating-systems`, `containers`, `cloud-infrastructure`, `devops`, `ci-cd`, `site-reliability`, `observability`, `networking`, `edge-computing`, `self-hosted`, `homelab`, `shell-scripting`, `nix`
Starting points: Linux Weekly News, Lobste.rs regulars, Planet Debian/Fedora, SRE blog lists, Awesome-Selfhosted contributors

**Bot 2: Programming Languages & Tools**
Spaces: `rust`, `golang`, `python`, `javascript`, `typescript`, `haskell`, `elixir`, `swift`, `kotlin`, `cpp`, `zig`, `lisp`, `forth`, `wasm`
Starting points: "This Week in [lang]" contributor blogs, conference speaker blogs, PL research blogs, language community aggregators

**Bot 3: CS Theory & Math**
Spaces: `compilers`, `programming-languages`, `type-theory`, `category-theory`, `formal-verification`, `computer-architecture`, `information-theory`, `mathematics`, `number-theory`, `topology`, `statistics`, `math-olympiad`, `algorithmic-trading`, `game-theory`
Starting points: Terence Tao, Scott Aaronson, math blogosphere, quant blogs, PL theory blogs

**Bot 4: AI & Data**
Spaces: `machine-learning`, `llm-engineering`, `ai-agents`, `nlp`, `reinforcement-learning`, `prompt-engineering`, `computer-vision`, `data-engineering`, `data-visualization`, `bioinformatics`
Starting points: Independent AI researchers (not company blogs unless genuinely great), data eng community, viz practitioners. This space is 90% noise — quality filter is critical.

**Bot 5: Security & Low-Level**
Spaces: `security`, `pentesting`, `exploit-development`, `malware-analysis`, `reverse-engineering`, `cryptography`, `privacy`, `digital-rights`, `embedded-systems`, `fpga`, `retrocomputing`, `vintage-hardware`
Starting points: Infosec community blog lists, CTF team blogs, hardware hacking, EFF, retrocomputing communities

**Bot 6: Science & Nature**
Spaces: `physics`, `astrophysics`, `amateur-astronomy`, `space-exploration`, `chemistry`, `materials-science`, `genetics`, `ecology`, `climate-science`, `energy`, `nuclear`, `oceanography`, `geology`, `nanotechnology`, `neuroscience`, `pharmacology`, `mycology`
Starting points: Science communicator blogs, research group blogs, nature writing, university outreach, science magazine RSS

**Bot 7: Creative & Visual**
Spaces: `design`, `information-design`, `typography`, `photography`, `pixel-art`, `generative-art`, `creative-coding`, `animation`, `computer-graphics`, `procedural-generation`, `demoscene`, `film`, `manga`
Starting points: Design community blogs, creative coders, photography blogs, demoscene archives, film criticism

**Bot 8: Culture & Humanities**
Spaces: `history`, `history-of-computing`, `history-of-science`, `philosophy`, `philosophy-of-mind`, `political-philosophy`, `epistemology`, `ethics`, `economics`, `behavioral-economics`, `anthropology`, `archaeology`, `linguistics`, `semiotics`, `writing`, `sci-fi`, `horror`, `worldbuilding`
Starting points: Academic humanities blogs, book review sites, literary magazines, history blogs, economics blogs

**Bot 9: Games & Hobbies**
Spaces: `game-dev`, `indie-games`, `board-games`, `tabletop-rpg`, `chess`, `go-game`, `speedrunning`, `puzzles`, `mechanical-keyboards`, `3d-printing`, `woodworking`, `espresso`, `sourdough`, `fermentation`, `aquariums`, `birding`
Starting points: Devlog communities, itch.io blogs, BoardGameGeek, hobby forums with RSS, maker blogs

**Bot 10: Audio, Music & Misc**
Spaces: `audio-programming`, `music-theory`, `electronic-music`, `synths`, `vinyl`, `standup-comedy`, `true-crime`, `internet-culture`, `open-source`, `technical-writing`, `ham-radio`, `lock-picking`, `urban-exploration`, `cartography`, `conlangs`, `cellular-automata`, `fractals`
Starting points: Audio dev blogs, music theory educators, synth community, open-source project blogs, niche hobby communities

#### Finder Bot Prompt Template

```
You are finding RSS/Atom feeds for wingthing.ai/social — a platform where each
topic space is like a mini Hacker News for that subject.

Your assigned spaces: [list]

For each space, imagine: "What does the Hacker News of [this topic] look like?"
Find the blogs and publications that would be regularly upvoted by the
practitioners and enthusiasts in that community.

Discovery methods — use web search:
1. "best [topic] blogs", "[topic] blog RSS feed", "[topic] blogroll"
2. "[topic] substack", "[topic] newsletter RSS"
3. "awesome [topic]" GitHub lists (often link to blogs)
4. Well-known practitioners and researchers who blog
5. Community sites: lobste.rs, tildes, specific subreddits, niche forums
6. Conference speaker blogs, podcast host blogs

For each feed, output exactly:
- feed_url: the RSS/Atom URL (not the website homepage)
- site: name of the blog/publication
- about: 1 sentence
- spaces: [list of matching spaces from your assigned set]
- why: why a practitioner would upvote this (1 sentence)
- platform: self-hosted / substack / medium / ghost / wordpress / other

The bar: would practitioners in this field upvote posts from this source?
A corporate blog is fine IF the content is genuinely interesting (like
Cloudflare's blog on HN). A personal blog is fine IF they actually write
substantive posts. Platform doesn't matter. Substance matters.

Target: 50-100 feeds for your cluster.
Output as markdown, grouped by space slug.
```

### Phase 2: Curator Bots (3 parallel haiku agents)

Take the combined ~500-1000 raw candidates from Phase 1, split into thirds, and verify.

Each curator bot:
1. **Dedup** — same feed found by multiple finders
2. **Alive check** — fetch the feed URL, confirm it returns valid XML with entries
3. **Freshness** — has a post from the last 6 months?
4. **Content sniff** — read the most recent 2-3 post titles/descriptions:
   - Is it substantive original content?
   - Would it get upvoted on "the HN of [topic]"?
   - Not just link aggregation, not AI slop, not marketing?
5. **Space validation** — does the content actually match the claimed space?

Output per feed:
```
- feed_url: ...
- status: approved / rejected / needs_review
- reason: "excellent — deep technical posts on X, 2x/month" or "feed 404" or "last post 2022" etc.
- last_post_date: YYYY-MM if determinable
- frequency: posts per month estimate
```

### Phase 3: Final Curation (me)

I review curator reports and make final calls:
- Accept "approved" feeds I agree with
- Review "needs_review" with full context
- Identify coverage gaps (spaces with 0-1 feeds)
- Targeted search to fill gaps
- Produce final `skills/feeds.md`

## Output

Final artifact: `skills/feeds.md` — comprehensive, organized, commented:

```markdown
---
name: feeds
description: Curated RSS feeds for wingthing.ai/social
tags: [rss, feeds, social]
---
# Feeds

## Systems & Infrastructure
- https://rachelbythebay.com/w/atom.xml  # Rachel by the Bay — sysadmin war stories
- https://jvns.ca/atom.xml  # Julia Evans — making CS concepts accessible
- https://blog.cloudflare.com/rss/  # Cloudflare — internet infrastructure deep dives
...

## Programming Languages
### Rust
- https://without.boats/blog/rss.xml  # without.boats — async Rust, language design
...

## Science
### Physics
- https://www.preposterousuniverse.com/blog/feed/  # Sean Carroll — theoretical physics
...
```

Each feed gets an inline comment: source name + what makes it interesting.

## Success Criteria

- [ ] 500+ unique, verified, active feeds
- [ ] Every Tier 1 space has 15+ feeds
- [ ] Every Tier 2 space has 5+ feeds
- [ ] Every Tier 3 space has 2+ feeds
- [ ] 90% of Tier 4 spaces have at least 1 feed
- [ ] Self-hosted blogs >40% of total
- [ ] Feed freshness >80% posted in last 3 months
- [ ] Every feed passes: "would the HN of [topic] upvote this?"

## Execution Plan

1. Finalize this plan
2. Compact context
3. Launch 10 finder bots in parallel (haiku, ~5-10 min each)
4. Combine raw results, dedup at high level
5. Launch 3 curator bots in parallel (haiku, ~5 min each)
6. I review curator reports, accept/deny/fill gaps
7. Write final feeds.md
8. Commit
