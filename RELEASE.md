# Release v0.150.0

Bryan: prepare the next stable release as **v0.150.0**, following v0.149.5.
The CLI version comes from the release tag through `VERSION`; there is no
separate source version constant to update. Android's independent application
version is unchanged.

This release includes the integrated security, pairing and lifecycle changes:
encrypted terminal output rejects plaintext, malformed authenticated previews
leave input usable, explicit local-only wings bind loopback, phone pairing
explains LAN listeners, and restart hints preserve the live wing mode. It also
reclaims crashed sandbox directories only after proving their owners dead,
keeps ambiguous or locked directories, and tests serve-error cleanup. Main's
GitLab repository links are retained.

## Prepare and verify

1. Merge this commit into main preserving the merge commits. Confirm main
   contains `bcd9018`, `014b7e5`, and `1743ed7`, and that the checkout is clean.
   Before any rollout, run the read-only account collision queries and OAuth
   prerequisites in the deployment runbook. Investigate collisions before
   proceeding; retain Google sign-in and configured email recovery.
2. On the release host, run `nice -n 10 make check`,
   `nice -n 10 make check-android`, and, in a fresh checkout with only PATH and
   HOME set, `nice -n 10 make test-browser-join`. Run
   `nice -n 10 go test -race -p 2 ./internal/relay/... ./internal/sandbox/... ./internal/egg/... ./internal/wing/...`.
   Follow [GitLab release verification](docs/release-verification.md) for the
   runner contract and required static, integration, compatibility, Linux,
   real-provider and native macOS gates before publishing.
3. Verify v0.150.0 is still unused, then validate and build locally:

   ```sh
   sh scripts/check-release-tag.sh v0.150.0
   nice -n 10 make release CHANNEL=stable VERSION=v0.150.0
   (cd dist && sha256sum -c SHA256SUMS)
   scripts/check-release-contract.sh dist/wt-linux-amd64
   dist/wt-linux-amd64 --version
   ```

   The contract executable must match the host platform; use the Darwin or
   arm64 binary on those hosts. Confirm the version is v0.150.0 and preserve
   all four binaries and their checksum manifest together.

## Publish and deploy

1. Follow [GitLab release verification](docs/release-verification.md) for the
   separately authorized tag and publication steps. Tag the approved main commit
   `v0.150.0` on GitLab and require every `.gitlab-ci.yml` job to pass for that
   exact tag and source head. Retain the `verify-release` artifact and check its
   receipt, gate logs and asset hashes before publication. The GitHub release
   workflow is informational only; GitLab CI verifies artifacts but does not
   publish or deploy them.
2. Publish the four platform binaries and SHA256SUMS at the matching GitLab
   release, using the verified artifacts without rebuilding. Stage the verified
   download mirror as described in
   [docs/download-mirror.md](docs/download-mirror.md), including the required
   retained stable versions. Replace `latest.json` last, selecting v0.150.0
   with `draft: false`, `prerelease: false`, and the five matching asset URLs.
   `nice -n 10 make check-downloads` must pass before deployment.
3. Follow the deployment runbook's staged GitHub OAuth secret removal and
   deploy sequence, retaining Google OAuth and any existing enrollment list.
   Use `nice -n 10 make deploy` only with the verified mirror staged. See
   [docs/fly-ops.md](docs/fly-ops.md) for operational checks. Verify sign-in,
   recovered account machines, checksum-verified installation, and terminal
   encryption after rollout. No release or deployment is performed by this
   preparation commit.

## Compatibility and rollback

`wt wing start --local-only` now listens on loopback. Phone pairing over a LAN
requires the printed `--listen 0.0.0.0` command; preserve local-only or relay
mode when restarting. Loopback alone does not expose a phone listener to a LAN.

Sandbox recovery keeps legacy or unreadable ownership records and live owners;
it is bounded, so leftover directories may remain. The first implementation
also has a narrow allocation-versus-sweep lock race, permits a workload to
replace its own writable owner record, and can repeatedly miss directories
beyond its scan bound. These limitations should be tracked for hardening.

For rollback, retain the previous verified binaries, mirror and known-good
deployment image. Redeploy that image and restore the matching stable feed
after verifying its checksums. Do not reverse identity migration 008 or restore
disabled GitHub OAuth credentials; older builds may not serve recovered
identities correctly, so use the deployment runbook's recovery precautions.
