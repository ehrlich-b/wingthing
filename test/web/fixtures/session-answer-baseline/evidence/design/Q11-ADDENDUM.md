# Q11 addendum: what the first answer missed (11/09, after 9 PM)

Read this with two files already in this folder:

- `Q11-WINGTHING-REDESIGN.md`: the journey table, the route table, every screen, the no-jump rules and cards R1 to R11. I wrote it after the 8:20 PM scope change; it covers asks 1 to 5 below in full. This addendum does not repeat it.
- `Q11-wingthing-redesign-mockups.html`: static pictures and the token block.

**One correction to that redesign, from Bryan at 9 PM.** I had made the terminal the main column and attached it live by default. He wants the page he was looking at: **Messages on the left, Terminal on the right, Take over and Hand back.** "Fix it, just make it work, don't break this." Section 1 below replaces section 3.3 and card R2 of the redesign, and mockup 3 (which shows the columns the other way round; its pane states in mockup 4 still apply).

**Filing.** The eight cards from the first answer and R1 to R11 overlap. To avoid building things twice, this is how they line up; the right-hand column is what to file from this addendum.

| First answer | Redesign | Addendum |
|---|---|---|
| 1 hero type, 7 homepage details | R9 | folded into A1 |
| 2 app fonts, 6 chips and voice | R3 | keep the filed cards |
| 3 preview works, 4 one session page | R2 | **A6 replaces both**; mechanics are `wt-session-page-real-loop-1109` |
| 5 Home says what is true | R4 | folded into A1 |
| 8 home and work in the shell | R5 | keep; A4 adds the states |
| — | R1 | **A2** |
| — | R8 | **A3** |
| — | R6, R7 | **A5** (screens only; mechanics are `wt-passkey-flow-works-1109`) |
| — | R10, R11 | file later from the redesign |
| — | — | **A7** app icon |

Seven cards, not six: one per ask, one for the session page, one for the icon.

Labels: **[E]** read in code or seen live, **[I]** inference, **[H]** hunch. Source: `~/repos/wingthing` at `537b462e`.

---

## 1. The session page is the hero

