# Release download mirror

The relay can serve verified CLI artifacts from a local directory at `/dl/`.
Set `WT_DOWNLOAD_DIR` to the release tree; leave it unset to disable downloads.
The Docker image copies the staged, ignored `downloads/` directory to
`/app/downloads`. Directory listings and symlinks are refused.

Keep tagged artifacts immutable. Each `<tag>/` directory has the four
`wt-{linux,darwin}-{amd64,arm64}` binaries and `SHA256SUMS`. Upload/stage all
assets before replacing `latest.json`. The feed uses the GitHub release JSON
shape (`tag_name`, `prerelease`, `draft`, `assets`), but mirror asset URLs are
constructed from the shared base and validated tag. Preview uses an independent
`preview.json` release-list feed and `wt-preview-*` files.

The default download base is `internal/downloads/base.txt`. Change it there and
run `make generate-installer` to regenerate both installer copies. The relay
renders its installer from the same template and default. `WT_DOWNLOAD_BASE`
provides an absolute HTTPS override for a relay, CLI or installer invocation;
no credentials, query strings or shell metacharacters are accepted in a base.

Installers and updates try the mirror first and GitHub second. Any failed or
invalid binary/checksum/contract falls back to a complete verified bundle;
sources are never mixed. Both failures produce one plain sentence. Updates
still validate channel identity, serialize daemon lifecycle changes and replace
the executable atomically only after all verification succeeds. Relay version
checks retain their cache and retry backoff when both sources fail.

This affects the newly built client. Mirroring older release binaries does not
change their embedded updater. Existing clients can use the new installer to
reinstall a historical version, and acquire the new updater only through a new
reviewed CLI release.

Deployment requires a complete staged tree: `latest.json` selects stable
`v0.149.5` and lists its four platform binaries and `SHA256SUMS` with the
`https://wingthing.ai/dl/v0.149.5/` URLs. Keep both `v0.147.0/` and `v0.149.5/`,
each with four nonempty binaries and their matching `SHA256SUMS`.
Copy a verified release tree with `cp -R /path/to/verified/downloads/. downloads/`,
then run `make check-downloads`. This validates every binary hash and refuses
symlinks, incomplete manifests and stale latest metadata.
`make deploy` checks staging before running its existing check and gate targets;
the Docker build runs the same validation, so `fly deploy` also refuses an
unstaged image. Downloads remain ignored by Git.
