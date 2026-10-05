import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

// Synthetic only: advertisements, lifecycles and receipts below are fixture
// evidence for focused tests, not live acceptance of any home. Every request
// still crosses the real HomeClient encrypted path into FixtureWire, and state
// is saved by the real LocalConversationStore in a temporary directory.

private func text(_ value: String) -> JSONValue { .string(value) }

// Removing a key is spelled as `.null`; values replace or add keys.
private func merged(_ base: [String: JSONValue], _ overrides: [String: JSONValue]) -> [String: JSONValue] {
    var fields = base
    for (key, value) in overrides { if value == .null { fields.removeValue(forKey: key) } else { fields[key] = value } }
    return fields
}

private func advertisement(_ overrides: [String: JSONValue] = [:]) -> JSONValue {
    .object(merged(["available": .bool(true), "protocol": text("wingthing.exact_stop.v1"), "operation": text("conversation_stop"),
                    "semantics": text("terminate_execution"), "conversation_id": text("child"), "session_id": text("child-session"),
                    "provider_session_id": text("child-native")], overrides))
}

private func childView(_ state: String = "working", provider: String = "child-native", alive: Bool = true, source: String = "claude_hook", cursor: Int64 = 4, extra: [String: JSONValue] = [:]) -> JSONValue {
    .object(merged(["session_id": text("child-session"), "agent": text("claude"), "provider_session_id": text(provider), "state": text(state),
                    "state_source": text(source), "ready": .bool(true), "process_alive": .bool(alive), "cursor": .integer(cursor),
                    "head_cursor": .integer(cursor), "events": .array([])], extra))
}

private func receipt(_ stop: PendingStop, status: String, _ overrides: [String: JSONValue] = [:], outer: [String: JSONValue] = [:]) -> JSONValue {
    let notSent = status == "not_sent" || status == "not_running", observed = status == "termination_observed"
    var fields: [String: JSONValue] = [
        "conversation_id": text("child"), "session_id": text("child-session"), "provider_session_id": text("child-native"),
        "request_id": text(stop.requestID), "semantics": text("terminate_execution"), "status": text(status),
        "definitely_not_sent": .bool(notSent), "signal_attempted": .bool(!notSent), "signal_acknowledged": .bool(status == "requested" || observed),
        "termination_observed": .bool(observed), "process_alive": .bool(!observed), "causality": text("unverified_other_actors_or_crash_may_terminate"),
        "reserved_cursor": .integer(4), "retried": .bool(false), "reason": text("synthetic fixture receipt"),
    ]
    if observed { fields["termination_state"] = text("failed"); fields["termination_source"] = text("egg_process"); fields["termination_cursor"] = .integer(6) }
    return .object(merged(["conversation_id": text("child"), "session": text("child-session"), "receipt": .object(merged(fields, overrides)),
                           "observation": text("requested is not stopped")], outer))
}

private func route(_ wire: FixtureWire, view: JSONValue = childView(), advertisement stop: JSONValue? = advertisement(), reply: JSONValue? = nil) async throws {
    let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
    let rootView = try lifecycleJSON(lifecycle("idle"))
    let tasks: JSONValue = .array([.object(["conversation": root, "lifecycle": rootView]), .object(["conversation": child, "lifecycle": view])])
    var read: [String: JSONValue] = ["session": text("child-session"), "lifecycle": view]
    if let stop { read[StopCapability.field] = stop }
    var routes: [String: JSONValue] = [
        "conversation_list:": .object(["conversations": .array([root, child])]),
        "conversation_read:child": .object(["conversation": child, "tasks": tasks]),
        "conversation_read:root": .object(["conversation": root, "tasks": tasks]),
        "session_read:child-session": .object(read),
        "session_read:same-session": .object(["session": text("same-session"), "lifecycle": rootView]),
    ]
    if let reply { routes["conversation_stop:child"] = reply }
    await wire.route(routes)
}

