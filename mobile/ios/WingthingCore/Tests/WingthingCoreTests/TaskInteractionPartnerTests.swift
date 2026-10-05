import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private func childLifecycle(_ state: String, provider: String?, cursor: Int64, ready: Bool = true, events: [JSONValue] = []) -> JSONValue {
    .object(["session_id": .string("child-session"), "agent": .string("claude"), "provider_session_id": provider.map(JSONValue.string) ?? .null, "state": .string(state), "state_source": .string("claude_hook"), "ready": .bool(ready), "process_alive": .bool(true), "cursor": .integer(cursor), "events": .array(events)])
}

private func message(_ sequence: Int64, _ text: String) -> JSONValue {
    .object(["sequence": .integer(sequence), "type": .string("message"), "role": .string("assistant"), "text": .string(text)])
}

// `tree` is the child row's lifecycle inside conversation_read (nil = absent);
// `read` is what session_read returns for the child session.
private func routeChild(_ wire: FixtureWire, tree: JSONValue?, read: JSONValue) async throws {
    let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
    var childTask: [String: JSONValue] = ["conversation": child]
    if let tree { childTask["lifecycle"] = tree }
    let tasks: JSONValue = .array([.object(["conversation": root, "lifecycle": try lifecycleJSON(lifecycle("idle"))]), .object(childTask)])
    await wire.route([
        "conversation_list:": .object(["conversations": .array([root, child])]),
        "conversation_read:child": .object(["conversation": child, "tasks": tasks]),
        "session_read:child-session": .object(["lifecycle": read]),
    ])
}

@MainActor private func onlineModel(_ wire: FixtureWire, _ directory: URL) async throws -> (WingthingModel, HomeProfile) {
    let home = try profile(), model = WingthingModel()
    try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
    return (model, home)
}

@Suite @MainActor struct TaskInteractionPartnerTests {
    @Test func refreshOfSameChildKeepsDraftAndEarlierOutputWhenTreeRowHasNoProviderBinding() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-task-interaction-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = FixtureWire()
        let (model, home) = try await onlineModel(wire, directory)
        let child = reference(home, task: "child")
        try await routeChild(wire, tree: nil, read: childLifecycle("idle", provider: "child-native", cursor: 2, events: [message(1, "First child output"), message(2, "Second child output")]))
        await model.refresh(); await model.open(child)
        expectEqual(model.selected, child); expectEqual(model.execution?.providerSessionID, "child-native")
        expectEqual(model.transcript.events.map(\.sequence), [1, 2]); expectTrue(model.inputReady)
        model.draft = "Compare the child output with the parent plan"

        // One newer event exists; the native reader pages only after the client's cursor.
        try await routeChild(wire, tree: nil, read: childLifecycle("idle", provider: "child-native", cursor: 3, events: [message(3, "Third child output")]))
        await model.refresh()
        expectEqual(model.phase, .online); expectEqual(model.selected, child)
        expectEqual(model.draft, "Compare the child output with the parent plan")
        expectEqual(model.transcript.events.map(\.sequence), [1, 2, 3])
        let reads = await wire.requests.filter { $0["operation"] == .string("session_read") }
        expectEqual(reads.last?["arguments"]?["after_cursor"], .integer(2))
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectTrue(prompts.isEmpty)

        // A different provider binding is still a new execution: nothing carries over.
        let replaced = childLifecycle("idle", provider: "replacement-native", cursor: 1, events: [message(1, "Replacement output")])
        try await routeChild(wire, tree: replaced, read: replaced)
        await model.refresh()
        expectEqual(model.execution?.providerSessionID, "replacement-native"); expectEqual(model.draft, "")
        expectEqual(model.items.map(\.content), ["Replacement output"])
    }

    @Test func pendingInputStaysBoundAcrossRefreshWhenProviderBindsWhilePolling() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-task-interaction-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let wire = FixtureWire()
        let (model, home) = try await onlineModel(wire, directory)
        let child = reference(home, task: "child")
        let starting = childLifecycle("starting", provider: nil, cursor: 1, ready: false)
        try await routeChild(wire, tree: starting, read: starting)
        await model.refresh(); await model.open(child)
        expectNil(model.execution?.providerSessionID); expectFalse(model.inputReady)

        // The provider session binds while the conversation screen polls.
        let bound = childLifecycle("idle", provider: "child-native", cursor: 2, events: [message(2, "Child is ready")])
        try await routeChild(wire, tree: starting, read: bound)
        await model.pollTranscript()
        expectEqual(model.execution?.providerSessionID, "child-native"); expectTrue(model.inputReady)

        model.draft = "Report the child's evidence"
        await wire.configure(reply: .object(["receipt": .object(["status": .string("unconfirmed")])]))
        await model.sendOrCheck()
        let pending = try unwrap(model.pending)
        expectEqual(pending.delivery, .unconfirmed); expectEqual(pending.execution.providerSessionID, "child-native")

        // The tree now reports the same binding. Reconnect only reads and keeps the exact pending input.
        try await routeChild(wire, tree: bound, read: bound)
        await model.refresh()
        expectEqual(model.phase, .online)
        expectEqual(model.pending?.id, pending.id); expectEqual(model.pending?.input, "Report the child's evidence")
        expectFalse(model.inputReady)
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(prompts.count, 1)
    }
}
