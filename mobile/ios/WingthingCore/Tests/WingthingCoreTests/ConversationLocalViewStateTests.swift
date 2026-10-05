import Foundation
import Testing
@testable import WingthingCore

@Suite struct ConversationLocalViewStateTests {
    @Test func messageAnchorSurvivesPrecedingInsertionAndViewportChange() {
        let before = [MessageReadingFrame(id: "first", minY: 20, height: 100), MessageReadingFrame(id: "reading", minY: 144, height: 900)]
        let position = ConversationReadingPosition.capture(frames: before, offset: 331.25, viewportHeight: 400, contentHeight: 1800)!
        expectEqual(position.messageID, "reading"); expectEqual(position.offsetWithinMessage, 187.25); expectTrue(!position.followingLatest)
        let after = [MessageReadingFrame(id: "inserted", minY: 20, height: 300), MessageReadingFrame(id: "first", minY: 344, height: 100), MessageReadingFrame(id: "reading", minY: 468, height: 1200)]
        expectEqual(position.restoredOffset(frames: after, viewportHeight: 240, contentHeight: 2400), 655.25)
        expectNil(position.restoredOffset(frames: after.filter { $0.id != "reading" }, viewportHeight: 240, contentHeight: 2400))
    }
    @Test func spacingBeforeAndBetweenMessagesAlsoRestoresExactly() {
        let frames = [MessageReadingFrame(id: "first", minY: 20, height: 100), MessageReadingFrame(id: "next", minY: 164, height: 800)]
        let top = ConversationReadingPosition.capture(frames: frames, offset: 0, viewportHeight: 400, contentHeight: 1800)!
        expectEqual(top.offsetWithinMessage, -20)
        expectEqual(top.restoredOffset(frames: frames, viewportHeight: 400, contentHeight: 1800), 0)
        let gap = ConversationReadingPosition.capture(frames: frames, offset: 140.5, viewportHeight: 400, contentHeight: 1800)!
        expectEqual(gap.messageID, "first")
        expectEqual(gap.restoredOffset(frames: [MessageReadingFrame(id: "first", minY: 420, height: 100)], viewportHeight: 400, contentHeight: 1800), 540.5)
    }
    @Test func nearBottomFollowsButEarlierReadingDoesNot() {
        let frames = [MessageReadingFrame(id: "long", minY: 20, height: 1600)]
        let near = ConversationReadingPosition.capture(frames: frames, offset: 1220, viewportHeight: 400, contentHeight: 1700)!
        expectTrue(near.followingLatest)
        expectEqual(near.restoredOffset(frames: frames, viewportHeight: 400, contentHeight: 2400), 2000)
        let earlier = ConversationReadingPosition.capture(frames: frames, offset: 1219, viewportHeight: 400, contentHeight: 1700)!
        expectTrue(!earlier.followingLatest)
        expectEqual(earlier.restoredOffset(frames: frames, viewportHeight: 400, contentHeight: 2400), 1219)
        expectNil(ConversationReadingPosition.capture(frames: frames, offset: .nan, viewportHeight: 400, contentHeight: 1700))
    }
    @Test func restartPreservesExactDraftAndReadingWithoutCrossingProviderOrHome() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-local-view-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let home = try profile(), file = directory.appendingPathComponent("view.json")
        let execution = ExecutionReference(conversation: reference(home), sessionID: "session", providerSessionID: "provider")
        let store = try LocalConversationViewStore(profile: home, file: file)
        try store.saveDraft("  Unsent text\nexact spacing  ", for: execution)
        try store.saveReading(.init(messageID: "17:0", offsetWithinMessage: 187.25, followingLatest: false), for: execution)
        let restored = try LocalConversationViewStore(profile: home, file: file)
        expectEqual(try restored.draft(for: execution), "  Unsent text\nexact spacing  ")
        expectEqual(try restored.reading(for: execution)?.offsetWithinMessage, 187.25)
        let replacement = ExecutionReference(conversation: execution.conversation, sessionID: execution.sessionID, providerSessionID: "replacement")
        expectNil(try restored.draft(for: replacement)); expectNil(try restored.reading(for: replacement))
        let repinned = try HomeProfile(id: home.id, origin: URL(string: "https://different-home.invalid")!, transport: home.transport, expectedUserID: home.expectedUserID, homeWingID: home.homeWingID, homeWingPublicKey: home.homeWingPublicKey)
        expectThrows(try LocalConversationViewStore(profile: repinned, file: file))
        try restored.saveDraft("", for: execution)
        let cleared = try LocalConversationViewStore(profile: home, file: file)
        expectEqual(try cleared.draft(for: execution), ""); expectEqual(try cleared.reading(for: execution)?.messageID, "17:0")
    }
}
