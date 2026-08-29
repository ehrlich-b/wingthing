# Control a remote agent from a self-hosted browser

Run the Wingthing web app on the computer in front of you, then connect a remote
computer to it through SSH. Claude or Codex runs on the remote computer; your
browser stays pointed at `localhost`.

```text
localhost browser -> local portal -> SSH tunnel -> remote wing -> Claude or Codex
```

This is the smallest self-hosted setup. It does not use wingthing.ai.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | The remote computer. The localhost service is only the browser portal and gateway; it does not move execution to the computer in front of you. |
| **Workspace** | An existing project directory on the remote computer, included in that wing's allowed `--paths`. Wingthing does not copy or synchronize the project. |
| **Display** | A persistent terminal in `https://localhost:8443`, with `wt attach` available on the remote computer. This workflow is for human-visible PTYs; use the several-computer MCP pattern for semantic headless runs. |
| **Provider credentials** | The remote OS user's provider-agent home. Authenticate Claude, Codex, or another provider on the remote computer; the portal and SSH tunnel do not supply those credentials. |
| **Durable memory** | The authoritative egg/session state, provider history, and optional Wingthing memory remain on the remote wing. The local portal keeps only its gateway/browser state. Neither the SSH tunnel nor the portal replicates durable agent memory. |

## 1. Start the private web app

On the computer where you will use the browser:

```sh
wt serve --local --https
```

On first use, WT explains and performs a local trust ceremony:

- it creates a localhost-only CA and server certificate on demand in
  `~/.wingthing/local-tls`;
- both private keys remain mode `0600` on this browser computer and are never
  copied to the remote wing or installed in a trust store; and
- it installs only the public CA certificate in this computer user's trust store.

The CA itself is name-constrained to localhost and loopback IPs. Leave the server
running. HTTPS mode binds both of its listeners to loopback: the browser uses
`https://localhost:8443`, while the wing reaches the private HTTP endpoint on
`127.0.0.1:8080` through SSH. Local mode has no human login screen, so neither
listener may be exposed to a LAN or the public internet. WT refuses a non-loopback
local bind, rejects non-loopback Host headers, and requires same-origin browser
mutations and WebSocket handshakes.

## 2. Carry it to the remote computer over SSH

In a second terminal on the browser computer:

```sh
ssh -N \
  -o ExitOnForwardFailure=yes \
  -R 127.0.0.1:18743:127.0.0.1:8080 \
  you@remote.example
```

This creates `127.0.0.1:18743` on the remote computer. Traffic sent there travels
inside SSH to the private portal on your browser computer. It uses SSH access you
already have; it does not open a new Wingthing service to the remote network.

## 3. Start the remote wing

On the remote computer, install Wingthing and the agent CLI you want to run. The
durable provider credential belongs to this computer; Wingthing never copies a
Claude or Codex token from the browser computer.

For Claude, verify the remote login before starting the wing:

```sh
claude auth status
claude auth login  # run this if the login is missing or expired
```

On a headless host, Claude prints a one-time HTTPS page that you may open in any
browser, then asks you to paste the resulting code back into the remote terminal.
If that terminal is open in Wingthing, the paste travels through the encrypted
browser-to-wing terminal channel; the roost cannot read it. Claude redeems the code
and stores the durable credential on the remote host. Codex and other agents follow
the same rule using their own login commands.

Now log the wing into the portal through the forwarded port and start it:

```sh
wt login --roost http://127.0.0.1:18743
wt start \
  --roost http://127.0.0.1:18743 \
  --paths /path/to/project
```

The project directory must already exist on the remote computer and be included in
`--paths`. Wingthing routes control; it does not copy the project, provider
credentials, or agent memory between computers.

## 4. Open the agent

Open [https://localhost:8443/app/](https://localhost:8443/app/) in the browser. The
remote wing appears in the machine list. Select it, select the project directory,
and start Claude or Codex.

Closing the browser does not stop the agent terminal. Reopen the session from the
same page, or attach from a terminal on the remote computer with `wt attach`.

## What protects this setup

- The roost listens only on the browser computer's loopback interface.
- Host, browser-mutation Origin, and WebSocket Origin checks defend the no-login
  loopback service against DNS rebinding and hostile web pages.
- The browser connection uses a locally trusted HTTPS certificate. Only its public
  CA is added to this user's trust store; the CA private key stays on this computer.
- The forwarded port listens only on the remote computer's loopback interface.
- SSH authenticates the two computers and encrypts the host-to-host connection.
- The wing authenticates to the portal with its own device token.
- Terminal payloads are additionally encrypted between the browser and the wing;
  the portal handles routing metadata but not plaintext terminal contents.
- Project files and provider credentials remain on the remote computer.

`--local` trusts software that can reach its loopback port. Do not change either
`127.0.0.1` binding to `0.0.0.0`. This guide uses `wt serve`, so it runs a portal
and gateway without adding an embedded local wing. For an always-on URL or several
human users, run an HTTPS roost with OAuth instead; see the
[private team roost guide](../shared-web-roost/INSTRUCTIONS.md).

Inspect the local certificate at any time with `wt local-cert status`. To remove
its public CA from this user's trust store, run `wt local-cert remove`. Removal
leaves the private files on this computer so WT cannot silently orphan and replace
a root that was previously trusted.

## WSL note

The same topology works when the wing runs in WSL. The least surprising setup is
to SSH directly into WSL so its reverse-forwarded port is also WSL loopback. If SSH
terminates in Windows instead, Windows must explicitly bridge that loopback port to
the WSL virtual adapter and restrict the firewall rule to the WSL subnet. Never
solve that extra hop by exposing a local-mode roost to the LAN.
