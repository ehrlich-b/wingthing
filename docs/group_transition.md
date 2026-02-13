# Process Group Transition Plan

## Current State (prod)

- Single Fly machine in ewr, no process groups
- Volume `wt_data` mounted at `/data` with no process group scope
- CMD from Dockerfile: `sh -c 'mkdir -p /data/.wingthing && exec ./wt serve --addr :8080'`
- `WT_JWT_SECRET` already set in Fly secrets
- Bryan uses this daily — zero downtime tolerance for screwups

## What the deploy changes

1. **fly.toml adds `[processes]`** with `login` and `edge` groups. Fly may interpret this as "the existing machine has no process group, destroy it and create a new `login` machine." If it destroys before creating, the volume detaches and reattaches — brief downtime. If it fails to reassign the volume to the new machine, bad.

2. **`[[mounts]]` gains `processes = ["login"]`** scoping. The existing volume has no process group association. Need to verify Fly reassigns it to the `login` group cleanly.

3. **CMD override via process group command.** The `login` process command replaces the Dockerfile CMD. Functionally identical but the boot path changes.

4. **`auto_stop_machines` changed from `"stop"` to `false`.** This keeps machines running instead of stopping when idle. Correct for always-on relay, but a billing change.

## Risk matrix

| Risk | Severity | Mitigation |
|------|----------|------------|
| Volume not reassigned to login group | HIGH — data loss/downtime | Test on staging first |
| Fly destroys existing machine before creating login | MEDIUM — brief downtime | Deploy during low-traffic window |
| Edge machines created unexpectedly | LOW — just scale to 0 | `fly scale count edge=0` |
| Graceful shutdown untested on Fly | LOW — worst case is current behavior (hard kill) | SIGTERM handling is additive |
| Auto-detect picks wrong role | LOW — /data check is reliable | Verify with `fly ssh console` after deploy |

## Staging test plan

```bash
# Create staging app
fly apps create wingthing-staging

# Create a small volume
fly volumes create wt_data --region ewr --size 1 --app wingthing-staging

# Copy secrets
fly secrets set WT_JWT_SECRET=$(fly secrets list --app wingthing | grep WT_JWT_SECRET) --app wingthing-staging
# ^ that won't work, secrets aren't readable. Generate a new one:
fly secrets set WT_JWT_SECRET=$(openssl rand -base64 32) --app wingthing-staging

# Deploy with the new fly.toml
fly deploy --app wingthing-staging

# Check what Fly created
fly machines list --app wingthing-staging
fly scale show --app wingthing-staging

# Verify login machine has volume
fly ssh console --app wingthing-staging -C "ls /data"

# Verify auto-detect worked
fly logs --app wingthing-staging | grep "auto-detected"

# Test graceful shutdown
fly machines kill <machine-id> --app wingthing-staging
# Watch logs for "graceful shutdown" + "relay.restart"

# Add an edge, verify it works
fly scale count edge=1 --region ewr --app wingthing-staging
fly machines list --app wingthing-staging
fly logs --app wingthing-staging | grep "auto-detected"
# Edge should show "auto-detected node role: edge"

# Clean up
fly apps destroy wingthing-staging
```

## Prod deploy plan

Do this when Bryan is NOT actively using prod (no open PTY sessions).

```bash
# 1. Check current state
fly machines list
fly volumes list

# 2. Deploy
fly deploy

# 3. Verify
fly machines list              # should show 1 login machine
fly scale show                 # should show login=1, edge=0
fly logs | grep "auto-detected"  # should say "login"
fly ssh console -C "ls /data/.wingthing"  # volume still there

# 4. Smoke test
# Open app.wingthing.ai, verify login works, connect a wing, open a PTY

# 5. If anything is wrong
fly deploy --image <previous-image-ref>
# or: fly machines update <id> --metadata fly_process_group=""
# to remove the process group and go back to single-machine mode
```

## TODO before deploying

- [ ] Run staging test plan above
- [ ] Verify Fly handles volume reassignment to process group
- [ ] Verify existing Fly secrets carry through process group migration
- [ ] Test `fly machines kill` triggers SIGTERM → graceful shutdown → relay.restart
- [ ] Confirm edge auto-detect works (no volume = edge)
- [ ] Have rollback plan ready (previous image ref noted)
