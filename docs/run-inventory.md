# Recovering durable run IDs

`agent_list` uses the existing `agent.read` grant. It lists all active runs and
terminal runs finished in the last seven days (configurable from one hour to
30 days). `idempotency_key` is an optional exact, owner-scoped filter, including
older terminal admissions, for reconciling a lost receipt without launching or
steering work. Ordinary runs and MCP task-augmented runs have identical coverage.
MCP task retention does not erase the underlying run.

## Authority and fields

| Boundary or field | Source and contract |
|---|---|
| principal / authenticated user | Existing client-owner mapping and `Runs.owned`: both must match; legacy default exception only for the existing local-default authority |
| cwd authorization | Existing allowed-path rules applied before paging; no caller-supplied principal or paths |
| local stdio/socket | Wing resolves clients.yaml, owner, grants, lock/passkeys and scope on each request |
| direct MCP | Authenticated signaling user and live direct policy/grants; no new grants |
| remembered SSH | Pinned wing and receiving wing's local client policy; no recursive onward discovery |
| scoped mailbox | Its explicit tool allowlist remains authoritative; inventory is not implicitly added |
| run_id / session_id | Durable run and execution IDs; legacy tasks have an empty session_id |
| wing_id | Stable machine ID, on envelope and each row; `(wing_id, run_id)` is the inventory key |
| agent / model / label / cwd / isolation | Launch metadata; no prompt or transcript |
| status / phase / terminal | Current foreground lifecycle; process survival is separate |
| parent_id / idempotency_key | Exact durable linkage/reconciliation key; replacement executions remain separate runs |
| created_at / started_at / finished_at / timeout_seconds | Existing lifecycle metadata; no deadline means timeout_seconds=0 |
| failure_kind | Content-free semantic classification, not provider error text |
| cleanup | `unknown` unless surviving descendants or containment failure establish `attention`; completion alone never proves cleanup |
| output_availability | `available` for durable completed output, `unavailable` for legacy completed NULL output, otherwise `pending` or `unknown` |
| legacy | Identifies the old tasks-only projection; unfinished legacy tasks become unknown outcomes under existing restart reconciliation |
| runs / count / next_cursor | Only authorized rows; count is this page's count, no fleet totals |
| snapshot_revision / observed_at / terminal_since | Opaque admissions boundary, observation time and fixed recent-history cutoff |
| excluded fields | Prompt, original_prompt, direction, output/text, provider transcript/session/turn IDs, error/containment text, launch config/credentials, usage fields and process argv |

Deleted egg/session records do not delete the committed run. Inventory performs
no egg probes, output reconstruction, lifecycle reconciliation or worker launch.
Legacy tasks are discoverable without inventing a session ID. Missing or corrupt
authorized inventory state is an error, never a successfully completed empty
list. Known user/path denials exclude partial records before integrity checks;
unknown authority fails closed. Invalid retention dates cannot prove expiration.

## Pages and compatibility

Pages use fixed admissions watermark and terminal cutoff, with **live metadata**,
not a frozen status snapshot. Sort by immutable task created_at then full run ID,
ascending. Later admissions wait for a fresh scan. An active run completing
between pages stays eligible because its finish time is newer than the fixed
cutoff. New scans include it until that cutoff's retention window expires.
Concurrent archival can remove rows; a scan is not a globally atomic fleet view.

Cursors are authenticated encrypted opaque tokens using a domain-separated key
from the existing wing identity. They bind wing/state directory, user, principal,
path policy, exact key filter and history window. The state path resolves parent
symlinks, so aliases of the same database share scope while different physical
state directories stay separate. Cursors survive a daemon/client restart with
the same committed store/key, expire after 24 hours, and fail closed
when malformed, foreign, policy-changed, or their continuation anchor is removed.
Restart a failed scan; retain previously known IDs as stale, not deleted.

