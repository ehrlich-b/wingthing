# Codex tool sandbox opt-in

Set `codex_native_sandbox: true` in egg.yaml to add Codex native tool sandboxing to a Codex egg session. The default remains the existing invocation. This option also applies to native `agent_run` turns and exact-session resumes. It does not change the standalone headless prompt adapter or other providers.

```yaml
codex_native_sandbox: true
fs: [ro:/, rw:./, deny:~/.ssh, deny-write:./egg.yaml]
network: none
dangerously_skip_permissions: true
```

The resolved `fs` write grants become canonical, sorted, deduplicated `sandbox_workspace_write.writable_roots`. A write grant covering cwd selects `workspace-write`; no grants selects `read-only`. Codex implicitly writes cwd in workspace mode, so a policy granting only other directories is rejected. Implicit `/tmp` and `$TMPDIR` write grants are disabled. Read denials, read-only exceptions inside a grant, protected files, symlinks and rename protection remain enforced by the outer sandbox.

`dangerously_skip_permissions: true` selects `approval_policy="never"` without disabling the native sandbox. Otherwise approvals use `on-request`. `network: none` disables tool network access even when the controller needs provider domains. Explicit tool domains, wildcard access or local ports enable the native network boolean; the outer sandbox retains destination and port enforcement. Read-only with enabled tool network is rejected because these native keys cannot represent it.

`wt doctor launch` includes the exact native config argument array and enforcement coverage in `codex_sandbox`. Provider state write holes and provider control domains are not added to native tool grants. No credentials are read or copied by the renderer. CLI config overrides take precedence over repository configuration. In this opt-in mode passthrough accepts only `--model`/`-m` and a literal prompt after `--`; policy, profile, cwd, feature and arbitrary config overrides are rejected. Inherited opt-in cannot be disabled by child config.

The outer Wingthing sandbox remains required for startup, MCP, hooks, filesystem exceptions, network destinations and resource limits. Trusted outer-boundary bypass mode is rejected for this option. Codex must pass the existing supported-version check (0.159.3 or newer); that check establishes syntax/lifecycle compatibility, not kernel sandbox availability. Doctor reports native nested availability as unverified. If the host cannot run the native sandbox inside the outer sandbox, launch fails; there is no unrestricted retry. This is an experimental rendering contract, not a replacement for backend capability probes or full security admission.

Run `make test-codex-sandbox` for offline conformance: closed/open/destination/loopback network intent, read-only/workspace mode, approvals, roots, symlink canonicalization, rejected overrides, exact resume and unchanged legacy launches. Native kernel enforcement must be verified separately on the deployment host.

Native key definitions: [official OpenAI configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference). CLI rendering implements only the controls described above.
