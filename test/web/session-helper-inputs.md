# Session proof helper inputs

Both helpers require explicit paths and can be launched from an unrelated working
directory. Outputs belong outside the source checkout. Existing input directories
are read only; the baseline workspace and report directory must be empty, disjoint
from each other and from the inputs. Capture retention refuses existing targets.

```
python3 -B /src/project/test/web/session-answer-baseline.py \
  --source /src/project --baseline /inputs/session-packet \
  --work-dir /tmp/session-replay --output /reports/session-replay
python3 -B /src/project/test/web/retain-session-captures.py \
  --source /captures/native --output /reports/native --mode native-final
python3 -B -m unittest discover -s test/web -p test_session_helper_inputs.py -v
```

The baseline packet is exactly the eight files named and hashed in
`fixtures/session-answer-baseline-inputs.json`. The copy under
`fixtures/session-answer-baseline/` uses neutral relative source identifiers and a
neutral machine label. It retains the diagnostic observations and semantic
assertions required by the decision audit. Copy these files to the explicit
baseline directory before replaying. The delivery probe is the published
refinement that reads `evidence/REMEDIATION.json` directly.

`fixtures/session-answer-fixture-provenance.json` records the origin and the three
text transformations. The public baseline and audit manifests hash the sanitized
bytes. They do not claim those hashes are the historical hashes. The separate
`fixtures/session-answer-historical-inputs.json` pins the original eight inputs;
those raw bytes must be supplied privately. Select them explicitly with
`--packet-format historical`. The default is `--packet-format public`; either
format rejects missing, changed, or other-format inputs before writing. The
isolated audit's manifest is bound to the selected packet. No audit assertion or
diagnostic is changed by the selection.

The replay validates the packet, source support files and every pinned subject
hash before checking replay host eligibility or writing. It copies only declared packet inputs, fetches the declared
subject from the explicit source repository, then executes the unchanged audit,
delivery probe, real three-second Go transport probe and all ten JavaScript
diagnostics. It separately requires all twenty historical regressions to fail on
the pinned subject. Old observations never substitute for diagnostic execution.
The reproduction report includes all retained packet/support/subject input hashes;
capture retention prints every copied filename, byte count and SHA-256.

The unit tests reconstruct declared inputs in fresh temporary directories and
exercise interface validation and capture copying without a browser or provider.
Full diagnostic replay requires a non-root user on a suitable host other than
`forge` and the pinned Go dependencies in the permitted cache.
