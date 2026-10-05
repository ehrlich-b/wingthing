import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private actor HistoryWire: HomeWire {
    let fixture = FixtureWire()
    var cursors: [Int64] = []
    let holdSecond: Bool
    let stall: Bool
    private var held: CheckedContinuation<Void, Never>?
    private var arrival: CheckedContinuation<Void, Never>?
    private var arrived = false
    init(holdSecond: Bool = false, stall: Bool = false) { self.holdSecond = holdSecond; self.stall = stall }
    func prepare() async throws {
        let root = conversationJSON("root"), view = try lifecycleJSON(lifecycle("idle", cursor: 0))
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
            await withCheckedContinuation { held = $0 }
        }
        let cursor = stall ? after : min(3, after + 1)
        let events: [JSONValue] = cursor > after ? [.object(["sequence": .integer(cursor), "type": .string("message"), "source": .string("claude_transcript"),
            "raw": .object(["type": .string("assistant"), "message": .object(["role": .string("assistant"), "content": .string("Page \(cursor)")])])])] : []
        guard case .object(var view) = try lifecycleJSON(lifecycle("idle", cursor: cursor, events: events)) else { throw ClientError.staleReference }
        view["has_more"] = .bool(cursor < 3); view["head_cursor"] = .integer(3)
        let result: JSONValue = .object(["lifecycle": .object(view)])
        return try JSONEncoder().encode(JSONValue.object(["type": .string("tunnel.res"), "request_id": outer["request_id"]!, "payload": .string(try cipher.seal(JSONEncoder().encode(result)))]))
    }
    func waitUntilHeld() async { if !arrived { await withCheckedContinuation { arrival = $0 } } }
    func release() { held?.resume(); held = nil }
}

@Suite @MainActor struct HistoryDrainTests {
    private func setup(_ wire: HistoryWire, directory: URL) async throws -> (WingthingModel, HomeProfile) {
        let home = try profile(), model = WingthingModel()
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
    }
    @Test func cancellationStopsDrainingWithoutPublishingLatePages() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(holdSecond: true), (model, home) = try await setup(wire, directory: directory)
        let opening = Task { await model.open(reference(home)) }
        await wire.waitUntilHeld(); opening.cancel(); await wire.release(); await opening.value
        expectEqual(await wire.cursors, [0, 1]); expectEqual(model.transcript.cursor, 1); expectFalse(model.busy); expectNil(model.error)
    }
    @Test func stalledPageFailsInsteadOfSpinning() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("history-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = HistoryWire(stall: true), (model, home) = try await setup(wire, directory: directory)
        await model.open(reference(home))
        expectEqual(await wire.cursors, [0]); expectTrue(model.error?.contains("stopped advancing") == true); expectFalse(model.busy)
    }
}
