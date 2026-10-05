import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private func continuationResult(_ intent: PendingContinuation, changes: [String: JSONValue] = [:]) -> JSONValue {
    var fields: [String: JSONValue] = ["request_id": .string(intent.requestID), "source_session": .string(intent.source.sessionID),
        "session": .string("next-execution"), "wing_id": .string(intent.source.conversation.wingID),
        "provider_session_id": .string(intent.source.providerSessionID!), "conversation_id": .string(intent.source.conversation.conversationID),
        "root_conversation_id": .string(intent.source.conversation.conversationID), "parent_conversation_id": .null,
        "new_turn": .object(["input_sha256": .string(intent.inputSHA256)]), "launch_state": .string("started"), "continuation_state": .string("started")]
    fields.merge(changes) { _, new in new }; return .object(fields)
}

private actor ContinuationHoldWire: HomeWire {
    let inner: ConversationTransportFixture
    private var hold = false
    private(set) var holding = false
    init(_ inner: ConversationTransportFixture) { self.inner = inner }
    func holdNext() { hold = true }
    func release() { holding = false }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply { try await inner.get(url, bearer: bearer) }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        if hold {
            hold = false; holding = true
            while holding { try await Task.sleep(for: .milliseconds(5)) }
        }
        return try await inner.exchange(url, bearer: bearer, request: request)
    }
}

@Suite @MainActor struct ContinuationJourneyTests {
    @Test func lostAcknowledgementRestartReconnectAndExplicitReplayKeepOneExactIntent() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("continuation-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = ConversationTransportFixture(scenario: "lost-reply"), model = WingthingModel(), home = try await wire.profile()
        let file = directory.appendingPathComponent("state.json")
        let root = reference(home, wing: wire.wingID, task: wire.rootID)
        try await model.configure(profile: home, existingBearer: wire.bearer, cacheFile: file, wire: wire)
        await model.refresh(); await model.open(root)
        expectTrue(model.canContinue); expectEqual(model.currentStatus, .archived)
        let original = try unwrap(model.execution)
        model.draft = "First follow-up"; await model.sendOrCheck()
        let pending = try unwrap(model.pendingContinuation)
        expectEqual(pending.source, original); expectEqual(pending.progress, .unconfirmed)
        expectFalse(model.connected); expectFalse(model.inputReady)
        let committed = await wire.counts(); expectEqual(committed.launches, 1); expectEqual(committed.requests, 1)
        let restored = WingthingModel()
        try await restored.configure(profile: home, existingBearer: wire.bearer, cacheFile: file, wire: wire)
        expectFalse(restored.connected)
        await restored.refresh()
        expectEqual(restored.pendingContinuation?.id, pending.id)
        expectEqual(restored.pendingContinuation?.source, original)
        expectEqual(restored.execution?.sessionID, "fixture-turn-1")
        let afterRead = await wire.counts(); expectEqual(afterRead.requests, 1)
        // A new execution's unrelated draft must survive acknowledgement of
        // the original source; replay may never retarget the saved request.
        restored.draft = "Unsent next message"
        await restored.sendOrCheck()
        expectNil(restored.pendingContinuation); expectEqual(restored.draft, "Unsent next message")
        let replayed = await wire.counts(); expectEqual(replayed.launches, 1); expectEqual(replayed.requests, 2)
        let intents = await wire.intents(); expectEqual(intents, [.object(pending.arguments)])
        expectEqual(Set(pending.arguments.keys), ["resume_session", "conversation_role", "input", "request_id"])
        let store = try LocalConversationStore(profile: home, file: file)
        expectEqual(try await store.continuation(for: root)?.progress, .started)
        await restored.refresh()
        let afterReconnect = await wire.counts(); expectEqual(afterReconnect.requests, 2)
        expectEqual(restored.draft, "Unsent next message")
    }

