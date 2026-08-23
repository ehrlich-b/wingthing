# Wingthing in a sandboxed AI VM

Use this mode when a dedicated VM or container is already the security boundary
and the agent is intentionally supposed to have the full authority of its VM
user. This is the natural fit for a disposable Ubuntu development VM with its
own repositories, credentials, and outbound network access.

Wingthing replaces the `tmux; claude` part of that workflow. It does not replace
the hypervisor, network segmentation, VM provisioning, package setup, or
credential distribution.

## Install and register the model-facing API

On the VM:

```bash
curl -fsSL https://wingthing.ai/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"

claude mcp add wingthing -- wt mcp stdio --client claude-code --unsandboxed
```

`--unsandboxed` is deliberate here. It tells Wingthing that the outer VM is the
boundary, so it must not require Linux user namespaces or create a second
filesystem/network sandbox. This avoids changing Ubuntu 24.04 AppArmor settings
just to use terminal persistence.

The flag applies to every persistent terminal and headless prompt, loop, or
swarm created through that MCP server. The MCP capability response calls the
mode `outer-boundary`, initialization warns the model that it has the full
authority of the VM user, and every MCP audit entry records the mode. A model
cannot enable it on a single tool call.

## Start the durable agent

From the directory the agent should begin in:

```bash
cd ~/repos
wt egg claude --name claude --unsandboxed -- --permission-mode bypassPermissions
```

Detach without stopping Claude with `Ctrl+B`, then `Q`. On the VM, reattach with:

```bash
wt attach claude
```

From a laptop whose `~/.ssh/config` contains an alias such as `ai-vm`, install
`wt` locally and use the existing SSH trust path:

```bash
wt attach claude --remote ai-vm
```

OpenSSH still owns user authentication, host-key verification, jump hosts, and
transport encryption. Neither local attach nor SSH attach needs a Wingthing
account, daemon, or hosted relay.

## What the boundary does and does not mean

In this mode Wingthing provides process/PTY persistence, replay, named sessions,
structured CLI operations, MCP orchestration, ownership guardrails, and audit
attribution. It does **not** restrict the agent's filesystem, network, syscalls,
credentials, or `sudo` access. If the VM user has a private SSH key and
passwordless sudo, the agent does too.

That is appropriate only when the VM and its network policy were created for
that authority. MCP client names and grants prevent accidental interference
inside Wingthing; they are not protection from a hostile process running as the
same OS user.

To use Wingthing as an additional inner boundary, omit `--unsandboxed` and check
the resolved policy first:

```bash
wt egg explain claude
wt doctor
wt egg claude --name claude
```

The default inner sandbox denies `~/.ssh` and broad network access. That is a
safer default on a normal workstation, but it intentionally conflicts with a VM
workflow whose agent is expected to SSH into development machines. Configure an
`egg.yaml` explicitly if only selected credentials or destinations should cross
the inner boundary.

## Optional hosted access and E2E encryption

Hosted access is an independent choice:

```bash
wt login
wt start
```

The wing then makes an outbound connection to `wingthing.ai`, and the hosted web
client can reach the VM without opening another inbound port. Terminal and wing
API payloads are application-encrypted between the client and wing; the relay
routes ciphertext while still seeing routing/account metadata. The hosted
service also delivers the browser JavaScript, so this is not a guarantee against
a fully compromised service delivering a malicious client.

`--unsandboxed` does not weaken encryption in transit, because sandboxing and
relay encryption are separate boundaries. It does mean the agent is part of the
trusted endpoint: a same-user process can read or alter Wingthing state, invoke
the local CLI, and potentially compromise other same-user sessions. E2E
encryption never protects against a compromised client or wing endpoint.

For the strongest and simplest VM flow, use SSH attach and leave the hosted wing
disabled. Enable the hosted path when browser/mobile reachability, outbound-only
connectivity, or collaboration is worth the additional service trust.
