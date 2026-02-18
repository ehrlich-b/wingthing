# Preview Panel: Live App Preview in Terminal View

**Status:** Idea / design sketch
**Date:** 2026-02-18

---

## The Idea

When a sandboxed agent builds a web app (or any URL-accessible artifact), it drops a well-known file inside the egg session directory. The wingthing frontend detects this file via the encrypted tunnel and renders the URL in an iframe panel alongside the terminal --- like Claude Desktop's "preview" tab, but for any agent, any app, running on your own machine.

The user never copies a URL. The user never opens a new tab. The dashboard appears next to the conversation that created it.

## Why This Matters

The Slide sandbox flow today:

```
1. Sales rep: "track my partner's orders"
2. Sonnet builds the app, deploys it
3. Sonnet says: "Your dashboard is at https://wingthing.slide.tech/apps/sarah/order-tracker/"
4. User copies URL, opens new tab, navigates there
```

With preview panel:

```
1. Sales rep: "track my partner's orders"
2. Sonnet builds the app, deploys it
3. Sonnet writes the well-known file
4. Dashboard appears RIGHT THERE in a split panel next to the terminal
```

Step 4 happens automatically. No copy-paste. No context switch. The user sees their app come to life in real time as the conversation produces it.

## The Well-Known File

### Location

```
<agent working directory>/.wt-preview
```

The agent writes this in its normal writable start directory (e.g. `~/sales/` in the Slide deployment). No sandbox changes needed --- it's already writable.

### Format: Two modes

The file contents determine what the preview panel shows:

**Mode 1: URL preview** --- file starts with `url:` prefix

```
url:https://wingthing.slide.tech/apps/sarah/order-tracker/
```

Frontend loads this in an iframe. The URL is displayed prominently with a copy button. This is the "here's your live dashboard" mode.

**Mode 2: Markdown preview** --- anything else

```markdown
# Backup Health Report

| Partner | Last Backup | Status |
|---------|-------------|--------|
| Acme Corp | 2 hours ago | OK |
| Initech | 14 hours ago | WARNING |

![chart](https://wingthing.slide.tech/apps/sarah/chart.png)
```

If the contents don't start with `url:`, they're treated as markdown. The frontend renders this in a sandboxed iframe --- markdown only, images allowed, **no raw HTML**. This lets agents show quick reports, tables, status summaries without deploying a whole web app.

**Why not JSON?** Because the agent can just `cat > .wt-preview` a markdown blob without worrying about escaping quotes in JSON. The `url:` prefix is unambiguous and trivial to detect.

### The consume-on-read trick

**The egg process watches for `.wt-preview` and consumes it (deletes the file) as soon as it reads it.** This is the key design choice:

1. Agent writes `.wt-preview` in its working directory
2. Egg detects the file (fsnotify or polling the working dir)
3. Egg reads the contents, deletes the file, forwards the preview data up to the wing
4. Wing sends it through the encrypted tunnel to the frontend
5. Frontend opens the preview panel (iframe for URLs, rendered markdown otherwise)

**The file disappearing IS the signal to the agent that the preview is showing.** The CLAUDE.md instructions say: "After you write `.wt-preview`, it will disappear --- that means the frontend picked it up and is displaying it."

This solves three problems at once:
- **No sandbox hole needed** --- agent writes in its normal writable directory
- **No git pollution** --- the file is gone before anyone could accidentally `git add` it
- **Clean signal** --- agent can check: file gone = preview is live

### Updating the preview

Agent writes `.wt-preview` again. Egg consumes it again. Frontend updates the panel. Same flow every time. Can switch between URL and markdown modes freely.

### Clearing the preview

Agent writes `.wt-preview` with just a blank line or empty content. Egg consumes it, sends null upstream, frontend closes the panel.

## Detection: Egg Watches the Working Directory

The egg process already manages the agent's working directory and PTY. Adding a file watch is natural:

1. **Egg watches** the agent's working directory for `.wt-preview` creation (fsnotify or tight poll --- the egg is local, this is cheap)
2. **Egg reads + deletes** the file atomically
3. **Egg sends** the preview data to the wing process via the existing egg<->wing channel (gRPC or direct, depending on session type)
4. **Wing forwards** through the encrypted tunnel to the frontend as a new inner message type:

| Inner type | Direction | Payload |
|-----------|-----------|---------|
| `preview.update` | wing -> browser | `{session_id, mode: "url", url: "..."}` or `{session_id, mode: "markdown", content: "..."}` or `{session_id, mode: null}` (close) |

Egg parses the file: starts with `url:` → mode "url" with the URL. Otherwise → mode "markdown" with the raw content. Empty/blank → mode null (close panel).

This is a push model, not polling. The egg watches locally (fast, no tunnel overhead), and only sends a message when something changes. The frontend never polls for previews --- it just listens for `preview.update` messages on the tunnel stream.

## Frontend UI

### Layout

When a preview URL is active, the terminal view splits:

```
+-------------------------------------------+
|  Terminal (xterm.js)  |  Preview panel     |
|                       |                    |
|  $ sonnet is typing   | [Order Tracker] X  |
|  ...                  | https://wingth...  |
|                       | [copy] [open]      |
|                       | +--------------+   |
|                       | | Dashboard    |   |
|                       | | content      |   |
|                       | | here         |   |
|                       | +--------------+   |
|                       |                    |
+-------------------------------------------+
```

### Preview panel header (prominent)

The header bar above the iframe is the **main thing the user interacts with**. It must make the URL obvious and copyable --- this is a preview of a real, permanent, shareable URL.

