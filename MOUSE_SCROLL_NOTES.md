# Mouse + scroll rework — working notes

**Delete this file before the PR goes up.** Notes-to-self for QA, not review material.

Branch `mouse-wheel-scroll` here and the same branch name in `slidehq/devops`.
Both halves are required — see "Deploy order" below, the order is load-bearing.

## The problem, restated

Selection and scrolling were fighting over one switch:

- Claude enables mouse tracking → xterm.js forwards every button event to it and stops
  selecting locally → copy/paste broke for everyone (June, `b7ffeed`).
- `CLAUDE_CODE_DISABLE_MOUSE=1` → selection came back, but Claude never sees the wheel,
  and the alt screen has no xterm scrollback → scrolling died entirely.
- `4abe51b` papered over that by mapping wheel → PgUp/PgDn. Granularity is Claude's page
  size, which is the "half-page jump" Mike Smith and Taylor are complaining about (7/23–7/24).

They want *different events*. Selection needs buttons, scrolling needs the wheel. Nothing
actually required trading one for the other.

## What the change does

`web/src/mouse.js` strips the mouse-mode set/reset sequences on their way to xterm and
records what the app asked for. xterm keeps selecting locally (as far as it knows no app
ever enabled the mouse); the wheel handler forwards notches to the app in the encoding it
requested. Claude's mouse goes back on (`internal/egg/agents.go`).

Filter sits on *every* write path: live PTY, replay/reattach, P2P datachannel, canvas
sessions, audit replay. Native terminal clients are untouched on purpose — they forward
mouse events for real and should keep doing so.

Fallback: no app mouse → the old PgUp/PgDn path still runs in the alt screen (vim, less).
So the floor is today's behavior.

## Verified before building (don't re-derive this)

Probed a real Claude Code 2.1.219 on a pty:

- It emits `1049h` + `1000h` + `1002h` + `1003h` + `1006h` (SGR) at startup.
- It *does* scroll on SGR wheel reports — each report redraws, idle produces zero bytes.
  My first two probes said otherwise and were both wrong: nothing was scrollable on screen.
  Don't trust a negative result here without a long transcript on screen.

In real xterm.js in Chrome, fed that captured byte stream in 7-byte chunks:

- Unfiltered baseline → `mouseTrackingMode: 'any'` (reproduces the June bug exactly).
- Filtered → `mouseTrackingMode: 'none'`, `getSelection()` returns text, alt screen and
  bracketed paste survive.
- `term.write(emptyPayload, cb)` fires the callback — so a fully-held chunk can't hang the
  replay overlay. This was the one thing that could have broken everyone; it's fine.

## Known gaps and risks (my own review)

1. **Buttons never reach any TUI in the browser, permanently by design.** Clicks and hover
   are the browser's now. Same as today, but now it's structural. Claude requests `1003h`
   (motion) and will never get it, so any hover affordance it draws is dead. Cosmetic.
2. **Deploy order matters.** New settings + old binary = mouse on with no filter = the June
   copy/paste break returns for everyone. Binary first, always.
3. **`WHEEL_STEP_APP = 40` is a guess.** It's the px-per-notch feel knob and the one number
   to tune during QA. Trackpad and wheel mouse will want different things; 40 is a compromise.
4. **localStorage restore gap.** The serialized buffer contains no mouse DECSETs, so right
   after a page reload the first wheel tick may fall back to a PgUp jump until live output
   re-establishes tracking. Self-healing. Fix would be persisting the flag next to the buffer —
   didn't, felt like over-engineering for one tick.
5. **1016 (SGR-pixels) is treated as plain SGR** with cell coordinates, which would be the
   wrong units if an app ever used it. Claude doesn't. Latent, documented, not worth plumbing.
6. **Spectators still can't scroll** — the wheel sends input and spectator input is rejected
   server-side. Pre-existing, unchanged by this. Arguably should scroll locally someday.
7. **Mobile/touch in the alt screen still doesn't scroll.** The touch proxy calls
   `scrollToLine`, which does nothing there. Pre-existing; would need the same wheel
   translation. Couldn't test on a device so I left it alone.
8. **Native terminal users get mouse capture back** — that includes you running `wt egg`
   locally. Drag-select in iTerm2 now needs Option held. Normal for a mouse-enabled TUI,
   but it is a change from today.
9. **Version coupling.** If a future Claude Code changes mouse protocol, the failure mode is
   falling back to PgUp/PgDn (today's behavior), not breakage. Re-probe after CC upgrades.
10. `web/package.json` gained `"type": "module"` so `node --test` can run the source
    directly with no new dependency. `vite.config.js` was already ESM and there is no CJS
    anywhere in `web/`, so this is a no-op for the build — verified `npm run build` after.

## QA plan

Automated first, 30 seconds:

```
make test-web     # 12 cases: chunk splits, combined params, resets, utf-8, callbacks
make check        # web build + go test + test-web + build
```

Then a real browser session — this is the part that matters, since the last two attempts
both passed "works on my machine" and broke in the field:

1. Long Claude transcript, wheel up and down. **Smooth, roughly line-granular, no half-page
   jumps.** This is the Mike Smith / Taylor complaint — if this still jumps, the fix failed.
2. Click-drag select a SQL query mid-transcript, Cmd+C. **Clipboard has it, no shift held.**
   This is the June complaint — if this fails, the filter isn't catching something.
3. Scroll to the bottom, let new output arrive → auto-scroll resumes.
4. Trackpad *and* a real wheel mouse. Tune `WHEEL_STEP_APP` if either feels wrong.
5. Reload the page mid-session (reattach path) → scroll still works after replay. One page-jump
   on the very first tick is expected (gap 4 above), not a bug.
6. Exit Claude to the shell inside the egg → wheel scrolls xterm's own scrollback, and **no
   `[<64;40;10M` junk gets typed into the prompt.** This validates the alt-screen-exit cleanup.
7. `vim` in the egg → wheel still scrolls via the PgUp/PgDn fallback.
8. Preview pane / side-by-side still behaves.
9. Open an audit replay → renders normally (it goes through the same filter).
10. Spectate a session → still can't scroll. Known, not a regression.

Do all of it on qa-wingthing before prod.

## Deploy order

1. Tag + release wingthing. Ansible pulls
   `https://github.com/ehrlich-b/wingthing/releases/latest/download/wt-linux-amd64`
   (`wingthing_version: latest` in prod), so the web bundle only ships inside a release binary.
2. *Then* run the devops branch: it drops `CLAUDE_CODE_DISABLE_MOUSE` from
   `claude-settings.json.j2` → `~/.claude/settings.json`. New sessions pick it up; no roost
   restart needed, so nobody's session gets killed.
3. Stage → test → prod.

Reversed order reintroduces the copy/paste break. Rollback is the same two steps backwards,
and the devops half alone is enough to stop the bleeding if it goes wrong.