Two additive SQLite indexes are justified: the baseline has no owner/history
index for legacy tasks. They bound routine history selection by principal,
type/status and finish time; they do not change run, task, result or usage data.
To detect lost task-projection owners, routine discovery also walks the existing
`agent_runs` principal index and checks that owner's task metadata. This required
owner-history metadata pass has cost proportional to that owner's modern
admissions; expired JSON and output remain unread. Unknown retention dates use
the recent-history index's NULL range. No additional schema migration is needed.
The API caps pages at 200, cursor bytes at 4096 and key bytes at 200. It selects
only metadata from durable JSON, not full records into application memory.

Probe `control_contract.operations` for `agent_list`. An older peer is explicitly
unsupported; terminal_list and tasks/list cannot establish a complete run roster.
Known IDs can still use status/result on old peers. An unavailable peer is an
error, never an empty inventory. No long-poll inventory service is added.

CLI examples (run only on an isolated accepted candidate):

```sh
wt agent runs --json --client codex --limit 100
wt agent runs --json --client codex --cursor '<next_cursor>'
wt agent runs --json --client codex --idempotency-key '<original-key>'
wt agent recover --json --client codex --max-pages 20
```

`recover` starts without IDs, uses agent_list then existing agent_status and
agent_result. It reads result once for terminal rows and never waits on them.
Classification uses that validated terminal receipt's status and failure kind;
a frozen MCP cancellation stays interrupted even if the underlying run finishes.
Bounded or failed scans return errors; the consumer does not infer absence from
an incomplete page. Native CLI/socket/SSH and actual unattended-provider
acceptance require coordinator execution outside the worker.

## Local gate receipt (2026-10-11)

Pinned source: `2c3688f1f59cee08713d77476a917d7bc4bf7867`; no fresh upstream,
W7 lifecycle repair, installed runtime, provider or network operation was used.
Plugin contract read from committed `e8a2b526a28927f78f10dbd238c3737537ac9def`.
Go 1.26.9 and module/build caches were privately copied offline into this clone.

| Package | Artifact and independent result | Gate |
|---|---|---|
| 1: contract | Field/authority table above; prompt/output/transcript/config/usage excluded | PASS |
| 2: durable enumeration | Store read transaction, authorization before LIMIT, UTC timestamp + full-ID ordering, two partial indexes; 250 runs / 3 pages with no loss/duplicates | PASS |
| 3: adapters | Independent serialized local/direct/socket-authority/remembered-SSH pool requests return the same three IDs; no native transport opened | PASS |
| 4: restart/cursors | Unacknowledged original key recovered without resubmission; committed store reopen, live terminal transition, encrypted foreign/stale/filter cursor rejection, missing/corrupt state errors | PASS |
| 5: first consumer | Four real fixture IDs / two pages / four status + three result calls; one each completed, running, interrupted and unavailable output; cleanup unknown for all four | PASS |
| 6: bounds/compatibility | Full store/control unit tests; nonzero inventory tests; affected race tests; Mac arm64 + Linux amd64 compilation; indexed query-plan proof; old peers remain explicit failures | PASS |
| 7: restrictive two-wing policies | Identical local `same-id` retains two wing-qualified keys; live grants, owner/user/cwd and receiving socket scope checked before one-row pages; foreign cursor rejected | PASS (race) |
| 8: retention/removed sessions | Five retained records, four discovered; expired native MCP task remains an ordinary run; two pages / four statuses / four one-shot results; no removed session/output reconstruction or run lifecycle writes | PASS (race) |

Core verification produced 126 unit test/subtest passes and 33 race test/subtest
passes. The two ordered continuations each added one nonzero race test pass.
Vet then caught three fixture-only copies of a Server containing mutexes. Explicit
server construction repaired them; the three affected race tests and affected
package vet passed (38 race pass executions total).
The new command tests execute Cobra and API seams in process, not built CLI
acceptance. The earlier toolchain mismatch and incomplete remembered-wing
fixture were repaired; all three failed attempts (including vet) remain in scratch
logs. No FAIL gate remains.

Native built CLI, real socket/SSH acceptance and an actual unattended run after
client restart are **REQUIRED-UNRUN**: the worker must not start those transports,
process fixtures or providers. Local implementation/fixture readiness is complete;
product acceptance remains externally pending. The general repository check/gate
profiles include prohibited native/browser/provider/network portions and were
not substituted for this row's safe focused gates.
