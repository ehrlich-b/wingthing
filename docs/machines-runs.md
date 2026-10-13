# Inspect machines and bounded runs

In bare `wt`, `m` selects an inventory scope. Press `i` in the machine picker
or `M` in the navigator to inspect a saved route, pinned wing ID, control
contract, inventory freshness and setup/recovery guidance. A CLI-only profile
is labelled unverified for parent MCP. `wt mcp connect add NAME --ssh SSH_ALIAS`
can verify an existing CLI-only profile on that same SSH target. Omitted binary
and state selectors reuse the saved values. Verification failure preserves the
profile; a concurrently changed profile or an existing pin cannot be replaced. A saved verification is not evidence
of current connectivity. Identity failures require explicit out-of-band review;
the UI never changes host trust, provisions a host or replaces a wing.

Press `d` for read-only launch readiness / egg reach and completed-copy forms.
The forms run the existing `doctor launch-plan` and `run-inspect` CLI verbs with
literal argv in a bounded child process. The launch form plans catalog unattended
arguments inside the configured boundary (`--unattended`); it never runs them. Workspace, provider HOME, allowed HOME
root, executable and policy are explicit inputs. Blank policy explicitly selects
built-in defaults; configured policy-base and host HOME can be named separately.
A remote path must be inspected on its owning host. Native capability facts and
saved provider evidence use the full doctor CLI. Reports spend no model turns,
open no wing connection, repair no policy and resume no agent. Scroll with j/k
or PageUp/PageDown; Escape preserves navigator scope. Reports are bounded to
128 KiB and 30 seconds; report refusal is not provider execution failure.

`h` opens authorized wing-owned run history; `i` shows full wing/run/session
identity, original idempotency key, phase, isolation and execution deadline.
Omitted/zero timeout means no execution deadline. Inventory preserves pending
and running status when the optional native failure field is absent; an absent
failure field on an unsuccessful terminal receipt still means unknown outcome. An open receipt uses the latest
matching run projection on refresh. Unavailable output retains its terminal
outcome; unknown admission requires reconciliation with the original key.
Completion does not establish descendant cleanup. No automatic resend, resume
or privileged fallback is added. Legacy `wt run` remains a separate task route.

These UI adapters retain the typed doctor, inventory and snapshot contracts.
Doctor and offline inspector remain deliberate local read-only consumers; they
are not new remotely callable MCP tools. Fixture journeys prove CLI routing,
receipt and report behavior, not real provider login or real SSH host trust.

Run `make test-storyboard-machines-runs` for unit and built-PTY journeys.
`WT_STORYBOARD_EVIDENCE=/absolute/directory` retains per-card PTY byte streams,
120x36 screen text and executable SHA256 for faithful screenshot rendering.

Verified profiles also bind native SSH inventory, attachment, rename, stop and
pin to the saved wing ID at each receiving control handshake. A replaced wing
at the same host/state path is refused before terminal I/O or mutation. Inventory
returns the authenticated origin wing ID. No identity failure or unavailable
verified wing falls back to standalone egg access or starts a replacement daemon.
Existing CLI-only profiles retain their standalone SSH contract. A receiving
binary without the identity guard fails with an upgrade diagnostic.

New terminal/agent launches through a verified profile use the receiving wing's
authorized start primitive and policy. Per-launch config, trace, resume and
unsandboxed selectors are refused rather than ignored. Other standalone-only
CLI verbs are unavailable on the verified route; use its typed MCP interface.
