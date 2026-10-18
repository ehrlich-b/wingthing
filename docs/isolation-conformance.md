# Native isolation conformance

`make test-isolation` runs finite offline canaries against Wingthing's current
Linux sandbox. It uses the production egg environment selection, launch
preparation, sandbox factory, native wrapper and post-start boundary. No vendor
CLI, model, account, production endpoint or new infrastructure is required.
Python 3, Go and a host capable of the native Linux sandbox are prerequisites.
Use a disposable HOME and TMPDIR when running on a development host.

Each invocation retains `go-test.jsonl` and `report.json` in a fresh directory
under `.scratch/isolation/`. The report records the source commit and content
digest, platform/kernel, UID, Go version, exact command, per-test outcomes and
known limitations. A build failure, missing test, skipped canary, source change
or unsupported platform returns nonzero. A successful Go package with skipped
resource probes is **incomplete**, never full conformance.

| Boundary | Probe and positive control | Current Linux expectation |
| --- | --- | --- |
| Writes outside workspace | Write workspace, HOME sibling and outside-HOME fixture; overwrite egg.yaml; inspect policy bytes afterwards | Workspace writable; HOME sibling and policy denied; OS-user-writable outside-HOME path remains writable (known gap) |
| Secret paths | Plant canaries in all eight default denied paths and a workspace symlink to one; read an ordinary HOME file | Denied canaries never visible; ordinary HOME remains readable |
| Disallowed egress | Live host loopback HTTP fixture, no-network dial, allowed CONNECT, denied `.invalid` CONNECT, direct dial ignoring proxy settings; inspect proxy audit | Positive CONNECT returns fixture bytes; no-network and bypass fail; blocked domain returns 403 and an audit event |
| CPU and FDs | Query soft/hard limits; exhaust FDs; run a bounded CPU child | 32 FDs and EMFILE; one-second CPU child killed by SIGKILL |
| Memory and PIDs | 64 MB cgroup vs 160 MiB child; 16-PID tree vs at most 40 sleeping children with cleanup | OOM child killed; process creation stops at EAGAIN; lack of delegation is an explicit unrun probe |
| Credential leakage | Synthetic allowed env, unselected cloud secret, SSH-agent selector, provider API key and provider auth file | Allowed env retained; unrelated secret and SSH selector stripped; own provider key/file accessible (known gap) |

The filesystem and credential gaps are observations pinned by the baseline,
not stronger isolation promises. These are tests of Wingthing's existing
sandbox, not tests of installed providers' own sandbox implementations or a
native-config translation layer. Future backends can run the same finite
operations and compare receipts. macOS and installed-provider conformance remain
separate work; this initial target intentionally cannot pass on other platforms.

## Baseline on 2026-10-18

On Linux amd64, kernel `6.12.111+deb13-cloud-amd64`, as an unprivileged user,
the write, secret, egress, credential, CPU and FD canaries completed with the
expectations above. The two resource-tree canaries were refused before exec:
creating a child cgroup returned `permission denied`. Go reported those probes
as skipped; the strict target returned 1 and reported **INCOMPLETE/FAIL**.
There was no fallback, cgroup replacement or weakening of resource policy.
A delegated Linux host is still needed to establish memory and PID enforcement.
The per-run source-bound report is the authority for the exact checkout tested.
