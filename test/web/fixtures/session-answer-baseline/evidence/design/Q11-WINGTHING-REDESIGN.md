# Q11b: wingthing, one product. Redesign for an overnight build (11/09)

Bryan, 8:20 PM: "I'm like 4 clicks in and now I'm back to like basically old wingthing?? I'm lost, redesign this to make sense overnight."

This supersedes the cards in `Q11-WINGTHING-UX-ANSWER.md`; its findings stand and are folded in here. Mockups: `Q11-wingthing-redesign-mockups.html` in this folder (static, seven sections; its `:root` block is the token set). Source: `~/repos/wingthing` at `537b462e0ee577587af78061b4b4afe7b61eff90`.

Labels: **[E]** read in code or seen live, **[I]** inference, **[H]** hunch. What I walked live (read-only, 11/09): the homepage, and in the app Home, Sessions, Machines, one session page, New session's first screen, Account. Install, login, docs, the legacy terminal page, the machine page and the phone are from code only; I did not start a session or attach a terminal.

---

## 0. The decision, in one page

**There are two apps in one bundle, and every important action crosses from the new one into the old one.** The new shell ("paper": `web/src/paper.js`, `paper.css`) renders Home, Sessions, Machines and Account. The old app (`web/index.html`, `style.css`, `render.js`, `pty.js`, `nav.js`) still owns everything that matters: starting a session, the live terminal, the machine page, the passkey gate, search. The new shell hides itself (`show(false)`) and hands over. [E]

| Action in the new shell | Where it sends you | Code |
|---|---|---|
| New session → Choose project → Enter | old terminal page `#s/<id>` with the old sidebar | `paper.js:369` → palette → `render.js:212` `switchToSession` |
| Take over terminal / Answer in terminal | same | `paper.js:360` |
| A push notification, a link the server builds | same | `notify.js:102`, `internal/relay/workers.go:1070` |
| Click a machine | old machine page, re-parented into the shell but with old markup, lowercase buttons and its own passkey banner | `paper.js:521-528`, `render.js:1403-1530` |
| "~ home" in the old sidebar, or any unknown hash | old home: search-everywhere, availability cards, coordinators, tiles | `nav.js:52-79`, `main.js:98` |

