import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

func conversationJSON(_ id: String, parent: String? = nil, wing: String = "mac") -> JSONValue {
    .object(["conversation_id": .string(id), "root_conversation_id": .string(parent ?? id), "parent_conversation_id": parent.map(JSONValue.string) ?? .null, "title": .string(id == "root" ? "Personal parent" : id), "agent": .string("claude"), "cwd": .string("/synthetic/workspace"), "wing_id": .string(wing), "session_id": .string(id == "root" ? "same-session" : "child-session"), "launch_state": .string("started")])
}
func lifecycleJSON(_ view: SessionLifecycle) throws -> JSONValue { try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(view)) }

@Suite @MainActor struct NativeJourneyTests {
    @Test func testNativeParentChildInspectReconnectAndExactPendingReceiptJourney() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-ios-native-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), model = WingthingModel()
        let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
        let rootLifecycle = try lifecycleJSON(lifecycle("idle"))
        let childView: JSONValue = .object(["session_id": .string("child-session"), "agent": .string("claude"), "provider_session_id": .string("child-native"), "state": .string("needs_input"), "state_source": .string("claude_hook"), "ready": .bool(false), "process_alive": .bool(true), "cursor": .integer(1), "events": .array([])])
        let tasks: JSONValue = .array([.object(["conversation": conversationJSON("root", wing: "other-wing")]), .object(["conversation": root, "lifecycle": rootLifecycle]), .object(["conversation": child, "lifecycle": childView])])
        await wire.route([
            "conversation_list:": .object(["conversations": .array([root, child])]),
            "conversation_read:root": .object(["conversation": root, "tasks": tasks]),
            "conversation_read:child": .object(["conversation": child, "tasks": tasks]),
            "session_read:same-session": .object(["lifecycle": rootLifecycle]),
            "session_read:child-session": .object(["lifecycle": childView]),
        ])
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        let before = await wire.calls
        expectTrue(before.isEmpty) // Configuration/cache restore performs no network or provider action.
        await model.refresh()
        expectTrue(model.connected); expectEqual(model.roots.count, 1); expectNil(model.parent)
        await model.open(reference(home))
        expectEqual(model.parent?.reference, reference(home)); expectEqual(model.currentStatus, .ready)
        expectEqual(model.execution?.providerSessionID, "native-id")
        await model.open(reference(home, task: "child"))
        expectEqual(model.currentStatus, .needsInput); expectEqual(model.parentStatus, .ready)
        expectEqual(model.parent?.reference, reference(home)); expectFalse(model.inputReady)
        expectFalse(try unwrap(model.attention).nativeApprovalDecisionSupported)
        await wire.configure(user: "another-owner"); await model.refresh()
        expectEqual(model.currentStatus, .offline); expectNil(model.attention)
        await wire.configure(user: home.expectedUserID); await model.refresh()
        expectEqual(model.currentStatus, .needsInput)
        await model.openParent(); model.draft = "Read the linked child evidence"
        await wire.configure(reply: .object(["receipt": .object(["status": .string("unconfirmed")])]))
        await model.sendOrCheck()
        let pending = try unwrap(model.pending)
        expectEqual(pending.delivery, .unconfirmed); expectEqual(pending.execution.providerSessionID, "native-id")
        let sent = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        await model.refresh()
        let afterRefresh = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(sent.count, afterRefresh.count)
        expectEqual(model.pending?.id, pending.id)
        await wire.configure(reply: .object(["receipt": .object(["status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)])]))
        await model.sendOrCheck()
        let checked = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(checked.count, 2); expectEqual(checked[0]["arguments"], checked[1]["arguments"])
        expectNil(model.pending)
        await wire.configure(user: "another-owner")
        await model.refresh()
        expectFalse(model.connected); expectEqual(model.parentStatus, .offline)
        expectEqual(model.parent?.reference, reference(home))
        let restarted = WingthingModel()
        try await restarted.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: FixtureWire())
        expectEqual(restarted.parent?.reference, reference(home)); expectFalse(restarted.connected)
        expectEqual(restarted.currentStatus, .offline)
    }

    @Test func testTaskTreeKeepsSameIDsOnDifferentWingsAndMetadataDoesNotInventHomeIdentity() throws {
        let rowsJSON: JSONValue = .array([
            .object(["conversation": conversationJSON("root", wing: "mac")]),
            .object(["conversation": conversationJSON("child", parent: "root", wing: "mac")]),
            .object(["conversation": conversationJSON("root", wing: "linux")]),
            .object(["conversation": conversationJSON("child", parent: "root", wing: "linux")]),
        ])
        let tasks = try JSONDecoder().decode([ConversationTask].self, from: JSONEncoder().encode(rowsJSON))
        let rows = orderedTaskTree(tasks)
        expectEqual(rows.map { $0.task.conversation.wingID }, ["mac", "mac", "linux", "linux"])
        expectEqual(rows.map(\.depth), [0, 1, 0, 1]); expectEqual(Set(rows.map(\.id)).count, 4)
        let context: JSONValue = .object(["version": .string("wingthing.personal-coordinator.v1"), "dot_id": .string("root"), "task_id": .string("child"), "parent_task_id": .string("root"), "executor_id": .string("mac"), "executor_identity_state": .string("known"), "home_roost_id": .null, "home_roost_identity_state": .string("not_persisted"), "owner_epoch": .null, "owner_epoch_state": .string("unsupported"), "cross_host_adoption_supported": .bool(false)])
        let metadata = try JSONDecoder().decode(CoordinatorContext.self, from: JSONEncoder().encode(context))
        expectEqual(metadata.dotID, "root"); expectNil(metadata.homeRoostID); expectNil(metadata.ownerEpoch)
        expectFalse(metadata.hasPublishedHomeAndEpoch); expectFalse(metadata.crossHostAdoptionSupported)
    }
}
