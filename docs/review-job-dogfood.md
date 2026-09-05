# Fixed implement/review job (in progress)

Goal: submit one native Wingthing job, disconnect the Mac before implementation
finishes, and retrieve the exact patch, independent review, tests, and terminal
outcome after the VM-side handoff completes. At most two revisions; explicit
process and total deadlines. No release, push, merge, or production mutation.

## Contract being implemented

- One fixed implement/test/review/test loop, not a general DAG or file-sync service.
- A personal, explicitly enabled coordinator wing owns the durable job. Shared
  hosts and org wings fail closed. Outbound native control must prove its owner
  matches the submitting principal before starting either worker.
- The submission pins both execution wing IDs, a full source commit on each
  already prepared replica, exact writable file paths, test argv, models, and
  deadlines. Workers prepare separate job workspaces from that source.
- Existing native `agent_run`/`agent_wait`/`agent_result` own model execution.
  Workspace operations capture and apply a bounded exact candidate. Test exit
  codes come from sandboxed subprocesses, never model prose.
- Authoritative tests use fresh pinned replicas, never model workspaces. Ignored
  build files left by either model cannot influence the evidence. Explicitly
  allowed ignored additions are still captured as candidate bytes. Git metadata
  is read-only to agents and tests; coordinator state is denied. Tests also deny
  provider credentials, strip ambient credentials, disable networking, and kill
  their process group on normal exit as well as timeout.
- Codex progress remains in the transcript. A separate `final_output` records
  the last agent message only after a successful completed turn and process
  exit. Reviews parse only this field and fail closed when it is absent.
- The reviewer receives the exact candidate digest, patch, and implementation
  test evidence. Independent tests and a digest-bound structured review are
  necessary for success. Review workspace changes fail the job.
- Durable request IDs deduplicate submission retries. The coordinator records
  intent before submitting a child and never retries an ambiguous child spawn.
- A live job holds an OS file lock. After process loss, unlocked nonterminal
  jobs become `interrupted`; they are never automatically replayed. Surviving
  child run references remain inspectable. Crash resumption is not claimed.
- Every operation is owner-scoped, granted, bounded, and audited. Credentials
  remain wing-local. No provider credential or SSH cache transfer.

## Completion evidence required

1. Unit state-machine tests: disconnect-independent execution, test failure,
   review revisions and exhaustion, timeout, unavailable worker, wrong owner,
   ambiguous spawn, and restart interruption without duplicate execution.
2. Native transport/schema/strict-decoding and owner-boundary integration tests.
3. A real two-VM Terra/Sol task with all Mac clients/forwards disconnected before
   Terra completes; no Mac-side copy or handoff after submission.
4. Fresh client retrieval of exact patch, accepted review, test evidence, and
   terminal job status; verify deployed executable digests.
5. Relevant full integration, org/legacy/direct-policy browser, and compatibility
   checks, with explicit environmental limitations rather than false passes.

Previous baseline: private artifact `dogfood-72c7be52603d-1e41e766e978`.
Existing dirty hardening edits and the original checkout's recovery memo are
preserved. This document is implementation intent, not completion evidence.

## Private operator setup

On both personal wings, create a canonical workspace directory and configure
`$WINGTHING_DIR/review-jobs.yaml` with mode 0600:

```yaml
owner_principal: user-<authenticated-owner-hash>
workspace_root: /home/dogfood/work/review-workspaces
sources:
  - /home/dogfood/work/wingthing-pinned-source
```

Each source must already exist, be clean, and have HEAD at the requested full
commit. No repository fetch or credential transfer happens during submission.
The coordinator needs its own existing Wingthing device login and must reach
both wings independently of the submitting client. Policies are opt-in and do
not enable org/shared-host workflows.

Write a JSON spec with `request_id`, `prompt`, `base_commit`, `implementer` and
`reviewer` (each an explicit `wing_id`, absolute `source`, and `model`), exact
`allowed_paths`, `test_argv`, `max_revisions` (0-2), `run_seconds`, `test_seconds`,
and `timeout_seconds`. Submit through the private client:

```sh
wt review submit --wing-id COORDINATOR --roost PRIVATE_ROOST --spec job.json --json
wt review status JOB --wing-id COORDINATOR --roost PRIVATE_ROOST --json
wt review result JOB --wing-id COORDINATOR --roost PRIVATE_ROOST --artifact patch --json
```

Result artifacts are `patch`, `tests`, `review`, and `implementation`. A terminal
status is not necessarily success: only `succeeded` means the same patch passed
both test gates and an independent digest-bound review. Keep the job ID. Retrying
the identical spec/request ID returns the same job; changing its contents is a
conflict. One job runs per coordinator at a time. Restarted nonterminal jobs are
reported as `interrupted` on retrieval, not silently resumed. Child IDs remain in
the job for inspection; a worker's admitted deadline still bounds surviving runs.

The final-message adapter uses Codex's documented JSONL agent-message and turn
completion events; it never parses terminal rendering. See the official
[non-interactive mode reference](https://learn.chatgpt.com/docs/non-interactive-mode).
