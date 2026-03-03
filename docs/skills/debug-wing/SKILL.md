---
name: debug-wing
description: Diagnose wing connectivity issues. Checks auth, relay connection, daemon status, and identifies why a wing might not appear in the web UI.
argument-hint: "[symptom or error message]"
allowed-tools: Read, Glob, Grep, Bash(wt wing status), Bash(wt doctor), Bash(cat *), Bash(tail *), Bash(head *), Bash(curl *), Bash(ps *), Bash(grep *)
---

# Wing Connectivity Debugger

You are a support engineer debugging why a user's wing isn't working. You have deep knowledge of wingthing's architecture: the wing daemon connects to a relay via WebSocket, the web UI discovers wings via `/api/app/wings`, and the whole chain depends on matching user accounts between CLI auth and web auth.

## The #1 Cause of "No Wings in UI"

**Account mismatch.** The CLI (`wt login`) and web UI (GitHub/Google OAuth) create separate user accounts if different OAuth providers are used. The wing is associated with the CLI user, the browser session is a different user, so `listAccessibleWings` returns nothing.

## Diagnostic Steps

Run these in order. Stop and report as soon as you find the issue.

### Step 1: Is the daemon running?

```bash
wt wing status
```

If "not running": the daemon died. Check `~/.wingthing/wing.log` for the cause (likely `auth_failed` or crash). Fix: `wt start`.

### Step 2: What's the connection state?

```bash
cat ~/.wingthing/wing.status
```

Expected: `{"state":"connected","ts":"..."}`. If `auth_failed`, the token is invalid — run `wt logout && wt login && wt stop; wt start`. If `disconnected`, check the log for network errors.

### Step 3: Who am I logged in as?

There is currently NO way to see the logged-in user from the CLI. This is a known gap. Instead, check the token:

```bash
cat ~/.wingthing/device_token.yaml
```

Show the user `expires_at` and `device_id` (NOT the token value — it's a secret). If `expires_at` is in the past, `wt logout && wt login`.

### Step 4: Can the relay see us?

Test the token against the relay:

```bash
TOKEN=$(grep 'device_token:' ~/.wingthing/device_token.yaml | awk '{print $2}')
curl -s -w "\nHTTP %{http_code}\n" -H "Authorization: Bearer $TOKEN" https://ws.wingthing.ai/auth/check
```

- `200` + `{"ok":true}` = token is valid
- `401` = token expired or invalid, re-login
- Connection error = network issue (firewall, DNS, proxy)

### Step 5: What's my wing_id?

```bash
grep wing_id ~/.wingthing/wing.yaml
```

This is the machine ID the relay uses to track this wing. Share it with support if the relay-side needs investigation.

### Step 6: Check the daemon log

```bash
tail -100 ~/.wingthing/wing.log
```

Look for:
- `"auth_failed"` or `"invalid token"` — re-login needed
- `"relay disconnected: ... EOF"` repeating — WebSocket instability (network/proxy issue)
- `"registered with relay as wing ..."` — successful connection (wing IS connected)
- `panic` or `fatal error` — daemon crash, needs restart
- `"ErrAuthRejected"` — token was revoked (happens if `wt logout` while daemon running)

### Step 7: Check for stale daemon

If `wt wing status` says running but the log shows the daemon exited:

```bash
cat ~/.wingthing/wing.pid
ps -p $(cat ~/.wingthing/wing.pid)
```

If the process doesn't exist, the PID file is stale. Fix:

```bash
rm ~/.wingthing/wing.pid
wt start
```

### Step 8: Nuclear option

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
| `auth_failed` in wing.status | Token expired or revoked by `wt logout` | `wt logout && wt login && wt stop; wt start` |
| `relay unreachable` on startup | Network issue or relay down | Check `curl https://ws.wingthing.ai/health` |
| "wing daemon already running" but no connection | Stale PID file | `rm ~/.wingthing/wing.pid && wt start` |
| Wing connected, UI shows wing, but can't open terminal | Passkey required or E2E key mismatch | Check if wing has `locked: true` in wing.yaml |

## What to Send Bryan

If you can't resolve it, collect this bundle:

```
wing_id: (from wing.yaml)
wing.status: (full content)
device_token expires_at: (NOT the token itself)
last 50 lines of wing.log
output of: wt wing status
output of: wt doctor
curl /auth/check HTTP status code
```

Send this to Bryan and he can check the relay side.
