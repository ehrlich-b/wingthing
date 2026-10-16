# PR #47 Linux CI repair

Base: `d0bc2b7614c412df8ba03554b17065caaef91777`.
Compared with main `1eda0ddb` (PR #46).
Read the complete failed-job logs from [run 38195328965](https://github.com/ehrlich-b/wingthing/actions/runs/38195328965).

## Reported failures

### TestNorthstarPTYAttachReturn and TestNorthstarPTYAttention build deadlines

These were nested **build** deadlines, not attachment, TMPDIR, or terminal timing
failures. The first build downloaded all modules; the next was still compiling
`internal/pake`'s new BoringSSL C++ code. `northstarBuild` forcibly selected empty
`.scratch/northstar/go-build` and `go-mod` caches, discarding the caches populated
by the outer Go invocation and CI setup. Main has no Northstar tests or this
helper. The Linux host reproduced AttachReturn at 60.02 seconds in `make check`, and
Attention at 60.02 seconds in the focused race run.

The helper now inherits the invoking build environment and its cache selectors.
The disposable application's environment remains separate. Its one-minute build
deadline, build cancellation checks, symlinked TMPDIR launch, and socket-length
assertions are unchanged.

### TestNorthstarPTYAttention race

`northstarStartPTY` drained emulator query replies with `Emulator.Read` while
cleanup called `Emulator.Close`. The dependency's `closed` flag is unsynchronized;
the fixture mutex did not protect its drain goroutine. CI reported this race;
The Linux host also reproduced it in RemoteAttach, which shares the helper.

Cleanup now closes the emulator's query pipe, joins its reader, and only then
closes the emulator. Terminal rendering, hook publication, bell counts, and
privacy assertions remain intact.

### TestNorthstarPTYRemoteAttach race and lost navigator return

The fake SSH transport's SIGWINCH worker called `pty.Setsize(master)` (and hence
`master.Fd()`) while the output reader released the closed master descriptor.
The race-instrumented SSH child exited with code 66, overwriting the expected
navigator screen with its race report. CI's stack names both operations; the Linux host
reproduced the failing SSH child and shared emulator race.

The SSH fixture now stops signal delivery and joins its resize worker before
closing the master. Exact SSH routes, detach return, selection, resize, and
termios assertions are unchanged.

### TestPreflightSandboxCheck on Debian and Ubuntu

The AppArmor rejection was the **expected negative case**. Main already performs
the same required sandbox capability check and returns `wt doctor --fix` guidance.
This branch added launch-diagnostic projection, whose `Error()` replaced that
message with `launch failed during provider startup: sandbox_unavailable`.
The test correctly rejected the missing repair instructions.

The bounded diagnostic now includes a fixed repair instruction for
`sandbox_unavailable`; the formatted Claude diagnostic does too. It never copies
the underlying provider/configuration error. A regression check verifies local
and reconstructed wire errors, repair guidance, and exclusion of a private cause.
No sandbox requirement, CI sysctl, AppArmor policy, or test assertion was relaxed.

### Failures hidden behind the container preflight failure

The Linux host's full Debian battery reached the compiled CLI tests and found that the new
Northstar helper tried to invoke Go inside a runtime-only image. Preview binaries
and the new run-inspect snapshot fixture were also absent. Main lacks these
Northstar and run-inspect tests.

`e2e-linux` now builds both CLI channels and stages the complete snapshot tree
into both images. Northstar accepts the existing explicit binary selectors and
copies the selected CLI under its test-created TMPDIR alias. Both channels still
execute the real CLI and retain their channel assertions. No toolchain or live
provider is needed inside the container for these fixtures.

## Additional failures exposed by the Linux host's namespace-capable kernel

These tests also exist on main, whose Ubuntu host jobs skip native sandbox tests
when AppArmor rejects the capability probe. The Linux host can create the namespaces.

* `TestClaudeLifecycleRefusesHostSettingsOutsideFinalPolicy/allowed`: the egg
  test binary had no `_deny_init` dispatch. Sandbox re-execution entered the test
  runner instead of the enforcement wrapper. A Linux-only TestMain now dispatches
  the actual wrapper, matching the production CLI. With race instrumentation,
  repeated capability-probe re-execs also slept one second at exit, consuming the
  three-second launch deadline. The Linux fixture passes `atexit_sleep_ms=0` to
  descendants, preserving all other GORACE options and race detection. The same
  allowed case passed 3 times with this setting; its deadline stays three seconds.
* `TestNativeRunPTYResizeKeepsInputCancellable`: one EAGAIN did not prove the
  master's queue would remain full. Linux asynchronously transfers bytes into the
  slave queue, letting the ten-byte write finish before cancellation. This failed
  13 of 30 local repetitions. The fixture now sends 640 KiB with no slave reader,
  exceeding both queues and requiring a blocked write. The original cancellation
  and nonblocking-after-resize assertions remain. It passed 30 repetitions.
* The native race run also exposed real Linux teardown races: relay `Fd()` versus
  `Close`, and two callers of sandbox `Destroy`. Host relay reads now use a
  nonblocking socket and `SyscallConn.Read`, retaining atomic close-on-exec receipt
  of passed descriptors. Close wakes and joins the receiver, and peer EOF ends it.
  Sandbox destruction uses `sync.Once`. Regression checks exercise idle shutdown,
  peer exit, and simultaneous destruction.

## Validation

CI's original logs used Go `1.26.9`; no macOS validation is claimed here.

The three reported PTY tests passed three times with `-race -count=3` on the final
source (75.582s; an earlier three-run check also passed in 83.708s).
The additional native egg/relay tests passed three times with the race detector.
Both namespace-failure tests passed three times in each distro's ordinary
Docker security boundary. The Linux host has no AppArmor userns sysctl, so this negative
reproduction uses Docker's namespace rejection; the same product reason and
repair assertion run without a skip. The privileged batteries exercise namespace
success separately. Existing platform/capability skips are preserved; none added.

Final gates all exited 0:

| Command | Evidence |
| --- | --- |
| `make check` | `acceptance-check.log`; cmd/wt 150.202s; internal/egg 23.156s; web tests/build and final CLI build passed |
| `make gate GATE=static RACE_BASE=origin/main` | `acceptance-static.log`; vet, all Go packages changed from main under race, govulncheck, npm audit, release contract passed |
| `make e2e-linux` | `acceptance-e2e.log`; full Debian and Ubuntu privileged batteries passed |
| Reported PTY selection, `-race -count=3` | `acceptance-pty-race.log`; every selected test passed 3 times |
| Both namespace-failure tests, `-test.count=3` | `acceptance-preflight-debian.log`, `acceptance-preflight-ubuntu.log`; every selected test passed 3 times, no skips |
| Native egg and relay regression selection, `-race -count=3` | `verified-native-race.log`; both packages passed |

Retained logs and the tested source diff are in `.scratch/ci-linux/`; their full
SHA-256 receipts are in `.scratch/ci-linux/SHA256SUMS`. Key receipts:

```text
source.diff               24f062918ba8e5aba9594fdbba42ab7a7dcb30c6ebcf9322088d968161b96771
acceptance-check.log      726b0e72de129f33d9a83472f8bcece229eaa4a3f14e43f78649f91f54c2bafb
acceptance-static.log     b96d3dbd7dad38b6941b56d02414a186856a0dcb4b27933141d310fb89531fa5
acceptance-e2e.log        c9353d0ca3fbeaf04ff0f8aa3a12afbd6b0051b5e84f0893d13304d93c2a024f
acceptance-pty-race.log   58528c99f5bc96cb64486a6b65c8c78ada063b8b41c36e4cbea0fd834b3e2d54
```

The source diff excludes this report to avoid a self-referential hash. Full
acceptance commands ran after the final code edits. The initial static invocation
overlapped early edits and is retained as an investigation log, not pristine
baseline evidence; the original CI logs and focused baseline run establish the
race failures. One intermediate full check also saw an initial Codex receipt
fixture fail with `unknown_outcome`; it passed 20 unchanged repetitions and the
final full check and race gate. No assertion or deadline was changed for it.

macOS-specific sandbox fixtures and their existing conditions remain intact;
native macOS acceptance is separate. No new test skips were introduced.
