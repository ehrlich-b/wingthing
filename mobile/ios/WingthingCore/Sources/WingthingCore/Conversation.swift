import Foundation

// Additive observation metadata. It is not an authority grant or a migration
// protocol. Never derive HomeRoostID from ExecutorID or the configured URL.
public struct CoordinatorContext: Codable, Equatable, Sendable {
    public let version: String
    public let dotID: String
    public let taskID: String
    public let parentTaskID: String?
    public let executorID: String
    public let executorIdentityState: String
    public let homeRoostID: String?
    public let homeRoostIdentityState: String
    public let ownerEpoch: JSONValue?
    public let ownerEpochState: String
    public let crossHostAdoptionSupported: Bool
    enum CodingKeys: String, CodingKey {
        case version, dotID = "dot_id", taskID = "task_id", parentTaskID = "parent_task_id"
        case executorID = "executor_id", executorIdentityState = "executor_identity_state"
        case homeRoostID = "home_roost_id", homeRoostIdentityState = "home_roost_identity_state"
        case ownerEpoch = "owner_epoch", ownerEpochState = "owner_epoch_state", crossHostAdoptionSupported = "cross_host_adoption_supported"
    }
    public var hasPublishedHomeAndEpoch: Bool { homeRoostID != nil && ownerEpoch != nil && ownerEpoch != .null && ownerEpochState != "unsupported" }
}

public struct Conversation: Codable, Equatable, Sendable, Identifiable {
    public let conversationID: String
    public let rootConversationID: String
    public let parentConversationID: String?
    public let title: String
    public let agent: String
    public let cwd: String
    public let wingID: String
    public let sessionID: String
    public let launchState: String
    public let launchError: String?
    public let coordinatorContext: CoordinatorContext?
    public var id: String { (try? JSONEncoder().encode([wingID, conversationID]).base64EncodedString()) ?? "" }
    public var isRoot: Bool { parentConversationID?.isEmpty != false }
    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id", rootConversationID = "root_conversation_id", parentConversationID = "parent_conversation_id"
        case title, agent, cwd, wingID = "wing_id", sessionID = "session_id", launchState = "launch_state", launchError = "launch_error", coordinatorContext = "coordinator_context"
    }
}

public struct ConversationTask: Codable, Equatable, Sendable, Identifiable {
    public let conversation: Conversation
    public let lifecycle: SessionLifecycle?
    public let lifecycleError: String?
    public let historyUnavailable: Bool?
    public let coordinatorContext: CoordinatorContext?
    public var id: String { conversation.id }
    enum CodingKeys: String, CodingKey { case conversation, lifecycle, lifecycleError = "lifecycle_error", historyUnavailable = "history_unavailable", coordinatorContext = "coordinator_context" }
}

public struct ConversationRead: Codable, Sendable {
    public let conversation: Conversation
    public let tasks: [ConversationTask]
    public let coordinatorContext: CoordinatorContext?
    enum CodingKeys: String, CodingKey { case conversation, tasks, coordinatorContext = "coordinator_context" }
}

public struct SessionEvent: Codable, Equatable, Sendable, Identifiable {
    public let sequence: Int64
    public let type: String
    public let source: String?
    public let role: String?
    public let text: String?
    public let raw: JSONValue?
    public let truncated: Bool?
    public var id: Int64 { sequence }
}

public struct SessionLifecycle: Codable, Equatable, Sendable {
    public let sessionID: String
    public let agent: String
    public let providerSessionID: String?
    public let state: String
    public let stateSource: String
    public let stateCursor: Int64?
    public let reason: String?
    public let ready: Bool
    public let processAlive: Bool
    public let cursor: Int64
    public let headCursor: Int64?
    public let hasMore: Bool?
    public let events: [SessionEvent]?
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", agent, providerSessionID = "provider_session_id", state, stateSource = "state_source"
        case stateCursor = "state_cursor", reason, ready, processAlive = "process_alive", cursor, headCursor = "head_cursor", hasMore = "has_more", events
    }
    public var inputReady: Bool {
        agent == "claude" && stateSource == "claude_hook" && providerSessionID?.isEmpty == false && processAlive && ready && ["idle", "completed"].contains(state)
    }
}

public enum ObservedStatus: String, Sendable {
    case unknown, offline, unavailable, archived, starting, working, ready, turnCompleted, needsInput, failed
    public var label: String {
        switch self {
        case .turnCompleted: "Turn completed"
        case .needsInput: "Needs your input"
        default: rawValue.prefix(1).uppercased() + rawValue.dropFirst()
        }
    }
}