Keep exactly this shape (`paper.js:301-313`, the page in Bryan's screenshot):

```
 fix-login-redirect                      Claude · Studio · wingthing   ● Needs you
 ← Sessions
┌ Messages ─────────────────────────┬ Terminal ───────────── Read-only ┐
│ You      7:41  Fix the redirect…  │ ┌──────────────────────────────┐ │
│ Claude   7:58  Found it: the …    │ │  the agent's real screen,     │ │
│ Claude   8:02  Ready to deploy.   │ │  colours kept, updating       │ │
│ ┌ Needs you ────────────────────┐ │ └──────────────────────────────┘ │
│ │ wants to run ./deploy.sh      │ │ [ Take over ]                    │
│ └───────────────────────────────┘ │ Your keys go to the session until │
│ [ Message fix-login-redirect ][Send]│ you hand it back.               │
└───────────────────────────────────┴──────────────────────────────────┘
```

What it is, in the words for the homepage: **your agent's terminal session, turned into a chat you can read and answer from anywhere, for any agent.** The chat is the same conversation that is in the terminal; the terminal is there so you can see it is real and step in.

### States, so it never looks broken

Both columns always keep their size. Each state changes only the text inside.

| State | Messages column | Terminal column |
|---|---|---|
| **Reading** (first second) | Three grey placeholder rows, same height as real ones | Pane at full size, one grey line "Connecting to Studio…" |
| **Live** | Turns as they happen, including ones typed directly in the terminal. Composer on | Kicker "Read-only". The agent's screen, updating. **Take over** |
| **Agent is working** | Last turn, then a quiet line "Claude is working…" in the reserved slot under the composer. Composer stays on; the text queues ("Sends when it's ready") | Same as Live |
| **Waiting on you** | The needs-you card is the last item: what it asks, the command if known, **Answer in the terminal** (takes over and focuses the pane). Composer on, for a typed answer | Kicker "Read-only · waiting for you". Pane border orange |
| **You have control** | Composer off, one line: "Messages wait while you have control." | Kicker "You're typing". Border blue. Button reads **Hand back** |
| **No readable transcript** | One sentence, no empty-state art: "wingthing can't read this agent's conversation, so there's no chat here. The terminal on the right is the whole session." Composer off, placeholder "Use the terminal" (the client already knows this case: `paper.js:227`, presentation state `unsupported`) | Unchanged; Take over is the way to talk to it |
| **Nothing said yet** | "No messages yet. What you send goes to the agent's prompt." | Live |
| **Machine offline** | Messages stay, dimmed. Composer off: "Studio is offline." | Kicker "Last screen, 6:12 PM". Cached screen at 55%. **Retry**. Under it: "The session is kept and will be here when Studio reconnects." |
| **Machine locked** | Messages hidden behind nothing; column shows "Unlock Studio to read this session." | The unlock prompt (section 4), inside the pane |
| **Session ended** | Messages stay. Composer off: "This session ended at 5:40 PM." | Kicker "Ended". Last screen at 55%. **New session here** |
| **Older wt on the machine** | As normal if readable | "Update wt on Studio to see its terminal here: `wt update`" |

Strings that go away: "Preview unavailable from this machine.", "Agent has control", the raw request id under the composer.

Rules for the two halves:

- The terminal pane shows the screen with its colours (an xterm fed the snapshot or the live stream), not a stripped 18-line text tail (`paper.js:134,311`).
- **Take over** swaps the same pane to the live terminal in place; the sidebar and Messages stay. **Hand back** returns it to read-only. Neither navigates. The old `#s/` page is not involved.
- Messages and terminal never disagree about order: a turn typed during control appears in Messages after Hand back, in place.
- On the phone: a **Messages | Terminal** switch under the header, Messages first, Terminal first when it is waiting on you.

### First thing a new user reaches

Start session → this page, immediately, with the Reading state, then the agent's first screen on the right and "No messages yet" on the left. Not the old terminal page, not the Sessions list.

### Centrepiece of the homepage

Replace the "Work, with direction." illustration (`home.html:117-131`) with this page: a still of the Waiting-on-you state built from the look fixture's story, linked to the short capture (`web/public/demo/homepage.webm` and its poster already exist). Caption: "Any agent's terminal, as a chat. Read it, answer it, or take the keys."

---

## 2. First-time journey: the breaks

The full 17-step table is in the redesign, section 1. The six that stop a new person, with the fix each gets:

| Step | Break | Evidence | Fixed by |
|---|---|---|---|
| Install page | Teaches `wt egg claude` with "No Wingthing account or daemon is required"; a reader finishes with nothing in the app | `install.html:36-60` | A1 |
| Empty app | Shows the commands but only notices the new machine on a 30 s poll and says nothing while waiting | `paper.js:174-176`, `main.js:418` | A1 |
| Start a session | Enter drops into the old app | `render.js:212` | A2, A6 |
| Passkey gate | Three bare "authenticate" buttons; the grant dies with the tab | `index.html:178-180`, `render.js:1477`, `paper.js:191-192`, `tunnel.js:57-60` | A5 |
| Back to "home" | Old home with a self-running search and outage cards | `session-search.js:22,38,62,71,126` | A2, A4 |
| Watch | The preview call is one the server refuses | `paper.js:133`, `conversation_browser.go:34` | A6 |

Jargon a new account can currently meet, all to be gone after A1 and A2: wing (for a machine), egg, roost, coordinator, parent, execution, "entitled hosted account", "allowed wing", "typed agent manager", "direct transport", "relay".

Not walked live: install, login, the terminal page, the phone (code only). I started no session.

---

## 3. What the old UI did, and where each thing goes

Every item reachable in the old app (`web/index.html`, `render.js`, `nav.js`), with a decision. "Moved" means it exists in the shell after A2 or the named card; "dropped" means no route reaches it.

| Old UI | Decision |
|---|---|
| Sidebar: "~ home", "+ new session", session tabs | Dropped. Shell sidebar; Sessions list |
| Header: `wingthing.ai` logo, title, status, lock emoji, user name | Dropped. Shell header and status strip |
| Live terminal page `#s/` | Moved: the terminal pane after Take over (A6). Route redirects |
| Terminal header "copy" | Moved: "Copy" above the pane |
| "files" panel (add, download, copy to export folder) | Moved: **Files** in the session header, same panel restyled |
| "preview" side panel (a web preview iframe) | Moved: item in the session's ⋯ menu; opens in the terminal column |
| "x" end session | Moved: ⋯ → End session, with a one-line confirm |
| Passkey overlay, "type" overlay, modifier keys | Passkey → unlock prompt (A5). Keys bar stays on the phone only |
| "restoring session..." replay bar | Moved into the pane as its Connecting state |
| Chat page `#chat/` with Conversation and Activity tabs | Conversation is the Messages column. Activity: dropped for v1 |
| Session viewer dialog (transcript, snapshot, take control) | Dropped; it is the session page |
| "Search sessions everywhere" block | Moved: Sessions → "Search everywhere", on a click only (A4) |
| "Computers and connection status" | Dropped; Machines page |
| Coordinator card, "Coordinators" list, "New coordinator" | Moved: the wing rail on Home. Extra coordinators list: dropped for v1 |
| Session inventory with status filter and tiles ("no preview") | Dropped; Sessions groups |
| Fork a session | Moved: ⋯ → Fork, if the handler is kept; otherwise dropped for v1 [H] |
| "multi-session" canvas | Unlinked; `#canvas` still opens by URL. Decide later |
| Audit replay overlay | Moved: Machine → Past sessions → Replay, same overlay with shell colours |
| "Use Wingthing on your phone", install-app button, "Refresh agents" | Moved: one line in Account, "Install on this phone" |
| Share / reach panel | Moved: Machine → "Who can use this machine" |
| "your typed agent manager is ready" panel (MCP commands) | Moved: Home's no-browser-access state, two sentences and the two commands |
| Machine page (rename, update, access list, folder access, history, remove) | Moved: shell Machine page, three folded sections |
| Command palette | Kept as the project picker inside New session until the inline page ships |
| Reconnect banner, "Disconnected" banner | Moved: the status strip |
| Parent dot | Dropped; Home's needs-you count carries it |

---

## 4. Calm, accurate offline and identity states

**The rule: a state is drawn where it applies, in the size of what it affects, in grey unless it needs the person.** One offline machine is one grey row, not a card across the page.

| Situation | What the client knows | Where it shows | Words | Action |
|---|---|---|---|---|
| Machine offline | `online === false` | its row, its page, a session's terminal pane | "Offline · last seen 6:12 PM" | Retry |
| Machine connected, not yet checked | no reply yet | row | "Checking…" for up to 5 s, then the real state | — |
| **This account isn't on the machine's list** (web sign-in and the machine's `wt login` are different accounts, or the owner hasn't added you) | `tunnel_error === 'not_allowed'` (`paper-state.js:8`) | row, machine page | "Different account. Studio is signed in to wingthing as someone else, or hasn't added you." | "How to fix" opens two lines: on Studio run `wt login` as this account, or ask its owner to add `<your email>` |
| Machine's key changed | `key_mismatch` | row, page | "Reconnected with a new key." | Retry (refreshes it) |
| Locked | passkey states | row "Locked"; prompt only where you try to open it | section 5 | Unlock |
| No machines connected at all | all offline | status strip | "No machines are connected. Sessions are kept." | Retry |
| Search couldn't reach a machine | per-source failure | one grey line under results | "Didn't search build-1 (offline)." | — |
| This browser is offline | network | status strip | "You're offline. Showing what was last loaded." | — |
| Signed in, browser access not on this plan | `relay_allowed === false` | Home | what's included, the two commands, link to plans | — |

Never: "Search unavailable", "Computer offline" as a headline, "Last successful check: Unknown", "coverage incomplete", "Connect the unavailable computers, then retry", a UUID, orange or red on any of the above. Orange is for "needs you"; red is for an action that failed.

Identity on screen: the footer shows the display name; Account shows one line, "Signed in as Sam · sam@example.com", so when a machine says "Different account" the person can see which one they are.

---

## 5. Passkey screens

Mechanics are in `wt-passkey-flow-works-1109`. These are the pictures and words (mockups 4 and 6).

**Add (Account → Sign-in methods).** One button, **Add a passkey**. Click → the browser's sheet → a new row appears: "Passkey · MacBook, Chrome" with "Added today · also unlocks your machines". The name is editable in the row. Cancel: "Cancelled." in the reserved line. Unsupported browser: the button is replaced by "This browser can't create passkeys."

**Sign in.** Page title "Sign in", sub "to wingthing". Order: **Continue with Google**, **Sign in with a passkey**, "or", email field and **Email me a link**. The email field has `autocomplete="username webauthn"` so a saved passkey is offered in autofill. The page opens no sheet by itself. Failure under the button: "That passkey didn't match an account. Try another way."

**Step-up, the prompt that replaces "authenticate".** One component, inside the thing it blocks (terminal pane, machine page, New session). Never on page load, never from a timer, never full-screen.

> **Unlock Studio**
> Studio asks for your passkey before it shows terminals. It stays unlocked in this browser for 12 hours.
> **[ Unlock with passkey ]**
> Or attach from Studio itself: `wt attach fix-login-redirect`

| Outcome | Prompt shows |
|---|---|
| Success | Disappears; the pane carries on without another click |
| Cancelled | "Cancelled. Try again when you're ready." |
| Not accepted | "That passkey isn't one Studio accepts." |
| No passkey on the account | "Studio asks for a passkey, and you don't have one yet. Adding one takes a few seconds." **Add a passkey and unlock**. Alt line: "Or turn the lock off on Studio: `wt wing unlock`" (`cmd/wt/wing.go:881`) |
| Not on the machine's list | The "Different account" words from section 4; no button |

Remembered per user and machine across tabs until it expires or you log out; the hours shown come from the machine's setting (default is one hour today, `internal/config/wing.go:37`; 12 is my recommendation and Bryan's call [H]).

