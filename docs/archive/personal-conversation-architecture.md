# Personal conversational controller

This draft builds a persistent personal controller on Wingthing's existing
session control plane. A person opens a parent conversation, gives it work,
and inspects its children as tasks with native messages, tool activity, lifecycle
evidence and terminal access. The compact dot is an entry point to that parent;
its shape is not the orchestration contract.

The original public coordinator role and the next home-roost identity/network
direction are specified in [personal-home-roost.md](personal-home-roost.md).
DotID, HomeRoostID and ExecutorID have distinct responsibilities; the current
typed context reports a local root/executor while leaving home identity and
owner epoch explicitly unimplemented. The proposed personal default uses LAN
and user-selected endpoints without vendor discovery or automatic fallback.

Herdr already provides agent launch, prompt, read and wait operations. Its
official [automation](https://herdr.dev/docs/agent-automation/) and
[agent](https://herdr.dev/docs/agents/) documentation establish that baseline.
The local hands-on evaluation also proved subscribed Codex prompt/wait,
observers, writer takeover and explicit native cold resume. Wingthing needs
those reliable controls before a persistent conversational interface is useful.

## Resource and persistence model

| Resource | Identity and responsibility |
| --- | --- |
| Wing | Stable execution-machine ID; workspaces, provider credentials and runtime state stay on that machine. |
| Logical conversation | Durable owner, root and parent IDs, title, intent/checkpoint and acknowledged event cursor. |
| Execution | A particular Wingthing session and exact provider conversation ID; replacements retain the logical link and earlier executions. |
| Session | Persistent interactive PTY, egg process, sandbox, terminal streams and native lifecycle journal. |
| Headless run | Existing supervised task lifecycle; it remains a distinct resource rather than being renamed an interactive conversation. |
| Browser task | A qualified wing/conversation/session selection over the same typed handlers used by MCP. |

A linked launch reserves a caller request ID before starting an egg. Retrying
the same request returns the same logical and execution IDs; changed arguments
are rejected. An interrupted reservation is inspectable, not permission to
start another child. Friendly labels help navigation but never replace durable
IDs. Cross-wing selections, actions and content caches require both wing ID
and object ID.

The parent reads child events through a persisted per-execution import cursor.
Import and delivery records commit together. Reading is replayable; explicitly
checkpointing advances the parent's acknowledgement cursor with a revision
check. This is a recovery contract, not a promise that arbitrary model actions
execute exactly once. Recovery must inspect old execution tails as well as the
latest execution so resume cannot erase a pending completion or decision.

## Native evidence, input and ownership

The first interactive lifecycle adapter uses exact Claude transcript identity
and observational per-invocation hooks. Terminal silence and output matching
remain terminal observations. They do not prove startup readiness, accepted
input, a completed turn, or completed delegated work.

The typed view carries state, source, reason, process availability, transcript
cursor and independent state cursor. Native foreground Stop is an observed
foreground response boundary; later Stop hooks, background work and follow-up
prompts can continue the conversation. The parent must interpret that evidence
against its assigned task instead of treating every Stop as a final result.
Missing adapters or missing native proof produce an explicit unknown state.
Recorded process failure/exit is distinct from a lost control connection.

Prompt delivery requires a durable request reservation and bounded receipt
wait. A matching exact-provider native user transcript row is stronger evidence
than successfully writing bytes, but without a provider request ID it is a
text receipt rather than a provider-issued request acknowledgement. A single
writer lease across terminal, browser and MCP paths is necessary to exclude
competing human input while a receipt is pending. Timeouts and lost
acknowledgements remain unconfirmed; retrying the same request must not resend.

Human decisions remain visible provider needs-input events. The interface must
show their source and reason and offer terminal inspection/control. It does not
automatically approve a provider permission request, grant new authority, or
send a generic chat response as a permission decision.

## Same controller through browser and MCP

A linked parent receives an invocation-specific MCP configuration bound to its
logical conversation and existing owner. The browser, CLI and model use the
same schemas and owner checks. Parent launches record children under that
binding; the browser task tree is a rendering of runtime lineage.

The existing provider sandbox must already permit the selected control
executable, state directory and transport. Mounting a configuration alone does
not prove reachability. The fixture journey must demonstrate a parent invoking
its actual configured MCP server and creating two children. A blocked ordinary
workspace reports the exact path/transport boundary; it must not quietly
relax sandbox policy. Real provider login is a separate setup gate.

The browser transcript is read from typed native events. A submitted draft is
not inserted into history as if the provider accepted it. Request IDs and
pending receipt state survive reconnect. Readers can inspect earlier messages
and expanded tools without polling stealing their scroll position or focus.

## Minimum coherent acceptance journey

1. Start the separate personal preview. Confirm its channel label, fresh state,
   provider home, daemon identity, cookie namespace and update selection while
   stable sentinels and running sessions remain intact.
2. Open the parent. Its actual configured MCP launches two named children with
   stable lineage and qualified IDs. Observe native readiness and prompt receipts.
3. Inspect either child as a task: exact prompt, response, tools, lifecycle
   reason, execution machine and terminal. Reading does not acknowledge a
   completion or take the input writer lease.
4. End one foreground turn and block the other for a human decision. Preserve
   every intermediate transition, expose the pending decision and checkpoint
   delivery explicitly. Deliberate takeover revokes the old writer and leaves
   the provider alive.
5. Close/reopen the browser and restart control. Restore selection, pending
   requests, logical identity, children and event cursors without duplicate
   input or launches. A disconnected wing is reported offline with last-known
   state rather than being reported finished.
6. Replace a provider execution separately. Resume the exact native conversation
   when supported; retain old execution history and undelivered events. Report
   unsupported, lost or failed identity explicitly. Stop only the selected child.
7. Repeat through an admitted remote route and then a second native provider.
   Workspace paths stay remote; SSH routing does not synchronize workspaces,
   memory or credentials.

Fixture protocol proof, real subscribed-provider proof, visual browser proof
and physical remote proof are separate acceptance gates. The preview package
is a reviewable local candidate until those gates are recorded; it is not a
public release or a deployment.

## Scope and provenance

Development uses the separately reviewed unpublished snapshot `8782b76` in an
independent local repository, with no publication remote. Mainline comparison
base is `26f2400` (`v0.147.0`). Promotion requires selective review of the feature
diffs and their dependencies rather than wholesale promotion of that snapshot.

Slide/org-mode Context is a separate workstream. Overnight commits `267c343`,
`bf254601` and `0077675` concern caller binding, token exchange, sealed native
provider isolation and preflight in a separate cloud checkout. Their live
integration was not verified here, and they do not constitute this controller.

See [the acceptance backlog](personal-preview-parity-plan.md),
[preview setup and promotion](../preview-channel.md),
[remote sessions](../ssh-remote-sessions.md) and
[the existing resource model](agent-meta-layer.md).