**The redesign is one rule: the shell never hides.** Sidebar and header are always on screen; every destination is a view inside it; the live terminal is a pane on the session page. The old home, old sidebar, old header and old machine markup become unreachable. The old *engines* stay (xterm, the PTY attach, the tunnel, the project picker's logic); only their chrome goes. That is why it fits in a night: nothing new has to be invented below the surface.

Six more rules, each from something Bryan said tonight:

1. **The terminal is the session.** The session page is the live terminal with messages beside it, not a messages page with a preview that never loads.
2. **Nothing moves on its own.** Refreshes patch text in place; rows have fixed heights; status lines have reserved slots.
3. **Calm states.** Offline is grey, not orange. Orange means "needs you" and nothing else. Red means an action failed.
4. **One number, one source.** Every count comes from one selector module.
5. **Passkeys work and stay quiet.** One unlock prompt, shown only where it blocks, only on a click, remembered across tabs.
6. **Personal and team are visible in the frame**, as a filter under the brand.

---

## 1. The first-time journey, and where it breaks

| # | Step | What happens now | Break | Severity |
|---|---|---|---|---|
| 1 | Homepage | 92 px fused headline; preview copy about "session recovery"; caption with "roost", "entitled hosted account", "allowed wing" (`home.html:11,121-132`) | Can't tell what it is in five seconds | slows |
| 2 | Click "Install wingthing" | `/install`: title "install wt", headings "quick install" in lowercase blue, then "Run your first coding agent" teaches `wt egg claude --name work`, a local flow with "No Wingthing account or daemon is required" (`install.html:36-60`) | The page teaches a different product path from the one the app expects (`wt login`, `wt start`). A new user finishes this page with no machine in the app | **blocks** |
| 3 | Log in | `/login`: h1 "login", "sign in to wingthing.ai", "continue with GitHub" first, "send link" in mono (`login.html:21-36`). No passkey sign-in exists: the relay has only register, list and delete (`server.go:171-174`) | Lowercase, dated; the first button is the provider Bryan's own account is locked out of; no passkey | slows (blocks for him) |
| 4 | Land in the app with no machine | "No machines yet" plus three commands (`paper.js:174-176`) | Good content, but it does not notice when the machine connects until the 30 s poll (`main.js:418`), and says nothing while waiting | slows |
| 5 | `wt login`, `wt start` in a terminal | Prints `open https://app.wingthing.ai/` while the site links `wingthing.ai/app/` (`cmd/wt/wing.go:208`) | Two addresses; two browser tabs with separate session storage | cosmetic, but it doubles step 9 |
| 6 | Machine appears | Home says "No sessions yet. Choose a machine and set up an agent to begin." with "New session" (`paper.js:177-180`) | Fine | — |
| 7 | New session | `#new`: a machine select, "Ready on X", a list of agent names as a sentence, "Choose project" (`paper.js:185-195`) | You cannot pick the agent here; the next step is a different-looking dialog | slows |
| 8 | Choose project | The old command palette: "type a path...", footer "tab complete · shift+tab agent · ` wing · enter open" (`index.html:249-258`) | Agent choice is hidden behind shift+tab; no shell option; the footer's "terminal" is not clickable (friction log) | slows |
| 9 | Enter | **Old app.** `#s/<id>`: "~ home", "+ new session", "wingthing.ai machine-1 · claude 🔒 copy files x", "multi-session" | The shell is gone. Bryan: "I'm lost" | **blocks** |
| 10 | Machine asks for a passkey | A bare button "authenticate with passkey" over the terminal (`index.html:178-180`, `pty.js:154-163`), or "Authenticate to access this wing [authenticate]" on the machine page (`render.js:1477`), or "Machine locked … Unlock machine" in New session (`paper.js:191-192`) | Three designs for one gate, none says why. The grant lives in `sessionStorage` (`tunnel.js:57-60`) with a one-hour default (`internal/config/wing.go:37`), so every new tab and every hour asks again | **blocks** |
| 11 | Click "~ home" to get back | Old home: "Search sessions everywhere", which runs by itself every 15 s (`session-search.js:126`) and renders each unreachable machine as a card: "Search unavailable · Computer offline · Last successful check: Unknown" (`session-search.js:22,38,62,71`) | An outage poster as the first thing on screen, and the page shifts when it re-renders | **blocks** |
| 12 | Watch | New session page: "No messages in this session yet." and "Preview unavailable from this machine." The preview call is one the server always refuses (`paper.js:133`, `conversation_browser.go:34`) | Dead pane | **blocks** |
| 13 | Steer | Composer works on the new page; typing works only on the old page | Two places, two mental models | slows |
| 14 | Things jump | The shell rebuilds the whole main column with `innerHTML` whenever anything in it changes, checked every second (`paper.js:534-545,586`); the old home has 5 s, 15 s and 30 s timers (`coordinator-home.js:174`, `session-search.js:126`, `parent-dot.js:105`, `main.js:418`) | Focus, scroll and layout move under the pointer | slows, and it looks broken on video |
| 15 | Machine page | "Sessions 0 · No sessions yet" while the sidebar says "Sessions 1" | The sidebar count is recomputed every second from `workSessions()` (`paper.js:92,337`); the machine page is drawn once per event by different code with a different filter (`render.js:1462-1463`). Two sources [E]; which one was stale tonight is not established [H] | slows (trust) |
| 16 | Come back tomorrow | Sign-in is GitHub only on this account; unlock grant is gone; the last route was `#s/…` so you return into the old app | Lands in the wrong product, then nags | **blocks** |
| 17 | Phone | Shell has bottom tabs (`paper.css:275-280`); "Answer in terminal" goes to the old terminal page with its own key bar (`index.html:185-193`) | Same crossing, smaller screen | slows |

Six blockers. Four of them (9, 11, 12, 16) are the same defect: the old app is still reachable.

---

## 2. Information architecture

### One address, one route table

`app.wingthing.ai/` is the app. `wingthing.ai/app/` redirects to it. The CLI and the site print the same one.

| Route | View | Replaces |
|---|---|---|
| `#` | Home | wing view, old home |
| `#sessions` | Sessions | — |
| `#session/<machine>/<id>` | Session (terminal + messages) | `#s/<id>`, `#chat/<id>`, `#approve/…`, the viewer dialog |
| `#new`, `#new/<machine>` | New session | palette entry |
| `#machines` | Machines | — |
| `#machine/<id>` | Machine | `#w/<id>` |
| `#account` | Account | — |

Redirects, permanent: `#s/<id>?wing=<m>` and `#chat/<id>?wing=<m>` → `#session/<m>/<id>`; `#approve/<m>/<id>` → the session page with the needs-you card focused; `#w/<id>` → `#machine/<id>`; `#needs`, `#decisions` → `#`; `#pair` → the existing pair entry; anything unknown → `#`. `#canvas` stays reachable by URL only and is not linked in v1.

### The frame (mockup 1)

Sidebar, 232 px, identical on every screen, top to bottom:

1. Brand.
2. **New session** button. Always there, always the same place. `.` and Cmd+K open it.
3. Space filter: `All | Personal | <team>`. Hidden when the account has one space. Filters every list and every count.
4. Nav: **Home** (with "N needs you" in orange when N > 0, nothing when 0), **Sessions** (count), **Machines** (count).
5. Machines, grouped by space, one line each with a state dot. Clicking one opens its page.
6. Footer: avatar and display name → Account. No email.

Main column:

- **Header, 56 px:** title left; one line of meta and at most two actions right.
- **Status strip, 32 px, always present:** breadcrumb ("← Sessions") on inner pages; otherwise one calm sentence about connection ("All machines connected." / "build-1 is offline, last seen 6:12 PM."). Reconnecting, offline and update notices all appear here and nowhere else, so they never push content.
- **Body.**

Removed from the frame: the date line, the email, the old sidebar's session tabs, "multi-session", the `wingthing.ai` header logo, the lock emoji.

### Words

| Thing | Word | Never |
|---|---|---|
| A computer running `wt` | machine | wing, egg host, execution wing |
| The pinned summarising agent | wing | coordinator (in UI), parent |
| A running agent or shell | session | egg, execution |
| Personal vs org | Personal, and the team's name | scope, org, "slide" as a literal |
| The passkey gate | Unlock | Authenticate, locked wing |

States, everywhere, with one colour each: **Needs you** (orange), **Working** (blue), **Done** (green), **Idle** (grey ring), **Ended** (grey), **Offline** (grey ring), **Locked** (grey ring). Machine dots: green square connected, grey hollow offline or locked. No red or orange for a machine that is merely away.

---

## 3. Screens

Tokens are the `:root` block in the mockup file; summary: surfaces `#07131A / #0F1F28 / #142833`, ink `#E6F1F6 / #8CA5B2 / #6B8492`, rules `#1C2F3A / #2C4452`, blue `#2AA8FF`, orange `#FF8A3D`, green `#36D69A`, red `#FF5D6C`; sans `system-ui` stack, mono JetBrains Mono (already shipped); radii 6 and 10; row height 44; type 12/13/14/15/18/22 at weights 400/500/600 only. Sentence case for every label. "…" not "...".

### 3.1 Home (mockups 1, 2)

Sections in order; a section with nothing in it is not drawn, except where noted.

- **Needs you.** One card per waiting session, at most three, then "+N more" → Sessions filtered. Card: session name, one sentence ("wants to run a command on Studio"), the command in a `$` block when known, one button **Answer** → session page with the terminal focused.
- **Working.** One 44 px row per session: state, name with "agent · machine" under it, latest line (one line, ellipsis), relative time.
- **Recent.** Up to five idle, done or ended.
- **Rail (300 px):** "Message wing" with the composer when a wing exists. When none: one sentence, "A wing is an agent that watches the others and summarises here.", and a quiet "Set one up" link to docs. No false "Wing has not reported yet."

States:

| State | Body | Strip |
|---|---|---|
| No machine | First-run steps (mockup 2). Step 2 holds the three commands and Copy. The app polls every 3 s while this state is on screen and ticks the step itself | "Waiting for your first machine…" |
| Machine, no session | Step 2 ticked: "Studio connected". Step 3 active with **Start a session** | "Studio is connected." |
| Sessions, none need you | Working and Recent only. Header meta "3 working" | connection sentence |
| Everything offline | Rows stay, dimmed, state "Offline". No banner card | "No machines are connected. Sessions are kept." with Retry |
| Signed in but browser access not included | One panel: what's included, the two MCP commands, link to plans | — |

### 3.2 Sessions

Search field (filters the list locally as you type), **New session** is in the sidebar so not repeated. Groups: Needs you, Working, Idle and done, Offline. Row as on Home. With the space filter on All, the sub-line starts with the space: "Personal · Claude · Studio".

"Search everywhere" (past sessions and transcripts) is a link under the field that appears after you type. It runs once per click, never on a timer. A machine it could not reach is one grey line under the results: "Didn't search build-1 (offline)." No cards, no "Last successful check: Unknown".

Empty: "No sessions yet." and **Start a session**. No match: "No sessions match "x"." and Clear.

### 3.3 Session (mockups 3, 4)

Header: session name; meta "Claude · Studio · project" and the state; actions **Files** and a **⋯** menu (Rename, Copy link, End session). Strip: "← Sessions".

Body: terminal column (flexible) and Messages rail (360 px, "Hide" collapses it to a 40 px tab and is remembered).

**Terminal pane.** It is the existing xterm and PTY attach, mounted here instead of on its own page. The bar above it says who has input; the border is blue when it is you.

| State | Bar | Pane | Under it |
|---|---|---|---|
| Connecting | "Terminal · Connecting…" | reserved, same size, one grey line | — |
| Live, you type | "Terminal · You're typing" | live, blue border | — |
| Watching | "Terminal · Read-only" + **Take control** | live output or 4 s snapshot, grey border | "<who> is typing. Taking control doesn't stop the agent." |
| Locked | "Terminal · Locked" | unlock prompt over a blank pane (3.7) | — |
| Offline | "Terminal · Last screen, <time>" + **Retry** | last cached screen at 55% | "<machine> is offline. The session is kept and will be here when it reconnects." |
| Ended | "Terminal · Ended <time>" + **New session here** | last screen at 55% | "This is the last screen. Messages are kept." |
| Older wt on the machine | "Terminal · Unavailable" | one line | "Update wt on <machine> to see the terminal here: `wt update`" |

Default on open: attach live, exactly as the old page does today (`nav.js:104-116`), so the first thing a new user sees after pressing Enter is their agent starting, inside the frame. If input is held elsewhere the attach falls back to Watching; "Take control" is the existing takeover (`terminal-attachment.js:13-18`). The read-only snapshot goes through `session_action` (`session-viewer-state.js:98`), not the refused call.

"Full screen" hides sidebar and rail; Esc returns. On leaving the page the PTY detaches (as `showHome` does now, `nav.js:67-73`).

**Messages rail.** Transcript, newest at the bottom, "You" rows filled. Composer pinned at the bottom with one reserved 18 px line under it for delivery state ("Goes to the agent's prompt." / "Sending…" / "Delivered" / "Not confirmed. Check"). No request ids. Empty: "No messages yet." When the session needs you, the needs-you card is the last item in the rail and its button is "Answer in the terminal", which focuses the pane.

### 3.4 New session

One page, three choices, one button, no dialog:

1. **Machine**: rows (dot, name, space, agents installed). Offline ones are listed, disabled, with "offline". Preselected: the last one used, else the only connected one.
2. **Agent**: segmented control of what that machine has, plus **Shell**. Preselected: last used.
3. **Project**: a path field with the existing completion list under it (the palette's listing logic, shown inline), recent projects first.

**Start session** → `#session/<machine>/<id>`. While starting, the button reads "Starting…" and the page does not navigate until the session id exists. Failures appear under the button in one line.

Tonight's v1 may keep the palette dialog and change only where it lands; the inline page is a second-wave card.

### 3.5 Machines, and Machine (mockup 5)

**Machines:** grouped under "Personal" and each team's name with counts (the `space_name` / `space_kind` fields from Q10's card 9; until they exist, `org_id` present → "Team"). Row: dot, name, "Connected · Claude, Codex" or "Offline · last seen 6:12 PM" or "Locked", session count. Rail: "Add a machine: run `wt start` on it." and one link, "More ways to connect".

**Machine:** header name; meta space, agents, state; action **New session here**. Body: "Sessions on <name>" with the same rows and the same selector as everywhere else; then three folded sections: Details (platform, version, update button), Who can use this machine (the allow list), Past sessions. Locked: the unlock prompt replaces the sessions list; the header and folded sections stay. No "authenticate" button, no emoji, no public key outside Details.

### 3.6 Account, and sign in (mockup 6)

**Sign-in methods** (replaces "Sign-in" and "Passkeys"): one list. Rows: Google / GitHub / Email link / each passkey. A passkey row reads "Passkey · MacBook, Chrome", "Added Nov 9 · last used 8:02 PM · also unlocks machines".

- **Add a passkey**: one click, the browser sheet, done. The name is filled in from the device and browser and is editable in the row afterwards. On success the row appears where the button was pointing; no page reload. On cancel: "Cancelled." in the reserved line under the button. On an unsupported browser the button is replaced by "This browser can't create passkeys."
- **Remove**: one confirm line in place ("Remove this passkey? Remove · Keep"). If it is the last method the button is disabled and reads "This is your only way in. Add another first." If it is the last passkey and a machine requires one: "Studio will ask for a passkey you no longer have. Remove anyway?"

Other sections: Plan, Notifications ("Get a phone notification when an agent needs you" · **Turn on**), Teams, Log out. All sentence case.

**Sign in page:** "Sign in" / "to wingthing". Buttons in this order: Continue with Google, **Sign in with a passkey**, then "or" and the email field with **Email me a link**. GitHub appears only if the deployment has it, last. The email field carries `autocomplete="username webauthn"` so the browser offers a saved passkey in autofill; the page never opens a passkey sheet on load. After the link is sent: "Check your email for a sign-in link." in the reserved line.

Dependency, stated plainly: passkey *sign-in* needs relay endpoints that do not exist at this commit; today passkeys are verified only by a machine over the tunnel (`tunnel.js:399-448`). The team lead's end-to-end card owns that. The login card below ships the button behind a capability check so the page is correct either way.

### 3.7 The unlock prompt

One component, used in the terminal pane, on the Machine page and in New session. Never a modal over the whole app, never shown by a timer or on page load. Background checks never trigger it (`data.js:171` already passes `skipPasskey`); a locked machine simply shows state "Locked".

> **Unlock Studio**
> Studio asks for your passkey before it shows terminals. It stays unlocked in this browser for 12 hours.
> **[ Unlock with passkey ]**
> Or attach from Studio itself: `wt attach fix-login-redirect`

| Outcome | What the prompt shows |
|---|---|
| Success | Prompt disappears; the pane continues what it was doing (attach, list, start) without another click |
| Cancelled sheet | "Cancelled. Try again when you're ready." Button stays |
| Wrong or unknown passkey | "That passkey isn't one Studio accepts." Button stays |
| Account has no passkey | Title unchanged; text "Studio asks for a passkey, and you don't have one yet. Adding one takes a few seconds." Button **Add a passkey and unlock** (register, then unlock, one flow). Alt line: "Or turn the lock off on Studio: `wt wing unlock`" |
| Not allowed on this machine | "You don't have access to Studio. Ask its owner to add you." No button |

Remembering: keep the grant in `localStorage`, keyed by user and machine, with its expiry, so it survives tabs and reloads; clear on log out. The duration shown is the machine's own setting. Raising the default from 1 h to 12 h (`internal/config/wing.go:37`) is a product choice I recommend [H]; the web change alone removes most of the nagging.

### 3.8 Phone (mockup 7)

Bottom tabs: Home, Sessions, Machines, New. Home leads with the needs-you card and a 44 px **Answer** button. Session page: a two-way switch **Terminal | Messages** under the header (terminal first when the session needs you, messages first otherwise); the key bar (esc, ctrl, ↑, ↓, ⏎, type) sits above the tabs only on Terminal. No sidebar; the space filter is a chip in the top bar.

### 3.9 Site pages

- **Homepage:** hero values from the earlier answer (64 px cap, weight 600, −0.022em, smoothing on); preview uses the fixture story and the app's state chips; caption "Agents run on your machines. Watch and steer from the browser, your phone or the CLI."
- **Install:** title "Install wingthing". Step 1 install, step 2 `wt login`, step 3 `wt start`, then "Open the app". The local-only `wt egg` path moves under a fold, "Use it without an account".
- **Login:** 3.6.

---

## 4. No-jump rules (binding on every card)

1. The shell never replaces a region's HTML on a timer. A view is built once per route; after that, updates set text and attributes on keyed nodes (`data-k="<machine>/<session>"`), and add or remove whole rows only when the set changes.
2. Every row is exactly 44 px (56 on the phone). Text that can be long is one line with an ellipsis.
3. Every place that sometimes shows a status has a reserved line (18 px) that is empty when there is nothing to say.
4. Relative times update their own text node.
5. Rows do not reorder on refresh. A row changes group only when its state changes.
6. Loading uses the same box as loaded. No spinners that change height.
7. Notices go in the status strip. Nothing is inserted above content.
8. `scrollbar-gutter: stable` on every scrolling region.
9. Focus, selection and scroll position are never touched by a refresh.

The test that pins it (part of card R8, and run by every later card): the look fixture with live polling on, session lines and timestamps changing every 2 s, for 60 s on Home, Sessions, Session and Machine at both sizes; sample `getBoundingClientRect()` of every `[data-stable]` element (header, strip, section heads, first ten rows, composer, terminal pane) every 500 ms; assert zero change in x, y, width, height, and a `layout-shift` PerformanceObserver total of 0.

---

## 5. Build cards

All cards: `Clone /work/git/wingthing.git into wingthing, check out 537b462e0ee577587af78061b4b4afe7b61eff90`. Gates as in Q10: `make web`, `go vet -p 2 ./...`, `make test-launch-plan`, `git diff --check`. Screens come from the existing harness `test/web/look.mjs` (fixture roost; 1440 × 900 and 390 × 844). Each card reads this file and the mockups first.

To let lanes run side by side, each card owns files. `paper.js` is the collision point, so R1 lands first and splits it; everything else builds on R1's commit.

### Order

| Wave | Cards | Result |
|---|---|---|
| 0 (first, alone, about an hour) | R1 | Shell split into files, tokens in, route table, old app unreachable |
| 1 (parallel on R1) | R2, R3, R4, R5, R6, R9 | **v1: one product.** Terminal in the shell, calm states, counts agree, unlock prompt, site pages |
| 2 (parallel, after R2 and R4 merge) | R7, R8, R10, R11 | Sign-in methods, no-jump proof, inline New session, phone |

If only three land by morning, they must be R1, R2 and R3; that alone removes "I'm lost".

### R1 `wt-web-one-shell-1109`: the shell never hides

- **Why:** five actions leave the new shell for the old app (table in section 0).
- **Deliver:** split `web/src/paper.js` into `shell/frame.js` (sidebar, header, strip, router), `shell/select.js` (the only place sessions, machines, needs-you and counts are computed), and one file per view under `shell/views/` with today's markup moved unchanged; add `web/src/tokens.css` from the mockup's `:root`; implement the route table and redirects of section 2; `showHome` and unknown hashes resolve to `#`; the old `#sidebar`, `#main-header` and `#home-section` are never displayed; the palette's success path and `render.js:212`, `notify.js:102`, `workers.go:1070` use `#session/<machine>/<id>`; sidebar gets the New session button and loses the date and email.
- **Owns:** `web/src/shell/**`, `tokens.css`, `nav.js`, `main.js` routing, `session-route.js`.
- **Acceptance:**
  1. Look run visits `#`, `#sessions`, `#machines`, `#account`, `#new`, `#s/redirect?wing=studio`, `#chat/redirect?wing=studio`, `#w/studio`, `#bogus` at both sizes: after each, `#paper` is visible, `.pp-side` (or the phone tabs) is in the viewport, and `#sidebar`, `#main-header`, `#home-section` have zero rendered area.
  2. Starting a session through the fixture ends with `location.hash` matching `^#session/` and the shell visible.
  3. `grep -rn "#s/" web/src internal/relay --include=*.js --include=*.go` shows only the redirect parser and its tests.

### R2 `wt-web-session-terminal-in-shell-1109`: the live terminal is a pane

- **Why:** the terminal that works lives in the old app; the new session page has a preview that the server always refuses.
- **Deliver:** `shell/views/session.js` per 3.3: mount `DOM.terminalSection`'s xterm in the terminal column (the re-parenting pattern at `paper.js:521-528`), attach on open, detach on leave; the seven pane states with the copy in 3.3; read-only snapshot via `session_action`; Messages rail with collapse; header actions Files and ⋯ (Rename, Copy link, End session) wired to the existing handlers; xterm theme from tokens in `terminal.js` and `canvas.js`; Full screen.
- **Owns:** `shell/views/session.js`, `session.css`, `terminal.js` theme, `pty.js` mount points.
- **Acceptance:**
  1. Fixture session at 1440: an `.xterm` is inside the shell's main column, the sidebar is visible, and typed fixture input reaches the fixture PTY; at 390 the Terminal | Messages switch exists and the pane is within the viewport.
  2. Fixture variants (offline machine, ended session, input held elsewhere) each show the bar text from the table in 3.3, and the pane's bounding box is identical across all of them.
  3. `grep -rn "Preview unavailable\|2B2420\|EFE6D6" web/src` returns nothing, and a Go test asserts every `session.control` operation named in `web/src` is accepted by `BrowserSessionControl` or handled at `internal/tunnel/tunnel.go:422`.

### R3 `wt-web-type-tokens-voice-1109`: one look, one voice

- **Why:** the app names three fonts it never loads and paints Helvetica; chips are 12 px small caps; labels mix three casings; the brand loses its colour.
- **Deliver:** all shell and legacy-engine CSS reads `tokens.css`; remove the literal Inter, Space Grotesk and IBM Plex names; state chip component per the mockup (`.st`), 44 px rows, radii 6 and 10; orange only for needs-you, grey for offline and locked; zero counts not drawn; sentence-case sweep of every string under `web/src` and `web/index.html` that a user can see; strip `.local` from hostnames; row title falls back to the project when the name equals the agent.
- **Owns:** `paper.css` → `shell/*.css`, `style.css` variables block, string literals only in other files.
- **Acceptance:**
  1. In look captures of all shell views at both sizes, every first font family in computed styles is `system-ui` or a family with a loaded face, and `grep -rn "Space Grotesk\|IBM Plex\|'Inter'" web` is empty.
  2. A test collects the visible text of every button, link, heading and status element across the captured views and fails on any starting with a lowercase letter (allow-list: `wt`, code), and on any "...".
  3. With an offline fixture machine, no element on Home, Sessions or Machines has a computed colour or border colour equal to the orange or red token.

### R4 `wt-web-home-first-run-1109`: Home tells a new user what to do

- **Why:** steps 4 to 6 of the journey; the false "Wing has not reported yet"; the red zero.
- **Deliver:** `shell/views/home.js` per 3.1 with all five states; first-run steps that tick from live data with a 3 s poll only while in that state; needs-you cards; Working and Recent rows; wing composer only when a wing exists; status-strip sentences.
- **Owns:** `shell/views/home.js`, `home.css`.
- **Acceptance:**
  1. Fixtures for each of the five states at both sizes: the heading text and primary button match 3.1, and the page text never contains "has not reported" or a bare "0".
  2. Zero-machine fixture: when the fixture connects a machine, step 2 shows the tick and the machine's name within 5 s without a reload.
  3. One blocked fixture session: exactly one needs-you card with a button named "Answer" whose click ends on that session's page with focus inside the terminal pane.

### R5 `wt-web-machines-spaces-counts-1109`: machines, spaces, and numbers that agree

- **Why:** "Sessions 1" beside "Sessions 0"; the machine page is old markup; Bryan's home/work split; Q10's held card 9.
- **Deliver:** `/api/app/wings` returns `space_name` and `space_kind` (Q10 card 9); `shell/select.js` is the only source of counts and is space-aware; space filter in the sidebar (hidden with one space, remembered); `shell/views/machines.js` grouped by space; `shell/views/machine.js` per 3.5 replacing the re-parented old page, with Details, access list and past sessions folded and wired to the existing requests; a session ended from the UI shows a one-line error if the kill fails.
- **Owns:** `shell/views/machines.js`, `machine.js`, `shell/select.js`, the relay handler for `/api/app/wings`.
- **Acceptance:**
  1. Fixture with two personal machines and one team machine, at both sizes: two headed groups, no UUID on screen; choosing "Personal" removes every team row from Home, Sessions and Machines.
  2. For every machine in the fixture, the session count in the sidebar group, on the Machines row, in the Machine page heading and the number of rows listed there are equal; and the Sessions nav count equals the rows on `#sessions`. Checked again after the fixture adds and ends a session.
  3. `grep -rn "sessionsData.filter" web/src` has hits only in `shell/select.js` and tests.

### R6 `wt-web-unlock-prompt-1109`: one unlock prompt that remembers

- **Why:** three bare "authenticate" designs, a grant that dies with the tab, no explanation, no way forward without a passkey.
- **Deliver:** `shell/unlock.js` component per 3.7 with its five outcomes, used by the session pane, Machine page and New session; remove `#passkey-overlay`, the `wd-lock-banner` buttons and the legacy card badges; grants in `localStorage` keyed by user and machine with expiry, cleared on log out; no code path opens a WebAuthn sheet without a click on the prompt's button; "Add a passkey and unlock" chains registration then unlock.
- **Owns:** `shell/unlock.js`, `unlock.css`, `tunnel.js` grant storage.
- **Acceptance:**
  1. Playwright with a virtual authenticator and a locked fixture machine, at both sizes: the prompt shows the title "Unlock <name>" and one primary button; after clicking, the terminal attaches with no second click; a new tab on the same machine shows no prompt.
  2. Same fixture, account without a passkey: the button reads "Add a passkey and unlock", and after the flow the account lists one passkey and the pane is attached.
  3. A test loads every shell route with a locked fixture machine and no click: `navigator.credentials.get` and `.create` (stubbed to count) are called zero times in 30 s.

### R7 `wt-web-signin-methods-1109`: sign-in page and sign-in methods

- **Why:** login is lowercase and GitHub-first; passkeys are listed with a lowercase "remove" and no names; Bryan wants passkeys to be good.
- **Deliver:** `login.html` per 3.6 (order, copy, `autocomplete="username webauthn"`, passkey button shown only when the relay advertises passkey sign-in, no sheet on load); Account "Sign-in methods" list per 3.6 with add, inline rename, remove with the last-method and machine-requires warnings; the rest of Account in sentence case. Uses Q10 card 11's identities list when present and degrades to provider plus passkeys otherwise.
- **Owns:** `internal/relay/templates/login.html`, `shell/views/account.js` (moved from `render.js:520-860`).
- **Acceptance:**
  1. Login captured at both sizes: first heading "Sign in"; the email input has `autocomplete` containing `webauthn`; zero credential calls on load.
  2. Virtual authenticator: clicking "Add a passkey" once yields a row starting "Passkey ·" with a device name, without a reload; renaming it persists after reload.
  3. With one sign-in method, its Remove control is disabled and the text "This is your only way in" is present; with two, removal succeeds and the list has one row.

### R8 `wt-web-no-jump-1109`: nothing moves on refresh

- **Why:** "the UI is jumping around periodically".
- **Deliver:** replace the per-second `innerHTML` commit (`paper.js:534-545,586`) with keyed in-place updates per section 4; reserved status lines; `scrollbar-gutter`; remove every timer that re-runs search or re-renders a list (`session-search.js:126`, `coordinator-home.js:174`); add the stability test of section 4 to the look harness as `stable.mjs`, and `data-stable` marks on the listed elements.
- **Owns:** `shell/frame.js` commit step, `test/web/stable.mjs`.
- **Acceptance:**
  1. `stable.mjs` at both sizes on Home, Sessions, Session and Machine: 60 s, 120 samples, zero change in any `[data-stable]` box, layout-shift total 0, while the fixture changes lines and times every 2 s.
  2. During that run, a focused composer keeps focus and caret position, and a scrolled list keeps `scrollTop`.
  3. `grep -rn "setInterval" web/src/shell` shows only the data poll and the relative-time ticker.

### R9 `wt-site-first-impression-1109`: homepage, install

- **Why:** journey steps 1 and 2: the overwhelming headline, and an install page that teaches a path with no account.
- **Deliver:** `home.html` hero and details per the earlier answer (sections 2 and card 7 there) with the fixture story in the preview; `install.html` per 3.9; `base.html` smoothing, nav font, footer; `cmd/wt/wing.go:208` prints the canonical address; `wingthing.ai/app/` redirects to it.
- **Owns:** `internal/relay/templates/home.html`, `install.html`, `base.html`, the one CLI string, the redirect.
- **Acceptance:**
  1. Homepage at 1440: `.hero h1` font-size ≤ 64 px, letter-spacing ≥ −1.6 px, height ≤ 150 px; adjacent glyph boxes on a line never overlap by more than 0.5 px; at 390 no horizontal overflow.
  2. Install page text contains, in order, the install line, `wt login`, `wt start`, and a link to the app; `wt egg` appears only inside a `details` element.
  3. Neither page contains "roost", "entitled" or "allowed wing"; `GET /app/` on the site host returns a redirect to the app host.

### R10 `wt-web-new-session-inline-1109`: start a session on one page

- **Why:** journey steps 7 and 8: agent choice hidden behind shift+tab in an old dialog; no shell.
- **Deliver:** `shell/views/new.js` per 3.4 using the palette's listing and launch calls; Shell as an agent option where the machine supports it; remove the palette from this flow.
- **Owns:** `shell/views/new.js`, `new.css`.
- **Acceptance:**
  1. Fixture at both sizes: machine, agent and project are all chosen with visible controls on one page, and Start ends on the new session's page with the terminal attached.
  2. With an offline fixture machine selected by URL, Start is disabled and the page names the machine as offline.
  3. `#command-palette` is never displayed during the flow.

### R11 `wt-web-phone-pass-1109`: the phone is the same product

- **Why:** journey step 17.
- **Deliver:** 3.8: four bottom tabs, Home needs-you first, Session's Terminal | Messages switch with the key bar, 56 px rows, space chip.
- **Owns:** phone blocks in `shell/*.css`, `shell/views/session.js` phone branch (after R2 merges).
- **Acceptance:**
  1. Every shell route at 390 × 844: no horizontal overflow, bottom tabs within the viewport, every tappable control at least 44 px tall.
  2. Blocked fixture session: from Home, two taps (Answer, then a key on the key bar) deliver input to the fixture PTY.
  3. `stable.mjs` passes at 390.

---

## 6. Not for a lane

- The demo account: a personal account with labelled machines and no Slide identity. The shell stops printing the email (R1), which helps but does not replace this.
- Recovering Bryan's own sign-in tonight (Q10 card 11's note).
- Whether the default unlock duration becomes 12 hours: Bryan's call.
- Canvas ("multi-session"): unlinked in v1, not deleted. Decide later whether it returns as a view in the shell.

## 7. Unverified

- The live terminal page, machine page, login, install and phone as rendered (code only).
- How Bryan reached the old home tonight; the code paths that can show it are `showHome` from the old sidebar and any hash the shell does not own (`main.js:98`, `paper.js:500-504`). R1 closes all of them regardless.
- Which side of the "Sessions 1 / 0" pair was stale.
- That the fixture harness can drive a WebAuthn virtual authenticator against a locked fixture machine without relay changes (R6 and R7 assume the team lead's mechanics card provides it).
- That splitting `paper.js` in R1 takes about an hour; if it runs long, waves 1 and 2 slip with it. R9 touches only the site templates and can start at once, alongside R1.
