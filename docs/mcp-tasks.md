# Native MCP tasks

`wt mcp stdio` and `wt mcp connect` support the experimental
[MCP 2025-11-25 Tasks utility](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks).
Initialization advertises task creation, listing, and cancellation.
`agent_run` declares `execution.taskSupport: "optional"`; other tools keep
their ordinary call contract. `agent_start` creates an interactive PTY session
without a semantic final result, so it does not support task augmentation.

## MCP client behavior

Configure Claude Code or another MCP host with the same `wt mcp stdio` or
`wt mcp connect` command you already use. A host supporting Tasks can submit
`agent_run` as a task, display accepted work in its native task interface, and
retrieve the final result separately. No Wingthing plugin is required.

**Verify:** whether this Claude Code build negotiates 2025-11-25, chooses task
augmentation for optional tools, and displays the run in its background task
list. Server support alone cannot make a client use Tasks. The protocol does
not prescribe a particular task UI. Test the real client before relying on
its background task display.

Clients offering 2025-06-18 receive that version, the existing tools capability,
and tool definitions without execution markers. Plain `agent_run` calls keep
returning the admission receipt. Those clients can continue using
`agent_status`, `agent_wait`, `agent_result`, and `agent_stop`.

## Wire example

Send task options at the `tools/call` envelope level, outside `arguments`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "agent_run",
    "arguments": {
      "agent": "codex",
      "prompt": "Inspect the tests and report the result",
      "cwd": "/path/to/project",
      "timeout_seconds": 900,
      "idempotency_key": "test-inspection-1"
    },
    "task": { "ttl": 60000 }
  }
}
```

The immediate result contains a `task` with a stable `taskId`, `status:
"working"`, ISO timestamps, `ttl`, and a suggested 5000 ms `pollInterval`.
It has no tool `content`. Use the returned ID with:

```json
{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":"RETURNED_ID"}}
{"jsonrpc":"2.0","id":3,"method":"tasks/result","params":{"taskId":"RETURNED_ID"}}
{"jsonrpc":"2.0","id":4,"method":"tasks/list","params":{}}
{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"taskId":"RETURNED_ID"}}
```

`tasks/get` maps pending/running runs to `working`, successful runs to
`completed`, failures/timeouts to `failed`, and stopped runs to `cancelled`.
These runs do not request client input, so they do not enter `input_required`.
`tasks/result` waits for a terminal status using the request lifetime, without
an `agent_wait` timeout or the SSH pool's ordinary call timeout. It returns the
same CallToolResult payload as `agent_result`, with related-task metadata.
Run errors remain in that payload, as with ordinary `agent_result` calls.

`agent_run.label` is optional display text of at most 200 Unicode characters,
independent of the session slug. Spaces, punctuation and Unicode are accepted;
whitespace is collapsed and control/format characters are removed. Labels need
not be unique. Admission, status and result return the stored label; use the
returned run/session IDs to address the work.

The task's final payload differs from the admission receipt returned by plain
`agent_run`. This preserves the existing tool behavior while supplying the
durable run's result. **Verify:** this mapping against the spec's requirement
that task results match the underlying request, and the real client's result
rendering.

Cancelling a result request with `notifications/cancelled` ends that observer;
it does not stop accepted execution. Disconnecting stdio or SSH also cancels
observers. `tasks/cancel` durably sets the task to `cancelled` before responding
and requests the ordinary agent stop and descendant cleanup. Its cancelled
result stays frozen if the provider later completes. Cancelling an already
terminal task returns invalid params (`-32602`).

## Durability, ownership, and retention

The task record is committed with durable wing run admission. A new MCP client
using the same principal can get, list, and retrieve it after a disconnect or
wing restart. Another principal cannot read or cancel it. Existing grants and
path restrictions apply: admission needs `agent.run`, observation needs
`agent.read`, and cancellation needs `agent.stop`.

Wingthing treats requested `ttl` as retention after completion. To preserve
active execution, the actual protocol TTL is `null` while working; after
completion it includes elapsed run time plus requested retention, measured
from creation as the MCP spec requires. Omitted or `null` TTL means unlimited
retention. Numeric retention accepts 0 through 31536000000 ms. Expiry removes
task visibility and retrieval, while leaving the durable run and its ordinary
`agent_result` intact. TTL never changes the run timeout.

Listings use opaque cursors and pages of at most 50 tasks. Only task-augmented
admissions appear. Remembered SSH and explicit `--roost` connectors return
task IDs encoding the owning wing and run. Keep that ID intact; task methods
route from it without a separate `wing_id`. Initial aggregate `agent_run`
arguments still require `wing_id`.

Aggregate listing merges local and remote pages. An unavailable wing returns
an explicit error instead of a silently incomplete list; tasks on available
wings remain individually retrievable. Upgrade the connector and its wings
together: older wings do not implement the internal task control operations.
Optional status and progress notifications are not emitted; clients should
use `tasks/get` and its polling interval.

Isolated tests cover negotiation, markers, lifecycle and result parity,
pagination, isolation, cancellation, request lifetime, restart recovery,
built stdio with fake Codex, and built aggregate routing across fake SSH loss.
Two-wing direct WebRTC routing and durable cancellation recovery are covered too.
**Verify:** native display and optional task selection in each real MCP host,
including this Claude Code build.
