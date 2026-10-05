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

Start a persistent **interactive** parent on the Mac using the hosted account's exact owner principal. In the same terminal, replace `<account-id>`, then run:
```sh
PHONE_OWNER="user-$(printf '%s' '<account-id>' | shasum -a 256 | cut -c 1-20)"
"$WT_PHONE" mcp stdio --client "$PHONE_OWNER"
```
Paste this single JSON line, replacing the workspace/user and retry-ID placeholders; wait for `structuredContent.launch_state: started`, then Ctrl-D. This fresh state has no `clients.yaml`; the broker captures its fixed tool subset and finite bounds, not unlimited authority.
```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_start","arguments":{"agent":"claude","label":"coordinator","cwd":"/Users/<mac-user>/phone-coordinator-work","conversation_role":"parent","request_id":"<unique-launch-request-id>","args":["--permission-mode","dontAsk","--allowedTools","mcp__wingthing__wingthing_capabilities,mcp__wingthing__agent_start,mcp__wingthing__session_status,mcp__wingthing__session_read,mcp__wingthing__session_wait,mcp__wingthing__session_prompt,mcp__wingthing__conversation_list,mcp__wingthing__conversation_read,mcp__wingthing__conversation_checkpoint"]}}}
```
Refresh iOS Conversations, open `coordinator`, wait for native ready/idle evidence, and send a message. `"$WT_PHONE" conversation transport <session-id> --json` should report `host_ready: true`. Keep this interactive execution alive for subsequent phone prompts. iOS New conversation uses `-p` headless execution; this branch does not implement its expected `headless_continuation`/`resume_session` handler, so that route cannot provide follow-ups after exit.

Native passkey approval is not implemented: locked wings need a locally pinned user passkey and an encryption-identity-bound auth token; unlocked wings also demand this for enrolled users. Use this fresh unlocked wing with no pinned passkeys; preserve the existing roost's lock.

The inspected `origin/main` (`72c7be5`) already routes these encrypted operations; no relay code deploy is needed. The running hosted deployment/account was not verified. An ineligible `direct-free` account still needs an operator-managed entitlement linked to an active subscription (or the configured migration cohort); `hosted_relay: allow` alone cannot grant access. The [hosted native path](personal-conversations.md#hosted-native-path) lists the code checks and exact database/config requirements. No production change is made here.
