# Phone coordinator through wingthing.ai

Use a **stable build of this branch**, an awake, online Mac, and an installed/authenticated Claude CLI. The separate wing uses the Mac's normal provider login; its Wingthing state, bearer, key, sessions and daemon files are isolated. Preview remains loopback-only.

From this clone, build into a new executable outside the coordinator workspace:
```sh
nice -n 15 make web
mkdir -p "$HOME/.local/bin"
nice -n 15 go build -p 2 -ldflags '-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=stable' -o "$HOME/.local/bin/wt-phone" ./cmd/wt
WT_PHONE="$HOME/.local/bin/wt-phone"
export WINGTHING_DIR="$HOME/.wingthing-phone"
test ! -e "$WINGTHING_DIR" || exit 1 # choose another fresh directory if it exists
"$WT_PHONE" channel --json # must report stable
mkdir -p "$HOME/phone-coordinator-work"
printf 'fs: ["ro:/", "rw:./"]\nnetwork: none\n' > "$HOME/phone-coordinator-work/egg.yaml"
"$WT_PHONE" login --roost https://wingthing.ai # approve device login as the phone's account
cat > "$WINGTHING_DIR/wing.yaml" <<YAML
wing_id: $(cat "$WINGTHING_DIR/wing-id")
roost: https://wingthing.ai
label: phone-coordinator
paths: ["$HOME/phone-coordinator-work"]
conversations: enabled
hosted_relay: allow
locked: false
allow_keys: []
YAML
chmod 600 "$WINGTHING_DIR/wing.yaml"
"$WT_PHONE" wing start --roost https://wingthing.ai
```
Keep `WINGTHING_DIR` exported for every command here, including later stop/status commands. Do not copy state from the existing local roost. Keep state and executable under the OS home, outside writable workspaces, TMPDIR and provider profile paths; the broker refuses exposed layouts instead of adding sandbox exceptions.

The bearer is `device_token` in this wing's private `$WINGTHING_DIR/device_token.yaml` after `wt login` (or an existing valid device bearer for the same hosted account). Obtain `<account-id>` with `curl --fail -H 'Authorization: Bearer <bearer-token>' https://wingthing.ai/auth/check`, reading `user_id`. Use that bearer to GET `/api/app/wings` on the same origin and find this wing. Enter these values in iOS **Home**:

| Form field | Value |
| --- | --- |
| Transport | Hosted home I choose |
| HTTPS home address | `https://wingthing.ai` |
| Account ID | `<account-id>` from `/auth/check.user_id` |
| Home wing ID | `<wing-id>` from `$WINGTHING_DIR/wing-id`, matching the roster |
| Home wing public key | `<base64-X25519-public-key>` from that roster entry, checked against `public_key` in this wing's `device_token.yaml` |
| Existing access token | `<bearer-token>`, without the `Bearer ` prefix |

Connect Home, tap **New conversation**, choose `phone-coordinator-work`, enter `coordinator`, an exact supported `<claude-model-id>` and your first message, then tap **Create conversation**. The phone creates the headless Claude root under the hosted account's owner; its logical conversation persists after the turn exits. The broker retains its fixed tool subset, finite bounds and existing sandbox protections.

After the turn ends, wait for **Ready for follow-up**, then send your next message. `session_read` and `conversation_read` advertise `headless_continuation`; iOS calls `agent_start` with `resume_session`, `conversation_role: parent`, `input` and an immutable `request_id`. Each follow-up creates a new execution of the same conversation/provider session/model. If unconfirmed, retry the saved message rather than creating another conversation.

Fallback: start an interactive `agent_start` parent (omit `-p`) through `"$WT_PHONE" mcp stdio --client <phone-owner>` on the Mac. `<phone-owner>` is `user-` plus the first 20 hex characters of SHA-256 of the exact `<account-id>`. Open it in iOS and keep its execution alive for `session_prompt` follow-ups.

Native passkey approval is not implemented: locked wings need a locally pinned user passkey and an encryption-identity-bound auth token; unlocked wings also demand this for enrolled users. Use this fresh unlocked wing with no pinned passkeys; preserve the existing roost's lock.

On wingthing.ai's login-node database, Bryan needs `entitlements.user_id='<account-id>'` with `entitlements.subscription_id=subscriptions.id` and `subscriptions.status='active'` unless migration-eligible. Bryan can run this read-only SQL there; at least one returned row satisfies `IsUserPro`:
```sql
SELECT e.id, e.user_id, e.subscription_id, s.status
FROM entitlements e
JOIN subscriptions s ON s.id = e.subscription_id
WHERE e.user_id = '<account-id>' AND s.status = 'active';
```
`users.tier='pro'` alone is insufficient. The alternative is `users.created_at` no later than the running `WT_RELAY_MIGRATION_BEFORE` cutoff (checked in: `2026-08-26T00:00:00Z`). `hosted_relay: allow` cannot grant entitlement; missing rows require operator/billing action.

The inspected `origin/main` (`72c7be5`) already routes these encrypted operations; no relay code deploy is needed. The running hosted deployment/account was not verified. The [hosted native path](personal-conversations.md#hosted-native-path) lists all admission checks. No production change is made here.
