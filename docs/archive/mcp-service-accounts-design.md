# MCP service accounts and API credentials

Status: proposed future design. Nothing in this document is implemented, advertised, or
enabled by the current MCP server.

## Summary

Wingthing's remote MCP surface currently authorizes a human who signs in through the OAuth
authorization-code flow. Some future consumers will not have a human present: scheduled jobs,
CI pipelines, long-running agents, and server-to-server integrations. Those consumers need a
first-class machine identity rather than a fake user or a shared human login.

This design introduces a service account as a non-human MCP principal. A service account has
an explicit owner, is assigned existing MCP roles, and can have independently rotatable and
revocable credentials. Human OAuth and service-account authentication converge on one trusted
principal object before the existing role and tool policy is evaluated.

Two credential paths are proposed:

1. An opaque API token for MCP clients that can send a configured bearer token but cannot
   perform a client-credentials exchange.
2. The MCP OAuth Client Credentials extension for unattended clients that can obtain and
   renew short-lived access tokens.

Both paths authenticate the same service-account principal and receive the same tools. They
do not create a second authorization system.

## Terminology

- **User**: a human authenticated through Wingthing's existing login and OAuth consent flow.
- **OAuth client**: software connecting to Wingthing. A public Dynamic Client Registration
  client ID identifies software; it is not a user and is not a credential.
- **Service account**: a non-human principal representing one workload or integration.
- **Credential**: a secret or public key that authenticates a service account.
- **Access token**: a bearer token accepted by `POST /mcp` after authentication or exchange.
- **Owner**: the human accountable for a service account. Ownership is audit metadata, not an
  implicit permission grant.

A token configured in one person's local client is not automatically a service account. If
the action should be attributed to that person, the existing human OAuth flow is preferable.
Service accounts are for workloads whose identity should survive personnel and login sessions.

## Goals

- Support unattended access to the existing Streamable HTTP MCP endpoint.
- Preserve the current role policy and maximum-subset semantics.
- Re-evaluate authorization on every request so configuration changes take effect immediately.
- Give every workload a distinct, attributable principal and credential.
- Allow credentials to be created, expired, rotated, and revoked independently.
- Store no recoverable opaque token secrets in the roost database.
- Work with direct-bearer clients while providing a standards-based path for MCP SDKs.
- Keep human OAuth behavior and existing tokens backward compatible.

## Non-goals

- A REST facade over MCP tools.
- General-purpose Wingthing user API keys.
- Sharing one credential among unrelated people or workloads.
- Adding write-capable tools or weakening a tool's own validation.
- Replacing human OAuth, consent, or Dynamic Client Registration.
- Building external identity federation, workload identity, mTLS, or DPoP in the first version.
- Implementing this proposal as part of the initial remote MCP release.

## Current state

An MCP access JWT currently contains a user ID in `sub`, a public OAuth `client_id`, a dedicated
`token_use`, issuer, `/mcp` audience, and a one-hour expiry. On every request, the resource
server loads the user, finds role membership by email, removes MCP-disabled roles, and applies
the union of the tools allowed by the remaining roles.

That per-request authorization is the right security property. The user-specific assumptions
are the seam to replace:

- The request identity contains `UserID` and `Email` rather than a generic principal.
- Role membership can only be discovered through email lists.
- Audit events are keyed as user events.
- Trusted tool environment variables assume a human email.
- The token endpoint only accepts authorization-code and refresh-token grants.

Dynamic OAuth registrations must not be repurposed as service accounts. They are public client
identifiers with no secret and describe the connecting application, not the authority under
which it acts.

## Architecture

```text
Human authorization code ──> MCP user JWT ─────────────┐
                                                       │
Opaque API token ──────────> credential lookup ────────┼─> MCP principal
                                                       │       │
Client credentials ────────> short-lived MCP SA JWT ──┘       v
                                                        enabled roles
                                                              │
                                                              v
                                                        existing tool policy
                                                              │
                                                              v
                                                        ToolRunner + audit
```

Authentication answers "which principal is calling?" Authorization answers "which tools may
that principal use now?" No credential is itself a role grant.

