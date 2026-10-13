# Opening and controlling an existing session

The personal dashboard opens an exact `(origin_wing_id, session_id)` from search
or the session inventory. It refreshes qualified inventory before selecting a
route: healthy direct first, then a verified personal gateway. The viewer keeps
conversation events separate from terminal output. Only native conversation
events count as provider messages; terminal text is displayed as terminal text.

The viewer retains the last 400 text events and truncates individual text above
16,384 characters with a visible marker; raw provider records stay on the wing.
Pending prompts survive viewer close and same-tab reload in owner-scoped session
storage. A corrupt or unavailable receipt store disables message submission.

Send message calls qualified `session_prompt` with a generated request ID.
Its receipt distinguishes native admission from transport submission; uncertain
outcomes retain that original request ID. Messages, input and takeover are never
automatically repeated on another route. CLI `wt session action` and MCP
`session_action` provide the same qualified read/prompt contract.

Take control is available in Wingthing Preview and requires an execution
egg that acknowledges an exclusive input lease (currently a preview egg). A new `require_lease`
attachment field makes older execution wings reject the request; no legacy
attachment fallback is used. Existing ordinary phone attachments remain
compatible, while explicit phone takeover requires this same lease proof.

Take control uses `session.control` operation `fleet_attach`, then `fleet_read`
and `fleet_input`. All carry `attachment_id`, `origin_wing_id` and the exact
`session_id`; attach additionally uses explicit `takeover: true`. The attachment
ID is generated once by the client and bound to its authenticated user and
X25519 tab key. A route is retained for the attachment lifetime. No egg token
leaves the wing. Existing personal-owner, clients, path, passkey and remote-pin
checks remain; organization/shared wings cannot use this personal attachment
adapter. A locked wing may refuse the local owner control bridge even after a
browser passkey ceremony; the viewer reports the refusal without bypassing it.

Hand back uses `fleet_detach` with the same capability and exact identity. It
releases terminal input without stopping the agent. A disconnected/quiet browser
loses its attachment after 15 seconds without a successful operation. There are
at most 32 browser attachments, readiness is bounded to 5 seconds, input to
16 KiB and output retention to 64 KiB. Closing the viewer attempts hand-back;
if the acknowledgement is lost it shows uncertainty and the idle lease remains.

The paired phone uses existing PAKE `fleet_attach`, `fleet_read`, `fleet_input`
and the additive `fleet_detach`. Its input JSON carries exact origin/session;
the outer session selector is unused. The phone connection is already the
capability, so it does not use the browser's additional `attachment_id`.
`action` with operation `session_prompt` sends a correlated message.

Native hooks currently expose attention but no correlated permission response
channel. Typed approve/reject is unavailable; the viewer explains that a human
must inspect the provider terminal and enter its decision deliberately. It never
maps an attention event or terminal text to guessed approval keystrokes. A future
native responder must retain the exact provider request and sandbox/grant gates.
Browser-at-phone-width tests are browser evidence, not native iPhone, cellular,
public tunnel, real SSH or live-provider acceptance.