    @Test func failureRestoresMessageAndExplicitNewSendCreatesANewRequestThenStopIsExact() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("continuation-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = ConversationTransportFixture(scenario: "failed"), home = try await wire.profile(), model = WingthingModel()
        let file = directory.appendingPathComponent("state.json"), root = reference(home, wing: wire.wingID, task: wire.rootID)
        try await model.configure(profile: home, existingBearer: wire.bearer, cacheFile: file, wire: wire)
        await model.refresh(); await model.open(root)
        let priorExecution = try unwrap(model.execution)
        model.draft = "Keep working"; await model.sendOrCheck()
        expectNil(model.pendingContinuation); expectEqual(model.draft, "Keep working"); expectTrue(model.canContinue)
        let store = try LocalConversationStore(profile: home, file: file)
        let failed = try unwrap(try await store.continuation(for: root))
        expectEqual(failed.progress, .failed)
        let initial = await wire.counts(); expectEqual(initial.launches, 0); expectEqual(initial.requests, 1)
        await model.sendOrCheck()
        expectEqual(model.draft, ""); expectNil(model.pendingContinuation); expectTrue(model.canStop)
        let intents = await wire.intents()
        expectEqual(intents.count, 2); expectTrue(intents.contains { $0["request_id"] != .string(failed.requestID) })
        let target = try unwrap(model.execution)
        // A confirmation for a different execution cannot inherit this live
        // task, even when it is the same logical conversation.
        await model.requestStop(expectedExecution: priorExecution)
        expectNil(model.pendingStop)
        let notStopped = await wire.counts(); expectEqual(notStopped.stops, 0)
        await model.requestStop(expectedExecution: target)
        expectEqual(model.pendingStop?.execution, target); expectEqual(model.pendingStop?.progress, .terminationObserved)
        let stopped = await wire.counts(); expectEqual(stopped.launches, 1); expectEqual(stopped.stops, 1)
        await model.refresh()
        let after = await wire.counts(); expectEqual(after.stops, 1); expectEqual(after.requests, 2)
        expectFalse(model.canStop)
    }