## Declarative authorization

Service-account identity and role assignment belong in `wing.yaml` with the rest of the MCP
authorization policy. Secret material does not.

```yaml
mcp:
  enabled: true
  default_allow_all: false
  roles:
    engineering:
      enabled: true
      allow: [database-search, issue-search]
      members: [alice@example.com]
    reporting:
      enabled: true
      allow: [issue-search]
  service_accounts:
    nightly-report:
      enabled: true
      roles: [reporting]
      owner: alice@example.com
```

Validation must fail closed:

- Service-account names are non-empty, normalized, and unique.
- Every referenced role exists.
- Unknown fields are rejected.
- A disabled or removed service account authenticates to no MCP tools.
- MCP-disabled roles contribute no tools, just as they do for users.
- Service-account roles use the same maximum-subset union as human roles.

A `SIGHUP` policy reload changes service-account authorization atomically with role and tool
changes. Credentials remain durable operational state and do not require a configuration
change for routine rotation.

## Persistent credential state

The roost database stores credential metadata and one-way verification material. One possible
schema is:

```text
mcp_service_account_credentials
  id                  text primary key
  service_account     text not null
  kind                text not null       # opaque_token, client_secret, public_key
  secret_hash         blob                # opaque token/client secret only
  public_key          blob                # private_key_jwt only
  created_by_user_id  text
  created_at          datetime not null
  expires_at          datetime not null
  revoked_at          datetime
  last_used_at        datetime
```

The configured service-account name is the authorization source of truth. A database
credential whose account is absent or disabled in the current policy cannot authorize a call.

Multiple active credentials per account permit zero-downtime rotation. Credential creation
shows an opaque secret exactly once. Listings show only credential ID, kind, timestamps, and
status. Copying either the database or configuration must not reveal a usable secret.

Opaque credentials contain at least 256 random bits. Because generated secrets have high
entropy, a SHA-256 fingerprint is sufficient for lookup; an HMAC with a server-side pepper can
add defense against accidental secret-generation regressions. Comparisons are constant-time.
Secrets are never derived from the JWT signing key, `WT_JWT_SECRET`, account name, or owner.

## Direct API tokens

Direct API tokens provide compatibility with clients that accept a fixed bearer token. A
token has a recognizable selector plus a random secret:

```text
wt_sa_<credential-id>.<random-secret>
```

The selector finds the credential row without scanning every hash. It is not secret. The
server hashes the secret, verifies the row, then resolves the named service account from the
current policy.

Example Codex configuration:

```toml
[mcp_servers.wingthing]
url = "https://wing.example/mcp"
bearer_token_env_var = "WINGTHING_MCP_TOKEN"
```

```sh
export WINGTHING_MCP_TOKEN='wt_sa_...'
```

The token must only be accepted in the `Authorization: Bearer` header over HTTPS. It is never
accepted in a URL, query parameter, cookie, or MCP arguments. Direct tokens should have a
deployment-defined maximum lifetime; 90 days is a reasonable initial ceiling, not a protocol
requirement.

Opaque tokens are long-lived bearer credentials and therefore less desirable than a
short-lived exchange. Their advantages are client compatibility and immediate server-side
revocation. They should be issued only when interactive OAuth or client credentials is not
usable.

## OAuth client credentials

For unattended clients that support it, Wingthing should implement the official MCP OAuth
Client Credentials extension. Confidential clients are provisioned administratively and tied
one-to-one to a service account. They are separate from public Dynamic Client Registration
records.

The authorization-server metadata adds:

```json
{
  "grant_types_supported": [
    "authorization_code",
    "refresh_token",
    "client_credentials"
  ],
  "token_endpoint_auth_methods_supported": [
    "none",
    "client_secret_basic",
    "private_key_jwt"
  ]
}
```

The initial implementation may support `client_secret_basic`; `private_key_jwt` is preferred
for workloads able to hold an asymmetric key. Client secrets are generated, stored, and
rotated under the same rules as opaque API tokens. Private keys never enter Wingthing; the
roost stores the registered public key and verifies a short-lived client assertion.

