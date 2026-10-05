# Preview Panel: Live App Preview in Terminal View

**Status:** Implemented; this document records the shipped contract and remaining follow-up work
**Reviewed:** 2026-08-28

---

## The Idea

When a sandboxed agent builds a web app (or any URL-accessible artifact), it
writes the per-session file named by `WT_PREVIEW_FILE` in its working directory.
The wing session supervisor consumes this file and sends the preview through the
encrypted terminal channel. Markdown renders immediately; a URL appears for review
and requires the user to choose **load preview** before the browser fetches it.

The user does not have to copy a URL or open a new tab. The address and an inert
disclosure appear next to the conversation that created it; the user decides
whether the browser should load the page.

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
4. A split panel shows the URL; the user reviews it and chooses **load preview**
```

The panel opening happens automatically. The network request does not: the user
reviews the address and chooses **load preview** or **open**. This preserves the
no-copy-paste workflow without letting a network-confined agent silently use the
attached browser as an egress path.

## The Well-Known File

### Location

```
<agent working directory>/.wt-preview-{sessionID}
```

The agent writes this in its normal writable start directory (e.g. `~/sales/` in the Slide deployment). No sandbox changes needed --- it's already writable. The per-session suffix prevents collisions when multiple sessions share a working directory.

### Format: Three forms

The file contents determine what the preview panel shows:

**Mode 1: URL preview** --- file starts with `url:` prefix

```
url:https://wingthing.slide.tech/apps/sarah/order-tracker/
```

The frontend displays the URL with load, copy, and open controls. It does not load
the iframe until the user chooses **load preview**. This is the "here's your live
dashboard" mode.

**Mode 2: named text content** --- file starts with `file:<name>`

The remainder of the file is displayed as Markdown when the sanitized filename
has a Markdown extension, or as escaped source otherwise. A download button uses
that filename and its derived content type.

**Mode 3: Markdown preview** --- anything else

```markdown
# Backup Health Report

| Partner | Last Backup | Status |
|---------|-------------|--------|
| Acme Corp | 2 hours ago | OK |
| Initech | 14 hours ago | WARNING |
```

If the contents don't start with `url:`, they're treated as markdown. The frontend
renders this in a sandboxed, network-inert iframe: raw HTML is escaped, links are
shown as text, and image syntax becomes a text label instead of a browser request.
This lets agents show quick reports, tables, and status summaries without deploying
a web app or turning the user's browser into an egress path.

**Why not JSON?** Because the agent can write a markdown blob to
`$WT_PREVIEW_DIR/$WT_PREVIEW_FILE` without escaping quotes in JSON. The `url:`
and `file:` prefixes are unambiguous and trivial to detect.

### The consume-on-read trick

**The wing session supervisor watches the file named by `WT_PREVIEW_FILE` and
consumes it (deletes it) after it forwards the preview.** This is the key design
choice:

1. Agent writes `$WT_PREVIEW_FILE` in its working directory.
2. The wing's session watcher detects the file using filesystem notifications
   with bounded polling as a fallback.
3. The watcher reads and validates the file.
4. The wing encrypts the bounded preview payload, sends `pty.preview` through
   the session transport.
5. After the send succeeds, the wing deletes the file. The frontend opens the
   preview panel. Markdown renders without network access;
   URLs wait for explicit user activation.

**The file disappearing tells the agent that Wingthing accepted the preview.** It
does not mean the user loaded an agent-authored URL.

This solves three problems at once:
- **No sandbox hole needed** --- agent writes in its normal writable directory
- **No git pollution** --- the file is gone before anyone could accidentally `git add` it
- **Clean signal** --- agent can check: file gone = preview is live

### Updating the preview

The agent writes `$WT_PREVIEW_FILE` again. The watcher consumes it and the frontend
updates the panel. Every new URL returns to the explicit-load state.

### Clearing the preview

The agent writes `$WT_PREVIEW_FILE` with just a blank line or empty content. The
watcher consumes it and the frontend closes the panel.

## Detection: the wing watches the working directory

The wing's session controller already manages the agent's working directory and PTY:

1. **Wing watches** the working directory for the session-specific filename.
2. **Wing reads a bounded regular file and deletes it.** The read rejects symlinks,
   non-regular files, oversized content, and file swaps between inspection and open.
3. **Wing encrypts and forwards** the preview to the frontend as `pty.preview`:

| Inner type | Direction | Payload |
|-----------|-----------|---------|
| `pty.preview` | wing -> browser | encrypted `{mode: "url", url: "..."}`, `{mode: "markdown", content: "..."}`, or `{mode: null}` associated with `session_id` |

The wing parses the file: `url:` selects validated URL mode; `file:<name>` sends
the remaining text with a sanitized download filename and derived content type;
anything else is Markdown. Empty or blank content closes the panel.

This is a push model. The wing watches locally with filesystem notifications and
bounded polling as a fallback, and sends only when the session-specific file
changes. The frontend does not poll for previews; it listens for encrypted
`pty.preview` messages on the terminal transport.

## Frontend UI

### Layout

When a preview URL is active, the terminal view splits:

```
+-------------------------------------------+
|  Terminal (xterm.js)  |  Preview panel     |
|                       |                    |
|  $ sonnet is typing   | [Preview]       X  |
|  ...                  | https://wingth...  |
|                       | [load][copy][open] |
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

