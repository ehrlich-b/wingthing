# TODO

## Direct Agent Invocation

`wt [prompt]` currently creates a task in the store but nothing runs it. Next step: invoke the agent directly and store the result inline (no daemon needed).

## Cleanup

- Prune dead code: `internal/daemon`, `internal/transport`, `internal/ws` (no longer used)
- Prune dead WS daemon/client handlers in `internal/relay`
- Remove `SocketPath()` from config
- Vite migration for `web/` (currently vanilla JS)
- Consider merging wt.db and social.db into one store
