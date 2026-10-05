# Personal preview and agent-session parity

Date: 2026-10-03. Status: composed local draft, with explicit live acceptance
gates below. A checked item means implemented and covered by the named fixture
or build evidence; it does not imply authenticated-provider or browser QA.

The first product milestone is a personal preview that reliably launches,
inspects, prompts, waits for, reconnects to, resumes, and stops named agent
sessions. A persistent parent conversation and inspectable children use those
same session operations. Herdr provides the practical baseline. Slide org-mode
Context integration is a separate workstream and is not a dependency here.

## Source and promotion boundaries

- Mainline comparison base: `26f2400` (`v0.147.0`).
- Local preview development base: reviewed, unpublished `8782b76`.
- This snapshot supplies reviewed provider continuation, session file controls,
  browser session improvements, and SSH remote support. The exact earlier source
  receipts are in the owner's `state/wingthing-org-review-20260929/receipts`.
- The development repository is an independent local clone. Its publication
  remote has been removed. Existing source checkouts, binaries, daemons, state,
  org enrollment, and deployed services are untouched.
- Using this snapshot for an isolated draft is not promotion of the snapshot to
  mainline. Promotion requires a separately reviewed diff for each coherent
  feature, including its dependencies and tests. Experimental Context commits
  are not imported.

## Catch-up backlog and acceptance map

Priority 0, trustworthy sessions and preview boundaries:

- [x] Separate preview binary, package, version, state, sockets, PIDs and updates.
- [x] Clean preview credentials and enrollment; no automatic stable migration.
- [x] Matching macOS OS account context and canonical provider data/config
  identity across explicit login, scoped status and guarded native/headless
  runtime. Private terminal login has fake-provider and source-review evidence.
- [ ] Real preview credential save/read and personal account/model entitlement.
  The original relocated-HOME attempt failed after browser OAuth; human retries
  remain held until independent review of the frozen coherent auth/runtime path.
- [x] Native readiness plus durable prompt reservation and exact native text
  receipt. Provider-issued request acknowledgement remains unavailable.
- [x] Native Claude lifecycle events; unsupported/unknown when proof is absent.
- [x] Exact provider conversation identity for transcript and continuation.
- [x] Cursor-based transcript/events, including partial append recovery.
- [x] Native completion waits distinct from terminal output waits.
- [x] Fixture detach/reconnect without provider exit, duplicate input or relaunch.
- [x] Explicit provider exit, unsupported resume, timeout and offline outcomes.
- [x] Existing exact-session stop and process-tree cancellation retained.
- [x] Typed browser, CLI and MCP operations share owner and object checks.
- [ ] Authenticated personal provider startup, multiline composer and cold
  continuation through the packaged preview.
- [ ] Native Codex live supervisor integration. The exact-thread protocol
  parser, command plan and journal fixtures are implemented; readiness stays
  unsupported until a real provider connection is owned by the session runtime.

Priority 1, useful daily interface and remote parity:

- [x] Search/filter agent sessions by name, location, provider and state.
- [x] Readable names, working directory and stable machine identity.
- [x] Attention and offline indicators with useful explanations.
- [x] Keyboard navigation, focus preservation and mobile action controls.
- [x] Discoverable attach, rename, resume and acknowledged inventory stop.
- [x] Native prompt/response/tool reader, preserving historical scroll/details.
- [x] Wing-qualified routes, selection, content caches and attention actions.
- [x] CLI/receiver fixtures for remote inventory, exact selection and launch in
  an explicit existing remote working directory.
- [x] Literal argument forwarding, non-TTY JSON and explicit input EOF handling.
- [x] Preview channel identity check before receiver state access or execution.
- [x] Remote fixtures for detach/reattach, lost SSH and held writer ownership.
- [x] Channel, long socket-path, incompatible receiver and sandbox diagnostics.
- [ ] Physical owned-VM session journey. Two admitted SSH routes passed package
  verification but failed the existing mount policy before creating a session.
- [x] Composed Observe/Take control protocol and acknowledged canvas stop/error
  behavior; actual egg/encryption fixtures and pure frontend tests passed.
