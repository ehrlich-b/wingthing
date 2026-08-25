# Open a remote agent session in your browser

Use this setup when you want to start or resume an agent terminal on a workstation
or VM from a web browser. The project, agent process, and provider credentials stay
on the remote computer. That computer connects outward, so it needs no inbound port.

## Before you start

The shipped browser terminal uses a relay. You need one of:

- Wingthing Pro hosted relay access;
- temporary relay access on an existing grandfathered account; or
- a self-hosted roost, where you operate the relay yourself.

The new hosted free tier does not include browser-terminal relay. For free remote
control by a parent AI, use the
[several-computer AI setup](../remote-orchestration/INSTRUCTIONS.md) instead.

## Connect the remote computer

On the computer that has the project and will run the agent:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
wt wing status
```

Leave `wt start` running as a daemon. Then open `https://app.wingthing.ai`, select
the computer, choose a project directory, and start or resume a session.

## What persists

Closing the browser does not stop the session. Reopen it from the browser later, or
attach directly on the remote computer:

```sh
wt attach
wt attach <session-id-or-name>
```

Wingthing does not copy projects between computers. The selected directory and any
untracked files must already exist on the remote computer.
