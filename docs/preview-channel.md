# Personal preview channel

Preview is a separately compiled `wt-preview`, not a renamed stable `wt`.
`make release CHANNEL=preview` creates local artifacts without installing, publishing,
tagging, enrolling a wing, or changing a running service. The local candidate
version defaults to `v0.148.0-preview.YYYYMMDD.gCOMMIT`; override
`PREVIEW_VERSION` when building the composed candidate. This does not reserve a
public stable release version.

| Boundary | Stable | Preview |
| --- | --- | --- |
| Executable | `wt` | `wt-preview` |
| Default state, auth, sessions, sockets, PID and daemon lock | `~/.wingthing` | `~/.wingthing-preview` |
| Provider data home | Existing personal provider home | `STATE/provider-home`, initially clean |
| Claude subprocess HOME | Existing OS account home | OS account home on macOS; data home on Linux |
| Personal portal HTTP / HTTPS defaults | 8080 / 8443 | loopback 8180 / 8181 |
| Update selection | GitHub latest stable release | published, non-draft `v*-preview.*` prereleases |
| Update assets | `wt-OS-ARCH` | `wt-preview-OS-ARCH` |
| Organization / shared coordinator | Existing behavior | Disabled in this first preview |
| Host sandbox policy installer | Existing `doctor --fix` behavior | Refuses to install or replace shared policy |

`wt-preview channel --json` reports channel, version, state selection and update
feed without initializing state. Browser `/api/app/me` advertises
`release_channel`, `executable`, and `channel_label` for an explicit preview UI
banner. Its cookie name is `wt_preview_session`, so preview login/logout cannot
replace stable localhost cookies (ports alone do not isolate cookies). Generated
browser shim wrappers are regular files. Provider debug/history links are supported
when their resolved target stays inside the selected preview state tree.
Provider settings, shell files, tokens, environment credentials and
platform credential environment are not copied from the host. Read-only
provider executables may be shared; authenticating a clean preview provider home
is a separate deliberate setup action. Existing Slide Claude authentication is
not a personal preview login route.

`wt-preview provider setup-guide claude --json` prints preparation and first-class
`provider login claude` commands for that exact profile. Login requires a private
user-owned controlling terminal and refuses detectable managed/recorded contexts;
macOS login uses the matching shared Keychain/runtime context. Source review and
human authorization precede any real login.
The explicitly invoked `provider status claude --json`
returns bounded, allowlisted vendor metadata; errors and uncertain namespace
results remain `unknown`. These commands are preview-only and do not gate agent
launches. See [personal provider onboarding](preview-provider-onboarding.md) for
the human browser handoff, reported-state meanings and verification limits.

