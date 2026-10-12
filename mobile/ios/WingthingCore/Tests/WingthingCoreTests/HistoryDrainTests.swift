import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private final class ManualHistoryClock: HistoryReadClock, @unchecked Sendable {
    private let lock = NSLock()
    private var time = Duration.zero
    private var sleepers: [UUID: (Duration, CheckedContinuation<Void, any Error>)] = [:]
    var elapsed: Duration { lock.withLock { time } }
    func sleep(until deadline: Duration) async throws {
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
                lock.withLock {
                    if Task.isCancelled { continuation.resume(throwing: CancellationError()) }
                    else if time >= deadline { continuation.resume() }
                    else { sleepers[id] = (deadline, continuation) }
                }
            }
        } onCancel: {
            self.lock.withLock { self.sleepers.removeValue(forKey: id)?.1.resume(throwing: CancellationError()) }
        }
    }
    func advance(by duration: Duration) {
        lock.withLock {
            time += duration
            for (id, sleeper) in sleepers where sleeper.0 <= time {
                sleepers.removeValue(forKey: id); sleeper.1.resume()
            }
        }
    }
}

private actor HistoryWire: HomeWire {
    let fixture = FixtureWire()
    var cursors: [Int64] = []
    let holdSecond: Bool
    let stall: Bool
    let head: Int64
    let advancingForever: Bool
    let cancellableHold: Bool
    let pendingAtHead: Bool
    let providerID: String
    private(set) var cancelledReads = 0
    private var held: CheckedContinuation<Void, any Error>?
    private var arrival: CheckedContinuation<Void, Never>?
    private var arrived = false
    init(holdSecond: Bool = false, stall: Bool = false, head: Int64 = 3, advancingForever: Bool = false,
         cancellableHold: Bool = false, pendingAtHead: Bool = false, providerID: String = "native-id") {
        self.holdSecond = holdSecond; self.stall = stall; self.head = head; self.advancingForever = advancingForever
        self.cancellableHold = cancellableHold; self.pendingAtHead = pendingAtHead; self.providerID = providerID
    }
    func prepare() async throws {
        let root = conversationJSON("root"), view = try pageLifecycle(cursor: 0, events: [])
        await fixture.route(["conversation_list:": .object(["conversations": .array([root])]),
            "conversation_read:root": .object(["conversation": root, "tasks": .array([.object(["conversation": root, "lifecycle": view])])])])
    }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply { try await fixture.get(url, bearer: bearer) }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        let outer = try JSONDecoder().decode(JSONValue.self, from: request)
        let wing = try TunnelIdentity(rawPrivateKey: Data(base64Encoded: wingPrivate)!)
        let cipher = try wing.cipher(peerPublicKey: unwrap(outer["sender_pub"]?.string))
        let inner = try JSONDecoder().decode(JSONValue.self, from: cipher.open(unwrap(outer["payload"]?.string)))
        guard inner["operation"] == .string("session_read") else { return try await fixture.exchange(url, bearer: bearer, request: request) }
        guard case .integer(let after)? = inner["arguments"]?["after_cursor"] else { throw ClientError.staleReference }
        cursors.append(after)
        if holdSecond && cursors.count == 2 {
            arrived = true; arrival?.resume(); arrival = nil
            try await withTaskCancellationHandler {
                try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
                    if cancellableHold && Task.isCancelled { continuation.resume(throwing: CancellationError()) }
                    else { held = continuation }
                }
            } onCancel: {
                if self.cancellableHold { Task { await self.cancelHeld() } }
            }
        }
        let cursor = stall ? after : advancingForever ? after + 1 : min(head, after + 1)
        let events: [JSONValue] = cursor > after ? [.object(["sequence": .integer(cursor), "type": .string("message"), "source": .string("claude_transcript"),
            "raw": .object(["type": .string("assistant"), "message": .object(["role": .string("assistant"), "content": .string("Page \(cursor)")])])])] : []
        let result: JSONValue = .object(["lifecycle": try pageLifecycle(cursor: cursor, events: events)])
        return try JSONEncoder().encode(JSONValue.object(["type": .string("tunnel.res"), "request_id": outer["request_id"]!, "payload": .string(try cipher.seal(JSONEncoder().encode(result)))]))
    }
    private func pageLifecycle(cursor: Int64, events: [JSONValue]) throws -> JSONValue {
        guard case .object(var view) = try lifecycleJSON(lifecycle("idle", cursor: cursor, events: events)) else { throw ClientError.staleReference }
        view["provider_session_id"] = .string(providerID)
        view["has_more"] = .bool(advancingForever || pendingAtHead || cursor < head)
        view["head_cursor"] = .integer(advancingForever ? cursor + 1 : head)
        return .object(view)
    }
    private func cancelHeld() { cancelledReads += 1; held?.resume(throwing: CancellationError()); held = nil }
    func waitUntilHeld() async { if !arrived { await withCheckedContinuation { arrival = $0 } } }
    func release() { held?.resume(); held = nil }
}

