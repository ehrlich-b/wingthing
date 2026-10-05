import Foundation

public enum InputDelivery: String, Codable, Sendable {
    case savedLocally, unconfirmed, nativeReceiptObserved, definitelyNotSent
    public var label: String {
        switch self {
        case .savedLocally: "Saved on this phone"
        case .unconfirmed: "Delivery unconfirmed"
        case .nativeReceiptObserved: "Native input receipt observed"
        case .definitelyNotSent: "Not sent"
        }
    }
}

// Future coordinator inbox states remain separate from current session_prompt
// receipt semantics. This draft neither sends to nor claims a durable inbox.
public enum RoostDeliveryStage: String, Codable, Sendable {
    case received, accepted, processing
}

public struct PendingInput: Codable, Equatable, Sendable, Identifiable {
    public let id: UUID // Also the immutable session_prompt request_id.
    public let execution: ExecutionReference
    public let input: String
    public let savedAt: Date
    public private(set) var delivery: InputDelivery
    public private(set) var notice: String?
    public init(id: UUID = UUID(), execution: ExecutionReference, input: String, savedAt: Date = Date()) throws {
        guard !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, input.utf8.count <= 1 << 20 else { throw ClientError.invalidConfiguration("Input is empty or too large.") }
        self.id = id; self.execution = execution; self.input = input; self.savedAt = savedAt; delivery = .savedLocally
    }
    public var arguments: [String: JSONValue] {
        ["session": .string(execution.sessionID), "request_id": .string(id.uuidString), "input": .string(input), "timeout_seconds": .integer(3)]
    }
    public mutating func markUnconfirmed() { delivery = .unconfirmed }
    public mutating func applyReceipt(_ receipt: JSONValue) {
        let status = receipt["status"]?.string
        if status == "native_receipt_observed", receipt["native_receipt_observed"] == .bool(true) {
            delivery = .nativeReceiptObserved; notice = nil
        } else if status == "not_sent", receipt["definitely_not_sent"] == .bool(true) {
            delivery = .definitelyNotSent; notice = receipt["reason"]?.string
        } else { delivery = .unconfirmed }
    }
}

public struct ParentSelection: Codable, Equatable, Sendable {
    public let reference: ConversationReference
    public let title: String
    public let dotID: String?
    public let homeRoostID: String?
    public let ownerEpoch: JSONValue?
    public init(reference: ConversationReference, title: String, context: CoordinatorContext? = nil) {
        self.reference = reference; self.title = title; dotID = context?.dotID
        homeRoostID = context?.homeRoostID; ownerEpoch = context?.ownerEpoch
    }
}

public struct CachedConversation: Codable, Equatable, Sendable {
    public let reference: ConversationReference
    public let tasks: [ConversationTask]
    public let transcript: TranscriptState
    public let savedAt: Date
    public init(reference: ConversationReference, tasks: [ConversationTask], transcript: TranscriptState, savedAt: Date = Date()) { self.reference = reference; self.tasks = tasks; self.transcript = transcript; self.savedAt = savedAt }
}

private struct LocalState: Codable {
    let profileID: UUID
    let userID: String
    var parent: ParentSelection?
    var pending: [PendingInput] = []
    var conversations: [CachedConversation] = []
    var stops: [PendingStop]? // Optional so caches written before Stop still load.
    var continuations: [PendingContinuation]? // Older caches remain readable.
    var launches: [PendingConversationLaunch]?
}