Direct preview tasks explicitly set `CLAUDE_CONFIG_DIR=PROVIDER_DATA_HOME/.claude`
for Claude and `CODEX_HOME=PROVIDER_HOME/.codex` for Codex. Interactive preview
Claude sessions already select that same `.claude` directory. Claude documents
that an explicit `CLAUDE_CONFIG_DIR` also selects a different macOS Keychain
entry; a clean `HOME` alone does not establish that credential namespace. The
provider CLI reports explicit `data_home` and `os_home` fields: on macOS its
shared OS-context helper preserves the actual UID-account home for Keychain
selection while the configuration directory stays in the canonical preview data
home. This needs the matching reviewed runtime host-configuration denial; an
authentication-only HOME substitution is insufficient.
See [Claude credential management](https://code.claude.com/docs/en/authentication#credential-management).
Codex documents [`CODEX_HOME` as its state directory](https://developers.openai.com/codex/config-advanced/#config-and-state-locations);
its [0.159.3 credential storage source](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/login/src/auth/storage.rs)
also derives the keyring entry from that directory. These are per-invocation
environment settings, with no global settings, credentials or plugins copied.
Pure environment tests establish directory selection and rejection of ambient
credentials; live provider login and Keychain isolation remain unverified.

A preview provider's configured MCP can reopen its existing parent state while
`HOME` names exactly that state's real `provider-home` directory. This requires
a regular preview marker and the full state validation, including nested links;
linked markers or provider homes do not qualify. The OS-account stable tree
remains forbidden even with relocated `HOME`, and `HOME/.wingthing` cannot be
adopted as preview state. This does not change the provider's home, import
authentication, or grant a sandbox exception. `make e2e-mac` runs
the actual macOS preview parent fixture through configured MCP initialization
and child reservation, then verifies the existing nested-proxy denial.

State directories are marked preview. An explicit `WINGTHING_DIR` is accepted
only for an empty/previously marked preview directory, and may not overlap the
stable default tree, including symlinks. `WINGTHING_PREVIEW_DIR` is an optional
preview-specific selector; conflicting selectors fail. Stable refuses marked
preview state. State symlinks must resolve to existing targets inside that exact
preview tree; dangling, looping, and foreign/stable targets fail. The narrow
external-link exception is an existing executable regular file linked from
`provider-home/.local/bin`. A copied stable egg
PID cannot become a preview session: preview verifies its executable and session
identity before listing or attaching. Daemon stop/restart/update verifies the
same executable identity. These are channel/lifecycle guards, not a claim that
an arbitrary workspace or provider is a new security boundary.

Session socket addresses must fit the OS limit: 103 bytes on macOS and 107 on
Linux. CLI launches check the exact `STATE/eggs/SESSION/egg.sock` path before
creating state; MCP/runtime launches check before creating an egg or process.
Errors include the path, byte count and limit, with a shorter `WINGTHING_DIR`
suggestion. Preview also accepts `WINGTHING_PREVIEW_DIR`. Use a short dedicated
state root when the checkout path is long; state need not live in the checkout.
MCP configuration and refusal audit records retain their normal behavior.

On the two admitted Ubuntu guests, an isolated temporary preview executable
failed the unprivileged mount capability probe before session creation. Existing
SSH transport worked. The preview's `doctor --fix` refuses the shared Linux
installer because it uses the stable `wingthing` AppArmor profile. A separately
reviewed root-owned preview executable and executable-specific profile is a
live host setup action; see [the archived access review](archive/personal-access-review.md). No host policy was
changed and no outer-boundary fallback was used for those tests.

## Local package journey

Build and verify from an isolated checkout:

```sh
make check
make gate GATE=integration
```

The black-box proof uses short disposable `/tmp` directories, fixture credentials,
one real sandboxed stable command session and a loopback preview daemon. It checks
read-only inspection, overlap/symlink/import rejection, stable-session metadata
rejection, install, checksummed upgrade/restart, stop, and uninstall. Stable
binary and sentinel/token hashes must match before and after, and its fixture
session remains live until test cleanup. It needs ordinary local process/socket
permissions; it never runs a provider model or contacts an org.

Review the local package before choosing an install destination:

```sh
WT_PREVIEW_INSTALL_DIR=/chosen/personal/bin dist-preview/preview-package.sh install
/chosen/personal/bin/wt-preview channel --json
/chosen/personal/bin/wt-preview roost start
```

The package installer verifies its checksum and compiled identity before atomic
replacement. Its only installed name is `wt-preview`; stable `wt` is untouched.
For remote SSH entry, use a matching preview binary on the remote wing.
`--expected-channel preview` is checked before any remote state access, including
when `--remote-binary /staged/path` is used. Older stable executables fail closed.
No provider credentials or workspace are transferred by SSH routing.

## Upgrade, rollback, and promotion

There is no published preview feed implied by this local build. When publication
is separately authorized, use a unique `v*-preview.*` GitHub prerelease with
preview assets and SHA256SUMS. The stable `/releases/latest` route excludes these
prereleases. Preview selects only published preview prereleases; absence fails
with an explicit local-package route, without selecting stable.

Before publication, upgrade or binary rollback with a reviewed local package:

```sh
wt-preview update --file /reviewed/package/wt-preview-OS-ARCH
```

The updater verifies checksum and compiled channel, snapshots the preview daemon
restart arguments before replacing the executable, and restarts only that daemon.
It rejects stable executable names and symlink/hardlink aliases of a neighboring
stable `wt` (the aliases covered by the regression proof). Keep a backup of
preview state before a candidate that changes its schema: arbitrary schema
downgrade is not promised by binary rollback. There is no automatic migration
between stable and preview, including credentials or sessions.

Stop preview before uninstalling its executable:

```sh
wt-preview stop
dist-preview/preview-package.sh uninstall
```

Uninstall removes only the verified preview executable and retains all state.
Promotion means reviewing and selectively applying source commits to mainline,
then running the stable compatibility gates. It never renames the preview binary
to `wt`, reuses its state, or blindly merges an experimental branch.