**Remove.** In the row: "Remove this passkey? Remove · Keep". Last sign-in method: button disabled, "This is your only way in. Add another first." Last passkey while a machine requires one: "Studio will ask for a passkey you no longer have. Remove anyway?"

---

## 6. App icon

Today: the web has `web/public/icons/wt.svg` (the "wt" letterforms, blue on navy, cropped to a 336-unit view box). iOS has no asset catalog at all (`mobile/ios/WingthingApp/` holds only `Info.plist` and `WingthingApp.swift`; the project file has no `xcassets` reference). Android points at one drawable, `@drawable/wingthing_icon`, with no adaptive icon (`AndroidManifest.xml:9`). [E]

**The mark.** The existing "wt" letterforms, unchanged in shape. One source file, `brand/mark.svg`, 1024-unit square:

- Background `#07131A`, full bleed, square corners (each platform applies its own mask), **no alpha anywhere**.
- "w" in `#2AA8FF`, "t" in `#FF8A3D`. Blue back, orange front, which is the kingfisher; it also keeps the icon from being one more blue square on a phone. [H; the one-colour version is the fallback if Bryan prefers it]
- The pair is centred optically and is 64% of the canvas wide. At 29 px that is about 18.5 px wide with strokes close to 2 px, so both letters stay distinct. No wordmark, no gradient, no shadow, no outline.