- **Title:** "Preview" in URL mode, or the sanitized filename in content mode.
- **Full URL displayed prominently**: `https://wingthing.slide.tech/apps/sarah/order-tracker/` --- not truncated, not hidden behind a tooltip. This is a real link they can share.
- **Copy button** right next to the URL. One click, URL in clipboard, brief "Copied!" confirmation. This is how they grab the link to send to a coworker, paste in Slack, bookmark, etc.
- **Load preview button.** The iframe remains network-inert until the user reviews
  the address and chooses this button. Every replacement URL requires a fresh click.
- **Open in new tab button** (external link icon). Opens the URL in a real browser tab
  only when the user clicks it.
- **Collapse/close button** to dismiss the panel and go full-width terminal.

The copy button is the star. The whole point is: agent builds it, user sees it live, user copies the URL and runs off to show someone. The preview panel is a launchpad, not a cage.

### Other layout details

- Default split: 50/50 or 60/40 (terminal wider)
- Draggable divider to resize

### When no preview is active

Full-width terminal. No empty panel. No placeholder. The preview panel only appears when there's something to show.

### iframe considerations (URL mode)

- Receiving the agent message does not navigate or fetch. The user must explicitly
  load each URL.
- Loaded pages run in an opaque sandbox origin. Cross-origin pages must permit iframe
  embedding through their own response headers. The iframe and open link use a
  no-referrer policy.
- For localhost URLs during development, the browser's localhost is not the remote
  wing. A future wing-side HTTP tunnel could bridge that gap.

### Markdown rendering

- Use a markdown library (marked, markdown-it, etc.) to render to HTML
- Render into an iframe with an empty sandbox policy: no scripts and no normal origin.
- **No image fetches or active links** --- their labels and targets remain readable text.
- **No raw HTML passthrough** --- escape agent-authored HTML before rendering the
  small supported Markdown surface.
