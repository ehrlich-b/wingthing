# Offline run inspection

`wt run-inspect --snapshot PATH --json` explains a completed copy of Wingthing
state. Run it from the workspace containing that copy. It never loads ambient
configuration, opens an egg socket, tests process liveness, launches a provider,
replays a prompt, resumes a session, or runs archive restoration. It does not
replace an installed executable. The human report renders the same typed data.
The separate `run-inspect` command preserves every existing `wt run [prompt]`
invocation, including a literal prompt named `inspect`.

The Go interface is `runinspect.Inspect(ctx, path, Options{WorkspaceRoot: root})`
and returns `*runinspect.Report`, version 1. Report fields describe observed
runs, legacy task ancestors, conversations, checkpoint attempts, archives, graph edges, issues,
suggested actions and the host observations still required. `run_id` identifies
a durable task; `session_id` identifies its execution. A conversation's current
execution can differ from the execution whose run receipt is being inspected.

## Supplying a snapshot

The inspector requires `snapshot.json`, a version 1 manifest with
`kind: "completed_copy"` (or `"fixture"`), `completed: true`, an RFC3339
`captured_at`, and `files`. Each file entry has a relative `path`, an exact byte
count `bytes`, and a lowercase hex `sha256`. Every regular file except the
manifest must be listed. The supplier writes the manifest after completing and
quiescing the copy. A manifest is a supplier assertion, not proof that a live
copy was coherent. Capture provenance and concurrent changes still require
operator review.

Include a consolidated SQLite backup named `wt.db`, and copied egg and
conversation-broker artifacts as needed. Merely copying an open database while
omitting its WAL is insufficient. WAL, SHM and rollback-journal sidecars are
refused. No snapshot collection command is included: collection is an operator
operation outside this inspector's authority. Symlinks, missing/unlisted files,
unsupported manifest versions, duplicate JSON keys (including ambiguous case aliases)
and hash mismatches are refused. Input inventory
and hashes are verified again before returning the report. The current bounds
are 4,096 files, 128 MiB per file and 256 MiB in total. Table reads stop at 10,000
rows and report incomplete evidence; archive decoding stops at 32 MiB.

Paths must resolve inside the explicitly supplied workspace. Default
`.wingthing` and `.wingthing-preview` paths and aliased directories are refused.
SQLite is opened read-only and immutable through a retained rooted file
descriptor (`/dev/fd`, on Unix), with query-only mode and memory-only temporary
storage. `store.Open` and its migrations are never used. If that descriptor
transport is unavailable, the database is reported unreadable; unrelated egg
artifacts remain inspectable.

## Evidence and limits

| Observed record | Interpretation |
| --- | --- |
| `tasks` without a corresponding `agent_runs` row | Legacy ancestor and historical output evidence; execution identity remains unverified. |
| `wt.db`, `agent_runs.record`, `revision` | Wing admission, launch/submission intent, ownership and acknowledged result; revision is its CAS fence. |
| `eggs/SESSION/run.SHA256(RUN_ID).json`, version 1 | Egg reservation or native run receipt. Nonterminal records prove admission; they cannot establish present liveness. |
| `conversations`, `conversation_executions` | Logical parent/root identity, owner and current versus earlier executions. |
| `conversations.revision`, `delivered_cursor`, `conversation_events.sequence` | Checkpoint CAS fence and explicit acknowledgement. Freshness uses events belonging to the same root; pending events are counted, not inferred from globally spaced sequence numbers. |
| `conversation_wake_policy.delivery_cursor` | Wake delivery only; it does not advance checkpoint acknowledgement. |
| `conversation-brokers/SESSION/journal/REQUEST.json` | Retained checkpoint request and expected revision. A cursor covered by a later durable checkpoint does not prove which writer committed it or that a reply was delivered. |
| `chat.meta`, `chat.jsonl.gz`, `.chat-*.tmp` | Archive identity, complete gzip checksum, transcript identity and interrupted rotation evidence. A transcript archive is separate from exact run output. |
| `egg.meta`, `lifecycle.jsonl` | Launch provider binding or persisted SessionStart rebinding; contradictory, invalid or partial binding evidence prevents resume eligibility. No journal/lock file is created or repaired. |
| `result.text` on a valid receipt | An exact inline output handle; missing text differs from present empty text. |

Graph edges are `observed`, `absent` or `inconsistent`. Missing ancestors do not
erase a valid terminal receipt. Conflicting execution ownership or terminal
receipts have no winner. Partial records, older table schemas and unsupported
egg versions remain explicit unknowns, while independent valid records are
inspected. A new schema field is not interpreted as a new generation fence.
Provider paths and metadata paths inside records are never followed.

`completed_with_output`, `completed_output_unavailable`, `failed`, `timeout`,
`stopped`, `running_at_snapshot` and `admitted_outcome_unknown` are distinct
results. A terminal egg receipt absent from a nonterminal wing record is
`terminal_receipt_not_in_wing`; this is observed state consistent with a lost
acknowledgement, not proof of the original transport failure. Archive integrity
does not prove that it includes all subsequent provider activity.

