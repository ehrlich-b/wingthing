# First run in the terminal navigator

Interactive bare `wt` opens the session navigator. With no local wing or
session inventory, it explains wings, roosts and the initial local connection,
then looks for Claude Code and Codex on PATH. This inspection does not execute
either binary, check a provider account, download an installer or start a wing.
`s` skips setup and reviews the current directory for a normal shell session.

Press `n` to set up an agent. `c` chooses Claude Code, `o` chooses Codex, and
Escape returns to the navigator while retaining the chosen agent. If the
selected binary is missing, the review names the local machine, exact command
and expected download. Enter explicitly consents to that one vendor installer
as the current OS user. No package manager or sudo fallback runs.

The commands were checked against the official vendor documentation on
2026-10-11:

- [Claude Code setup](https://code.claude.com/docs/en/setup):
  `curl -fsSL https://claude.ai/install.sh | bash`
- [Codex CLI](https://learn.chatgpt.com/docs/codex/cli):
  `curl -fsSL https://chatgpt.com/codex/install.sh | sh`

After installation, or when a binary is already found, Enter runs the selected
binary's `--version`. A second Enter hands the terminal to `claude auth login`
or `codex login`. Vendor output goes directly to the terminal; Wingthing does
not record it in diagnostics or telemetry. Provider authentication and billing
are separate from Wingthing accounts. A successful version check is not proof
of authentication or session readiness.

Successful login returns to the normal cwd review. Accepting that review uses
the existing scoped Claude or generic Codex launch route. Admission, attach,
detach and recovery retain their existing session identity semantics. In
particular, the [protected Claude parent mailbox](wt-claude.md) currently
requires macOS; first-run setup does not remove that platform requirement.

Failure preserves the agent and stage. Enter on the recovery screen first
returns to consent, then a further Enter retries. `r` rescans PATH after the
user repairs it; `c`/`o` chooses another agent and `b` goes back. An installer
that exits successfully without putting the selected binary on PATH shows the
vendor's expected `~/.local/bin` directory rather than reinstalling by default.
An unsuccessful download remains a failure even when the pipeline's shell
would otherwise exit successfully.

Existing wings, session rows and saved remote machine selections retain the
normal agent/cwd prompts. First-run setup never installs on a saved remote.
