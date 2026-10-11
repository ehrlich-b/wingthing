# Terminal commands, toolchains, and service lifetime

These notes describe the six terminal gaps from the forge dogfood run. Commands
sent over SSH use the receiving machine's directories and launch environment;
the local client's toolchains and service bus are not copied to that machine.

## 1. Commands relative to the working directory

An explicit relative executable containing `/`, such as `./run.sh` or
`bin/run.sh`, is resolved against the egg's `--cwd` (`-C`) before executable
validation and sandbox startup. Thus `wt -r forge terminal -C /dir -- ./run.sh`
executes `/dir/run.sh`. Symlinks and `..` retain filesystem traversal semantics;
bare names still use PATH lookup. The regression runs the selected script in a
different egg working directory through a symlinked parent.

## 2. Toolchain homes are explicit environment policy

The default environment allowlist deliberately omits `ELAN_HOME` so a launcher
or coordinator's cache setting cannot override elan's normal home selection;
Wingthing neither invents `/work/cache/elan` nor maps a toolchain home into a
cache directory. An explicit `env: [ELAN_HOME]` entry inherits the receiving
launch process's value verbatim, and `env: ['*']` inherits it along with the rest
of that process's environment. If a terminal sees the wrong value, inspect the
effective egg policy and the remote launch environment (including shell startup
files); remove the inherited setting to use elan's default, or set the receiving
launcher's `ELAN_HOME` to the directory holding the pinned toolchain and allow
that variable explicitly. For wing-owned launches, this is the wing's startup
environment. Grant filesystem access separately if the toolchain is outside the
egg's readable paths; environment inheritance does not grant mounts. Regression
coverage checks omission by default, preservation when explicitly allowed, and
absence of changes to the launching user's home or toolchain variables.

## 3. User systemd belongs to the host

Omitting `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS` from the default egg
environment is intentional: access to the host user manager would allow an
egg to start processes outside its PID namespace and filesystem policy, and to
control unrelated user services. Environment filtering does not guarantee that
every host Unix socket is inaccessible under a permissive filesystem policy.
Exporting these variables alone does not create an in-egg user manager or
guarantee socket access. Run `systemctl --user`
from the host's logged-in user session, including an ordinary SSH session with
that user's runtime directory and active bus, when managing host services. If
the whole machine is an intentional outer sandbox, a terminal started with
`--unsandboxed` inherits the launch environment and can use an already available
host user bus; that selects the host boundary for the entire terminal. Regression
coverage confirms that ordinary terminals do not implicitly forward these two
service endpoints. Host service management is a separate operator action.

## 4. Process IDs inside Linux eggs

Every Linux egg now uses the existing PID-namespace init to mount a private
procfs before dropping privileges and installing seccomp. This fixes ordinary
eggs retaining host procfs while `getpid()` returned a namespace-local PID,
which caused procps to fail looking up itself. Inside an egg, `/proc/self`, `$$`,
and `ps` refer to that namespace; the host's `wt session ps` and host `ps` use
host PIDs, so their numbers need not match. The init is PID 1 and reaps orphaned
children, and the command is its child. Linux integration regressions check
self lookup, effective UID, a different PID namespace, exclusion of host PIDs,
working `ps`, and retained secret masks in ordinary and sealed eggs. A policy
mask obscured by the procfs swap refuses startup instead of silently disappearing.

## 5. What a successful session stop confirms

`wt session kill` waits for the direct session child to be reaped, after a
SIGTERM grace period and SIGKILL escalation when necessary; the existing ignored
SIGTERM regression verifies this. The response intentionally acknowledges this
process boundary rather than waiting for the egg server's later PTY closure,
snapshot restoration, diagnostic persistence, final lifecycle capture, and RPC
shutdown. The egg supervisor can therefore briefly remain in host `ps` after
the response, and group signal delivery is asynchronous. Check the exact host
PID and process state: an egg supervisor still doing cleanup or a zombie awaiting
its parent is different from a live command still doing work. For inventory
reconciliation, wait for the egg to disappear from `wt session ps`; a stop
response does not certify the absence of every detached descendant in
`--unsandboxed` mode. A live child that remains after cleanup is a failure to
investigate, rather than permission to report a fully stopped process tree.

