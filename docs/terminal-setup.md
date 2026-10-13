# First local session

Run bare `wt` in a terminal. Empty inventory includes local PATH detection and
onboarding. `a` opens detection; `1` selects Claude Code, `2` selects Codex.
A missing selected agent opens its exact vendor installer/source and requires
Enter consent. Escape changes nothing. Install runs as the current OS user,
with no automatic sudo, login or policy change. Download failure survives the
installer pipe; retry returns to consent. `R` rescans PATH without installing.
After installation, `v` checks `--version`, `l` hands the terminal to native
provider login, and Enter reviews cwd before launch. Provider output is not
captured by this setup adapter; providers and terminal software may keep logs.
Preview login retains the existing prepared-provider-state gate.

`wt setup --json` supplies the same read-only detection and command descriptions
for scripts. Install/authentication are deliberately human-only handoffs, not
model actions. Existing `agent_start` / launch-plan typed controls retain the
normal policy and authority boundaries after setup.

`wt claude --cwd DIR` starts a scoped parent on a missing local wing.
`wt codex --cwd DIR --name review` uses the existing sandboxed egg route and
supports its detach/JSON flags. No Wingthing login or relay is required for local
inventory; provider inference/authentication may require internet or billing.
`Ctrl+B`, then `q` detaches a viewer; `x` requires explicit `y` to stop work.
Recent durable runs are available through `h`; recovery never replays a task.
