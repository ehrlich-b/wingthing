# Read-only launch preflight

`wt doctor launch-plan` explains an explicit proposed launch before a model turn
is spent. It reads named policy/evidence files, stats explicit credential paths,
and hashes the selected executable. It never invokes that executable, discovers
a production wing, reads credential contents, logs in, fixes policy, creates
directories in the selected provider HOME, binds a socket, or changes daemon state.

The local planner is a deliberate intermediate consumer of the existing
policy/agent interfaces. It is not yet an MCP tool or a remote wing operation.
All inputs must name the intended workspace, HOME, allowed HOME scope, route and
executable. Relative CWD needs an explicit absolute `--path-base`. Omitted
`--config` explicitly selects built-in egg defaults; it does not discover
workspace/global configuration. Named bases need `--bases-dir`, with no legacy
wing fallback. Invalid/missing explicit policies fail closed. Policy traversal is bounded to
32 file reads, ten base levels and 64 KiB per input. The production
YAML parser and merger preserve old scalar/list/mapping network contracts,
section masks, deny override rules and agent-profile drilling. Discovery and
preflight share the resolver walk. `--merge-home` names the host HOME used for
inheritance overrides, while `--home` names the provider's execution HOME. When
omitted, `--merge-home` explicitly selects the same value as `--home`; ambient
HOME is never consulted. Supply the actual host HOME when these differ.

For an MCP launch inside a configured root, pass `--policy-base /configured/root`
together with that root's explicit `--config`. Relative grants and denials are
anchored there by the same helper as `PrepareMCPLaunch`. Omitting `--policy-base`
selects CWD for a direct launch. The JSON report records both selected inputs
and their provenance; no live wing is discovered to infer them.

```sh
wt doctor launch-plan --route agent_run --agent codex --unattended \
  --cwd /absolute/disposable/work --home /absolute/disposable/home \
  --allowed-home-root /absolute/disposable \
  --executable /absolute/installed/codex \
  --config /absolute/disposable/egg.yaml \
  --facts /absolute/disposable/facts.json \
  --provider-evidence /absolute/disposable/provider-evidence.json --json
```

There is no `--fix`, prompt, token, resume or passthrough argument path. An
optional `--provider-url` supplies routing metadata to the existing policy
resolver; only derived hosts are exported. `--provider-state-dir` describes an
explicit CODEX_HOME or equivalent override. `--credential-path` is repeatable
and only checks metadata/scope. Credential presence never proves authentication.

`terminal_start` plans a command session without agent auto-drilling.
`agent_start` uses the production interactive catalog. `agent_run` checks the
production native-run predicate: Claude or compatible native Codex hooks;
other adapters report a failed route even if their interactive CLI exists.
Both preflight and the real egg call `egg.PrepareLaunch`: policy resolution,
final sandbox specification, provider argv, directory preparation and pinned
state-reader opens share one path. Preflight runs the actual `MkdirAll` and
`O_NOFOLLOW` opens in a throwaway mirror of selected directory-entry metadata.
Symlink targets are rebased into that mirror, including dangling targets; file
contents and credentials are never copied. The mirror is removed on return and
is placed outside the inspected input trees to preserve their directory mtimes.
Codex's actual hook/notify builders record their required spools into the sandbox
specification. The Mac final policy builder checks those spools against its
emitted write rules, both during preparation and again when creating a sandbox.
Regular files, dangling links, state links refused by the native readers, and
callback output denials block preparation. The disposable `launch-plan` identity
makes argv stable. Actual IDs and prompts are assigned by the runtime.

`LaunchState.ClaudeSettings` isolates Claude's settings merge and file generation,
which may read credential-bearing caller settings and is deferred; the planner
prints only its base adapter arguments and marks that gate unrun.
`CodexCapability` isolates the installed version probe; preflight supplies saved
digest-bound output through the production parser. Native Linux mount operations
and a foreign platform's final policy builder remain explicitly unrun, as do
proxy listeners, snapshots, the control endpoint, PTY and provider execution.
The mirror establishes directory layout compatibility; host ownership, ACLs,
read-only mounts and confinement still require the declared facts/native gates.

Saved provider evidence is a bounded JSON object:

```json
{
  "binary_sha256": "SHA256 of the selected executable",
  "version_output": "codex-cli 0.159.3\n",
  "help_output": "bounded complete help captured for that executable",
  "complete_help": true
}
```

Only a matching executable digest allows version/help to support compatibility
gates. A changed/missing binary leaves that evidence unverified. Planned options
are derived from actual adapter argv and compared with saved help; there is no
second flag catalog. Missing complete-help options fail; incomplete/absent help
leaves them unknown. Raw help, unparsed version text, environment values, provider URL
query text and credential contents are never diagnostics. Value-like environment
selectors are redacted and flagged without changing the production parser. The export carries
input digests, safe planned argv, paths, provenance and typed gates.

Capability facts are explicit declarations, not results of probes inside this
command. Unknown/omitted states remain unknown:

```json
{
  "os": "linux",
  "network": "proxy-only",
  "workspace_writable": "supported",
  "provider_home_writable": "supported",
  "native_sandbox": "unknown",
  "parent_sandbox": "none",
  "user_bus": "unsupported",
  "requires_user_bus": false,
  "elan_home": "/absolute/disposable/home/.elan",
  "elan_home_writable": "unknown",
  "raw_sockets_required": false
}
```

