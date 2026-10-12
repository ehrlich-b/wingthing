# Reviewing a session's launch policy

Press `i` in the navigator to inspect the selected machine and session. Details
show the reported workspace, provider home, filesystem mounts, denied reads and
writes, network mode, domains and loopback ports. Privileged sessions explicitly
show full OS-user authority and that the Wingthing sandbox is disabled.

The egg saves a private, bounded `session-policy.json` receipt from production
launch preparation. Inventory transports this snapshot from the owning machine;
the navigator does not load a current local `egg.yaml` for a remote process.
Environment values, provider arguments, prompts and credential contents are
excluded. Missing, corrupt, unsupported or differently bound receipts display
unknown policy instead of inferring permissions from a directory or an isolation
label. The snapshot is a review aid: displaying it does not independently prove
native confinement, provider readiness or descendant cleanup.

Protected scoped-parent launch admission remains refused on Linux where its
provider-write protection model is unavailable. The refusal retains a typed
`admission/policy` diagnostic and the selected policy path when available.
Native macOS qualification remains **REQUIRED-UNRUN** on this Linux host.