@Suite @MainActor struct HistoryDrainTests {
    private func setup(_ wire: HistoryWire, directory: URL, budget: HistoryReadBudget = HistoryReadBudget(),
                       clock: any HistoryReadClock = ManualHistoryClock()) async throws -> (WingthingModel, HomeProfile) {
        let home = try profile(), model = WingthingModel(historyBudget: budget, historyClock: clock)
        try await wire.prepare()
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh()
        return (model, home)
    }
    @Test func openingDrainsAllPagesBeforeReturningThenPollingReadsFromHead() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(), (model, home) = try await setup(wire, directory: directory)
        await model.open(reference(home))
        expectEqual(await wire.cursors, [0, 1, 2]); expectEqual(model.transcript.events.map(\.sequence), [1, 2, 3])
        expectEqual(model.transcript.cursor, 3); expectFalse(model.busy); expectNil(model.error)
        await model.pollTranscript(); expectEqual(await wire.cursors, [0, 1, 2, 3])
    }
    @Test func disconnectDiscardsAnInflightDrainAndIssuesNoNextPage() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(holdSecond: true), (model, home) = try await setup(wire, directory: directory)
        let opening = Task { await model.open(reference(home)) }
        await wire.waitUntilHeld(); await model.disconnect(); await wire.release(); await opening.value
        expectEqual(await wire.cursors, [0, 1]); expectEqual(model.transcript.cursor, 1); expectEqual(model.phase, .disconnected)
        let store = try LocalConversationStore(profile: home, file: directory.appendingPathComponent("state.json"))
        expectEqual(try await store.cached(reference(home))?.transcript.cursor, 1)
        expectTrue(model.historyNeedsContinue); expectFalse(model.canContinueReading)
    }
    @Test func cancellationStopsDrainingWithoutPublishingLatePages() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(holdSecond: true), (model, home) = try await setup(wire, directory: directory)
        let opening = Task { await model.open(reference(home)) }
        await wire.waitUntilHeld(); opening.cancel(); await wire.release(); await opening.value
        expectEqual(await wire.cursors, [0, 1]); expectEqual(model.transcript.cursor, 1); expectFalse(model.busy); expectNil(model.error)
        let store = try LocalConversationStore(profile: home, file: directory.appendingPathComponent("state.json"))
        expectEqual(try await store.cached(reference(home))?.transcript.cursor, 1)
        expectTrue(model.canContinueReading)
    }
    @Test func stalledPageFailsInsteadOfSpinning() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(stall: true), (model, home) = try await setup(wire, directory: directory)
        await model.open(reference(home))
        expectEqual(await wire.cursors, [0]); expectTrue(model.error?.contains("stopped advancing") == true); expectFalse(model.busy)
    }

    @Test func advancingPagesYieldAtThePageBudgetAndPollingCannotDrainPastIt() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(advancingForever: true)
        let (model, home) = try await setup(wire, directory: directory, budget: HistoryReadBudget(maxPages: 2))
        await model.open(reference(home))
        expectEqual(await wire.cursors, [0, 1]); expectEqual(model.transcript.cursor, 2)
        expectTrue(model.canContinueReading); expectFalse(model.busy); expectNil(model.error); expectFalse(model.canStop)
        await model.pollTranscript(); expectEqual(await wire.cursors, [0, 1])
        await model.continueReading()
        expectEqual(await wire.cursors, [0, 1, 2, 3]); expectEqual(model.transcript.events.map(\.sequence), [1, 2, 3, 4])
        expectTrue(model.canContinueReading); expectNil(model.error)
    }

    @Test func continuedReadingReachesHeadWithoutReplacingProgressOrDraft() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(head: 5)
        let (model, home) = try await setup(wire, directory: directory, budget: HistoryReadBudget(maxPages: 2))
        await model.open(reference(home)); model.draft = "Keep this unsent draft"
        await model.continueReading(); expectEqual(model.transcript.cursor, 4); expectTrue(model.canContinueReading)
        await model.continueReading()
        expectEqual(await wire.cursors, [0, 1, 2, 3, 4]); expectEqual(model.transcript.events.map(\.sequence), [1, 2, 3, 4, 5])
        expectEqual(model.draft, "Keep this unsent draft"); expectFalse(model.historyNeedsContinue); expectFalse(model.busy)
        await model.pollTranscript(); expectEqual(await wire.cursors, [0, 1, 2, 3, 4, 5])
    }

    @Test func aggregateDeadlineCancelsAnInflightReadAndResumesFromLastSavedPage() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let clock = ManualHistoryClock(), wire = HistoryWire(holdSecond: true, cancellableHold: true)
        let (model, home) = try await setup(wire, directory: directory, clock: clock)
        let opening = Task { await model.open(reference(home)) }
        await wire.waitUntilHeld(); clock.advance(by: .seconds(10)); await opening.value
        expectEqual(await wire.cursors, [0, 1]); expectEqual(await wire.cancelledReads, 1)
        expectEqual(model.transcript.cursor, 1); expectTrue(model.canContinueReading); expectTrue(model.connected)
        expectFalse(model.busy); expectNil(model.error)
        await model.continueReading()
        expectEqual(await wire.cursors, [0, 1, 1, 2]); expectEqual(model.transcript.cursor, 3); expectFalse(model.historyNeedsContinue)
    }

    @Test func relaunchRestoresPartialProgressAndItsExactDraftBeforeResuming() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(head: 5), budget = HistoryReadBudget(maxPages: 2)
        let (first, home) = try await setup(wire, directory: directory, budget: budget)
        await first.open(reference(home)); first.draft = "Draft saved with partial history"
        let restored = WingthingModel(historyBudget: budget, historyClock: ManualHistoryClock()), restoredWire = HistoryWire(head: 5)
        try await restoredWire.prepare()
        try await restored.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: restoredWire)
        expectEqual(restored.transcript.cursor, 2); expectTrue(restored.historyNeedsContinue); expectFalse(restored.canContinueReading)
        await restored.refresh()
        expectEqual(await restoredWire.cursors, [2, 3]); expectEqual(restored.transcript.cursor, 4)
        expectEqual(restored.draft, "Draft saved with partial history"); expectTrue(restored.canContinueReading)
        await restored.continueReading(); expectEqual(restored.transcript.cursor, 5); expectFalse(restored.historyNeedsContinue)
        expectEqual(restored.transcript.events.map(\.sequence), [1, 2, 3, 4, 5])
    }

    @Test func latePageAfterHomeReplacementCannotRetargetOrCacheTheNewHome() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let oldWire = HistoryWire(holdSecond: true), (model, oldHome) = try await setup(oldWire, directory: directory)
        let opening = Task { await model.open(reference(oldHome)) }
        await oldWire.waitUntilHeld()
        let newHome = try profile(), newWire = HistoryWire()
        try await newWire.prepare()
        try await model.configure(profile: newHome, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("other.json"), wire: newWire)
        await model.refresh(); await model.open(reference(newHome)); model.draft = "New home's draft"
        await oldWire.release(); await opening.value
        expectEqual(model.profile, newHome); expectEqual(model.selected, reference(newHome)); expectEqual(model.transcript.cursor, 3)
        expectEqual(model.draft, "New home's draft"); expectFalse(model.historyNeedsContinue); expectFalse(model.busy); expectNil(model.error)
        expectEqual(await oldWire.cursors, [0, 1])
        let oldStore = try LocalConversationStore(profile: oldHome, file: directory.appendingPathComponent("state.json"))
        expectEqual(try await oldStore.cached(reference(oldHome))?.transcript.cursor, 1)
    }

    @Test func cachedCursorCannotBeReusedForAReplacedProvider() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let (first, home) = try await setup(HistoryWire(), directory: directory, budget: HistoryReadBudget(maxPages: 2))
        await first.open(reference(home)); first.draft = "Old execution draft"
        let wire = HistoryWire(providerID: "replacement-provider"), restored = WingthingModel(historyClock: ManualHistoryClock())
        try await wire.prepare()
        try await restored.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await restored.refresh()
        expectEqual(await wire.cursors, [0, 1, 2]); expectEqual(restored.execution?.providerSessionID, "replacement-provider")
        expectEqual(restored.transcript.events.map(\.sequence), [1, 2, 3]); expectEqual(restored.draft, "")
    }

    @Test func pendingJournalAtHeadDoesNotOfferAnEndlessContinueAction() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(pendingAtHead: true), (model, home) = try await setup(wire, directory: directory)
        await model.open(reference(home))
        expectEqual(await wire.cursors, [0, 1, 2, 3]); expectEqual(model.transcript.cursor, 3)
        expectFalse(model.historyNeedsContinue); expectFalse(model.busy); expectNil(model.error)
    }
}
