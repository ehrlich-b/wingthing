# Claude native policy (experimental)

An egg can opt into a Claude Code policy compiler while retaining Wingthing's
outer sandbox:

```yaml
harness:
  driver: claude-code
  version: 2.1.285
  permission_mode: dontAsk
```

Pin the exact installed version. The compiler accepts the 2.1 series from
2.1.285, requires Linux or macOS, and checks `claude --version` before provider
startup. A different artifact or unsupported argument fails admission. Eggs
without `harness` keep their existing launch behavior, including existing
permission flags. Bases inherit the block; a child may replace it as a unit.
Invalid native configuration fails closed during discovery.

`permission_mode` supports `default`, `acceptEdits`, `plan`, and `dontAsk`.
The default is `default`. For unattended work, `dontAsk` denies tools needing
approval. `dangerously_skip_permissions` conflicts with this driver. The native
path owns approvals; Wingthing's interactive Bash approval hook is omitted.

The compiler translates the resolved egg filesystem denials into Read/Edit
permission rules and native read/write denials, translates writable mounts into
native write grants, and renders the effective domain allowlist, including
attributed provider domains. Absolute native sandbox paths use `/path`;
absolute permission patterns use `//path`. Relative egg paths anchor at the
configured working directory. Pattern characters in literal roots are rejected.

Native sandboxing is enabled, missing dependencies are fatal, unsandboxed retry
and command exclusions are disabled, and weaker isolation options stay disabled.
User, project and local settings sources are excluded for this invocation.
Passthrough options are limited to model, effort, exact session identity, and
inline model/effort preference JSON. File-based settings, hooks, plugins,
additional directories and permission overrides are refused. Existing isolated
model-policy preferences remain supported. Managed policy can still override
CLI settings; this is one reason the outer boundary remains required.

The production preparation step merges the generated policy with lifecycle
hooks into the private session `claude-settings.json`, exposes that file read-only
to the outer sandbox, and supplies its path through `--settings`. It does not
write the user's or project's settings. `harness-policy.json` records the native
policy, its SHA-256, scope owners and limitations. The hash covers the policy
before lifecycle hooks are added. `wt doctor launch --json` includes the same
policy when digest-bound version evidence is supplied; dry preparation never
reads user settings or credentials and never launches the provider. Auto updates
are disabled for this invocation.

This is supplemental isolation. Native Bash cwd/temp defaults, regex profile
holes, read-only mount hierarchies, rename protection, executable roots and
symlink behavior are not a translation-equivalence claim. Wingthing's outer
backend continues to enforce the complete egg policy across startup, shell,
file tools, MCP, LSP, hooks, setup, git helpers and subagents, plus resources and
network traffic for local processes. Remote MCP servers and remote tasks remain
external and unverified. Privileged host tool sockets are refused by this driver
because their callbacks execute outside the egg. There is no automatic outer-only or unrestricted fallback if
the nested native sandbox fails.

Run offline compiler/preparation conformance with:

```sh
go test -p 2 ./internal/egg -run '^TestClaudeHarnessConformance' -count=1
```

These fixtures test configuration, version admission, policy serialization,
argument safety, lifecycle composition and v1 compatibility. They do not prove
that an installed Claude sandbox works inside the selected backend. Before
using this experimental mode for enforcement, verify nesting and positive and
negative filesystem/network canaries with the pinned artifact in that backend.
The policy report keeps native runtime coverage unverified until that happens.

References: [Claude sandbox](https://code.claude.com/docs/en/sandboxing),
[settings precedence](https://code.claude.com/docs/en/settings), and
[CLI options](https://code.claude.com/docs/en/cli-reference).
