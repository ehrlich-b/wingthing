# Native platform gates

This lane runs on Linux. These scoped-parent storyboard cards remain FAIL in
the unchanged executable suite and are classified **REQUIRED-UNRUN** for native
macOS acceptance, rather than claiming a passing fixture or disabling a policy:

| Card | Test | First unmet requirement |
|---|---|---|
| 02/card-0 | TestStoryboardStartCard0 | Protected Claude parent admission before cold-start readiness |
| 02/card-1 | TestStoryboardStartCard1 | Protected Claude parent admission at the explicit cwd |
| 02/card-2 | TestStoryboardStartCard2 | Native local/forge scoped-parent admission; routing subcases pass |
| 02/card-4 | TestStoryboardStartCard4 | Native denial cannot reach its provider-executable check; transport/policy/recovery subcases pass |
| 02/card-5 | TestStoryboardStartCard5 | Concurrent parents and surviving waiter cannot reach provider readiness |

The exact observed refusal is:

> provider write protection is modeled only for the macOS Seatbelt profile, not linux

The product's documented platform boundary is in `docs/wt-claude.md:9`.
`internal/localmcp/conversation_broker_policy.go:46` refuses a non-Darwin write
model, and the Linux sandbox also refuses nonempty protected-write targets.
Neither boundary changed. Cross-platform CLI/policy/startup unit tests do not
establish native protected-parent readiness. Linux scoped-parent support needs
its own enforcement implementation and native negative protection probes;
removing either refusal alone would be a policy bypass.

Native macOS install, real vendor authentication, admitted-parent output and
same-ID attach/detach/reattach have not been run here. The new onboarding unit
tests use disposable executable/installer fixtures, and the five install card
tests observe detection and cancellation through real PTYs. They do not prove
real vendor readiness. No real vendor installer, account login or model API was
executed by this lane.

Flows 3–6 remain outside the brief's fix scope. Their failing native/provider
and physical iPhone obligations remain open; unchanged failures retain their
original baseline evidence under `evidence/run-1`, `run-2`, and `run-3`.