- **Title** (from `.wt-preview`): e.g. "Order Tracker"
- **Full URL displayed prominently**: `https://wingthing.slide.tech/apps/sarah/order-tracker/` --- not truncated, not hidden behind a tooltip. This is a real link they can share.
- **Copy button** right next to the URL. One click, URL in clipboard, brief "Copied!" confirmation. This is how they grab the link to send to a coworker, paste in Slack, bookmark, etc.
- **Open in new tab button** (external link icon). Opens the URL in a real browser tab.
- **Refresh button** to reload the iframe.
- **Collapse/close button** to dismiss the panel and go full-width terminal.

The copy button is the star. The whole point is: agent builds it, user sees it live, user copies the URL and runs off to show someone. The preview panel is a launchpad, not a cage.

### Other layout details

- Default split: 50/50 or 60/40 (terminal wider)
- Draggable divider to resize

### When no preview is active

Full-width terminal. No empty panel. No placeholder. The preview panel only appears when there's something to show.

### iframe considerations (URL mode)

- Same-origin if the app is on the same domain (wingthing.slide.tech) --- works perfectly
- Cross-origin apps need appropriate CORS/X-Frame-Options headers
- The agent-built apps (Node.js behind nginx) are same-origin by default --- this just works
- For localhost URLs during development, the wing could proxy through the tunnel

### Markdown rendering

- Use a markdown library (marked, markdown-it, etc.) to render to HTML
- Render into a sandboxed iframe: `<iframe sandbox="allow-same-origin">`  --- no `allow-scripts`
- **Images allowed** --- agents can reference charts, screenshots, generated PNGs
- **No raw HTML passthrough** --- strip all HTML tags from the markdown before rendering. Markdown only.
- The preview panel header in markdown mode shows "Preview" (no URL bar, no copy button --- there's no URL to copy)

## Agent Integration

### How the agent knows about this

Add to the CLAUDE.md template:

```markdown
## Live Preview

You can show content in a preview panel next to the terminal. Two modes:

**Show a URL (deployed app, dashboard, etc.):**
echo 'url:https://wingthing.slide.tech/apps/$WT_USER/<app-name>/' > .wt-preview

The user sees the page in a panel with a prominent copy button for the URL.
They can grab it and share it with anyone.

**Show markdown (quick report, table, status summary):**
cat > .wt-preview << 'EOF'
# Backup Status

| Partner | Last Backup | Status |
|---------|-------------|--------|
| Acme | 2h ago | OK |
| Initech | 14h ago | WARNING |
EOF

The user sees rendered markdown in the panel. Images are supported.

The file will disappear after the system reads it --- that's expected. It means the
preview is showing. To update the preview, write .wt-preview again.

To close the preview panel, write an empty file:
echo '' > .wt-preview
```

### What about local dev (no nginx)?

During app development, the agent might want to preview before deploying to nginx. The app runs on localhost:PORT which is only accessible on the wing machine, not in the browser.

Two options:
1. **Tunnel proxy:** Wing proxies HTTP requests from the browser through the encrypted tunnel to localhost:PORT. This is basically what the PTY session already does but for HTTP. Heavier but works for any URL.
2. **Deploy first, preview second:** Just deploy to nginx first (it takes 5 seconds), then preview the real URL. Simpler. Good enough for v0.

**Recommendation:** Deploy-first for v0. Tunnel proxy is a v1 feature if people want live-reload during development.

## Security

- The agent writes `.wt-preview` in its normal writable directory --- no new permissions
- The egg consumes (deletes) the file immediately --- no lingering artifacts
- **URL mode:** iframe sandbox `allow-scripts allow-same-origin`. URL must match a whitelist pattern (same domain, or localhost) to prevent previewing arbitrary external sites.
- **Markdown mode:** iframe sandbox `allow-same-origin` only (NO `allow-scripts`). All raw HTML stripped before rendering. Only markdown syntax + images allowed. This prevents XSS via agent-generated content.
- Passkey auth on the wing session already gates access --- no new auth surface

## Scope / Phasing

### v0 (ship with Slide sandbox)
- Single `.wt-preview` file, two modes: `url:` prefix for URLs, raw content for markdown
- Egg watches working dir, consumes file, pushes `preview.update` through tunnel
- URL mode: 50/50 split panel with prominent URL + copy button + open-in-new-tab
- Markdown mode: rendered markdown panel, images only, no scripts, no raw HTML
- Same-origin URLs only (wingthing.slide.tech/apps/*)
- Agent writes the file after deploying to nginx (URL mode) or anytime (markdown mode)

### v1
- Tunnel HTTP proxy for localhost URLs (live dev preview)
- Auto-refresh iframe when agent writes `.wt-preview` again (hot reload)
- Multiple preview tabs

### v2
- Bidirectional: preview panel can send events back to the terminal (click a row → agent queries more data)
- Agent can embed interactive controls (forms, filters) that pipe back to the conversation
- "Preview mode" where the agent watches for file saves and auto-rebuilds

## Relation to Existing Architecture

This feature touches:
- **Egg:** Watches agent working directory for `.wt-preview`, consumes (reads + deletes) the file, parses mode (url vs markdown), forwards preview data to wing. This is the new piece.
- **Wing side:** Receives preview data from egg, wraps it in a `preview.update` tunnel inner message, sends to frontend
- **Frontend:** New React component (preview panel). URL mode: iframe + URL bar + copy button. Markdown mode: rendered markdown in sandboxed iframe. Split layout logic, tunnel message handler for `preview.update`.
- **CLAUDE.md templates:** New section telling the agent about `.wt-preview`, both modes, and the "disappear means it's showing" contract

Does NOT require changes to:
- Relay (dumb pipe, doesn't care about preview data)
- Sandbox/egg.yaml (agent's working directory is already writable)
- Auth (session auth already covers this)
