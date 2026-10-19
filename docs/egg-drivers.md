# Egg execution drivers

Existing egg files select the `legacy` driver. It uses the same policy resolver,
provider argument and environment builders, platform sandbox, PTY, process
supervisor and retained session state as before. The `Driver` interface separates
capability description, prerequisite probing, preparation and sandbox creation.
Its returned sandbox keeps the existing Exec, PostStart and Destroy lifecycle.
The server continues to own streaming, cancellation, collection and retention.
Unknown driver IDs fail before preparation; there is no automatic backend switch.

`wt doctor launch-plan --json` includes a `driver` report with platform, backend,
execution location, protocol, capability scope, enforcing owner and exceptions.
The existing executable digest, version evidence, policy sources and arguments
remain in the surrounding report. No provider execution or capability probe is
performed by launch-plan. Reports contain no environment values or credentials.

Capability states are `supported`, `unsupported` and `unverified`. Static legacy
reports leave filesystem, network and resource enforcement unverified until
native acceptance. The explicit deployment probe can establish sandbox startup
availability only. It cannot certify every policy, cgroup quota or provider
behavior. Explicit required capabilities reject both unsupported and unverified
controls, including a supported control declared for a different scope.

The sandbox covers descendants created inside it: harness startup, shells, file
tools, local MCP/LSP processes, hooks, setup commands, git helpers and subagents.
Host services, remote execution, privileged callbacks and the Wingthing controller
remain outside that boundary. Explicit outer-boundary mode reports no Wingthing
sandbox enforcement. Domain observation mode and transport differences remain
visible in the surrounding effective policy; startup availability is not a claim
of closed networking or protected credential isolation.

This interface does not add native Claude/Codex policy, a version lock or minor
release certification, subscription auth brokerage, refresh leases, a versioned
resume contract, or v2 scoped literal/secret environments. Those capabilities are
reported unsupported by legacy. Existing v1 environment selection and credential
visibility are preserved, including their sharing with tool descendants. New
drivers must establish their own contracts before replacing that enforcement.

Linux v1 golden fixtures capture provider arguments, environment, effective
policy and the complete sandbox specification from the previous implementation
for Claude, Codex, Gemini and shell commands. Native sandbox integration tests
remain necessary to establish OS enforcement separately from launch equivalence.