**Outputs, all generated from that one file by a checked-in script:**

| Target | File | Notes |
|---|---|---|
| iOS | `WingthingApp/Assets.xcassets/AppIcon.appiconset/icon-1024.png` plus `Contents.json` (single-size universal) | 1024 × 1024, sRGB, opaque. Build setting `ASSETCATALOG_COMPILER_APPICON_NAME = AppIcon` |
| Android | `mipmap-anydpi-v26/ic_launcher.xml` and `ic_launcher_round.xml`: background colour `#07131A`, foreground vector with the pair at 52% of the 108 dp canvas (inside the 66 dp safe circle), and a monochrome layer for themed icons | Manifest `android:icon="@mipmap/ic_launcher"`, `android:roundIcon` |
| Web | `icons/wt.svg`, `wt-180.png`, `wt-192.png`, `wt-512.png`, `favicon.ico` (16, 32, 48) | The maskable 192 and 512 use the 52% size |
| Site | The nav logo mark in `base.html:54` | Same two colours |

---

## 7. Cards

Every card: `Clone /work/git/wingthing.git into wingthing, check out 537b462e0ee577587af78061b4b4afe7b61eff90`. Gates as in Q10: `make web`, `go vet -p 2 ./...`, `make test-launch-plan`, `git diff --check`. Screens come from `test/web/look.mjs` at 1440 × 900 and 390 × 844. Each card reads this file, the redesign and the mockups first.

Order: **A2 first** (it moves files the others touch); A1's site half and A7 can run beside it; then A6, A4, A5, A3 in parallel.

### A1 `wt-onboarding-no-dead-ends-1109`: a new account always has a next step

