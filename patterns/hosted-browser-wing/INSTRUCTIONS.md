# Use the hosted browser on a remote wing

Use this setup when a person wants to start or resume a persistent agent terminal
from `app.wingthing.ai` and the account already has hosted-relay access. This is a
different transport from free native MCP and from a self-hosted roost:

```text
hosted browser -> application-encrypted relay -> selected wing -> Claude or Codex
```

Native `wt mcp connect` sends MCP payloads directly to a wing and never falls back
to this relay. A self-hosted roost supplies its own browser and relay without a
wingthing.ai relay entitlement.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Driver** | A person using the hosted browser. |
| **Execution wing** | The wing explicitly selected in the browser. The hosted service routes control but does not run the agent. |
| **Workspace** | An existing allowed directory on the selected wing. Wingthing does not clone, upload, or synchronize it. |
| **Display** | A persistent browser terminal relayed between the browser and wing. The same session remains attachable with `wt attach` on the execution wing. |
| **Provider credentials** | The execution owner's provider-agent home on the wing. Authenticate the provider CLI on that machine before using the browser. |
| **Durable memory** | Session, task, provider-history, and optional Wingthing memory remain on the execution wing. The hosted gateway retains account, routing, entitlement, and other gateway metadata, not terminal bytes. |

## 1. Check both relay decisions

The hosted gateway must report relay access for the signed-in account. The public
`direct-free` deployment does not give new free accounts browser-relay access or
provide a self-service switch for granting it.

The wing must also allow hosted relay. An omitted `hosted_relay` setting has the
compatible effective value `allow`. If the wing was intentionally made direct-only,
do not weaken that policy without the operator's approval. To inspect it:

```sh
wt wing config
```

If the operator chooses to re-enable hosted relay, the change requires a restart:

```sh
wt wing config set hosted_relay=allow
wt stop
wt start
```

Account access and wing policy are independent. `hosted_relay: deny` wins even for
an entitled account.

## 2. Prepare the execution wing

On the computer that will run the agent, install Wingthing and the provider CLI,
authenticate the provider as the OS user who will own the work, and make sure the
project already exists under one of the wing's configured paths. Then connect the
wing to the hosted account:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
wt wing status
```

## 3. Start or resume the terminal

Open `https://app.wingthing.ai`, choose the online wing and an existing allowed
project directory, then choose the installed agent. The browser starts a persistent
PTY on that wing. Closing the browser detaches from the terminal; it does not stop
the process.

## Security boundary

Terminal payloads are application-encrypted between the browser and wing during
normal operation. The hosted service routes ciphertext but still sees account,
wing, session, selected-agent, working-directory, timing, and size metadata. It also
serves the browser client, so transport encryption does not remove the need to trust
that delivered code or its initial key handling. Provider credentials, project
files, task state, and terminal content remain on the wing.
