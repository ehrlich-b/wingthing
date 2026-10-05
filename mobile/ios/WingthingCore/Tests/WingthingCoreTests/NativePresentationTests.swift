import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

@Suite @MainActor struct NativePresentationTests {
    @Test func longMessageReadingBlocksKeepEveryNativeCharacterAndGrapheme() {
        let original = String(repeating: "A cafe\u{301} with 👩‍👩‍👧‍👦 and 👍🏽.\r\n\nשלום世界 a_long_word_without_spaces_abcdefghijklmnopqrstuvwxyz\n", count: 8)
        let blocks = NativeMessageBlock.readingBlocks(original, maximumCharacters: 32)
        expectEqual(blocks.map(\.content).joined(), original)
        expectEqual(Array(blocks.map(\.content).joined().utf8), Array(original.utf8))
        expectTrue(blocks.allSatisfy { !$0.content.isEmpty && $0.content.count <= 32 })
        expectEqual(blocks.map(\.id), Array(blocks.indices))
        expectTrue(blocks.contains { $0.content.contains("👩‍👩‍👧‍👦") })
        expectEqual(NativeMessageBlock.readingBlocks("", maximumCharacters: 32), [])
    }

    @Test func generatedLabelIsReadableWithoutChangingLogicalOrNativeIdentity() throws {
        let fixture = conversationJSON("root")
        guard case .object(var raw) = fixture else {
            Issue.record("Conversation fixture must be an object")
            return
        }
        raw["title"] = .string("web-coordinator-ab12cd34")
        let node = try JSONDecoder().decode(Conversation.self, from: JSONEncoder().encode(JSONValue.object(raw)))
        expectEqual(node.displayTitle, "Conversation")
        expectEqual(node.title, "web-coordinator-ab12cd34"); expectEqual(node.conversationID, "root")
        raw["title"] = .string("My release plan")
        let custom = try JSONDecoder().decode(Conversation.self, from: JSONEncoder().encode(JSONValue.object(raw)))
        expectEqual(custom.displayTitle, custom.title)
    }

    @Test func toolAndProviderRecordsRemainInspectableWithoutInterruptingNativeMessages() throws {
        let json: JSONValue = .array([
            .object(["sequence": .integer(1), "type": .string("message"), "role": .string("user"), "text": .string("Question")]),
            .object(["sequence": .integer(2), "type": .string("provider_event"), "raw": .object(["type": .string("queue-operation")])]),
            .object(["sequence": .integer(3), "type": .string("provider_event"), "raw": .object(["type": .string("assistant"), "message": .object(["role": .string("assistant"), "content": .array([
                .object(["type": .string("tool_use"), "name": .string("read"), "input": .object(["path": .string("synthetic")])]),
                .object(["type": .string("text"), "text": .string("Answer")])])])])])])
        let events = try JSONDecoder().decode([SessionEvent].self, from: JSONEncoder().encode(json))
        let original = events.flatMap(TranscriptItem.items), shown = NativeTranscriptPresentation(original)
        expectEqual(shown.messages.map(\.content), ["Question", "Answer"])
        expectEqual(shown.messages.map(\.id), ["1:0", "3:1"])
        expectEqual(shown.activity.map(\.id), ["2:0", "3:0"])
        expectEqual(Set((shown.messages + shown.activity).map(\.id)), Set(original.map(\.id)))
    }

    @Test func taskRefreshShowsNewChildEvidenceWithoutRetargetingTheSelectedDraft() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-tree-refresh-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), wire = FixtureWire(), model = WingthingModel()
        let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
        let view = try lifecycleJSON(lifecycle("idle"))
        await wire.route(["conversation_list:": .object(["conversations": .array([root])]),
                          "conversation_read:root": .object(["conversation": root, "tasks": .array([.object(["conversation": root, "lifecycle": view])])]),
                          "session_read:same-session": .object(["lifecycle": view])])
        try await model.configure(profile: home, existingBearer: "synthetic-existing-token", cacheFile: directory.appendingPathComponent("state.json"), wire: wire)
        await model.refresh(); await model.open(reference(home)); model.draft = "Preserve this exact text"
        let selected = model.selected, execution = model.execution
        await wire.route(["conversation_read:root": .object(["conversation": root, "tasks": .array([
            .object(["conversation": root, "lifecycle": view]), .object(["conversation": child])])])])
        await model.pollTaskTree(now: Date().addingTimeInterval(6))
        expectEqual(model.relatedTasks.map { $0.conversation.conversationID }, ["child"])
        expectEqual(model.selected, selected); expectEqual(model.execution, execution); expectEqual(model.draft, "Preserve this exact text")
        let requests = await wire.requests
        expectTrue(requests.allSatisfy { ["conversation_list", "conversation_read", "session_read"].contains($0["operation"]?.string ?? "") })
    }
}