- **Why:** a first-time user meets an install page for a different path, an empty app that doesn't notice their machine, and jargon (section 2).
- **Deliver:** `install.html`: "Install wingthing", three steps (install, `wt login`, `wt start`), "Open the app", with `wt egg` under a fold. Homepage hero type per the first answer and the session page as its picture (section 1). Home's first-run steps (redesign 3.1, mockup 2) that tick themselves on a 3 s poll while shown; the five Home states; no "Wing has not reported yet", no zero in red. One address printed by `cmd/wt/wing.go:208` and linked by the site.
- **Acceptance:**
  1. A fixture account goes zero machines → machine connects → session starts, at both sizes, with no reload: after each change the page shows exactly one primary button, and the last step ends on `#session/…`.
  2. The text of the homepage, install page and every shell view in those captures contains none of: "egg", "roost", "coordinator", "execution", "entitled", "allowed wing", "typed agent manager", "relay", and "wing" only as the brand or the pinned agent's name.
  3. Homepage `.hero h1` at 1440: font-size ≤ 64 px, letter-spacing ≥ −1.6 px, no two adjacent glyph boxes on a line overlap by more than 0.5 px; no horizontal overflow at 390.

### A2 `wt-web-retire-old-ui-1109`: every route lands in the shell

- **Why:** Bryan reached the old home from `#account` after using a session: "what in the ass is this." Five actions cross into the old app (redesign, section 0).
- **Deliver:** redesign card R1 (split `paper.js` into `web/src/shell/`, `tokens.css`, the route table and redirects, old `#sidebar`, `#main-header`, `#home-section` never displayed, links built as `#session/<machine>/<id>`), plus the table in section 3 of this file as the checklist: each "Moved" row has its entry point in the shell; each "Dropped" row has no reachable route. Remove the timers that belong only to the old home (`session-search.js:126`, `coordinator-home.js:174`, `parent-dot.js:105`).
- **Acceptance:**
  1. A crawl at both sizes visits every route in the redesign's route and redirect tables plus `#bogus`, and from each view clicks every link and button the fixture allows (session start, take over, hand back, end, account, back): after every step the shell's sidebar or phone tabs are in the viewport and `#sidebar`, `#main-header`, `#home-section` have zero rendered area.
  2. A checked-in list, `web/test/old-ui-inventory.json`, has one entry per row of the section 3 table with `moved` (a selector that the crawl finds) or `dropped` (a string that the crawl never finds); the test fails on any miss.
  3. None of these strings appears in any captured view: "Search sessions everywhere", "Search unavailable", "Computer offline", "no preview", "~ home", "multi-session".

### A3 `wt-web-no-layout-shift-1109`: nothing moves on refresh

- **Why:** "the UI is jumping around periodically."
- **Deliver:** redesign section 4 and card R8: keyed in-place updates instead of rebuilding the main column each second (`paper.js:534-545,586`); fixed 44 px rows (56 on the phone); a reserved line wherever a status can appear; notices only in the status strip; `scrollbar-gutter: stable`; `data-stable` on header, strip, section heads, the first ten rows, the composer and the terminal pane; the test `test/web/stable.mjs`.
- **Acceptance:**
  1. `stable.mjs` on Home, Sessions, Session and Machine at both sizes, 60 s with the fixture changing lines, states' timestamps and one machine's connection every 2 s: 120 samples of every `[data-stable]` bounding box show zero change in x, y, width and height, and the `layout-shift` observer total is 0.
  2. During the run a focused composer keeps focus and caret, and a scrolled list keeps its `scrollTop`.
  3. When the fixture adds a session mid-run, only rows below the insertion point move, and the test records that single expected move and no other.

### A4 `wt-web-calm-states-1109`: offline and identity states say what is true, quietly

- **Why:** a different-account mismatch rendered as a page-wide orange outage card.
- **Deliver:** the table in section 4: one state per machine drawn on its row, page and pane with those words; "Different account" with its two-line fix; grey for offline, locked, checking and different-account; search-everywhere on a click with one grey line per unreached machine; status-strip sentences; "Signed in as" line in Account.
- **Acceptance:**
  1. Fixtures for offline, not-allowed, key-changed, locked and all-offline at both sizes: each machine's row shows the words from section 4, and no element in the view has a computed text, border or background colour equal to the orange or red token.
  2. In the not-allowed fixture the page contains "Different account" and the user's own email inside the "How to fix" text, and contains none of "unavailable", "Unknown", "coverage".
  3. No state element in those fixtures is taller than one row (44 px; 56 on the phone) outside the terminal pane.