Fact states are `supported`, `unsupported`, `unknown` or omitted. Network is
`none`, `proxy-only`, `full`, `unknown` or omitted. Parent sandbox is `none`,
`sandboxed`, `scoped`, `unknown` or omitted. Facts describe the proposed launch,
including any inherited outer restrictions; they are not inferred from this
command's ambient HOME or environment. The planner never repairs them.

| Input conflict | Diagnosis |
| --- | --- |
| Enforcing policy denies the declared/default provider destination | `network.provider: FAIL` |
| Parent network is none but egg drills provider domains | `network.parent: FAIL` |
| Raw sockets required under proxy/TCP relay | `network.raw_sockets: FAIL` |
| Outer workspace or provider state is read-only | Required write gate `FAIL` |
| Selected ELAN_HOME has no grant or denied writes | Toolchain path gate explains the limitation |
| ELAN_HOME is discarded by the real environment selector | Filtering gate `PASS`; unused inherited path cannot block launch |
| Required provider directory or ancestor is a regular file | Directory/preparation gate `FAIL` |
| Required provider directory or ancestor is a dangling link | Actual directory preparation `FAIL` |
| Mac deny-write pins an existing directory | Contents remain writable under their grant |
| Codex HOME/state/spool directory is a symlink | Native pinned reader preparation `FAIL` |
| Final policy denies a required Codex callback spool or its output | Preparation `FAIL` |
| Codex retains inner sandbox under an egg/declared parent sandbox | `sandbox.nested: FAIL` |
| Linux scoped parent | `parent.scoped: FAIL`, regardless of positive native facts |
| No user bus, ordinary launch | No bus requirement is invented |
| No user bus, explicitly bus-dependent mode | `user_bus: FAIL` |
| Unknown native capability | `sandbox.native: REQUIRED-UNRUN` |
| Symlinked HOME/state escapes allowed scope | Scope gate `FAIL` |

Effective denials cite the inherited policy file(s) that supplied them. Section
masks and overridden denies disappear from effective provenance. Repeated declarations retain each file source. The production resolver currently
rejects a repeated base path used by two sections; preflight reproduces that
refusal rather than changing runtime resolution. Recursive cycles also fail. Denials dominate write grants, with Linux root-deny mount exceptions explicitly
left unverified. Where platform-specific write rules
are not established by the explicit declarations, the result stays unknown.
Provider reachability uses the enforcing proxy's exact domain matcher, so
unrelated domains or forwarded ports cannot mask a denial of the default Codex
endpoint. An explicit supported provider URL replaces that destination; observe
mode does not turn an allowlist mismatch into a blocking gate. This diagnosis
assumes the declared/default endpoint; provider configuration is never opened.
Linux wildcard network inside an egg remains proxy-only; Mac wildcard network
is reported separately. Neither inference establishes live confinement.

JSON `readiness` is `blocked` when any gate fails and `unverified` when required
evidence is absent. Individual `PASS` gates establish local facts or compatibility
with supplied evidence. Provider authentication is always external. A command-only terminal may be
`ready-local` from explicit declarations; `acceptance: externally-pending` always
remains separate. This command never emits live-ready status. Human output renders the same gates and provenance.
The command exits successfully after rendering a diagnosis; scripts should read
`readiness` and each gate's status. Contract/input errors exit unsuccessfully.

# Disposable outside-worker acceptance

The worker must not run these native commands. The coordinator uses the same
clone and private cache environment, with all mutable state below `.scratch`.
The preparation script below is a concrete native/built-CLI handoff; it creates
fresh state, captures saved version/help only when an executable is explicitly
selected, checks help/JSON/human output and verifies unchanged inventory around
preflight. It performs no model call, login or credential copying.

```sh
source .scratch/env.sh
python3 -B scripts/launch-plan-acceptance.py \
  --wt "$PWD/.scratch/wt-launch-plan" \
  --root "$PWD/.scratch/launch-acceptance" \
  --provider /absolute/installed/codex
```

Native confinement and relay checks use the existing sandbox tests outside the
worker. This exact target uses a fresh disposable HOME/TMPDIR, requires nonzero
tests and rejects skips as missing acceptance:

```sh
source .scratch/env.sh
taskpolicy -b nice -n 15 make -j1 accept-launch-plan-native GO=go
```

It does not start a wing, provider or browser. Native tests can bind disposable
loopback transports outside the worker. Mac and Linux execute their own native
tests; one host's fixture results do not promote the other host.

An authorized real provider **tool-call** canary is separately operated. No
provider tool-call receipt is present in this row. The existing
`scripts/real-codex-canary.sh` is a **no-tools** provider completion canary and
cannot satisfy that gate. The coordinator must supply the previously authorized
exact tool-call canary command/receipt; no new model call is authorized here.

The 21:55–22:30 my-server incident entries and full raw help captures for two saved
provider versions were not found in the available committed input RUNLOGs.
They remain REQUIRED-UNRUN source evidence. Synthetic version/help fixtures
exercise compatibility and redaction locally, without impersonating recordings
of installed providers. Local fixture readiness and product acceptance are
reported independently in the row receipt.
