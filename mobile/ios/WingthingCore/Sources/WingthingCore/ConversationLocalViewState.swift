import Foundation

public struct MessageReadingFrame: Equatable, Sendable {
    public let id: String
    public let minY: Double
    public let height: Double
    public init(id: String, minY: Double, height: Double) { self.id = id; self.minY = minY; self.height = height }
}

// Coordinates are points within a stable native message, never a row index.
// The store binds this local presentation state to an exact execution.
public struct ConversationReadingPosition: Codable, Equatable, Sendable {
    public let messageID: String
    public let offsetWithinMessage: Double
    public let followingLatest: Bool
    public init(messageID: String, offsetWithinMessage: Double, followingLatest: Bool) {
        self.messageID = messageID; self.offsetWithinMessage = offsetWithinMessage; self.followingLatest = followingLatest
    }
    public static func capture(frames: [MessageReadingFrame], offset: Double, viewportHeight: Double, contentHeight: Double) -> Self? {
        let valid = frames.filter { !$0.id.isEmpty && $0.minY.isFinite && $0.height.isFinite && $0.height > 0 }.sorted { $0.minY < $1.minY }
        guard offset.isFinite, viewportHeight.isFinite, contentHeight.isFinite, viewportHeight > 0,
              let frame = valid.last(where: { $0.minY <= max(0, offset) }) ?? valid.first else { return nil }
        return Self(messageID: frame.id, offsetWithinMessage: offset - frame.minY,
                    followingLatest: max(0, contentHeight - viewportHeight - offset) <= 80)
    }
    public func restoredOffset(frames: [MessageReadingFrame], viewportHeight: Double, contentHeight: Double) -> Double? {
        guard offsetWithinMessage.isFinite, abs(offsetWithinMessage) <= Double(1 << 24), viewportHeight.isFinite, contentHeight.isFinite,
              viewportHeight > 0 else { return nil }
        let end = max(0, contentHeight - viewportHeight)
        if followingLatest { return end }
        guard let frame = frames.first(where: { $0.id == messageID && $0.minY.isFinite && $0.height.isFinite && $0.height > 0 }) else { return nil }
        return max(0, min(end, frame.minY + offsetWithinMessage))
    }
}

private struct LocalExecutionView: Codable {
    let execution: ExecutionReference
    var draft = ""
    var reading: ConversationReadingPosition?
}
private struct LocalViewState: Codable {
    let home: HomeProfile
    var executions: [LocalExecutionView] = []
}

// Small synchronous atomic writes make the last edit durable before the UI
// accepts another navigation or process termination. No queued draft write can
// later overwrite a receipt. The lock also serializes reading-state writes.
public final class LocalConversationViewStore: @unchecked Sendable {
    private let profile: HomeProfile
    private let file: URL
    private let lock = NSLock()
    private var state: LocalViewState
    public init(profile: HomeProfile, file: URL) throws {
        self.profile = profile; self.file = file
        if FileManager.default.fileExists(atPath: file.path) {
            let bytes = try Data(contentsOf: file)
            guard bytes.count <= 8 << 20 else { throw ClientError.storage("Local drafts exceed their bounded size.") }
            let saved = try JSONDecoder().decode(LocalViewState.self, from: bytes)
            guard saved.home == profile else { throw ClientError.staleReference }
            state = saved
        } else { state = LocalViewState(home: profile) }
    }
    public func draft(for execution: ExecutionReference) throws -> String? {
        try execution.conversation.validate(profile)
        return lock.withLock { state.executions.first { $0.execution == execution }?.draft }
    }
    public func reading(for execution: ExecutionReference) throws -> ConversationReadingPosition? {
        try execution.conversation.validate(profile)
        return lock.withLock { state.executions.first { $0.execution == execution }?.reading }
    }
    public func saveDraft(_ text: String, for execution: ExecutionReference) throws {
        guard text.utf8.count <= 1 << 20 else { throw ClientError.storage("This draft is too large to save on this phone.") }
        try update(execution) { $0.draft = text }
    }
    public func saveReading(_ position: ConversationReadingPosition, for execution: ExecutionReference) throws {
        guard !position.messageID.isEmpty, position.messageID.utf8.count <= 2048,
              position.offsetWithinMessage.isFinite, abs(position.offsetWithinMessage) <= Double(1 << 24) else { throw ClientError.storage("Invalid local reading position.") }
        try update(execution) { $0.reading = position }
    }
    private func update(_ execution: ExecutionReference, change: (inout LocalExecutionView) -> Void) throws {
        try execution.conversation.validate(profile)
        try lock.withLock {
            var next = state
            let index: Int
            if let saved = next.executions.firstIndex(where: { $0.execution == execution }) { index = saved }
            else { index = next.executions.count; next.executions.append(LocalExecutionView(execution: execution)) }
            change(&next.executions[index])
            next.executions.removeAll { $0.draft.isEmpty && $0.reading == nil }
            guard next.executions.count <= 128 else { throw ClientError.storage("Too many local drafts and reading positions are saved.") }
            let bytes = try JSONEncoder().encode(next)
            guard bytes.count <= 8 << 20 else { throw ClientError.storage("Local drafts exceed their bounded size.") }
            try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
            try bytes.write(to: file, options: .atomic)
            state = next
        }
    }
}