- The preview panel header in markdown mode shows "Preview" (no URL bar, no copy button --- there's no URL to copy)

## Agent Integration

### How the agent knows about this

Add to the CLAUDE.md template:

```markdown
## Live Preview

You can show content in a preview panel next to the terminal. Two modes:

**Show a URL (deployed app, dashboard, etc.):**
printf '%s\n' 'url:https://wingthing.slide.tech/apps/$WT_USER/<app-name>/' > "$WT_PREVIEW_DIR/$WT_PREVIEW_FILE"

The user sees the URL with load, copy, and open controls. Wingthing does not fetch
it until the user chooses load or open.

**Show markdown (quick report, table, status summary):**
cat > "$WT_PREVIEW_DIR/$WT_PREVIEW_FILE" << 'EOF'
# Backup Status

| Partner | Last Backup | Status |
|---------|-------------|--------|
| Acme | 2h ago | OK |
| Initech | 14h ago | WARNING |
EOF

The user sees rendered Markdown in the panel. Links and image syntax remain
readable text and perform no network requests.

The file disappears after Wingthing accepts it. To update the preview, write
`$WT_PREVIEW_DIR/$WT_PREVIEW_FILE` again.

To close the preview panel, write an empty file:
printf '\n' > "$WT_PREVIEW_DIR/$WT_PREVIEW_FILE"
```

### What about local dev (no nginx)?

During app development, the agent might want to preview before deploying to nginx. The app runs on localhost:PORT which is only accessible on the wing machine, not in the browser.

Two options:
1. **Tunnel proxy:** Wing proxies HTTP requests from the browser through the encrypted tunnel to localhost:PORT. This is basically what the PTY session already does but for HTTP. Heavier but works for any URL.
2. **Deploy first, preview second:** Just deploy to nginx first (it takes 5 seconds), then preview the real URL. Simpler. Good enough for v0.

**Recommendation:** Deploy-first for v0. Tunnel proxy is a v1 feature if people want live-reload during development.

## Security

- The agent writes the file named by `WT_PREVIEW_FILE` in its normal writable
  directory --- no new permissions.
- The wing consumes (deletes) the file immediately --- no lingering artifact.
- **URL mode:** only absolute HTTP(S) URLs without embedded credentials are
  accepted. Merely receiving a URL performs no network request; the user must
  explicitly load it. Once loaded, the iframe may run scripts but has an opaque
  sandbox origin (`allow-scripts` without `allow-same-origin`), so an
  agent-selected page or redirect cannot become same-origin with the Wingthing
  portal. The browser repeats URL validation so older wings cannot send
  executable URL schemes.
- **Markdown mode:** an empty iframe sandbox permits neither scripts nor a normal
  origin. Raw HTML is escaped, and links and images are network-inert text. This
  prevents both XSS and browser-assisted egress through agent-authored Markdown.
- Passkey auth on the wing session already gates access --- no new auth surface

## Scope / Phasing

### v0 (ship with Slide sandbox)
- One session-specific preview file with URL, named text-content, and raw
  Markdown forms
- Wing watches the working directory, consumes the file, and pushes encrypted
  `pty.preview` through the terminal transport
- URL mode: 50/50 split panel with prominent URL, explicit load, copy, and
  open-in-new-tab controls
- Markdown mode: rendered network-inert Markdown, no scripts, image fetches, active
  links, or raw HTML
- Absolute HTTP(S) URLs without embedded credentials; no automatic fetch
- Agent writes the file after deploying to nginx (URL mode) or anytime (markdown mode)

### v1
- Tunnel HTTP proxy for localhost URLs (live dev preview)
- Optional refresh control for an already loaded iframe
- Multiple preview tabs

### v2
- Bidirectional: preview panel can send events back to the terminal (click a row → agent queries more data)
- Agent can embed interactive controls (forms, filters) that pipe back to the conversation
- "Preview mode" where the agent watches for file saves and auto-rebuilds

## Relation to Existing Architecture

This feature touches:
- **Wing session controller:** Watches the agent working directory, validates and
  consumes the preview file, encrypts the bounded payload, and sends `pty.preview`.
- **Frontend modules:** URL mode provides explicit load, copy, and open controls;
  Markdown mode renders network-inert content in a sandboxed iframe.
- **Agent environment:** `WT_PREVIEW_FILE` supplies the collision-free filename.

Does NOT require changes to:
- Relay policy (the relay routes the encrypted PTY envelope)
- Sandbox/egg.yaml (the agent's working directory is already writable)
- Auth (session auth already covers this)