// Wraps the real FixtureWire: optionally holds the next exchange before the
// wing sees it, fails it before it reaches the wing, or loses the next reply
// after the wing processed it.
private actor StopHoldWire: HomeWire {
    let inner: FixtureWire
    private var holdNext = false, dropNext = false, failNext = false, open = true
    private(set) var isHolding = false
    init(_ inner: FixtureWire) { self.inner = inner }
    func holdNextExchange() { holdNext = true; open = false }
    func dropNextReply() { dropNext = true }
    func failNextBeforeSend() { failNext = true }
    func release() { open = true }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply { try await inner.get(url, bearer: bearer) }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        if failNext { failNext = false; throw URLError(.notConnectedToInternet) }
        if holdNext {
            holdNext = false; isHolding = true
            while !open { try await Task.sleep(for: .milliseconds(5)) }
            isHolding = false
        }
        let drop = dropNext; dropNext = false
        let reply = try await inner.exchange(url, bearer: bearer, request: request)
        if drop { throw URLError(.networkConnectionLost) }
        return reply
    }
}

private func waitUntil(_ condition: @Sendable () async -> Bool) async throws {
    for _ in 0..<400 { if await condition() { return }; try await Task.sleep(for: .milliseconds(5)) }
    throw ClientError.timeout
}

private func stopRequests(_ wire: FixtureWire) async -> [JSONValue] {
    await wire.requests.filter { $0["operation"] == text("conversation_stop") }
}

private func temporaryDirectory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-stop-adapter-\(UUID())") }

@MainActor private func onlineChild(_ hold: StopHoldWire, _ fixture: FixtureWire, _ directory: URL, view: JSONValue = childView(), advertisement stop: JSONValue? = advertisement()) async throws -> (WingthingModel, HomeProfile) {
    let home = try profile(), model = WingthingModel()
    try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: hold)
    try await route(fixture, view: view, advertisement: stop)
    await model.refresh(); await model.open(reference(home, task: "child"))
    return (model, home)
}

// Taps Stop with the request held at the wire, so the saved intent's id can be
// echoed by the synthetic home before release.
@MainActor private func heldStop(_ model: WingthingModel, _ hold: StopHoldWire, _ fixture: FixtureWire, view: JSONValue = childView(), reply: (PendingStop) -> JSONValue) async throws -> PendingStop {
    await hold.holdNextExchange()
    let tap = Task { await model.requestStop() }
    try await waitUntil { await hold.isHolding }
    let stop = try unwrap(model.pendingStop)
    try await route(fixture, view: view, reply: reply(stop))
    await hold.release(); await tap.value
    return stop
}