    @Test func unsavedRequestNeverReachesTransport() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("continuation-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = ConversationTransportFixture(scenario: "normal"), home = try await wire.profile(), model = WingthingModel()
        try await model.configure(profile: home, existingBearer: wire.bearer, cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh(); await model.open(reference(home, wing: wire.wingID, task: wire.rootID))
        expectTrue(model.canContinue)
        try FileManager.default.removeItem(at: directory)
        try Data("not a directory".utf8).write(to: directory)
        model.draft = "Preserve this message"; await model.sendOrCheck()
        expectNil(model.pendingContinuation); expectEqual(model.draft, "Preserve this message")
        expectTrue(model.error != nil)
        let counts = await wire.counts(); expectEqual(counts.requests, 0)
    }

    @Test func receiptMustBindRequestSourceProviderWingRootTargetAndExactInput() throws {
        let home = try profile(), source = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let intent = try PendingContinuation(source: source, input: "Exact message 🦉")
        for change: [String: JSONValue] in [
            ["request_id": .string("other")], ["source_session": .string("other")], ["provider_session_id": .string("other")],
            ["wing_id": .string("other")], ["conversation_id": .string("other")], ["root_conversation_id": .string("other")],
            ["parent_conversation_id": .string("other")], ["session": .string(source.sessionID)], ["session": .string(" ")],
            ["new_turn": .object(["input_sha256": .string(String(repeating: "0", count: 64))])], ["continuation_state": .string("uncertain")]
        ] {
            var uncertain = intent; uncertain.apply(continuationResult(intent, changes: change))
            expectEqual(uncertain.progress, .unconfirmed); expectNil(uncertain.targetSessionID)
        }
        var started = intent; started.apply(continuationResult(intent)); expectEqual(started.progress, .started)
        started.markUnconfirmed(); expectEqual(started.progress, .started)
        var failed = intent
        failed.apply(continuationResult(intent, changes: ["launch_state": .string("failed"), "continuation_state": .string("failed"), "provider_session_id": .string("")]))
        expectEqual(failed.progress, .failed)
        expectThrows(try PendingContinuation(source: source, input: "  "))
        expectThrows(try PendingContinuation(source: source, input: "-flag"))
        expectThrows(try PendingContinuation(source: source, input: "contains\0nul"))
    }

    @Test func noAdvertisementAndReconnectCannotAuthorizeANewLaunch() async throws {
        let wire = ConversationTransportFixture(scenario: "normal"), home = try await wire.profile()
        let client = try HomeClient(profile: home, existingBearer: wire.bearer, wire: wire)
        _ = try await client.verifyHome()
        let source = ExecutionReference(conversation: reference(home, wing: wire.wingID, task: wire.rootID), sessionID: "fixture-source", providerSessionID: wire.providerID)
        let intent = try PendingContinuation(source: source, input: "Follow up")
        do { _ = try await client.submitContinuation(intent, firstAttempt: true); fail("Launch without a fresh read") } catch { expectEqual(error as? ClientError, .staleReference) }
        _ = try await client.observe(source, after: 0)
        _ = try await client.verifyHome()
        do { _ = try await client.submitContinuation(intent, firstAttempt: true); fail("Reconnect reused earlier evidence") } catch { expectEqual(error as? ClientError, .staleReference) }
        let counts = await wire.counts(); expectEqual(counts.requests, 0)
    }

    @Test func persistedIntentIsImmutableAndOnlyOneUnconfirmedFollowUpPerConversation() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("continuation-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), source = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let file = directory.appendingPathComponent("state.json"), store = try LocalConversationStore(profile: home, file: file)
        var original = try PendingContinuation(source: source, input: "Saved original")
        try await store.saveContinuation(original); original.markUnconfirmed(); try await store.saveContinuation(original)
        do { try await store.saveContinuation(PendingContinuation(id: original.id, source: source, input: "Changed")); fail("Changed immutable message") } catch { expectEqual(error as? ClientError, .staleReference) }
        do { try await store.saveContinuation(PendingContinuation(source: source, input: "Duplicate")); fail("Created another unfinished request") } catch { }
        let reopened = try LocalConversationStore(profile: home, file: file)
        expectEqual(try await reopened.continuation(for: source.conversation), original)
        original.apply(continuationResult(original)); try await store.saveContinuation(original)
        var stale = try PendingContinuation(id: original.id, source: source, input: original.input); stale.markUnconfirmed()
        try await store.saveContinuation(stale)
        expectEqual(try await store.continuation(for: source.conversation)?.progress, .started)
    }

    @Test func inspectionOnlyClientRefusesEvenAnExplicitSavedReplay() async throws {
        let home = try HomeProfile.localPreview(origin: URL(string: "http://127.0.0.1:61428")!, expectedUserID: "local", homeWingID: "mac", homeWingPublicKey: wingPublic)
        let wire = FixtureWire(), client = try HomeClient(localPreview: home, wire: wire)
        let intent = try PendingContinuation(source: ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id"), input: "Never sent")
        for first in [true, false] {
            do { _ = try await client.submitContinuation(intent, firstAttempt: first); fail("Inspection route sent a launch") }
            catch { expectEqual(error as? ClientError, .unsupported("Local preview is inspection only.")) }
        }
        let requests = await wire.requests; expectTrue(requests.isEmpty)
    }

    @Test func advertisementMustMatchEndedNativeExecutionAndExactSource() throws {
        let home = try profile(), source = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let ended = try lifecycle("completed", alive: false)
        let fields: [String: JSONValue] = ["available": .bool(true), "source_session": .string("same-session"),
            "conversation_id": .string("root"), "provider_session_id": .string("native-id"), "model": .string("claude-opus-5-5")]
        expectTrue(ContinuationAvailability(result: .object(["headless_continuation": .object(fields)]), execution: source, lifecycle: ended) != nil)
        for change: [String: JSONValue] in [["available": .bool(false)], ["source_session": .string("other")],
            ["conversation_id": .string("other")], ["provider_session_id": .string("other")], ["model": .string("other-provider")]] {
            let bad = fields.merging(change) { _, new in new }
            expectNil(ContinuationAvailability(result: .object(["headless_continuation": .object(bad)]), execution: source, lifecycle: ended))
        }
        expectNil(ContinuationAvailability(result: .object(["headless_continuation": .object(fields)]), execution: source, lifecycle: try lifecycle()))
    }

    @Test func replacedHomeFencesDelayedLaunchReplyAndNeverRetargetsIt() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("continuation-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let original = ConversationTransportFixture(scenario: "normal"), held = ContinuationHoldWire(original)
        let home = try await original.profile(), model = WingthingModel(), file = directory.appendingPathComponent("old.json")
        let root = reference(home, wing: original.wingID, task: original.rootID)
        try await model.configure(profile: home, existingBearer: original.bearer, cacheFile: file, wire: held)
        await model.refresh(); await model.open(root)
        model.draft = "Original source only"; await held.holdNext()
        let send = Task { await model.sendOrCheck() }
        for _ in 0..<400 { if await held.holding { break }; try await Task.sleep(for: .milliseconds(5)) }
        expectTrue(await held.holding)
        let pending = try unwrap(model.pendingContinuation)
        let replacement = ConversationTransportFixture(scenario: "normal"), newHome = try await replacement.profile()
        try await model.configure(profile: newHome, existingBearer: replacement.bearer, cacheFile: directory.appendingPathComponent("new.json"), wire: replacement)
        model.draft = "Replacement draft"
        await held.release(); await send.value
        expectEqual(model.profile?.id, newHome.id); expectNil(model.execution); expectNil(model.pendingContinuation)
        expectEqual(model.draft, "Replacement draft")
        let oldCounts = await original.counts(), newCounts = await replacement.counts()
        expectEqual(oldCounts.launches, 1); expectEqual(newCounts.requests, 0)
        let store = try LocalConversationStore(profile: home, file: file)
        expectEqual(try await store.continuation(for: root)?.id, pending.id)
        expectEqual(try await store.continuation(for: root)?.progress, .started)
    }
}