## 6. A service needs an independent owner

A Linux egg owns its sandboxed process tree for the lifetime of its foreground
command, so ending that command tears down the Linux PID namespace and its
descendants; `nohup`, `&`, and `setsid` cannot extend that namespace's lifetime.
This is intentional ownership, including when an agent started the process.
To run a soak independently of an agent, launch a separate persistent terminal
from the host/client first, for example
`wt -r forge terminal --detach --name soak -C /dir -- ./supervisor.sh`, and keep
the supervisor in the foreground for its whole lifetime. Reattach with
`wt -r forge attach soak`, inspect with `wt -r forge session read soak`, and
stop it with `wt -r forge session kill soak`; the other agent egg can then exit
without owning the soak. Here `--detach` (or Ctrl+B, Q after attaching) detaches
the client while the egg continues, and does not detach a child from its egg.
For a host-managed Linux service, an operator can instead run
`systemd-run --user --unit=soak --property=Type=exec --working-directory=/dir /dir/supervisor.sh`
from the host user session; the [systemd service manager](https://github.com/systemd/systemd/blob/main/man/systemd-run.xml)
owns that transient service independently and supplies a clean environment, so
set any required toolchain variables through the service's environment policy.
Allowing sandboxed code to silently shed ownership would recreate the untracked
descendant problems in the related agent-run dogfood notes.

## Regression checks

Run `go test ./internal/egg ./internal/sandbox ./internal/eggclient ./cmd/wt ./internal/docscheck`
and `go vet ./...` on the development host. On native Linux,
run `go test -tags integration ./internal/sandbox -run 'TestSandboxPrivateProcfs|TestSandboxProcfsSwapRefusesLostPolicyMask'` as a normal
non-root user with user namespaces enabled as well as in the privileged Linux
battery; root-only execution can hide namespace mapping errors. These new tests
do not need loopback. Existing tests that bind TCP loopback in the affected
untagged suites are listed below; `TestPolicyProxyPreservesPlatformFullNetworkSemantics`
also needs it on Linux. Native sandbox checks require the OS to permit
sandbox/namespace creation; cross-compiling the Linux tests checks compilation
only. An execution sandbox that refuses host `ps` also cannot run
`TestDeadlineKillsProcessGroupAndReportsSurvivors` or
`TestUnboundedStopKillsProcessGroupAndReportsSurvivors`; these two tests require
host descendant inventory, not TCP loopback.

CLI fixtures create their short state aliases under a test-owned `/tmp`
directory instead of assuming the checkout's `.scratch` path leaves room for
an egg socket. Each fixture includes its own symlinked parent, and fixture
builds use the running test binary's Go toolchain.

| Package | Tests requiring TCP loopback |
| --- | --- |
| `internal/eggclient` | `TestContextThroughEggTools` |
| `internal/sandbox` | `TestProxyStartAndClose`, `TestPolicyProxyObserveModeIsExplicit`, `TestProxyAllowed`, `TestProxyRejectsNonCONNECT`, `TestProxyBlocksDeniedDomain`, `TestProxyBlocksAllowedNameThatRebindsToLoopback` |
| `internal/sandbox` | `TestObserveProxyRecordsButAllowsNamedLoopbackTarget`, `TestProxyCONNECTFlush`, `TestProxyCONNECTRawTCP`, `TestProxyCONNECTBadGateway`, `TestProxyConcurrent`, `TestProxyBoundsAndClosesActiveTunnels`, `TestProxySNIFiltering` |
| `internal/sandbox` | `TestProxyRecordsEgressEvents`, `TestProxyObserveModeDoesNotBlock`, `TestStartProxyDefaultsToEnforce` |

The broader tagged native battery additionally uses loopback in
`TestJail_ProxyAllowedDomain`, `TestJail_ProxyBlockedDomain`,
`TestJail_LinuxProxyBypassHasNoRoute`, `TestJail_LinuxDeclaredLoopbackPortForward`,
`TestJail_ProxyWildcard`, `TestJail_ProxySeatbeltEnforced`, and
`TestRealClaudeModelPolicyPreservesPersonalSettings`.
