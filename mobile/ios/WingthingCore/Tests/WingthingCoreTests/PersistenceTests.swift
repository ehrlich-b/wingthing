import Foundation
import Testing
@testable import WingthingCore

@Suite @MainActor struct PersistenceTests {
    @Test func testParentAndExactPendingInputSurviveRestartWithoutSharingSiblingWing() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-ios-test-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("state.json"), home = try profile()
        let store = try LocalConversationStore(profile: home, file: file)
        let root = ParentSelection(reference: reference(home), title: "Personal parent")
        try await store.selectParent(root)
        let mac = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let linux = ExecutionReference(conversation: reference(home, wing: "linux"), sessionID: "same-session", providerSessionID: "native-id")
        let pending = try PendingInput(execution: mac, input: "Review the exact child")
        try await store.savePending(pending)
        let restarted = try LocalConversationStore(profile: home, file: file)
        let restoredRoot = await restarted.parent(), restoredPending = await restarted.pending(for: mac), siblingPending = await restarted.pending(for: linux)
        expectEqual(restoredRoot, root); expectEqual(restoredPending, pending); expectNil(siblingPending)
        do { try await restarted.savePending(PendingInput(id: pending.id, execution: linux, input: pending.input)); fail("Pending request retargeted") } catch { expectEqual(error as? ClientError, .staleReference) }
        do { try await restarted.savePending(PendingInput(execution: mac, input: "Another input")); fail("Uncertain delivery duplicated") } catch {}
        expectThrows(try LocalConversationStore(profile: profile(home.id, user: "other-user"), file: file))
        expectThrows(try LocalConversationStore(profile: profile(), file: file))
    }

    @Test func testLocalSavedIsNotRoostAcceptanceAndOnlyExplicitNotSentProofAllowsNewInput() async throws {
        let home = try profile(), target = ExecutionReference(conversation: reference(home), sessionID: "same-session")
        var pending = try PendingInput(execution: target, input: "hello")
        expectEqual(pending.delivery, .savedLocally)
        pending.applyReceipt(.object(["status": .string("not_sent"), "bytes_written": .integer(0)]))
        expectEqual(pending.delivery, .unconfirmed)
        pending.applyReceipt(.object(["status": .string("not_sent"), "definitely_not_sent": .bool(true), "reason": .string("Provider busy before send")]))
        expectEqual(pending.delivery, .definitelyNotSent)
        expectEqual(pending.notice, "Provider busy before send")
        pending.applyReceipt(.object(["status": .string("native_receipt_observed")]))
        expectEqual(pending.delivery, .unconfirmed)
    }

    @Test func testCachedTranscriptCannotClaimCurrentWorkingAfterReload() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-ios-test-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("state.json"), home = try profile(), root = reference(home)
        let store = try LocalConversationStore(profile: home, file: file)
        var transcript = TranscriptState()
        try transcript.apply(lifecycle(), target: ExecutionReference(conversation: root, sessionID: "same-session", providerSessionID: "native-id"))
        try await store.cache(CachedConversation(reference: root, tasks: [], transcript: transcript))
        let restarted = try LocalConversationStore(profile: home, file: file)
        let restored = try await restarted.cached(root)
        expectEqual(restored?.transcript.status(connected: true), .unknown)
        expectEqual(restored?.transcript.status(connected: false), .offline)
    }
}