After authenticating the client, `/oauth/token` issues the existing ES256 access-token class
with explicit machine identity:

```json
{
  "iss": "https://wing.example",
  "sub": "service-account:nightly-report",
  "aud": "https://wing.example/mcp",
  "token_use": "mcp",
  "principal_type": "service_account",
  "client_id": "...",
  "credential_id": "...",
  "scope": "mcp",
  "iat": 0,
  "exp": 0,
  "jti": "..."
}
```

The token lifetime remains short (the current one-hour ceiling is acceptable). A client
credentials response does not include a refresh token; the confidential client authenticates
again when it needs another access token.

The server checks account existence, account state, credential state, and enabled roles again
on every MCP request. This makes account disablement immediate. Checking `credential_id` also
makes credential revocation immediate rather than waiting for the access JWT to expire.

The MCP `initialize` result advertises
`io.modelcontextprotocol/oauth-client-credentials` only after the extension is implemented
end to end. Extension negotiation must not be advertised for direct opaque tokens alone.

## Scopes and roles

Roles remain the authorization ceiling. A credential can never request a role or tool that its
configured service account does not have.

An initial implementation can expose one `mcp` scope and rely on the existing per-tool role
policy. Future token-level scopes may narrow that ceiling, for example `tool:issue-search`, but
scope union must never widen it:

```text
effective tools = tools allowed by current roles ∩ tools allowed by requested scopes
```

This avoids duplicating role policy inside durable credentials or long-lived JWT claims.

## Unified principal

Both authentication paths produce a common internal identity before entering the MCP server:

```go
type MCPPrincipal struct {
    Type         string   // user or service_account
    ID           string
    DisplayName  string
    Email        string   // human only
    OwnerEmail   string   // service account only
    ClientID     string
    CredentialID string
    Roles        []string
}
```

The exact Go shape is not normative. The important constraint is that code never decides
whether a subject is human by guessing from `sub`, an email-like string, or the credential
format. Token validation returns an explicit principal type and the resolver verifies it.

Trusted tool environment should add generic fields:

```text
WT_MCP_PRINCIPAL_TYPE
WT_MCP_PRINCIPAL_ID
WT_MCP_PRINCIPAL_NAME
WT_MCP_OWNER_EMAIL
WT_MCP_CREDENTIAL_ID
```

Existing `WT_MCP_USER` and `WT_MCP_EMAIL` remain for human compatibility and are empty for a
service account. No bearer token, client secret, assertion, or token fingerprint is passed to
the tool process.

## Auditing

Audit records must distinguish a human resource owner from a machine client. Each token and
tool event records, where applicable:

- Principal type, ID, and display name.
- Service-account owner.
- OAuth client ID and credential ID.
- Effective roles.
- Tool, exit status, and error status.
- Existing bounded argument detail or argument digest.
- Source address and user agent where available.

Credential creation, rotation, expiry, revocation, failed authentication, token issuance, and
policy denial are separately auditable events. Administrative actions record the human actor;
tool calls record the service account as principal.

The current audit schema's `user_id` can remain for backward compatibility, but generic
principal fields should be first-class rather than encoded only into a free-form detail blob.

## Rate limiting and execution

Pre-authentication endpoints continue to use the existing per-IP limit. After authentication,
service accounts also need per-principal and optionally per-credential limits so one workload
cannot consume every tool's concurrency allowance from many source addresses.

Tool timeout, concurrency, output caps, environment filtering, parameter validation, origin
validation, and read-only wrapper requirements are identical for human and service-account
calls. A service account is an authentication feature, not a relaxation of the execution
boundary.

## Management interface

The first management surface should be a local administrative CLI rather than a public API:

```text
wt mcp service-account list
wt mcp service-account credential create <name> --kind token --expires 90d
wt mcp service-account credential create <name> --kind client-secret --expires 365d
wt mcp service-account credential add-key <name> --public-key <path>
wt mcp service-account credential list <name>
wt mcp service-account credential revoke <credential-id>
```

Command names are illustrative. Commands must refuse accounts absent from the current policy,
require local administrative access, avoid printing secrets after creation, and append an
audit event. An authenticated admin UI can be considered after the lifecycle and authorization
model have proven stable.

