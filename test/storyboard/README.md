The storyboard acceptance suite targets the exact base commit
`f8f9ebd96e1c72336ffd796c8d496ecda0a85619`. It contains 51 named Go
acceptance tests, one for each stable article ID in flows 1–6. The catalog
contains all 100 cards; flows 7–12 only receive `PENDING-FEATURE` rows.

Run from the checkout:

```sh
python3 test/storyboard/run.py
python3 test/storyboard/file_bugs.py
```

`run.py` verifies the build/test compilation, then executes the complete suite
three times without test caching, nice level 15 and `GOFLAGS=-p=4`. Missing
features deliberately leave Go tests red. A nonzero final exit with a complete
`evidence/summary.json` is an acceptance result, not an aborted run. The runner
refuses to overwrite earlier numbered runs. Use a fresh checkout or archive the
numbered runs before collecting a new batch. `file_bugs.py` reads completed
results and files one bug per failing card on the explicitly authorized canonical
board. It passes descriptions by body file, adds the required lane/project/host
labels, estimated V/C metadata and the named test in acceptance criteria. `needs:coordinator`
gates dispatch until the provisional scoring is reviewed, as required by the board
schema for missing-source costs. Saved
per-card create receipts prevent duplicate filing after an interrupted batch.

Tests reuse Northstar's kernel PTY, VT emulator, exact child cleanup, scoped
HOME and WINGTHING_DIR, fixture SSH, fixture gRPC sessions, authenticated control
socket and `internal/testprovider` native protocol fixtures. No in-process CLI
stand-in or production test backdoor is added. The three Linux adaptations of
scoped creation, native mouse and failure diagnosis retain **all** their inherited
journey assertions. They use a Linux-compatible fixture policy: `deny-rename`
pins HOME and its ancestors; explicit `deny` rules protect each wing state and
mailbox. Northstar's original ancestor `deny-write` guards mean literal directory
entries on Darwin but recursively bind a directory read-only on Linux, preventing
provider callback spools and even causing a bind failure on `/work`. Applying that
policy unchanged would measure a fixture incompatibility. Manual egg FS arguments are resolved to absolute receiving-workspace paths before launching; the Linux mount verifier cannot verify a literal `./` mount. Every existing test
file and assertion remains unchanged. Native fixtures still probe denied state,
SSH key and unrestricted socket access; confinement is not replaced by
`--unsandboxed`. The mouse provider also recognizes Linux `EROFS` alongside
`EACCES`/`EPERM` as a failed protected write; every other OS error still fails.

The installer uses a fake release download transport, the real production
installer, the built Linux binary and its real SHA256SUMS. No release or website
is published or installed in the user's HOME. `make web/dist` is the repository's
supported terminal-test embedding prerequisite. Its placeholder does not establish
browser behavior. No live provider account, vendor installer or model API is used.
The offline local case blocks download transport; it demonstrates inventory and
fixture attachment without Wingthing account/network prerequisites, not full
host-wide network isolation or offline cloud inference.

A PASS means the mapped screen/state assertions completed. Fixture evidence is
not real Claude/Codex authentication or vendor readiness. Phone cards have an
executable owner CLI prerequisite and deliberately fail when it is missing. Their
catalog describes downstream phone screen/state obligations, but Linux does not
render or verify iPhone camera, Keychain, cellular, notifications or cryptography.
Even a future passing invitation prerequisite keeps the test red with
`REQUIRED-UNRUN` until physical-device evidence is integrated; it never silently
passes because iOS cannot run here. The two Pro-labeled cards within flow 5 are
still tested because the brief includes **every** card in flows 1–6.

Evidence includes frozen storyboard/PRD sources, machine-readable card catalog,
three Go JSON logs, raw ANSI transcripts and rendered screen checkpoints, receipt
files, per-card PASS/FAIL outcomes, separate flaky list, bug IDs and board create
receipts. `.txt` checkpoints include command output; `.ansi` contains all raw
PTY bytes. Complete Go logs also retain timeout screens and fixture diagnostics.
Incomplete execution or SKIP is rejected by the runner, never reported as PASS.
A fixture snapshot's `consistent` evidence state can coexist with
`terminal_receipt_not_in_wing`; the storyboard's `incomplete` classification is an
illustrative example, not a requirement that a hash-verified fixture be corrupt.
