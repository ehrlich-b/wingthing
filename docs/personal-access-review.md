# Remaining live access actions

No live access or host policy change is authorized by this draft. This document
separates the exact blocked operations from source implementation and fixture
validation. It is an approval preparation packet, not an executed setup.

## Owned Linux preview

Actual targets tested: existing owned work3 VM9006 and work2 VM9003. Strict
existing-key SSH, artifact checksums and preview channel validation succeeded.
Both refused the sandbox's unprivileged mount probe before creating a session:
`make root private: permission denied`, with the AppArmor user-namespace
restriction diagnostic. Temporary staging is removed; existing wt/Herdr
processes remain. Receipts are in the task workspace's
`physical-linux-preview-20261003/receipts/` and
`physical-linux-preview-work2-20261003/receipts/`.

Read-only work2 inspection confirmed a root-owned mode-0755 `/usr/local/bin/wt`
and root-owned mode-0644 `/etc/apparmor.d/wingthing`, with these exact policy
bytes and `apparmor_restrict_unprivileged_userns=1`:

```text
abi <abi/4.0>,
profile wingthing /usr/local/bin/wt flags=(unconfined) {
  userns,
}
```

That profile binds the installed executable path, not the isolated temporary
preview. The existing Linux installer may replace that binding, so it must not
be used for this preview. The new preview `doctor --fix` refuses that installer.
The read supports the separate-path/profile proposal; the proposed setup still
needs its own unprivileged capability verification.

A concrete next setup, if approved, is limited to **work3 VM9006**:

- Install the final reviewed Linux preview artifact at a new root-owned,
  root-writable-only, version-specific path under
  `/usr/local/lib/wingthing-preview/<source-SHA>/wt-preview`. Verify its package
  SHA-256 before use; do not replace `/usr/local/bin/wt` or another service.
- Add a separate executable-bound AppArmor profile/file named
  `wingthing-preview-<source-SHA>` with the existing `userns` permission shape,
  binding only that immutable executable path. Preserve the existing
  `wingthing` profile and global sysctls.
- Load only that profile and verify the unprivileged sandbox probe and a
  temporary preview session. All state, workspace and sessions remain in one
  owned temporary tree; no provider credential is copied.
- Stop owned test sessions, unload/remove only the new profile, remove the new
  executable tree and verify existing services remain. Keep the receipt.

This requires approval for the exact root-owned install and executable-scoped
AppArmor policy addition/removal on VM9006. The final artifact SHA/path and
profile bytes must be frozen in the package receipt before asking to execute.
It does not require disabling AppArmor or enabling namespaces globally.

## Cloud to this Mac

The Mac is already connected to its existing private NetBird network, but has
no SSH listener, sshd process or enabled Remote Login service. Outbound Mac SSH
to a VM does not establish the reverse cloud-to-Mac path. The remote CLI
controls Wingthing sessions; it does not control the desktop GUI.

For the requested SSH-equivalent acceptance, the remaining access actions are:

1. Admit the selected cloud worker to a private route reaching this Mac. Use an
   already authorized route if one exists; otherwise new VPN enrollment/route
   access is a separate approval.
2. Enable macOS Remote Login for only the intended existing local account.
3. Authorize one named cloud worker SSH public key for that account, with its
   fingerprint, source restriction and expiry/reversal recorded. Verify the Mac
   host key independently and keep strict host checking.
4. Select the separate preview binary/state explicitly, then test remote
   inventory, attach, detach and reconnect without changing stable sessions.

The cloud worker's admitted route and public-key fingerprint have not been
supplied/verified in this local task. A broad “enable remote access” approval
would therefore be premature. The parent-owned cloud lane must first prepare
those non-secret inputs; approval can then cover exact account, key, route,
listener and rollback. No SSH key, authorized-key record, Remote Login setting,
VPN membership or firewall rule was changed here.
