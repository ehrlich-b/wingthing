# GitLab release verification

Stable `vMAJOR.MINOR.PATCH` tags run `.gitlab-ci.yml`. Other `v*` tags fail the
tag guard before builds; branch pipelines do not start this release workflow.
The final `verify-release` job retains a `verified-release/` artifact for 30 days.
It contains the four binaries, `SHA256SUMS`, a validated `downloads/` tree and
`receipt.json`. Nothing is uploaded to Releases, installed or deployed by CI.

## Runner contract

Register protected, disposable shell runners with tags `wingthing-release-linux`
and `wingthing-release-macos`. Both need Git, Python 3, Go **1.26.9**, Node **22**,
npm and make; provision those exact tool versions before accepting release work.
Linux also needs Docker, Chromium's runtime libraries, build tools for CGO and a
host user-namespace/AppArmor policy that permits native sandbox tests and
historical binaries at temporary paths. Configure that policy on the isolated
runner before registration; the jobs do not change host security settings.
The Linux job installs the same pinned Claude CLI as the previous workflow.
macOS needs Xcode command-line tools and native Seatbelt support. Neither runner
requires a GitHub account or a GitHub release token. Compatibility uses Git tags
in the full GitLab checkout. Provider-gate prerequisites remain those of
`make gate GATE=claude`; missing prerequisites fail the job.

## Gate equivalence

| Previous tag workflow | GitLab verification |
| --- | --- |
| Stable tag guard | `release-tag` runs the same script |
| Linux `check`, pinned Claude, Claude/static gates | `deterministic`, same commands in `scripts/release-verify.py` |
| Integration, compatibility, Debian/Ubuntu and web E2E | Same commands, same order; runner supplies the sandbox policy |
| Native macOS sandbox gate | `macos-sandbox` runs `make e2e-mac` |
| Four-platform stable build and command contract | Same `make release CHANNEL=stable VERSION=...`, after Linux gates |
| Transfer tested artifacts after both jobs | `verify-release` downloads both jobs, verifies gate receipts and every checksum |
| Automatic GitHub publication | Replaced by verification artifacts and a separately authorized GitLab publication |

The GitHub workflow is now a manual informational job with read-only permissions.
No tag push can start its former release publisher.

## Local dry run

From a clean checkout, choose a synthetic stable version; no tag is created:

```sh
nice -n 10 python3 scripts/release-verify.py gate local --tag v0.0.0 --output .scratch/local-gates.json
nice -n 10 make release CHANNEL=stable VERSION=v0.0.0
nice -n 10 python3 scripts/release-verify.py package --dry-run --tag v0.0.0 --dist dist --gate .scratch/local-gates.json --output .scratch/verified-release
```

A dry-run receipt says `dry_run: true` and binds the local `make check` log,
source head, tag-shaped version and each staged asset hash. It cannot substitute
for the Linux and macOS release profiles. Packaging refuses dirty tracked source,
foreign-head receipts, missing/failed gates, changed gate logs, missing binaries
and checksum mismatch. Output directories and receipts must be new for each run.
It invokes the existing download validator, including the `latest.json` contract.

## Promotion checklist (separate authorization required)

1. Review gate equivalence above and runner provisioning. Check GitLab CI lint
   and actual runner availability before creating or pushing a release tag.
2. After authorization to tag, tag the approved source and push to GitLab. Confirm
   all jobs pass for that exact tag/head, including native macOS and provider
   gates. Download the final job artifact; verify `dry_run: false`, receipt head,
   gate logs and the hashes. Retain the artifact before its expiry.
3. After authorization to publish, create the matching **GitLab release** and
   attach the exact four binaries and `SHA256SUMS`, plus its verification receipt.
   Use GitLab's release UI/API; do not rebuild or use `gh release`.
4. After authorization to promote the mirror, copy the verified `downloads/`
   tree into staging, retain required historical versions, and run
   `make check-downloads`. Follow [Fly operations](fly-ops.md) for a separately
   authorized deployment; verify the downloaded binary version and command
   contract before declaring the installer ready.
5. After authorized promotion, fetch the **hosted homepage**, install page and
   installer. Save response bytes/statuses, deployed source/image, installed
   version and hashes. Check GitLab source, issue and manual-download links and
   absence of suspended source/install/badge/demo links. Append this evidence to
   the retained receipt and resolve `hosted_homepage_after_promotion`; a local
   build or screenshot alone does not prove the hosted website was updated.

CI syntax and artifact dependencies follow the
[GitLab YAML reference](https://docs.gitlab.com/ci/yaml/).