## Security properties

- A leaked opaque token grants only the account's current role-derived tools until expiry or
  revocation.
- A copied database does not contain plaintext opaque secrets or private keys.
- A copied `wing.yaml` contains authorization metadata but no credentials.
- Removing an account, disabling it, or disabling all its roles blocks the next call.
- Revoking a credential blocks direct tokens and credential-bound access JWTs immediately.
- MCP, wing, and handoff token classes remain non-interchangeable.
- User and service-account subjects remain unambiguous.
- Every service account has one declared owner and every workload has its own credential.

Long-lived bearer tokens remain replayable if stolen. Later hardening can add
`private_key_jwt`, DPoP, mTLS, cloud workload identity, or external authorization servers, but
none is required to establish the first-class principal boundary.

## Rollout plan

1. **Principal refactor**: introduce the generic MCP principal internally without changing
   human OAuth behavior. Add regression tests proving identical human authorization and audit.
2. **Declarative accounts**: parse and strictly validate service accounts and role references,
   but keep credential issuance unavailable by default.
3. **Opaque credentials**: add durable hashed credentials, local lifecycle commands, direct
   bearer authentication, principal-aware audit, and per-principal rate limits.
4. **Client credentials**: add confidential clients, token exchange, metadata, extension
   negotiation, and `client_secret_basic`.
5. **Asymmetric authentication**: add `private_key_jwt` after a concrete client requires it.
6. **Administrative UX**: consider an admin API or UI only after operational experience.

Each phase is independently releasable and fails closed when no service accounts are
configured. The initial remote MCP release needs none of these phases.

## Test plan

Unit and integration coverage must include:

- Human OAuth behavior remains unchanged.
- User and service-account token classes cannot cross identity types.
- Unknown, malformed, expired, and revoked credentials return the same unauthenticated result.
- Plaintext credentials never enter storage, logs, audit detail, or tool environment.
- A disabled or removed account is denied on its next request.
- Credential revocation invalidates already issued credential-bound access tokens.
- Role changes and MCP-disabled roles take effect on the next request.
- Multiple roles retain maximum-subset semantics.
- Optional scopes can only narrow the role-derived tool set.
- Cross-origin rejection occurs before bearer authentication.
- Rate limits cover credential exchange and direct-token MCP calls.
- Client credentials never receive refresh tokens.
- Access JWTs retain issuer, audience, expiry, client ID, and dedicated token-use validation.
- Direct-bearer Codex configuration can initialize, list tools, and call an allowed tool.
- An official MCP SDK can exchange client credentials and renew access without a browser.

## Open questions

- Must every service account owner be a current MCP-enabled user, or only an administrator?
- Should opaque direct tokens have a hard global maximum lifetime?
- Is `client_secret_basic` necessary, or can the first real automation use `private_key_jwt`?
- Should credential revocation be checked on every JWT-authenticated call or through a bounded
  cache with explicit invalidation?
- Should a future personal access token use the same credential table with `principal_type=user`?
- How should credential state be replicated if a roost deployment gains multiple independent
  authorization-server nodes?

These decisions should be made against a real unattended consumer rather than guessed as part
of the initial MCP release.

## References

- [Wingthing remote MCP surface](wingthing-mcp-design.md)
- [MCP OAuth Client Credentials extension](https://modelcontextprotocol.io/extensions/auth/oauth-client-credentials)
- [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
- [OAuth 2.0 client credentials grant (RFC 6749)](https://www.rfc-editor.org/rfc/rfc6749#section-4.4)
- [OAuth 2.0 Security Best Current Practice (RFC 9700)](https://www.rfc-editor.org/rfc/rfc9700)
- [JWT profile for OAuth access tokens (RFC 9068)](https://www.rfc-editor.org/rfc/rfc9068)
- [OAuth JWT assertions (RFC 7523)](https://www.rfc-editor.org/rfc/rfc7523)
- [Codex MCP bearer-token configuration](https://learn.chatgpt.com/docs/extend/mcp)