- [ ] Visual/interactive browser QA. The supported JS browser runtime is absent
  from this execution environment; markup and pure-function fixtures passed.

Priority 2, the persistent parent conversation:

- [x] Stable logical conversation identity and retained execution history.
- [x] Runtime-recorded root/parent/child relationships.
- [x] Request-ID reservations and wing-qualified browser references.
- [x] Native linked task inventory, transcript and tool-detail inspection.
- [x] Durable child events, old execution tails and CAS checkpoint cursors.
- [x] Native needs-input evidence and single-writer preview input arbitration.
- [x] Invocation-specific MCP configuration, actual parent initialization and
  bound child reservation in the real sandbox fixture.
- [ ] Full configured parent-to-two-child execution. On Mac the nested child
  proxy cannot bind under the parent's existing sandbox; no policy was weakened.
- [x] Explicit opt-in host wake controller, retained outbox, exact request
  recovery and bounded retry for proven unsent input. Typed synthetic-provider
  fixtures cover approval waits, restart and duplicate prevention.
- [ ] Authenticated parent continuation and synthesis after child events.
- [ ] Human decision/approval handoff with exact request identity and routing.
- [x] Compact persistent parent entry keyed by user, wing and logical root;
  provider exits, stale evidence and current read errors remain explicit.
- [ ] Second-provider and cross-machine linked-child extension.

Later improvements should follow actual dogfood friction: bulk attention review,
completion/unread state, session pinning/grouping, workspace preparation, export,
combined headless-run inventory, and richer scheduling. They must not replace
verification of the basic launch-to-result journey.

## First complete acceptance journey

1. Start only the personal preview, with its channel visible and stable sentinels
   retained. Verify that it has not inherited stable org enrollment or credentials.
2. Open the parent, launch two named children through its actual configured MCP,
   and observe native readiness and explicit prompt receipt evidence. A write to
   a PTY alone is not acknowledged input.
3. Open either child as a task and inspect its exact prompt, responses, tools,
   lifecycle, execution wing and terminal.
4. Complete one child and block the other for human input. Deliver completion
   once, expose the pending decision, and continue after the human's response.
5. Close/reopen the browser and restart the control process. Reconcile existing
   eggs; preserve parent history, links, event cursors and pending decisions.
6. Test provider exit separately: resume the verified conversation or report an
   explicit unavailable/failed state. Stop only the selected child.
7. Repeat via an authorized remote route. An absent route is an access blocker,
   never permission to create keys, enable SSH or expose a listener.

Fixture proof, real-provider proof, browser QA and physical remote proof are
separate evidence. See `personal-conversations.md` for the actual sandbox
boundary, `personal-parent-wake.md` for wake delivery and
`codex-native-interactive.md` for the second-provider boundary.
Included personal provider allowance is permitted; paid API fallback is not.
No public push, release, deployment or shared org change is part of this draft.

## Native mobile foundations

The composed Android and iOS drafts use an explicitly configured, pinned home
and the existing encrypted control contract. They preserve logical parent and
child selection, native transcript/tool evidence, scoped cached content and
uncertain input request identity. Reconnect does not resend pending input.
Neither draft has native pairing/sign-in or exact approval routing. Android APK
and device verification require a JDK/SDK/Gradle toolchain; iOS app and device
verification require full Xcode and the iOS SDK. Mac-SDK Swift tests/typechecking
and Go wire-contract fixtures do not establish mobile app acceptance. See the
respective `mobile/android/README.md` and `mobile/ios/README.md` for exact gates.

## Implementation owners

- Preview channel: `/root/preview_channel`, `wingthing-preview`.
- Session lifecycle: `/root/session_lifecycle`, `wingthing-lifecycle`.
- Remote attach: `/root/remote_attach`, `wingthing-remote`.
- UI and quality of life: `/root/ui_parity`, `wingthing-ui`.
- Parent/child conversations: `/root/parent_conversation`, `wingthing-conversation`.
- Herdr hands-on evaluation: `/root/herdr_evaluation`, `herdr-evaluation`.
- Integration and review: root, `wingthing-integration`.

Every lane commits a coherent draft; root integrates and verifies the composed
result. Installation and release promotion remain explicit reviewable actions.