Suggested actions are data (`inspect`, `wait_for_known_completion`,
`manual_resume_eligible`, `missing_evidence`) with source references and required
observations. They contain no executable operation. Waiting requires a current
host check of that exact execution. Manual resume eligibility requires a
terminal interrupted/failed execution, consistent ownership, the current
execution, a current checkpoint and a complete bound archive with no pending
rotation. It is conditional on the listed real-host observations; it never
means replaying an ambiguous prompt or authorizing an actual recovery.

Default output contains byte counts and hashes, not prompts, arbitrary errors,
checkpoint prose, titles or provider text. `--preview-bytes N` opts into up to
4,096 original bytes per run output. Previews are separately labelled untrusted
data, ASCII-quoted with controls, ANSI, bidi and invalid UTF-8 escaped. Omitted
counts refer to original bytes even at a split UTF-8 boundary. Checkpoint and
archive content remain metadata only.

Stdout creates no files. `--output-dir PATH` requires an existing separate
workspace directory and exclusively creates a private `run-inspect.json` or
`run-inspect.txt`. It refuses overlap with the snapshot, aliases and existing
report files.

## Original orphan example and validation

After `make -j1 ops ACTION=build`, from the clone root:

```sh
./wt run-inspect --snapshot internal/runinspect/testdata/orphan --json
```

The original fixture has a wing submission intent and a complete egg receipt.
Its hand-authored expectation is `completed_with_output` and
`terminal_receipt_not_in_wing`, with no resume recommendation. `records.sql`
records the independent database fixture definition. The manifest pins both
that definition and the frozen database and egg bytes. No user data is present.

`make -j1 test-run-inspect` exercises the typed reader and built CLI against
original, finite snapshots, including byte-for-byte preservation and stdout
no-file assertions. `make -j1 test-run-inspect-race` runs both tiers with the race
detector on the Go test hosts. The built CLI subprocess uses the ordinary built
binary. Canonical `test-run-recovery`, `test-run-recovery-race` and `check` retain
their existing transport coverage and require the coordinator's ordinary host.
No daemon restart is part of offline inspector validation.

## Serializer-backed orphan example

`make -j1 test-run-inspect-roundtrip` generates new finite fixtures below
`.scratch/runinspect-roundtrip/`, using the admitted `wingsession.Run` JSON,
`store.AdmitAgentRun`/`SaveAgentRun`, the egg run receipt writer,
`CheckpointConversation`, the broker journal writer, and
`CaptureSessionHistory`. The serializer adapters compile only with the
`runinspectfixture` test tag; the inspected `wt` binary is the ordinary build.
The builder requires a new absolute scratch destination, closes SQLite before
inventorying it, and exclusively creates a version 1 fixture manifest. It has
no live-state collection path. Tests retain their tiny fixtures for inspection.

`TestRunInspectRoundTripCLIIsolatedWorkspaceOrphan` prints the generated orphan
snapshot path. Replace `CASE` below with that fresh test directory name and run
from the clone root; all inputs are disposable fixtures:

```sh
./wt run-inspect --snapshot .scratch/runinspect-roundtrip/CASE/snapshot --json
./wt run-inspect --snapshot .scratch/runinspect-roundtrip/CASE/snapshot
```

That example serializes parent admission, child submission intent and a durable
successful egg receipt for `orphan` / `execution-orphan`. It then deliberately
removes the parent from the fixture database before sealing. This models an
incomplete supplied copy; healthy current foreign keys prevent that deletion.
The independent expectation is `completed_with_output`,
`terminal_receipt_not_in_wing`, `evidence_state: "incomplete"`, and a
`missing_parent` issue naming `orphan` and `missing-parent`. The successful
receipt remains visible, output is metadata only, and resume is not eligible.
The test executes the same snapshot in a second isolated workspace with no
installed tools on PATH and unusable ambient state paths.

An operator considering actual recovery still needs capture provenance and the
latest same-run/same-execution durable receipts; exact host PID/process identity,
provider thread/turn, deadline and ownership; the current conversation execution,
revision and explicit ACK cursor; and provider resume support, policy and a
complete archive bound to that egg. A persisted SessionStart may legitimately
rebind the native provider ID; contradictory egg metadata or incomplete binding
journals suppress eligibility. Wake delivery, archived transcript text and a
lost acknowledgement never authorize resending a prompt. These observations
must come from the actual host; the fixture command performs no recovery.

The round-trip gate compares typed Go, built JSON and human issue/source/action
inventories, checks Unicode/control previews by original byte count, verifies
repeated and failed-output inspections preserve hashes, modes and file/directory
mtimes, and compares two independently generated snapshots differing by one
acknowledged event. Add `INSPECT_TEST_FLAGS=-race` to run the fixture consumer and
its finite store oracles with the race detector. Ordinary-host recovery/check,
real-provider and live operational acceptance remain separate coordinator gates.
