# UI Redesign: From Prototype to Product

## The Core Problem

The UI is organized around **sessions** (nameless UUIDs) when it should be organized around **machines and projects**. The mental model should be:

```
Your Machines → Your Projects → Your Sessions
```

Not:

```
Flat list of [term] claude 8a3f2b1c [live]
```

## What the Dashboard Should Feel Like

When you open app.wingthing.ai, you should see your machine, what's running on it, and be one click from a new terminal in any project. Think Tailscale admin console meets VS Code remote.

```
┌─────────────────────────────────────────────────────────┐
│  wt                                        bryan [·]    │
├────┬────────────────────────────────────────────────────┤
│    │                                                    │
│ +  │  ehrlich-mbp                            ● online   │
│    │                                                    │
│ W  │  Projects                                          │
│    │  wingthing        ~/repos/wingthing          [→]   │
│ S  │  slideMCP         ~/repos/slideMCP           [→]   │
│    │                                                    │
│    │  Sessions                                          │
│    │  ● wingthing · claude                 2h    [↗]    │
│    │  ○ slideMCP · claude              detached  [↗]    │
│    │                                                    │
└────┴────────────────────────────────────────────────────┘
```

- Left rail = persistent session tabs. Click to switch instantly. No "back to home."
- `+` opens a new session
- `W` and `S` are the first letter of the project name
- Colored dot: green = live, yellow = detached

## New Session Flow: Command Palette

Click `+` (or keyboard shortcut). A command-palette overlay appears, like Cmd+K in Linear or Cmd+P in VS Code:

```
┌─────────────────────────────────────────┐
│  Open terminal                          │
│ ┌─────────────────────────────────────┐ │
│ │ slide                               │ │
│ └─────────────────────────────────────┘ │
│                                         │
│  slideMCP         ~/repos/slideMCP      │
│  slide-infra      ~/repos/slide-infra   │
│                                         │
│                    claude ▾     ⏎ open   │
└─────────────────────────────────────────┘
```

- Wing scans parent directory for git repos on registration (and periodically)
- Results filter as you type, fuzzy match on name and path
- Type a full path for anything not listed
- Agent selector defaults to last-used
- Enter to launch
- The `pty.start` message carries `cwd`

### How the scan works (wing side)

When `wt wing` starts, it walks the current directory (and optionally configured roots) looking for `.git` directories, depth-limited. Sends the list during `wing.register`. Lightweight, one-time on connect + can refresh on demand.

## In-Session View

```
┌────┬────────────────────────────────────────────────────┐
│    │ wingthing · claude                      🔒 ● live  │
│ +  ├────────────────────────────────────────────────────┤
│    │                                                    │
│ W  │                                                    │
│    │         [full-bleed xterm.js]                       │
│ S  │                                                    │
│    │                                                    │
│    │                                                    │
│    │                                                    │
└────┴────────────────────────────────────────────────────┘
```

- Header: project name + agent + lock icon (E2E) + status dot. That's it.
- Terminal gets maximum space.
- No modifier buttons on desktop (native keyboard works fine, xterm.js handles ctrl/alt natively).
- Mobile: modifier row stays but smaller, better touch targets.
- Left rail: 48px wide, just icons. Hover for tooltip with full project name.

## Design Changes Summary

| Now | Should be |
|-----|-----------|
| Sessions identified by UUID | Sessions named by project directory |
| No wing visibility | Machine shown with status dot |
| Split button dropdown for agent | Command palette with directory + agent |
| Navigate away to see session list | Persistent sidebar rail, click to switch |
| "live" / "detached" text badges | Colored dots (green/yellow), obvious at a glance |
| Modifier key buttons on desktop | Remove on desktop, keep on mobile |
| No project/directory context | Wing advertises git repos, shown in palette |
| Flat session cards | Grouped under machine, sorted by recency |

## Implementation Order

### Phase 1: Wire Protocol (build first, everything depends on it)

1. Wing scans for git repos on startup, sends project list in `wing.register`
2. `pty.start` gets a `cwd` field, wing sets `cmd.Dir` to it
3. Session cards show project name instead of UUID

### Phase 2: Command Palette UI

4. Replace split button with command palette overlay
5. Fuzzy search over project list from wing
6. Text input for arbitrary paths
7. Agent selector in palette footer

### Phase 3: Session Sidebar

8. Persistent left rail with session tabs
9. Click to switch sessions without navigating home
10. Status dots replace text badges
11. Remove modifier buttons on desktop

### Phase 4: Polish

12. Machine status in header/dashboard
13. Session grouping by project
14. Keyboard shortcut for new session (Cmd+K or similar)
15. Smooth transitions between sessions

## Deferred to v0.2

- **Sandbox for PTY sessions** -- the sandbox infra exists (`internal/sandbox/`) but only `executeRelayTask` uses it. PTY sessions run unsandboxed. The fallback sandbox (what most machines get today) provides essentially no real isolation, so `--dangerously-skip-permissions` should only be passed when a real sandbox (Apple Containers or Linux namespaces) is detected. Move this to v0.2 when sandbox support is more widespread.

## Current State (what's already done)

- Graceful wing shutdown: `sessionTracker` tracks active PTY child processes. On SIGTERM/SIGINT, sends SIGTERM to all children, waits 5s, force-kills stragglers.
- Per-session graceful termination: `cmd.Cancel` sends SIGTERM instead of SIGKILL, with 5s `WaitDelay` before force-kill.
- Signal handler in wing's `RunE` catches SIGTERM/SIGINT, triggers tracker shutdown, then cancels wing context.
