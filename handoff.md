# SSH Host Key Prompt in Mac Egg Sessions — Debug Handoff

## The Bug

A sandboxed Claude egg session on macOS prompted with:

```
The authenticity of host 'github.com (140.82.114.3)' can't be established.
```

The user's egg config has `ro:~/.ssh` and `env: *`. The expectation is that the
agent should NOT be making SSH connections, or at minimum should not be prompting.

## Branch State

Branch: `fix-ssh-auth-sock-leak`

The branch has a partial fix: `BuildEnv()` strips `SSH_AUTH_SOCK` when `~/.ssh`
is restricted. Currently updated to check both `deny:` and `ro:` modes (not just
`deny:`). But this fix may not fully explain the observed error.

### Files changed on this branch
- `internal/egg/config.go` — `sshDirRestricted()` + `BuildEnv()` filter
- `internal/egg/config_test.go` — tests for the stripping behavior
- `docs/egg-sandbox-design.md` — writeup of the vulnerability

## The Problem with SSH_AUTH_SOCK Stripping

The host key verification prompt happens **before** SSH authentication. The
sequence is:

1. Agent runs `git fetch` (or similar) with an SSH remote
2. SSH connects to github.com on port 22 (network access)
3. SSH tries to verify host key against `~/.ssh/known_hosts`
4. **Prompt appears here** — can't verify host
5. Only THEN would `SSH_AUTH_SOCK` matter for authentication

Stripping `SSH_AUTH_SOCK` prevents step 5 (successful auth) but does NOT prevent
steps 2-4 (the connection and the prompt). The user still sees the prompt.

## Why Can't SSH Read known_hosts?

The user has `ro:~/.ssh`. On macOS seatbelt, this is how it maps:

### How `ro:` works in seatbelt (apple.go)

The seatbelt profile starts with `(allow default)` — **everything is readable**.
`ro:` paths become `sandbox.Mount{ReadOnly: true}` in `ParseFSRules`, but in
`buildProfile()` read-only mounts don't generate any seatbelt rule because reads
are already allowed by default.

The write isolation logic is:
1. If any writable mounts exist → `(deny file-write* (subpath $HOME))`
2. Then each `rw:` mount gets `(allow file-write* (subpath ...))`
3. `ro:` paths get no special rule — they're implicitly read-only

**So `ro:~/.ssh` should allow reading `~/.ssh/known_hosts` just fine.**

### But: does the user's config also have `deny:~/.ssh`?

The **default** egg config includes `deny:~/.ssh` (from `DefaultDenyPaths()`).
Config merging matters here:

- If user's egg.yaml uses `base: default` (or no base), it merges with defaults
- The merged FS list could contain both `ro:~/.ssh` AND `deny:~/.ssh`
- In seatbelt, `deny:` generates `(deny file-read* file-write* (subpath ...))`
- SBPL gives precedence to **later rules**, so ordering matters
- If `deny:~/.ssh` comes after `ro:~/.ssh`, the deny wins → can't read known_hosts

**This is the most likely root cause: the default `deny:~/.ssh` is overriding
the user's `ro:~/.ssh` in the merged config.**

## What to Investigate

### 0. Check the actual egg.yaml

The bug is on the **mac**, not this WSL machine. The user's project is in `~/slide`
on the mac. Check:

```bash
cat ~/slide/egg.yaml
```

The user claims it has `ro:~/.ssh` and `env: *`. Verify this, and check what
`base:` it uses (if any). If it inherits from default, the merged config will
include `deny:~/.ssh` from `DefaultDenyPaths()` alongside the user's `ro:~/.ssh`.

### 1. Reproduce and capture the rendered config

The egg stores its rendered config in the Session struct (`RenderedConfig` field)
and writes session metadata. Start an egg on mac with the user's config and check:

```bash
# The egg logs the seatbelt profile on startup:
# "seatbelt profile:\n..."
# Check what the actual SBPL looks like.

# Also check the merged FS rules:
cat ~/.wingthing/eggs/<session-id>/egg.meta
```

Key question: **does the merged FS list have both `ro:~/.ssh` and `deny:~/.ssh`?**

### 2. Check SBPL rule ordering

If both are present, check the generated profile. In `buildProfile()`:
- Deny rules are emitted first (line ~88-99)
- Mount rules (including ro) come after (line ~101-148)

But `ro:` mounts don't emit any read rule — they're just `{ReadOnly: true}` mounts.
The deny `(deny file-read* file-write* (subpath ~/.ssh))` would block reads with
no corresponding allow to override it.

### 3. Verify the fix

If the issue is `deny:~/.ssh` + `ro:~/.ssh` conflict, the fix is in config merging:
`ro:~/.ssh` should override `deny:~/.ssh` when both are present (user intent is
read-only access, not full denial).

### 4. Also verify SSH_AUTH_SOCK stripping

Even with known_hosts readable, we still want SSH_AUTH_SOCK stripped when `~/.ssh`
is restricted. Test that:
- With `ro:~/.ssh`: SSH_AUTH_SOCK is stripped (agent can read keys but can't use agent socket)
- With `rw:~/.ssh`: SSH_AUTH_SOCK passes through (user wants full SSH)
- With `deny:~/.ssh`: SSH_AUTH_SOCK is stripped

## User's Mac Egg Config

The user said:
- `ro:~/.ssh` — read-only access to SSH dir
- `env: *` — pass through all environment variables
- The session is on macOS (seatbelt sandbox)

Need to see the actual egg.yaml to confirm the full config and base chain.

## Key Files

| File | What |
|------|------|
| `internal/egg/config.go:141` | `DefaultEggConfig()` — includes `deny:~/.ssh` |
| `internal/egg/config.go:119` | `DefaultDenyPaths()` — `~/.ssh` is first entry |
| `internal/egg/config.go:432` | `ParseFSRules()` — how `ro:`/`deny:` map to sandbox config |
| `internal/egg/config.go:486` | `sshDirRestricted()` — the SSH_AUTH_SOCK check |
| `internal/sandbox/apple.go:59` | `buildProfile()` — seatbelt SBPL generation |
| `internal/sandbox/apple.go:88` | Deny rules → `(deny file-read* file-write* ...)` |
