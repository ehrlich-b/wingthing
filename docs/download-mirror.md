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

Installers, updates and relay version checks use only the product's `/dl`.
Failed downloads, manifests, checksums or command contracts fail without any
forge fallback or changes to the existing binary. Updates still validate channel
identity, serialize daemon lifecycle changes and replace the executable atomically
after verification. Relay version checks retain their cache and retry backoff.

Stable metadata at `/dl/latest.json` is a release object. Preview metadata at
`/dl/preview.json` is a list of release objects with the same fields, ordered
newest first; only published, non-draft `v*-preview.*` prereleases qualify.
The preview feed currently returns 404. Preview update reports that the feed is
unavailable and directs users to `update --file` with a reviewed local package;
it never calls GitHub or selects stable instead.

This affects the newly built client. Mirroring older release binaries does not
change their embedded updater. Existing clients can use the new installer to
reinstall a historical version, and acquire the new updater only through a new
reviewed CLI release.

Production deployment requires a complete staged tree: `latest.json` selects a
stable tag and lists its four platform binaries and `SHA256SUMS` with matching
mirror URLs. Each retained release directory must contain the four nonempty
binaries and matching `SHA256SUMS`; text and binary checksum lines are accepted.
Copy a verified release tree with `cp -R /path/to/verified/downloads/. downloads/`,
then run `make check-downloads`. Every binary hash is checked; symlinks,
incomplete manifests and mismatched latest metadata are refused. New stable
releases require staging only, with no source change to the validator.
`make deploy` checks staging before its existing check and gate targets.
Production `fly.toml` sets `REQUIRE_DOWNLOADS=1`, so a production `fly deploy`
also refuses an unstaged image. Plain `docker build -t wingthing .` leaves the
build argument off. Self-hosters use `fly launch --config fly.selfhost.toml
--name my-wingthing` and `fly deploy --config fly.selfhost.toml`; this template
leaves the mirror requirement off. Downloads remain ignored by Git.

For manual downloads, see [GitLab releases](https://gitlab.com/ehrlich-b/wingthing/-/releases).

GitLab tag verification prepares this exact tree as a job artifact, together with
`receipt.json` binding the source, gates and all five asset hashes. Follow the
[release checklist](release-verification.md); stage those tested bytes rather
than rebuilding. CI does not publish a release or replace the hosted mirror.
