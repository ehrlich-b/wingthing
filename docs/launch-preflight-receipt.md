# Wave 8 launch preflight receipt

Row 08 ran on macOS in
`/Users/ehrlich/repos/w8-wingthing-launch-preflight-20261011`, from pinned base
`2c3688f1f59cee08713d77476a917d7bc4bf7867`. No fresh upstream was used. Admission
was satisfied for immutable offline scope. Launch was 2026-10-11 03:04 EDT;
final validation finished before the 04:59 EDT deadline. The toolchain, module
and build caches were private APFS copies of existing local caches. Go 1.26.9
used the private offline/resource environment recorded in `.scratch/env.sh`
and `.scratch/cache-receipt.txt`. Go embedding used the Makefile's local
placeholder web assets; no web/browser acceptance is claimed.

The implementation adds `wt doctor launch-plan` with JSON and human output.
It accepts explicit policy, workspace, executable, HOME/scope, credential-path
metadata and saved capability/version/help inputs. It does not discover a wing,
read ambient API keys, open credential contents, invoke an adapter, prepare
provider directories, bind sockets or enter doctor --fix. It preserves the
existing Codex argv and nested-sandbox fixes.

| Package | Concrete artifact and local gate | Status |
| --- | --- | --- |
| 1: trace launch | Explicit resolver/metadata consumer, source attribution, no ambient fallback fixtures | PASS local |
| 2: diagnose capabilities | Network-none/proxy/raw-socket, required writes, ELAN_HOME, nested sandbox and bus fixtures | PASS local |
| 3: provider/version | Runtime catalog and shared egg builders, digest-bound saved-help comparison, native route predicate | PASS local |
| 4: bounded command | In-process command help, JSON/human consumer, unchanged content/mode/mtime inventory, rejected fix/prompt/token paths | PASS local |
| 5: hosts/old config | Mac/Linux declarations, production parser/merge parity, aliases, missing binary, stale evidence and scope fixtures | PASS local |
| 6: acceptance handoff | Compiled `.scratch/wt-launch-plan`, documented examples and two outside-worker acceptance scripts | PASS local; native acceptance unrun |
| 7: provenance continuation | Table of inherited denials, masks, duplicate declarations, missing parents and repeated-base refusal | PASS local |
| 8: saved-version/export continuation | Exact argv oracle from pinned builders and both synthetic version/export scenarios | PASS local oracle/export; authentic saved recordings REQUIRED-UNRUN |

The row outcome is **PARTIAL** because continuation 8's authentic two-version
recordings are unavailable. All six core local artifacts and gates are complete;
there is no further independent local package waiting. Synthetic scenarios and
baseline-generated argv are explicitly distinguished from installed-provider
recordings and native acceptance. They do not replace those missing receipts.

The independent stable oracle uses the original pinned lifecycle/run builders,
with exactly their two MkdirAll blocks removed in private overlays. All argument
construction stays unchanged. Both original object copies are preserved.
A stable virtual planning root lets the comparison pin every argument and
path-bound hook hash. Reproduction inputs and the passing JSON log are in
`.scratch/oracle` and `.scratch/logs/pinned-oracle-stable.jsonl`.
`reproduce-stable.py` makes a fresh overlay child; `wave8-oracle-stable` in
`.scratch/final.mk` records the tested invocation. A separate JSON-map consumer
checks the public schema and required unknown gates without depending on the
report's Go struct. Export tests include sentinel prompt/token text in help,
credentials, routing query text and malformed env selectors.

Final focused validation recorded **82 tests/subtests (49 top-level)** with
zero failures and zero skips, plus **one independent pinned-oracle test**.
The full `internal/agent` and `internal/config` suites passed earlier; their
plain logs do not carry individual counts. Go vet passed for the required
cmd/agent/config packages and the new/touched supporting packages. The Mac CLI
compiled successfully. `git diff --check` passed. Both Python acceptance
scripts parsed successfully. Race and cross-compiled/native builds were not run
and are not claimed as required gates.

The focused logs and counts are retained in `.scratch/test-counts-final.json`,
`.scratch/logs/export-final-*.jsonl`, `.scratch/logs/export-final-vet.log`,
`.scratch/logs/export-final-build.log`, and the fsutil subset of
`.scratch/logs/final-unit.jsonl`. Final local gates have no outstanding FAIL.
An initial fix-flag test assertion and the first path-dependent oracle assumption
were corrected; they were test/capture issues, not changes to production policy.

The existing production resolver's duplicate-base behavior is preserved:
`base: {name: ./parent.yaml, fs: ./parent.yaml}` refuses a repeated reference.
The fixture proves both baseline refusal and preflight refusal. This row does
not fix that resolver behavior. Linux root-deny mount exceptions remain
explicitly unverified, while Linux scoped parents remain unsupported. Missing
user bus becomes a gate only for an explicitly bus-dependent requested mode.

REQUIRED-UNRUN items and reasons:

- Built CLI execution/inventory acceptance: prohibited inside this worker.
- Mac/Linux native confinement and disposable relay checks: prohibited inside
  this worker; fixture/compilation success does not establish enforcement.
- Authentic version/help recordings for two installed provider versions: not
  present in committed inputs; provider version/help probes are prohibited here.
- Authorized real provider tool-call canary: no exact command/receipt was
  supplied. The existing real Codex canary is a no-tools completion canary and
  does not satisfy this gate. No provider invocation was attempted.
- The specified 21:55–22:30 forge incidents: not found in committed RUNLOG
  inputs at the available local source. The brief's named failure shapes are
  exercised as independent fixtures; no incident receipt was fabricated.

The next coordinator command, **outside the worker sandbox**, is:

```sh
python3 -B scripts/launch-plan-acceptance.py \
  --wt "$PWD/.scratch/wt-launch-plan" \
  --root "$PWD/.scratch/launch-acceptance"
```

Native checks and optional explicit installed-provider capture are documented
in `docs/launch-preflight.md`. The selected root must be new; original inputs and
receipts are retained. A provider tool-call remains a separate authorized task.

Local commits only. No push, tag, merge, publication, deployment, external
message, installed-runtime change, production mutation, browser launch,
loopback bind or live-state action occurred. The pinned source and other clones
remain preserved. `.scratch/handoff.json` records final SHA, bundle/diff/report
and binary digests; scratch logs, caches and artifacts are not committed.
