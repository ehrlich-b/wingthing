# Remote MCP surface

A roost can expose its privileged tools to remote MCP clients at `POST /mcp`. The endpoint
uses OAuth authorization-code flow with PKCE and applies a role policy before listing or
calling a tool. It is disabled unless `wing.yaml` explicitly enables it.

## Configuration

Tool definitions and MCP policy live under the wing's normal configuration directory. MCP
uses the same `tools_dir` as in-wing agents; it does not have a second policy file or tool
registry.

```yaml
tools_dir: ~/.wingthing/tools

mcp:
  enabled: true
  default_allow_all: false
  roles:
    engineering:
      enabled: true
      allow: [database-search, issue-search]
      members:
        - alice@example.com
    support:
      enabled: true
      deny: [database-search]
      members:
        - bob@example.com
    sales:
      enabled: false
      members:
        - bob@example.com
```

There are two gates:

1. A user must belong to at least one role with `enabled: true`.
2. At least one enabled role must allow the requested tool.

An `allow` list is a whitelist. A `deny` list allows every tool except the listed names.
Setting both on one role is invalid. An enabled role with neither list follows
`default_allow_all`, which defaults to false.

Users can belong to more than one role. Their effective tool set is the union of the tools
allowed by all MCP-enabled roles (the maximum subset). A disabled role contributes neither
grants nor denies, so membership in a disabled role cannot reduce access granted by another
role.

Changing tools, members, or allow/deny rules and sending `SIGHUP` reloads the MCP tool runner
and policy together. Enabling or disabling the endpoint itself requires a roost restart.

## Tool definitions

Each YAML file in `tools_dir` defines one privileged command. Files should be mode `0600`.

```yaml
name: issue-search
description: Search issues with a bounded query
params:
  - name: query
    description: Query expression
    type: string
    required: true
  - name: limit
    description: Maximum number of results
    type: integer
run: /usr/local/libexec/issue-search "$1" "$2"
timeout: 20s
max_concurrent: 5
env:
  API_TOKEN: configured-secret
```

The optional ordered `params` list is published as the MCP tool's JSON input schema and is
mapped back to positional arguments for the existing runner. Supported types are `string`,
`integer`, `number`, `boolean`, `object`, and `array`. String parameters can also declare an
`enum`; all types can include `description` and `examples`.

Tools without `params` retain the compatibility schema:

```json
{"args": ["first positional argument", "second positional argument"]}
```

Tool YAML is decoded strictly. Unknown fields, malformed parameter metadata, invalid names,
and multiple YAML documents fail loading instead of silently weakening the interface.

## OAuth and authorization

The roost publishes protected-resource and authorization-server metadata under
`/.well-known/`. MCP clients dynamically register a public client, send the user through the
normal roost login and consent page, and exchange an authorization code using PKCE S256.

Access tokens are dedicated MCP JWTs. Validation requires the expected issuer, `/mcp`
audience, client ID, token-use claim, signature, and expiry. General wing JWTs and database
API tokens are rejected. Role membership is checked again when an access or refresh token is
used, so removing a member revokes future use without waiting for every token to expire.

The current authorization flow represents human users only. A proposed future design for
non-human workloads, direct API tokens, and OAuth client credentials is documented in
[MCP service accounts and API credentials](mcp-service-accounts-design.md). It is not part of
the current implementation.

Dynamic client registrations are stored in the roost database. Authorization codes and
refresh-token grants are short-lived process state; restarting the roost requires the MCP
client to authenticate again.

## Execution boundary

MCP calls use the same privileged `ToolRunner` implementation as in-wing calls. The command
receives its configured `env` values plus trusted `WT_MCP_*` identity values for the current
call. It does not inherit unrelated roost secrets such as OAuth, SMTP, JWT, or model-provider
credentials. Standard process variables such as `PATH`, `HOME`, locale, and certificate paths
remain available.

Each tool has a timeout and optional concurrency limit. Standard output and error are capped
to prevent an unexpectedly large backend response from exhausting roost memory or producing
an unbounded MCP result. Every allowed or denied MCP call is written to the roost audit log.

Tool commands still execute outside the agent sandbox. Treat every command and its argument
validation as a privileged API: use an argv-safe wrapper, enforce a narrow read-only
allowlist, bound backend results, and avoid putting secrets in output or command-line flags.

## Client setup

For a roost available at `https://wing.example`, Claude Code can register it with:

```sh
claude mcp add --transport http wingthing https://wing.example/mcp
```

Claude opens a browser for login and consent on first use. Removing and re-adding the entry
starts a fresh registration and authorization flow.
