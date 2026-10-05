# Personal preview provider setup

`wt-preview provider setup-guide claude --json` prints the exact provider home,
configuration directory, OS home, preparation commands, and a Wingthing login command for
the selected preview state. It does not initialize state or run the provider.
An initially empty preview profile requires a deliberate personal login;
existing host or organization credentials are not imported.

Choose a short state path on the execution machine, then inspect its guide:

```sh
WINGTHING_DIR=/tmp/my-personal-preview wt-preview provider setup-guide claude --json
```

Use a durable personal directory instead of `/tmp` for ongoing use. The guide
selects `STATE/provider-home` as `data_home` and its `.claude` directory as
`CLAUDE_CONFIG_DIR`. On macOS the shared OS-context helper obtains the actual
account home by UID for vendor `HOME` (`os_home`); data directories and cwd stay
under `data_home`. On Linux vendor `HOME` remains `data_home`. Both preparation
and follow-up inspection retain the exact state selection. The printed login
command uses the first-class `wt-preview provider login claude` route. Its only
vendor command is `auth login --claudeai`. The released Claude Code 2.1.287 command definition
supports that explicit Claude subscription flag and `auth status --json`.
The [official CLI reference](https://code.claude.com/docs/en/cli-reference)
documents subscription login and the status command's JSON/exit-code contract.

Arrange a time for the human browser phase before running the printed login
command directly in a private terminal. The human selects and confirms the intended personal subscription
account, completes OAuth, and returns to the terminal. Stop if the browser
shows an unintended organization account. This guide does not authorize an
agent to open the browser, complete login, copy a Keychain entry, import
configuration, or change sandbox permissions. A separate browser handoff must
have its own authorization and timing.

The login route requires an already-prepared, validated preview marker and real
provider home owned by the caller. Standard input, output and error must share
the user's foreground controlling terminal. Pipes, known Wingthing/agent
environment markers, known agent process ancestry, and Wingthing browser shims
are refused. The vendor inherits those terminal file descriptors directly;
this route creates no egg, PTY recorder, browser request shim, hooks, MCP,
lifecycle spool, transcript, or captured vendor output.

This is not a universal secrecy guarantee. Terminal software, shell hooks and
vendor tools may record their own output, and arbitrary capture software cannot
be identified reliably from terminal/process metadata. Do not launch login from
an agent's captured exec tool or a Wingthing session, even if a terminal is
available. Do not paste an OAuth callback or device code into chat. The vendor
subcommand handles browser sign-in; Wingthing adds no browser control or policy
override. No automatic login is added to startup or agent launch.

The macOS route depends on the matching coherent OS-context/runtime boundary.
Provided metadata shows that relocated `HOME` loses the user default
Keychain selection; a completed OAuth flow can then fail to save credentials.
Changing only login `HOME` would leave status and runtime inconsistent. The
vendor also probes host `HOME/.claude/ide` even with `CLAUDE_CONFIG_DIR` set, so
the matching native runtime denies host `.claude` and `.claude.json` discovery
and refuses conflicting mounts. This login command is the vendor auth subcommand
directly in the private terminal; it does not create an agent sandbox or start
its IDE/session initialization. No Keychain defaults, search lists, lock state, files or preferences
are changed or imported by Wingthing. The vendor performs the deliberate login
write to its selected per-directory credential service. Complete the source and
boundary review before a real login; these fake tests do not prove real saving
or entitlement.

After the human login, run the guide's scoped inspection command, or:

```sh
WINGTHING_DIR=/tmp/my-personal-preview wt-preview provider status claude --json
```

This explicit command invokes only `claude auth status --json`, with cwd in the
selected data home, a fresh environment and fixed canonical `CLAUDE_CONFIG_DIR`.
Its `HOME` follows the shared OS-context selection described above. It drops
ambient API keys, OAuth tokens, provider routes, proxy configuration and secure
storage overrides. Supported
[nonessential traffic settings](https://code.claude.com/docs/en/env-vars) disable
telemetry, error reporting and nonessential traffic for this invocation. The
vendor subprocess has a five-second timeout by default (configurable up to
30 seconds) and separate 64 KiB output limits. Raw stderr and unrecognized JSON
fields are discarded. It returns allowlisted account/profile metadata only
after the vendor reports the expected configuration directory. No automatic
status check is added to startup or agent launches, and these commands do not
contact a Wingthing server.

| Reported state | Next action |
| --- | --- |
| `profile_not_initialized` | Inspect the guide and manually prepare its exact state/home. |
| `not_installed` | Deliberately install a supported Claude CLI on this execution machine. |
| `reported_not_logged_in` | Use the manual login guide; also consider vendor Keychain or sandbox access errors. |
| `reported_authenticated` | Confirm reported email, organization and subscription belong to the intended personal account before a model call. |
| `unknown` | Resolve the diagnostic; do not interpret an error or mismatched directory as a missing login. |

A successful inspection command exits zero even when the report is unknown or
not logged in; scripts must inspect `state`. Invalid CLI arguments or invalid
preview state fail the command. Vendor-reported authentication does not prove
token freshness, personal account ownership, model access or entitlement.
Authentication is not used as a prelaunch gate: Keychain access failures under
a sandbox can look like no login. The
[credential documentation](https://code.claude.com/docs/en/authentication#credential-management)
states that `CLAUDE_CONFIG_DIR` also selects the macOS Keychain namespace and
[supports multiple accounts](https://code.claude.com/docs/en/authentication#log-in-with-multiple-accounts).
The implementation and fake-provider tests prove profile selection and output
handling. The October 3 frozen Mac candidate `3c16344` completed an authorized
private-terminal login and scoped status inspection, but its actual native
provider returned `authentication_failed` before any API usage. Successful
helper status therefore does not establish credential access from the native
runtime. A later normal official CLI request outside the inner native sandbox
also returned `Not logged in` with zero API usage. A native-only denial is not
established. The controlled comparison then isolated `USER`: the original
login/status context omitted it and remained authenticated, while adding only
`USER` selected a different local credential account and reported no login.
Credential-item metadata and normal vendor status both succeeded inside the
unchanged native sandbox with the original context. The failed direct request
did not establish lost persistence.

Mac preview login/status and final native/headless environments now consistently
omit `USER`, matching the approved login context. The existing vendor chooses
its fallback account; Wingthing does not hardcode one, copy credentials or widen
sandbox access. Fake-provider regressions and source review cover this repair.
Actual useful native model output remains a separate acceptance gate.

For a later authorized personal model check, the
[model documentation](https://code.claude.com/docs/en/model-config) lists
`claude-opus-5-5` and requires Claude Code 2.1.280 or later. The inspected local
2.1.287 release meets that version requirement. This setup guide makes no model
request and does not establish that the chosen account can use that model.

Run `make test-preview-provider` for exact-argv and error/timeout/output tests
plus a built-executable journey. Every provider subprocess in that target is
a temporary fake. Login process/stream tests inject terminal checks and exercise
only a fake vendor; the built CLI proves captured login is refused. These do not
establish real OAuth, Keychain access, or every external capture boundary.

Run `make test-preview-context` on macOS for the composed fake-provider journey:
status, native egg and headless run, each with `/tmp` and `/private/tmp` state
spellings. It observes the final process environment, canonical configuration
identity, selected workspace cwd, native history and executed lifecycle hook
spool. Synthetic path tests also cover symlink aliases, missing path suffixes,
lookalike sibling paths and guarded broad mounts. The composed fixture was
authored by Codex; the additional Go path tests were authored in a completed
`claude-opus-5-5` source review and then reviewed and run locally.

To test an already-frozen candidate without rebuilding it, set
`PREVIEW_CONTEXT_BINARY=/absolute/path/to/wt-preview`. The target never performs
real authentication or proves Keychain access. On October 3, all six routes and
the additional Go tests passed against the unchanged frozen `3c16344` binary
with scoped execution permission for disposable fixture sockets and sandboxes.
