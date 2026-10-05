import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

@Suite @MainActor struct DraftNavigationTests {
    private func route(_ wire: FixtureWire, childProvider: String = "child-native") async throws {
        let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
        let rootState = try lifecycleJSON(lifecycle("idle"))
        let childState: JSONValue = .object(["session_id": .string("child-session"), "agent": .string("claude"),
            "provider_session_id": .string(childProvider), "state": .string("idle"), "state_source": .string("claude_hook"),
            "ready": .bool(true), "process_alive": .bool(true), "cursor": .integer(1), "events": .array([])])
        let tasks: JSONValue = .array([.object(["conversation": root, "lifecycle": rootState]),
                                     .object(["conversation": child, "lifecycle": childState])])
        await wire.route([
            "conversation_list:": .object(["conversations": .array([root, child])]),
            "conversation_read:root": .object(["conversation": root, "tasks": tasks]),
            "conversation_read:child": .object(["conversation": child, "tasks": tasks]),
            "session_read:same-session": .object(["lifecycle": rootState]),
            "session_read:child-session": .object(["lifecycle": childState]),
        ])
    }

    @Test func inspectingAChildRestoresEachExecutionsExactUnsentDraft() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), model = WingthingModel()
        try await route(wire)
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh(); await model.open(reference(home))
        model.draft = "  Compare this child with the parent plan\n"
        await model.open(reference(home, task: "child"))
        expectEqual(model.draft, "")
        model.draft = "Child's unsent reply"
        await model.openParent()
        expectEqual(model.draft, "  Compare this child with the parent plan\n")
        await model.refresh()
        expectEqual(model.draft, "  Compare this child with the parent plan\n")
        await model.open(reference(home, task: "child"))
        expectEqual(model.draft, "Child's unsent reply")
        try await route(wire, childProvider: "replacement-child-native")
        await model.refresh()
        expectEqual(model.execution?.providerSessionID, "replacement-child-native")
        expectEqual(model.draft, "")
        let requests = await wire.requests
        expectTrue(requests.allSatisfy { $0["operation"] != .string("session_prompt") })
    }

    @Test func cachedFallbackNeverShowsAnotherExecutionsDraft() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-cache-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), model = WingthingModel()
        try await route(wire)
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh(); await model.open(reference(home, task: "child")); await model.openParent()
        model.draft = "Parent's private unsent thought"
        await wire.route(["conversation_read:child": .object(["error": .string("synthetic-read-unavailable")])])
        await model.open(reference(home, task: "child"))
        expectEqual(model.selected, reference(home, task: "child")); expectNil(model.execution)
        expectEqual(model.draft, "")
        try await route(wire); await model.refresh(); await model.openParent()
        expectEqual(model.draft, "Parent's private unsent thought")
    }

    @Test func PendingReceiptOwnsSubmittedTextAndNeverResurrectsAnUnsentDraft() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-receipt-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), model = WingthingModel()
        try await route(wire)
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh(); await model.open(reference(home))
        model.draft = "Summarize the child evidence"
        await wire.configure(reply: .object(["receipt": .object(["status": .string("unconfirmed")])]))
        await model.sendOrCheck()
        let inputID = try unwrap(model.pending).id
        await model.open(reference(home, task: "child")); await model.openParent()
        expectEqual(model.pending?.id, inputID)
        expectEqual(model.draft, "Summarize the child evidence")
        await wire.configure(reply: .object(["receipt": .object(["status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)])]))
        await model.sendOrCheck()
        await model.open(reference(home, task: "child")); await model.openParent()
        expectNil(model.pending); expectEqual(model.draft, "")
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(prompts.count, 2); expectEqual(prompts[0]["arguments"], prompts[1]["arguments"])
    }
    @Test func relaunchRestoresParentAndChildDraftsWithoutAnySubmission() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-relaunch-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), file = directory.appendingPathComponent("state.json")
        try await route(wire)
        let first = WingthingModel()
        try await first.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await first.refresh(); await first.open(reference(home))
        first.draft = "  Parent's unfinished thought\n"
        let parentExecution = try unwrap(first.execution)
        first.rememberReadingPosition(.init(messageID: "1:0", offsetWithinMessage: 31.25, followingLatest: false), for: parentExecution, persist: false)
        await first.open(reference(home, task: "child"))
        first.draft = "Child's separate draft"
        let restored = WingthingModel()
        try await restored.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await restored.refresh(); await restored.open(reference(home))
        expectEqual(restored.draft, "  Parent's unfinished thought\n")
        expectEqual(restored.readingPosition?.offsetWithinMessage, 31.25)
        await restored.open(reference(home, task: "child"))
        expectEqual(restored.draft, "Child's separate draft")
        try await route(wire, childProvider: "replacement-child-native")
        await restored.refresh()
        expectEqual(restored.draft, ""); expectNil(restored.readingPosition)
        let requests = await wire.requests
        expectTrue(requests.allSatisfy { !["session_prompt", "agent_start", "conversation_stop"].contains($0["operation"]?.string ?? "") })
    }

    @Test func editingOfflineIsDurableButStorageFailureKeepsTextAndSendsNothing() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-storage-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), file = directory.appendingPathComponent("state.json")
        try await route(wire)
        let model = WingthingModel()
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await model.refresh(); await model.open(reference(home)); await model.disconnect()
        model.draft = "Written while disconnected"
        let restarted = WingthingModel()
        try await restarted.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await restarted.refresh(); await restarted.open(reference(home))
        expectEqual(restarted.draft, "Written while disconnected")
        let local = file.appendingPathExtension("view-state")
        try FileManager.default.removeItem(at: local)
        try FileManager.default.createDirectory(at: local, withIntermediateDirectories: true)
        restarted.draft = "Keep this text if disk save fails"
        expectEqual(restarted.draft, "Keep this text if disk save fails")
        expectTrue(restarted.localSaveError != nil)
        let requests = await wire.requests
        expectTrue(requests.allSatisfy { $0["operation"] != .string("session_prompt") })
    }

    @Test func confirmedInputDoesNotResurrectTheOldDraftAfterRelaunch() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-draft-confirmed-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), file = directory.appendingPathComponent("state.json")
        try await route(wire)
        let model = WingthingModel()
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await model.refresh(); await model.open(reference(home)); model.draft = "Send this once"
        await wire.configure(reply: .object(["receipt": .object(["status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)])]))
        await model.sendOrCheck()
        let restored = WingthingModel()
        try await restored.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: file, wire: wire)
        await restored.refresh(); await restored.open(reference(home))
        expectEqual(restored.draft, ""); expectNil(restored.pending)
        let prompts = await wire.requests.filter { $0["operation"] == .string("session_prompt") }
        expectEqual(prompts.count, 1)
    }

}
