# Search sessions everywhere

Run a personal wing, then use `wt search 'DNS helper' --json`. Saved remotes must
already be configured and verified through the existing MCP connection handshake.
`--local` skips remote fanout. MCP `session_search` uses the same handler, requires
`query`, and defaults `include_remotes` to true. Scoped callers must pass false;
conversation-bound callers cannot search fleet inventories.

The dashboard searches authorized connected wings and their delegated personal
remotes over encrypted `sessions.search` tunnels. Gateway search requires the
existing `phone_fleet: true`, owner binding and local control socket. When remote
delegation is unavailable, local results remain usable and coverage is incomplete.
Organization/member connections retain local ownership and path filtering.

Queries match every space-separated word, ignoring case, across name, repo path,
machine label, agent, session ID and recorded human/assistant transcript text.
Different words may match different fields or messages. Tool payloads and terminal
screens are excluded. Results contain match field names, never transcript snippets.
Recognized credentials are removed before indexing and metadata matching. This is
best-effort pattern recognition, not a guarantee for arbitrary secret values.

Each result is identified by `(origin_wing_id, session_id)`. Names and shortened
IDs cannot select an action. Authenticated sightings of the same identity merge
their routes; equal session IDs on different wings remain distinct. The transcript
reader selects a fresh healthy direct route first, otherwise a verified gateway,
and calls the existing qualified read handler. It displays the selected route and
never sends session input. Expired routes require a refreshed search.

The private disposable cache is `session-search-v2/index.sqlite` under the wing
state directory (directory 0700, database 0600). It is separate from durable task
storage. SQLite FTS5 trigrams accelerate literal substring matching; one- and
two-character words use bounded scans. Every query checks current ownership and
paths before admitting source files or indexed candidates. Changed JSONL journals
resume at the last complete record after checking bounded prefix/checkpoint
fingerprints on growth. Replaced, truncated or mismatched sources reset;
changed gzip archives rebuild. Deleted sources/sessions are removed. Unchanged
sources do not reread indexed text. Live/native status still refreshes through
the existing lifecycle importer with a bounded journal projection.

Bounds per wing request: 1000 directory entries, 200 returned results, 8 query
words, 256 query bytes, 32 MiB of newly indexed/decompressed bytes, 8 MiB per
source refresh, 1 MiB per JSON record, 256 KiB retained text per source, 64 MiB of
database pages, 32 MiB of native lifecycle reads with 8 MiB per session and 1000 native hook
directory entries,
5 seconds, 32 configured remotes and 4 remote workers. A source over the retained-text limit remains
incomplete; it is not silently treated as fully searchable. Remote queries do not
recursively fan out. Invalid files, partial records, malformed data, cache errors,
unverified/unreachable origins and bounds set `complete: false`; matching rows
carry `search_incomplete` when their own source is incomplete. Results include
source observation and generation timestamps. A successfully checked empty set
differs from an unavailable source.

Use `make test-session-search` for index, ownership, schema and serialized built
CLI/MCP socket checks with two isolated wings and fixture SSH. Use
`make test-session-search-race` for race checks. The opt-in
`make test-session-search-web` needs `WT_PLAYWRIGHT_MODULE` pointing to a staged
Playwright module; it runs the real built dashboard against an isolated roost,
another local wing and fixture SSH. `WT_SEARCH_SHOTS` selects screenshot output.
No real provider, personal daemon, account or deployment participates in these
fixtures. Live remote SSH, native iPhone and physical phone acceptance are separate.
