import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private func childLifecycle(_ state: String, provider: String?, cursor: Int64, events: [JSONValue] = []) -> JSONValue {
    .object(["session_id": .string("child-session"), "agent": .string("claude"), "provider_session_id": provider.map(JSONValue.string) ?? .null, "state": .string(state), "state_source": .string("claude_hook"), "ready": .bool(true), "process_alive": .bool(true), "cursor": .integer(cursor), "events": .array(events)])
}

private func routeChild(_ wire: FixtureWire, _ view: JSONValue) async throws {
    let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
    let tasks: JSONValue = .array([.object(["conversation": root, "lifecycle": try lifecycleJSON(lifecycle("idle"))]), .object(["conversation": child, "lifecycle": view])])
    await wire.route([
        "conversation_list:": .object(["conversations": .array([root, child])]),
        "conversation_read:child": .object(["conversation": child, "tasks": tasks]),
        "session_read:child-session": .object(["lifecycle": view]),
    ])
}

@MainActor private func onlineModel(_ wire: FixtureWire, _ directory: URL) async throws -> (WingthingModel, HomeProfile) {
    let home = try profile(), model = WingthingModel()
    try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
    return (model, home)
}

// Every request the fixture wing decrypted is a read or the one explicit prompt;
// no stop, kill, or terminal input was ever attempted.
private func expectNoStopAttempt(_ wire: FixtureWire, sourceLocation: SourceLocation = #_sourceLocation) async {
    let requests = await wire.requests
    expectTrue(requests.allSatisfy { $0["type"] == .string("session.control") }, sourceLocation: sourceLocation)
    let operations = Set(requests.compactMap { $0["operation"]?.string })
    expectTrue(operations.isSubset(of: ["conversation_list", "conversation_read", "session_read", "session_prompt"]), sourceLocation: sourceLocation)
}

@Suite @MainActor struct StopCapabilityPartnerTests {
    @Test func homeClientHasNoStopAdapterAndSendsNothingForStopOperations() async throws {
        let home = try profile(), wire = FixtureWire()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: wire)
        _ = try await client.verifyHome()
        let execution = reference(home, task: "child")
        for operation in ["agent_stop", "terminal_stop", "session_stop", "terminal_send", "pty.kill"] {
            do {
                _ = try await client.control(execution, operation: operation, arguments: ["session": .string("child-session")])
                fail("\(operation) was sent without a supported stop contract")
            } catch {
                guard case .unsupported = error as? ClientError else { fail("\(operation) failed for the wrong reason: \(error)"); continue }
            }
        }
        let requests = await wire.requests
        expectTrue(requests.isEmpty)
    }

    @Test func stopIsAbsentWithoutSelectionAndQuietWhenChildIsReady() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-stop-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let fresh = WingthingModel()
        expectFalse(fresh.canStop); expectNil(fresh.stopNotice)
        expectFalse(WingthingModel.stopUnavailableHint.isEmpty)

        let wire = FixtureWire()
        let (model, home) = try await onlineModel(wire, directory)
        try await routeChild(wire, childLifecycle("idle", provider: "child-native", cursor: 1))
        await model.refresh(); await model.open(reference(home, task: "child"))
        expectEqual(model.currentStatus, .ready)
        expectFalse(model.canStop); expectNil(model.stopNotice)
        await expectNoStopAttempt(wire)
    }

    @Test func workingChildExplainsStopIsUnavailableAndThatLeavingDoesNotStopIt() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-stop-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = FixtureWire()
        let (model, home) = try await onlineModel(wire, directory)
        let child = reference(home, task: "child")
        try await routeChild(wire, childLifecycle("working", provider: "child-native", cursor: 2))
        await model.refresh(); await model.open(child)
        expectEqual(model.selected, child); expectEqual(model.currentStatus, .working)
        expectFalse(model.canStop); expectFalse(model.inputReady)
        let notice = try unwrap(model.stopNotice)
        expectTrue(notice.contains("isn't available yet"))
        expectTrue(notice.contains("keeps running"))
        expectTrue(notice.contains("leave this screen or disconnect"))

        // Repeated polls and reconnects while working still only read.
        await model.pollTranscript(); await model.refresh(); await model.pollTranscript()
        expectEqual(model.currentStatus, .working); expectFalse(model.canStop)
        await expectNoStopAttempt(wire)
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectTrue(prompts.isEmpty)
    }

    @Test func reconnectAndDisconnectKeepPendingInputAndNeverStopOrResend() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-stop-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = FixtureWire()
        let (model, home) = try await onlineModel(wire, directory)
        let child = reference(home, task: "child")
        try await routeChild(wire, childLifecycle("idle", provider: "child-native", cursor: 1))
        await model.refresh(); await model.open(child)
        model.draft = "Summarize the child evidence"
        await wire.configure(reply: .object(["receipt": .object(["status": .string("unconfirmed")])]))
        await model.sendOrCheck()
        let pending = try unwrap(model.pending)
        expectEqual(pending.delivery, .unconfirmed)

        // The child starts working; reconnecting reads it but sends nothing new.
        try await routeChild(wire, childLifecycle("working", provider: "child-native", cursor: 2))
        await model.refresh()
        expectEqual(model.currentStatus, .working); expectFalse(model.canStop)
        expectEqual(model.pending?.id, pending.id); expectEqual(model.pending?.input, "Summarize the child evidence")
        expectTrue(model.stopNotice?.contains("keeps running") == true)

        // Disconnecting releases the connection only. The wing gets no request.
        let before = await wire.requests.count
        await model.disconnect()
        let after = await wire.requests.count
        expectEqual(before, after)
        expectEqual(model.currentStatus, .offline); expectFalse(model.canStop)
        expectEqual(model.pending?.id, pending.id)
        expectTrue(model.stopNotice?.contains("Disconnecting doesn't stop") == true)

        await expectNoStopAttempt(wire)
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(prompts.count, 1)
    }
}
