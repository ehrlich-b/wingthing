# Bug Report: Egg Network Allowlist Cannot Be Narrowed Below the Agent Profile

Found while dogfooding `wt egg opencode` as the sandbox for an untrusted
DeepSeek coding stream (`~/repos/claude`, the arli hopper). Filed per the
Dogfooding section of `CLAUDE.md`.

**Version:** `wt version v0.143.0-24-gd121d71-dirty`, macOS (Seatbelt path).

## Summary

An egg's effective network allowlist is the **union** of `egg.yaml` and the
agent profile's hardcoded `Domains`. There is no way to narrow, override, or
opt out of the auto-drilled set. A config that declares exactly one domain
still launches with nine.

This makes it impossible to express "run opencode, but it may ONLY reach my
provider" — which is the whole reason to put an untrusted agent in a sandbox.

## Repro

```sh
mkdir -p /tmp/arli-egg-test && cd /tmp/arli-egg-test
printf 'network:\n  - api.arliai.com\nfs:\n  - "rw:./"\nenv:\n  - OPENAI_API_KEY\n' > egg.yaml
wt egg explain opencode --config ./egg.yaml
```

Actual:

```
domains (9)
  api.arliai.com    declared
  *.anthropic.com   auto      agent "opencode" requires network access to *.anthropic.com
  *.openai.com      auto      agent "opencode" requires network access to *.openai.com
  *.googleapis.com  auto      agent "opencode" requires network access to *.googleapis.com
  opencode.ai       auto      agent "opencode" requires network access to opencode.ai
  *.opencode.ai     auto      agent "opencode" requires network access to *.opencode.ai
  models.dev        auto      agent "opencode" requires network access to models.dev
  localhost         auto      agent "opencode" requires network access to localhost
  127.0.0.1         auto      agent "opencode" requires network access to 127.0.0.1
```

Expected: some way to get `domains (1)`.

## Root cause

- `internal/egg/agents.go:94` — the `"opencode"` profile hardcodes
  `Domains: []string{"*.anthropic.com", "*.openai.com", "*.googleapis.com",
  "opencode.ai", "*.opencode.ai", "models.dev", "localhost", "127.0.0.1"}`.
- `internal/egg/config.go:398` — the documented merge rule is
  `network: union (dedup); "*" in either -> ["*"]`. Union only; there is no
  subtract form and no precedence for the more specific config.
- `internal/egg/config.go:213` — `RequiresSandbox`'s comment confirms this is
  deliberate: "Agent network requirements are included because they are drilled
  into the effective policy at launch."

So the behavior is by design. The gap is that the design has no escape hatch.

## Why this blocks a real workload

The containment story for the DeepSeek stream today is iptables egress pinned
to `api.arliai.com` on a Linux droplet, plus systemd hardening — all
hand-rolled, all Linux-only. Moving it to `wt egg opencode` would replace that
with a single portable contract that also works on macOS. That is the migration
this report is trying to make possible.

It can't happen yet, because the profile silently reopens the entire
frontier-lab frontier on a workload whose only boundary is the sandbox.

### The sharp edge

opencode reaches ArliAI through a variable *named* `OPENAI_API_KEY` pointed at
`api.arliai.com` (Arli is OpenAI-wire-compatible). That variable is in
opencode's `EnvVars` allowlist, and `*.openai.com` is auto-drilled into the
network policy.

The result: an untrusted agent inside the egg holds an OpenAI-shaped API key
**and** has `api.openai.com` reachable, created entirely by the sandbox's own
defaults. There was a real `sk-proj-` key leak in this stream on 2026-08-10, so
this is not a hypothetical threat model.

Worth noting the env side is already correct — `env:` is an explicit allowlist
the operator controls. It is specifically `network:` that can be added to but
never subtracted from. The asymmetry looks unintentional.

## Suggested fixes

Either would unblock the migration; (2) is the better product.

1. **Let the agent auto-drill be overridable.** e.g.
   `network: {domains: [api.arliai.com], agent_domains: none}`, or a `-domain`
   subtract form in the list syntax. Keep union as the default so no existing
   config changes behavior.

2. **Make the provider domain follow `WT_PROVIDER_BASE_URL`.** That variable is
   already in opencode's `EnvVars` allowlist, which reads like the intent was
   there — but the domain list stayed static. Today, setting a custom base URL
   produces an agent that cannot legally reach its own provider while retaining
   reachability to three vendors nobody asked for.

   This makes "point opencode at an OpenAI-compatible provider" a typed,
   first-class operation instead of a config union the operator has to audit by
   hand. It also seems like the honest reading of the shared-roost parity goal:
   a roost-exposed egg should be able to state its egress narrowly, and an
   auditor should be able to read that statement and believe it.

## Credit where due

`wt egg explain` is excellent. The `declared` vs `auto` column plus a per-hole
reason string turned this into a five-minute finding instead of an afternoon of
reading merge code. Whatever happens to the rest of this report, that command
is doing its job.

## Offer

The reporter has both a macOS machine and a Linux droplet (userns/seccomp path)
running this workload, and is happy to test a branch on both — a real
two-platform bed for the enforcement proxy if that is useful.