public actor LocalConversationStore {
    private let profile: HomeProfile
    private let file: URL
    private var state: LocalState
    public init(profile: HomeProfile, file: URL) throws {
        self.profile = profile; self.file = file
        if FileManager.default.fileExists(atPath: file.path) {
            let bytes = try Data(contentsOf: file)
            guard bytes.count <= 8 << 20 else { throw ClientError.storage("The local conversation cache is too large.") }
            let loaded = try JSONDecoder().decode(LocalState.self, from: bytes)
            guard loaded.profileID == profile.id, loaded.userID == profile.expectedUserID else { throw ClientError.staleReference }
            state = loaded
        } else { state = LocalState(profileID: profile.id, userID: profile.expectedUserID) }
    }
    private func write(_ next: LocalState) throws {
        let bytes = try JSONEncoder().encode(next)
        guard bytes.count <= 8 << 20 else { throw ClientError.storage("The local conversation cache exceeds its bounded size.") }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try bytes.write(to: file, options: .atomic)
        state = next // Saved means the file write succeeded, never just UI optimism.
    }
    public func parent() -> ParentSelection? { state.parent }
    public func selectParent(_ selected: ParentSelection) throws {
        try selected.reference.validate(profile)
        var next = state; next.parent = selected; try write(next)
    }
    public func pending(for execution: ExecutionReference) -> PendingInput? { state.pending.first { $0.execution == execution } }
    public func savePending(_ pending: PendingInput) throws {
        try pending.execution.conversation.validate(profile)
        if let existing = state.pending.first(where: { $0.id == pending.id }), existing.execution != pending.execution || existing.input != pending.input { throw ClientError.staleReference }
        if state.pending.contains(where: { $0.execution == pending.execution && $0.id != pending.id && ![.nativeReceiptObserved, .definitelyNotSent].contains($0.delivery) }) { throw ClientError.response("This execution already has an input with unconfirmed delivery. Check its exact receipt first.") }
        var next = state; next.pending.removeAll { $0.id == pending.id }; next.pending.append(pending); try write(next)
    }
    public func pendingStop(for execution: ExecutionReference) -> PendingStop? { (state.stops ?? []).last { $0.execution == execution } }
    public func unfinishedLaunch() throws -> PendingConversationLaunch? {
        let value = (state.launches ?? []).last { !$0.progress.final }
        if let value { try value.validate(profile) }
        return value
    }
    public func saveLaunch(_ value: PendingConversationLaunch) throws {
        try value.validate(profile)
        var records = state.launches ?? []
        if let existing = records.first(where: { $0.id == value.id }) {
            guard existing.sameIntent(value) else { throw ClientError.staleReference }
            if existing.progress.final { return }
        }
        guard !records.contains(where: { $0.id != value.id && !$0.progress.final }) else {
            throw ClientError.response("Check the saved unconfirmed conversation first.")
        }
        records.removeAll { $0.id == value.id }; records.append(value)
        while records.count > 32, let index = records.firstIndex(where: { $0.progress.final }) { records.remove(at: index) }
        guard records.count <= 32 else { throw ClientError.storage("Too many saved conversation launches.") }
        var next = state; next.launches = records; try write(next)
    }
    public func continuation(for reference: ConversationReference, unfinishedOnly: Bool = false) throws -> PendingContinuation? {
        try reference.validate(profile)
        return (state.continuations ?? []).last { $0.source.conversation == reference && (!unfinishedOnly || !$0.progress.final) }
    }
    public func saveContinuation(_ value: PendingContinuation) throws {
        try value.source.conversation.validate(profile)
        var records = state.continuations ?? []
        if let existing = records.first(where: { $0.id == value.id }) {
            guard existing.sameIntent(value) else { throw ClientError.staleReference }
            if existing.progress.final { return }
        }
        guard !records.contains(where: { $0.source.conversation == value.source.conversation && $0.id != value.id && !$0.progress.final }) else {
            throw ClientError.response("Check the existing unconfirmed follow-up first.")
        }
        records.removeAll { $0.id == value.id }; records.append(value)
        while records.count > 32, let index = records.firstIndex(where: { $0.progress.final }) { records.remove(at: index) }
        guard records.count <= 32 else { throw ClientError.storage("Too many unconfirmed follow-ups are saved.") }
        var next = state; next.continuations = records; try write(next)
    }
    // Saved before any dispatch. One unfinished intent per execution; its
    // identity never changes and its progress never moves backward.
    public func saveStop(_ stop: PendingStop) throws {
        try stop.execution.conversation.validate(profile)
        var stops = state.stops ?? []
        if let existing = stops.first(where: { $0.id == stop.id }) {
            guard existing.sameIntent(stop) else { throw ClientError.staleReference }
            if existing.progress.isFinal || existing.progress.rank > stop.progress.rank { return }
        }
        if stops.contains(where: { $0.execution == stop.execution && $0.id != stop.id && !$0.progress.isFinal }) {
            throw ClientError.response("This task already has a stop request waiting for confirmation. Check it first.")
        }
        stops.removeAll { $0.id == stop.id }; stops.append(stop)
        while stops.count > 32 {
            guard let index = stops.firstIndex(where: \.progress.isFinal) else { throw ClientError.storage("Too many unconfirmed stop requests are saved on this phone.") }
            stops.remove(at: index)
        }
        var next = state; next.stops = stops; try write(next)
    }
    // Local only: forgetting never cancels a stop that may have been delivered.
    public func forgetStop(_ id: UUID) throws {
        var next = state; next.stops = (state.stops ?? []).filter { $0.id != id }; try write(next)
    }
    public func cache(_ cached: CachedConversation) throws {
        try cached.reference.validate(profile)
        var next = state; next.conversations.removeAll { $0.reference == cached.reference }; next.conversations.append(cached); try write(next)
    }
    public func cached(_ reference: ConversationReference) throws -> CachedConversation? {
        try reference.validate(profile)
        return state.conversations.first { $0.reference == reference }
    }
}

extension HomeClient {
    // Invoke only after an explicit Send/Check receipt action. Reuse the exact
    // saved execution + provider binding + request ID; never reconnect/resend.
    public func submit(_ pending: PendingInput) async throws -> PendingInput {
        var updated = pending
        do {
            let result = try await control(pending.execution.conversation, operation: "session_prompt", arguments: pending.arguments)
            updated.applyReceipt(result["receipt"] ?? result["prompt"] ?? result)
        } catch { updated.markUnconfirmed(); throw error }
        return updated
    }
}
