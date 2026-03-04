---
name: debug-wing
description: Diagnose wing connectivity issues. Checks auth, relay connection, daemon status, and identifies why a wing might not appear in the web UI.
argument-hint: "[symptom or error message]"
allowed-tools: Read, Glob, Grep, Bash(wt whoami), Bash(wt wing status), Bash(wt doctor), Bash(wt support), Bash(cat *), Bash(tail *), Bash(head *), Bash(curl *), Bash(ps *), Bash(grep *)
---

# Wing Connectivity Debugger

You are a support engineer debugging why a user's wing isn't working. You have deep knowledge of wingthing's architecture: the wing daemon connects to a relay via WebSocket, the web UI discovers wings via `/api/app/wings`, and the whole chain depends on matching user accounts between CLI auth and web auth.

## The #1 Cause of "No Wings in UI"

**Account mismatch.** The CLI (`wt login`) and web UI (GitHub/Google OAuth) create separate user accounts if different OAuth providers are used. The wing is associated with the CLI user, the browser session is a different user, so `listAccessibleWings` returns nothing.

## Diagnostic Steps

Run these in order. Stop and report as soon as you find the issue.

### Step 1: Who am I?

```bash
wt whoami
```

This shows the account the CLI is authenticated as (name, email, provider, wing_id). If it fails with "token expired", run `wt logout && wt login`.

Compare this with the account shown in the web UI (Settings page or the "signed in as" line on the empty state). If they don't match, that's the problem.

### Step 2: What's the daemon and relay status?

```bash
wt wing status
```

This shows:
- Daemon PID and whether it's running
- Account identity (verified against the relay)
- Relay connection state (connected/disconnected/auth_failed)
- Wing ID
- Active egg sessions

If "not running": `wt start`. If "auth_failed": `wt logout && wt login && wt start`. If "token expired": same fix.

### Step 3: Collect diagnostic bundle

```bash
wt support
```

This creates a zip at `/tmp/wt-support-<timestamp>.zip` containing:
- `meta.json`: wing_id, hostname, platform, version, daemon PID, token expiry, account info
- `wing.log`: last 10,000 lines
- `egg.log`: last 1,000 lines
- `doctor.txt`: agent detection results
- `wing.yaml` and `wing.status`: config and state files

Email the zip to support@wingthing.ai for relay-side investigation.

### Step 4: Check the daemon log

```bash
tail -100 ~/.wingthing/wing.log
```

Look for:
- `"FATAL: relay rejected authentication"` — re-login needed
- `"relay disconnected: ... EOF"` repeating — WebSocket instability (network/proxy issue)
- `"relay connected"` — successful connection
- `"wing daemon exiting"` — daemon shut down (check reason)
- `panic` or `fatal error` — daemon crash, needs restart

### Step 5: Nuclear option

If nothing above helps:

```bash
wt stop
wt logout
rm -f ~/.wingthing/wing.status ~/.wingthing/wing.pid
wt login
wt start --debug
tail -f ~/.wingthing/wing.log
```

The `--debug` flag enables verbose logging. Watch the log for connection events and report what you see.

## Common Failure Modes

| Symptom | Cause | Fix |
|---------|-------|-----|
| "no wings" in web UI but `wt wing status` says connected | Account mismatch (different OAuth provider on CLI vs web) | `wt logout && wt login` using same provider as web |
| Wing connects then disconnects every 60-90s | WebSocket terminated by proxy/firewall | Check corporate proxy, try different network |
| `auth_failed` in wing.status | Token expired or revoked by `wt logout` | `wt logout && wt login && wt start` |
| `relay unreachable` on startup | Network issue or relay down | Check `curl https://ws.wingthing.ai/health` |
| "wing daemon already running" but no connection | Stale PID file | `rm ~/.wingthing/wing.pid && wt start` |
| Wing connected, UI shows wing, but can't open terminal | Passkey required or E2E key mismatch | Check if wing has `locked: true` in wing.yaml |

## What to Send Support

Run `wt support` and email the resulting zip to support@wingthing.ai. It contains everything needed for diagnosis without any secrets.