### A5 `wt-web-passkey-screens-1109`: add, sign in, unlock, remove

- **Why:** "the passkey flow would be sweet if it actually worked"; today it is three bare buttons and a lowercase list.
- **Deliver:** section 5, screens and copy only, wired to the flows from `wt-passkey-flow-works-1109`: Sign-in methods list with add, inline rename and remove; `login.html` in the order given with the autofill attribute and no sheet on load (the passkey button shows only when the relay advertises passkey sign-in); the single unlock component with its five outcomes, used in the terminal pane, Machine page and New session; removal of `#passkey-overlay`, the `wd-lock-banner` buttons and the legacy "authenticate" badges.
- **Acceptance:**
  1. With a virtual authenticator at both sizes: one click on "Add a passkey" yields a row starting "Passkey ·" without a reload; on a locked fixture machine the prompt's title is "Unlock <name>", it has exactly one primary button, and after it succeeds the pane continues with no second click.
  2. Loading every shell route and the login page with a locked fixture machine and no click for 30 s calls `navigator.credentials.get` and `.create` zero times; the login email input's `autocomplete` contains `webauthn`.
  3. With one sign-in method its Remove control is disabled beside "This is your only way in"; the words "authenticate" and "Authenticate" appear nowhere in `web/src` or the templates' visible text.

### A6 `wt-web-session-page-states-1109`: the session page never looks broken

- **Why:** Bryan: "this would actually be kinda chill if it worked… don't break this." Design and states only; the data path is `wt-session-page-real-loop-1109`.
- **Deliver:** section 1: keep Messages left and Terminal right; every state in the table with its words and fixed column sizes; the terminal pane as a coloured screen, read-only by default; Take over and Hand back in place with no navigation; needs-you card with "Answer in the terminal"; the no-readable-transcript sentence; phone switch; Start session lands here; app palette in `terminal.js` and `canvas.js`.
- **Acceptance:**
  1. One fixture per state in section 1 at both sizes: the Messages and Terminal columns have identical bounding boxes across all desktop states, and each state's kicker and sentence match the table.
  2. Take over then Hand back at 1440: `location.hash` and the sidebar are unchanged throughout, the button text goes Take over → Hand back → Take over, and the composer is disabled only in between.
  3. `grep -rn "Preview unavailable\|Agent has control\|2B2420\|EFE6D6" web/src` returns nothing, and starting a session through the fixture ends on this page.

### A7 `wt-brand-app-icon-1109`: one mark for iOS, Android and the web

- **Why:** Bryan wants the iOS app on TestFlight and it has no icon; Android has a single drawable; the web icons are a separate file.
- **Deliver:** section 6: `brand/mark.svg`; a checked-in script that renders every output from it; the iOS asset catalog and build setting; Android adaptive and monochrome launcher icons and manifest entries; regenerated web icons and favicon; the site nav mark in the same colours.
- **Acceptance:**
  1. The iOS icon is a 1024 × 1024 PNG with no alpha channel whose four corner pixels are `#07131A`; the project builds with `ASSETCATALOG_COMPILER_APPICON_NAME = AppIcon` and the built app bundle contains an app icon.
  2. Re-running the script produces byte-identical outputs to the checked-in files; in the 29 px rendering the mark's bounding box is at least 17 px wide and contains pixels within a small tolerance of both `#2AA8FF` and `#FF8A3D`.
  3. Android: `ic_launcher.xml` has background, foreground and monochrome layers and the foreground's drawn bounds lie inside the central 66 dp of 108; `manifest.json`, the favicon and the 180 px touch icon all come from the same script run.

---

## Unverified

- How the old home was reached from `#account`. The paths that can show it are `showHome` and any hash the shell does not own (`nav.js:52-79`, `main.js:98`, `paper.js:500-504`); A2 closes all of them and its crawl would catch another.
- That "not allowed" is what the client saw for Bryan's identity mismatch tonight; the old search card collapses several failures into "Search unavailable" (`session-search.js:38`), so the exact cause on his machine is not established.
- That the lane image has a tool to render SVG to PNG and ICO; if not, A7 checks in the outputs and the script documents the tool.
- The iOS project's build in a lane (needs macOS); A7's first acceptance item may have to run on the Mac.
- Fork, Activity and the web preview panel: I listed them from the markup and did not trace their handlers.