@Suite @MainActor struct StopAdapterPartnerTests {
    @Test func supportedStopSendsOneExactRequestAndSeparatesRequestedFromNativeExit() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, home) = try await onlineChild(hold, fixture, directory)
        expectEqual(model.currentStatus, .working); expectTrue(model.canStop); expectFalse(model.canRetryStop)
        expectTrue(model.stopNotice?.contains("whole task") == true)
        model.draft = "Keep this unsent draft"

        await hold.holdNextExchange()
        let tap = Task { await model.requestStop() }
        try await waitUntil { await hold.isHolding }
        await model.requestStop() // A second tap while the first is in flight.
        let stop = try unwrap(model.pendingStop)
        expectEqual(stop.progress, .unconfirmed); expectTrue(model.stopSending); expectFalse(model.canStop)
        // Durably saved before the request left the phone.
        let disk = try LocalConversationStore(profile: home, file: directory.appendingPathComponent("state.json"))
        expectEqual(await disk.pendingStop(for: stop.execution)?.id, stop.id)
        try await route(fixture, reply: receipt(stop, status: "requested"))
        await hold.release(); await tap.value

        let sent = await stopRequests(fixture)
        expectEqual(sent.count, 1)
        expectEqual(sent.first?["arguments"], .object(["conversation_id": text("child"), "session": text("child-session"), "expected_provider_session_id": text("child-native"),
                                                       "request_id": text(stop.requestID), "expected_state_cursor": .integer(4), "timeout_seconds": .integer(5)]))
        expectEqual(model.pendingStop?.progress, .requested)
        expectEqual(model.currentStatus, .working) // An acknowledgement is not stopped.
        expectTrue(model.stopNotice?.contains("hasn't been confirmed as ended") == true)
        expectEqual(model.draft, "Keep this unsent draft")
        expectFalse(model.canStop); expectTrue(model.canRetryStop); expectEqual(model.stopActionTitle, "Retry stop")
        await model.requestStop() // Repeat tap: still one intent, no request.
        expectEqual(await stopRequests(fixture).count, 1)

        // Native exit read independently: never attributed to Stop.
        let exited = childView("failed", alive: false, source: "egg_process", cursor: 6)
        try await route(fixture, view: exited, advertisement: nil)
        await model.pollTranscript()
        expectEqual(model.currentStatus, .failed); expectEqual(model.pendingStop?.progress, .requested)
        expectTrue(model.stopNotice?.contains("can't confirm the stop caused it") == true)
        expectFalse(model.canRetryStop)

        // Synthetic re-advertisement: an explicit check resends the identical request.
        try await route(fixture, view: exited, reply: receipt(stop, status: "termination_observed", ["retried": .bool(true)]))
        await model.pollTranscript(); expectTrue(model.canRetryStop)
        await model.retryStop()
        let all = await stopRequests(fixture)
        expectEqual(all.count, 2); expectEqual(all[0]["arguments"], all[1]["arguments"])
        expectEqual(model.pendingStop?.progress, .terminationObserved); expectEqual(model.pendingStop?.receipt?.terminationCursor, 6)
        expectTrue(model.stopNotice?.contains("may also have ended") == true)
        expectFalse(model.canStop); expectFalse(model.canRetryStop)
    }

    @Test func absentMalformedOrCrossBoundAdvertisementFailsClosedWithZeroStopRequests() async throws {
        let variants: [String: (view: JSONValue, advertisement: JSONValue?)] = [
            "absent": (childView(), nil),
            "not available": (childView(), advertisement(["available": .bool(false)])),
            "string flag": (childView(), advertisement(["available": text("true")])),
            "other protocol": (childView(), advertisement(["protocol": text("wingthing.exact_stop.v0")])),
            "turn interrupt": (childView(), advertisement(["semantics": text("interrupt_turn")])),
            "other operation": (childView(), advertisement(["operation": text("terminal_stop")])),
            "other provider": (childView(), advertisement(["provider_session_id": text("replacement-native")])),
            "other session": (childView(), advertisement(["session_id": text("other-session")])),
            "other conversation": (childView(), advertisement(["conversation_id": text("root")])),
            "missing provider": (childView(), advertisement(["provider_session_id": .null])),
            "inside lifecycle": (childView(extra: [StopCapability.field: advertisement()]), nil),
        ]
        for (name, variant) in variants {
            let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
            let fixture = FixtureWire(), hold = StopHoldWire(fixture)
            let (model, home) = try await onlineChild(hold, fixture, directory, view: variant.view, advertisement: variant.advertisement)
            expectEqual(model.currentStatus, .working)
            if model.canStop || model.canRetryStop { fail("\(name) enabled Stop") }
            expectTrue(model.stopNotice?.contains("isn't available yet") == true)
            await model.requestStop(); await model.retryStop()
            expectNil(model.pendingStop)

            // The adapter itself refuses without exact evidence on this connection.
            let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: fixture)
            _ = try await client.verifyHome()
            let execution = ExecutionReference(conversation: reference(home, task: "child"), sessionID: "child-session", providerSessionID: "child-native")
            let observed = try await client.observe(execution, after: 0)
            expectNil(observed.stopCapability)
            let intent = try PendingStop(execution: execution, expectedStateCursor: 4)
            do { _ = try await client.submitStop(intent); fail("\(name) sent Stop") } catch {
                guard case .unsupported = error as? ClientError else { fail("\(name): \(error)"); continue }
            }
            do { _ = try await client.control(execution.conversation, operation: "conversation_stop", arguments: intent.arguments); fail("generic control sent Stop") } catch {
                guard case .unsupported = error as? ClientError else { fail("\(name): \(error)"); continue }
            }
            let sent = await stopRequests(fixture)
            if !sent.isEmpty { fail("\(name) produced a Stop request") }
        }
    }

    @Test func reconnectAndProviderReplacementRequireFreshExactEvidence() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, home) = try await onlineChild(hold, fixture, directory)
        expectTrue(model.canStop)

        // A connection's earlier evidence is dropped when it verifies again.
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: fixture)
        _ = try await client.verifyHome()
        let execution = ExecutionReference(conversation: reference(home, task: "child"), sessionID: "child-session", providerSessionID: "child-native")
        let observed = try await client.observe(execution, after: 0)
        expectEqual(observed.stopCapability?.execution, execution)
        _ = try await client.verifyHome()
        let intent = try PendingStop(execution: execution, expectedStateCursor: 4)
        do { _ = try await client.submitStop(intent); fail("stale evidence sent Stop") } catch {
            guard case .unsupported = error as? ClientError else { fail("\(error)"); return }
        }

        // Reconnect without the advertisement: Stop is unsupported again.
        try await route(fixture, advertisement: nil)
        await model.refresh()
        expectEqual(model.currentStatus, .working); expectFalse(model.canStop)
        expectTrue(model.stopNotice?.contains("isn't available yet") == true)

        // The provider is replaced and the new one is advertised: still refused.
        try await route(fixture, view: childView(provider: "replacement-native"), advertisement: advertisement(["provider_session_id": text("replacement-native")]))
        await model.pollTranscript()
        expectNil(model.stopCapability); expectFalse(model.canStop)
        await model.requestStop()
        expectTrue(await stopRequests(fixture).isEmpty)
    }

    @Test func lostReplyStaysUnconfirmedAndOnlyExplicitSameRequestRetrySends() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, home) = try await onlineChild(hold, fixture, directory, view: childView("idle"))
        // An earlier input whose delivery is still unconfirmed.
        model.draft = "Summarize the child evidence"
        await fixture.configure(reply: .object(["receipt": .object(["status": text("unconfirmed")])]))
        await model.sendOrCheck()
        let input = try unwrap(model.pending)
        try await route(fixture)
        await model.pollTranscript()
        expectEqual(model.currentStatus, .working); expectTrue(model.canStop)

        // The home processes the request but its reply is lost on the way back.
        try await route(fixture, reply: .object(["ok": .bool(true)]))
        await hold.dropNextReply()
        await model.requestStop()
        let stop = try unwrap(model.pendingStop)
        expectEqual(await stopRequests(fixture).count, 1)
        expectEqual(stop.progress, .unconfirmed); expectNil(stop.receipt)
        expectTrue(model.stopNotice?.contains("may not have reached") == true)
        expectTrue(model.canRetryStop); expectFalse(model.canStop)

        // Polling and reconnecting only read.
        await model.pollTranscript(); await model.refresh(); await model.pollTranscript()
        expectEqual(await stopRequests(fixture).count, 1)
        expectEqual(model.pending?.id, input.id); expectEqual(model.draft, "Summarize the child evidence")
        expectEqual(model.pendingStop?.id, stop.id)

        // Relaunch restores the same intent; a new tap never creates another.
        let relaunched = WingthingModel()
        try await relaunched.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: hold)
        await relaunched.refresh(); await relaunched.open(reference(home, task: "child"))
        expectEqual(relaunched.pendingStop?.id, stop.id); expectEqual(relaunched.pendingStop?.progress, .unconfirmed)
        await relaunched.requestStop()
        expectEqual(await stopRequests(fixture).count, 1)

        // Explicit check: the identical request, now with a lost reply, then delivered.
        await hold.dropNextReply()
        await relaunched.retryStop()
        expectEqual(relaunched.pendingStop?.progress, .unconfirmed); expectEqual(relaunched.pendingStop?.id, stop.id)
        try await route(fixture, reply: receipt(stop, status: "requested", ["retried": .bool(true)]))
        await relaunched.retryStop()
        let sent = await stopRequests(fixture)
        expectEqual(sent.count, 3)
        expectTrue(sent.allSatisfy { $0["arguments"] == sent[0]["arguments"] })
        expectEqual(relaunched.pendingStop?.progress, .requested); expectEqual(relaunched.pendingStop?.receipt?.retried, true)
        let prompts = await fixture.requests.filter { $0["operation"] == text("session_prompt") }
        expectEqual(prompts.count, 1)
    }

    @Test func explicitNoActionIsFinalAndAllowsANewIntent() async throws {
        for status in ["not_sent", "not_running"] {
            let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
            let fixture = FixtureWire(), hold = StopHoldWire(fixture)
            let (model, _) = try await onlineChild(hold, fixture, directory)
            let first = try await heldStop(model, hold, fixture) { receipt($0, status: status) }
            expectEqual(model.pendingStop?.progress, status == "not_sent" ? .notSent : .notRunning)
            expectTrue(model.stopNotice?.contains("nothing was stopped") == true)
            expectTrue(model.canStop); expectFalse(model.canRetryStop); expectEqual(model.stopActionTitle, "Stop")
            let second = try await heldStop(model, hold, fixture) { receipt($0, status: "requested") }
            expectTrue(first.id != second.id)
            let ids = await stopRequests(fixture).compactMap { $0["arguments"]?["request_id"]?.string }
            expectEqual(ids, [first.requestID, second.requestID])
            expectEqual(model.pendingStop?.progress, .requested)
        }
    }

    @Test func mismatchedOrInconsistentReceiptsNeverConfirm() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, _) = try await onlineChild(hold, fixture, directory)
        let stop = try await heldStop(model, hold, fixture) { receipt($0, status: "requested", ["request_id": text(UUID().uuidString)]) }
        let variants: [(String, JSONValue)] = [
            ("other conversation", receipt(stop, status: "requested", ["conversation_id": text("root")])),
            ("other session", receipt(stop, status: "requested", ["session_id": text("same-session")])),
            ("other provider", receipt(stop, status: "requested", ["provider_session_id": text("replacement-native")])),
            ("turn interrupt", receipt(stop, status: "requested", ["semantics": text("interrupt_turn")])),
            ("unknown status", receipt(stop, status: "stopped")),
            ("requested without acknowledgement", receipt(stop, status: "requested", ["signal_acknowledged": .bool(false)])),
            ("not sent without proof", receipt(stop, status: "not_sent", ["definitely_not_sent": .bool(false)])),
            ("not sent yet attempted", receipt(stop, status: "not_sent", ["signal_attempted": .bool(true)])),
            ("exit from another source", receipt(stop, status: "termination_observed", ["termination_source": text("claude_hook")])),
            ("exit before reservation", receipt(stop, status: "termination_observed", ["termination_cursor": .integer(3)])),
            ("exit state not native", receipt(stop, status: "termination_observed", ["termination_state": text("running")])),
            ("exit without state", receipt(stop, status: "termination_observed", ["termination_state": .null])),
            ("untyped process flag", receipt(stop, status: "termination_observed", ["process_alive": text("true")])),
            ("exit state without exit", receipt(stop, status: "requested", ["termination_state": text("failed")])),
            ("claimed causality", receipt(stop, status: "requested", ["causality": text("caused_by_stop")])),
            ("missing flag", receipt(stop, status: "requested", ["retried": .null])),
            ("string cursor", receipt(stop, status: "requested", ["reserved_cursor": text("4")])),
            ("reserved before observed cursor", receipt(stop, status: "requested", ["reserved_cursor": .integer(2)])),
            ("outer session", receipt(stop, status: "requested", outer: ["session": text("other-session")])),
            ("no receipt", .object(["conversation_id": text("child"), "session": text("child-session")])),
        ]
        expectEqual(model.pendingStop?.progress, .unconfirmed)
        for (name, reply) in variants {
            try await route(fixture, reply: reply)
            await model.retryStop()
            if model.pendingStop?.progress != .unconfirmed || model.pendingStop?.receipt != nil { fail("\(name) was accepted") }
            if model.pendingStop?.detail?.contains("didn't match") != true { fail("\(name) not explained") }
        }
        try await route(fixture, reply: receipt(stop, status: "requested"))
        await model.retryStop()
        expectEqual(model.pendingStop?.progress, .requested)
        let ids = Set(await stopRequests(fixture).compactMap { $0["arguments"]?["request_id"]?.string })
        expectEqual(ids, [stop.requestID])
    }

    @Test func staleBindingErrorStaysUncertainAndDismissIsLocalOnly() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, home) = try await onlineChild(hold, fixture, directory)
        let stale: JSONValue = .object(["error": text("stale stop target: a native turn boundary was recorded after expected_state_cursor; refresh before stopping")])
        let stop = try await heldStop(model, hold, fixture) { _ in stale }
        // A home error is not proof that nothing happened, and not a capability signal.
        expectEqual(model.pendingStop?.progress, .unconfirmed); expectTrue(model.pendingStop?.detail?.contains("stale stop target") == true)
        expectTrue(model.canRetryStop); expectFalse(model.canStop)

        expectTrue(model.canDismissStop)
        await model.dismissStop()
        expectNil(model.pendingStop)
        let disk = try LocalConversationStore(profile: home, file: directory.appendingPathComponent("state.json"))
        expectNil(await disk.pendingStop(for: stop.execution))
        expectEqual(await stopRequests(fixture).count, 1)
        expectTrue(model.canStop)
        let next = try await heldStop(model, hold, fixture) { receipt($0, status: "requested") }
        expectTrue(next.id != stop.id)
    }

    @Test func selectionAndProfileChangesFenceDelayedStopReplies() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, home) = try await onlineChild(hold, fixture, directory)
        let root = reference(home, task: "root"), child = reference(home, task: "child")

        await hold.holdNextExchange()
        let tap = Task { await model.requestStop() }
        try await waitUntil { await hold.isHolding }
        let stop = try unwrap(model.pendingStop)
        try await route(fixture, reply: receipt(stop, status: "requested"))
        await model.open(root) // Selection changes while the reply is delayed.
        expectEqual(model.selected, root); expectNil(model.pendingStop)
        model.draft = "Draft for the parent"
        await hold.release(); await tap.value
        expectEqual(model.selected, root); expectNil(model.pendingStop) // Not shown on another task.
        expectEqual(model.draft, "Draft for the parent"); expectNil(model.pending)
        let disk = try LocalConversationStore(profile: home, file: directory.appendingPathComponent("state.json"))
        expectEqual(await disk.pendingStop(for: stop.execution)?.progress, .requested) // Saved for its own task.
        await model.open(child)
        expectEqual(model.pendingStop?.id, stop.id); expectEqual(model.pendingStop?.progress, .requested)
        expectEqual(await stopRequests(fixture).count, 1)

        // A profile replacement while another reply is delayed.
        let otherDirectory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: otherDirectory) }
        let otherFixture = FixtureWire(), otherHold = StopHoldWire(otherFixture)
        let (second, secondHome) = try await onlineChild(otherHold, otherFixture, otherDirectory)
        await otherHold.holdNextExchange()
        let secondTap = Task { await second.requestStop() }
        try await waitUntil { await otherHold.isHolding }
        let secondStop = try unwrap(second.pendingStop)
        try await route(otherFixture, reply: receipt(secondStop, status: "requested"))
        try await second.configure(profile: try profile(), existingBearer: "synthetic-existing-token", cacheFile: otherDirectory.appendingPathComponent("replacement.json"), wire: FixtureWire())
        await otherHold.release(); await secondTap.value
        expectNil(second.pendingStop); expectFalse(second.canStop); expectTrue(second.profile?.id != secondHome.id)
        let oldDisk = try LocalConversationStore(profile: secondHome, file: otherDirectory.appendingPathComponent("state.json"))
        expectEqual(await oldDisk.pendingStop(for: secondStop.execution)?.progress, .requested)
        expectEqual(await stopRequests(otherFixture).count, 1)
    }

    @Test func inFlightReadsOvertakenByReconnectOrNewerReadNeverPublishStopEvidence() async throws {
        let fixture = FixtureWire(), hold = StopHoldWire(fixture), home = try profile()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: hold)
        let execution = ExecutionReference(conversation: reference(home, task: "child"), sessionID: "child-session", providerSessionID: "child-native")
        let intent = try PendingStop(execution: execution, expectedStateCursor: 4)
        for overtake in ["disconnect", "reverify", "newer read", "provider replacement"] {
            try await route(fixture)
            _ = try await client.verifyHome()
            await hold.holdNextExchange()
            let late = Task { try await client.observe(execution, after: 0) }
            try await waitUntil { await hold.isHolding }
            switch overtake {
            case "disconnect": await client.disconnect(); _ = try await client.verifyHome()
            case "reverify": _ = try await client.verifyHome()
            case "newer read":
                try await route(fixture, advertisement: nil)
                let newer = try await client.observe(execution, after: 0)
                expectNil(newer.stopCapability)
            default:
                try await route(fixture, view: childView(provider: "replacement-native"), advertisement: advertisement(["provider_session_id": text("replacement-native")]))
                do { _ = try await client.observe(execution, after: 0); fail("replaced provider accepted") } catch { expectEqual(error as? ClientError, .staleReference) }
            }
            try await route(fixture) // The overtaken read itself carries a valid advertisement.
            await hold.release()
            let observation = try await late.value
            if observation.stopCapability != nil { fail("\(overtake): an overtaken read republished Stop support") }
            do { _ = try await client.submitStop(intent); fail("\(overtake): stale evidence sent Stop") } catch {
                guard case .unsupported = error as? ClientError else { fail("\(overtake): \(error)"); continue }
            }
        }
        let sent = await stopRequests(fixture)
        expectTrue(sent.isEmpty)
        // Control: the newest read in the current epoch does publish.
        let fresh = try await client.observe(execution, after: 0)
        expectEqual(fresh.stopCapability?.execution, execution)
    }

    @Test func pollOvertakenByFailedReconnectNeverRepublishesSupport() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, _) = try await onlineChild(hold, fixture, directory)
        expectTrue(model.canStop)
        await hold.holdNextExchange()
        let poll = Task { await model.pollTranscript() }
        try await waitUntil { await hold.isHolding }
        await fixture.configure(user: "another-user") // Reconnect fails before reopening anything.
        await model.refresh()
        expectFalse(model.connected); expectNil(model.stopCapability)
        await hold.release(); await poll.value
        expectNil(model.stopCapability); expectFalse(model.canStop); expectFalse(model.canRetryStop)
        await fixture.configure(user: "fixture-user")
        await model.refresh()
        expectTrue(model.canStop) // Only a fresh read on the new connection restores it.
        let sent = await stopRequests(fixture)
        expectTrue(sent.isEmpty)
    }

    @Test func retryAfterAnAttemptThatNeverArrivedIsTheSameStopAndSendsOnlyOnTap() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, _) = try await onlineChild(hold, fixture, directory)
        model.draft = "Unsent follow-up"
        await hold.failNextBeforeSend()
        await model.requestStop()
        let stop = try unwrap(model.pendingStop)
        expectEqual(stop.progress, .unconfirmed)
        expectTrue(await stopRequests(fixture).isEmpty) // The home never received it.
        // The action is labeled as sending the same stop, which may be the one that ends the task.
        expectEqual(model.stopActionTitle, "Retry stop"); expectTrue(model.canRetryStop)
        expectTrue(model.stopNotice?.contains("that retry ends the whole task") == true)
        expectTrue(WingthingModel.stopRetryHint.contains("same stop request"))
        await model.pollTranscript(); await model.refresh(); await model.pollTranscript()
        expectTrue(await stopRequests(fixture).isEmpty) // Never sent automatically.
        try await route(fixture, reply: receipt(stop, status: "requested"))
        await model.retryStop()
        let sent = await stopRequests(fixture)
        expectEqual(sent.count, 1); expectEqual(sent.first?["arguments"], .object(stop.arguments))
        expectEqual(model.pendingStop?.id, stop.id); expectEqual(model.pendingStop?.progress, .requested)
        expectEqual(model.draft, "Unsent follow-up")
    }

    // Frozen backend semantics: process_alive reports the supervising egg, which
    // records the provider's native exit before it shuts down.
    @Test func nativeExitReceiptWithSupervisorStillAliveConfirmsProviderExitOnly() async throws {
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, _) = try await onlineChild(hold, fixture, directory, view: childView("idle"))
        model.draft = "Summarize the child evidence"
        await fixture.configure(reply: .object(["receipt": .object(["status": text("unconfirmed")])]))
        await model.sendOrCheck()
        let input = try unwrap(model.pending)
        try await route(fixture)
        await model.pollTranscript()
        expectTrue(model.canStop)

        let stop = try await heldStop(model, hold, fixture) {
            receipt($0, status: "termination_observed", ["process_alive": .bool(true), "termination_state": text("completed")])
        }
        let confirmed = try unwrap(model.pendingStop)
        expectEqual(confirmed.id, stop.id); expectEqual(confirmed.progress, .terminationObserved)
        let evidence = try unwrap(confirmed.receipt)
        expectTrue(evidence.processAlive) // Kept as a diagnostic, not treated as a contradiction.
        expectEqual(evidence.terminationCursor, 6); expectTrue(evidence.causality.hasPrefix("unverified"))
        let notice = try unwrap(model.stopNotice)
        expectTrue(notice.contains("may also have ended")); expectFalse(notice.contains("caused"))
        // The lifecycle read is not rewritten from the receipt.
        expectEqual(model.currentStatus, .working); expectEqual(model.transcript.lifecycle?.processAlive, true)
        expectFalse(model.canStop); expectFalse(model.canRetryStop)
        expectEqual(model.pending?.id, input.id); expectEqual(model.draft, "Summarize the child evidence")
        expectEqual(await stopRequests(fixture).count, 1)
    }

    @Test func persistenceFailureSendsNothing() async throws {
        guard getuid() != 0 else { return } // Directory permissions do not constrain root.
        let directory = temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let fixture = FixtureWire(), hold = StopHoldWire(fixture)
        let (model, _) = try await onlineChild(hold, fixture, directory)
        expectTrue(model.canStop)
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: directory.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path) }
        await model.requestStop()
        expectNil(model.pendingStop); expectTrue(model.error != nil)
        expectTrue(await stopRequests(fixture).isEmpty)
        expectTrue(model.canStop)
    }
}