public struct TranscriptState: Codable, Equatable, Sendable {
    public private(set) var cursor: Int64 = 0
    public private(set) var events: [SessionEvent] = []
    public private(set) var lifecycle: SessionLifecycle?
    // Not Codable: a restored snapshot never claims live state.
    private var observedAt: Date?
    enum CodingKeys: String, CodingKey { case cursor, events, lifecycle }
    public init() {}
    public mutating func apply(_ view: SessionLifecycle, target: ExecutionReference, now: Date = Date()) throws {
        guard view.sessionID == target.sessionID,
              target.providerSessionID == nil || target.providerSessionID == view.providerSessionID else { throw ClientError.staleReference }
        guard view.cursor >= cursor else { return } // A delayed earlier page cannot replace newer lifecycle.
        var known = Set(events.map(\.sequence))
        for event in view.events ?? [] where event.sequence > 0 && !known.contains(event.sequence) {
            events.append(event); known.insert(event.sequence)
        }
        events.sort { $0.sequence < $1.sequence }
        cursor = view.cursor; lifecycle = view; observedAt = now
    }
    public mutating func markUnavailable() { observedAt = nil }
    public func status(connected: Bool, now: Date = Date()) -> ObservedStatus {
        guard connected else { return .offline }
        guard let observedAt, now.timeIntervalSince(observedAt) >= 0, now.timeIntervalSince(observedAt) < 60, let view = lifecycle else { return .unknown }
        if !view.processAlive { return view.state == "failed" ? .failed : .archived }
        guard ["claude_hook", "claude_transcript", "egg_process"].contains(view.stateSource) else { return .unknown }
        switch view.state {
        case "idle": return view.stateSource == "egg_process" ? .unknown : .ready
        case "completed": return view.stateSource == "egg_process" ? .unknown : .turnCompleted
        case "working": return .working
        case "needs_input": return .needsInput
        case "starting": return .starting
        case "failed": return .failed
        default: return .unknown
        }
    }
    public func canSend(connected: Bool, now: Date = Date()) -> Bool {
        [.ready, .turnCompleted].contains(status(connected: connected, now: now)) && lifecycle?.inputReady == true
    }
}

public struct TranscriptItem: Identifiable, Equatable, Sendable {
    public let id: String
    public let kind: String
    public let title: String
    public let content: String
    public let truncated: Bool
    public static func items(from event: SessionEvent) -> [TranscriptItem] {
        let suffix = event.truncated == true
        guard let raw = event.raw else {
            guard event.type == "message", let role = event.role else { return [] }
            return [TranscriptItem(id: "\(event.sequence):0", kind: role, title: role.capitalized, content: event.text ?? "", truncated: suffix)]
        }
        let type = raw["type"]?.string ?? event.type
        let role = raw["message"]?["role"]?.string ?? type
        if let blocks = raw["message"]?["content"]?.array {
            return blocks.enumerated().map { index, block in
                let kind = block["type"]?.string ?? role
                let title = kind == "tool_use" ? "Tool: \(block["name"]?.string ?? "unknown")" : kind == "tool_result" ? "Tool result" : role.capitalized
                let content = block["text"]?.string ?? block["content"]?.string ?? block["input"]?.prettyText ?? block["content"]?.prettyText ?? block.prettyText
                return TranscriptItem(id: "\(event.sequence):\(index)", kind: kind == "text" ? role : kind, title: title, content: content, truncated: suffix)
            }
        }
        if let text = raw["message"]?["content"]?.string { return [TranscriptItem(id: "\(event.sequence):0", kind: role, title: role.capitalized, content: text, truncated: suffix)] }
        return [TranscriptItem(id: "\(event.sequence):0", kind: "provider_event", title: "Provider event: \(type)", content: raw.prettyText, truncated: suffix)]
    }
}

public struct HumanAttention: Equatable, Sendable {
    public let execution: ExecutionReference
    public let stateCursor: Int64?
    public let reason: String
    public let nativeApprovalDecisionSupported = false
    public init?(execution: ExecutionReference, lifecycle: SessionLifecycle) {
        guard lifecycle.sessionID == execution.sessionID,
              execution.providerSessionID == nil || execution.providerSessionID == lifecycle.providerSessionID,
              ["claude_hook", "claude_transcript"].contains(lifecycle.stateSource),
              lifecycle.processAlive, lifecycle.state == "needs_input" else { return nil }
        self.execution = execution; stateCursor = lifecycle.stateCursor
        reason = lifecycle.reason ?? "The provider needs a human response. The current API does not expose an exact approval decision reference."
    }
}
