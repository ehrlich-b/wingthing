import Foundation

// Positive Stop support is never inferred from a version, endpoint, model,
// process state, or error text. A home must attach this exact object beside
// `lifecycle` in an authenticated `session_read` result, bound to the one
// conversation execution and immutable provider session the read returned:
//
//   "conversation_stop": {"available": true, "protocol": "wingthing.exact_stop.v1",
//     "operation": "conversation_stop", "semantics": "terminate_execution",
//     "conversation_id": "…", "session_id": "…", "provider_session_id": "…"}
//
// Anything absent, extra-loose, or bound elsewhere means unsupported.
public struct StopCapability: Equatable, Sendable {
    public static let field = "conversation_stop"
    public static let protocolVersion = "wingthing.exact_stop.v1"
    public static let operation = "conversation_stop"
    public static let semantics = "terminate_execution"
    public let execution: ExecutionReference

    public init?(result: JSONValue, execution: ExecutionReference) {
        guard let provider = execution.providerSessionID, !provider.isEmpty, !execution.sessionID.isEmpty,
              case .object(let fields)? = result[Self.field],
              fields["available"] == .bool(true),
              fields["protocol"] == .string(Self.protocolVersion),
              fields["operation"] == .string(Self.operation),
              fields["semantics"] == .string(Self.semantics),
              fields["conversation_id"] == .string(execution.conversation.conversationID),
              fields["session_id"] == .string(execution.sessionID),
              fields["provider_session_id"] == .string(provider) else { return nil }
        self.execution = execution
    }

    public func covers(_ execution: ExecutionReference?) -> Bool { execution == self.execution }
}

// One authenticated read: lifecycle plus any Stop advertisement valid for it.
public struct ExecutionObservation: Sendable {
    public let lifecycle: SessionLifecycle
    public let stopCapability: StopCapability?
    public var continuation: ContinuationAvailability? = nil
}

public enum StopProgress: String, Codable, Sendable {
    // Saved before dispatch, so from then on the request may have been delivered.
    case unconfirmed, requested, notSent, notRunning, terminationObserved
    public var isFinal: Bool { self == .notSent || self == .notRunning || self == .terminationObserved }
    var rank: Int { self == .unconfirmed ? 0 : self == .requested ? 1 : 2 }
    public var label: String {
        switch self {
        case .unconfirmed: "Stop unconfirmed"
        case .requested: "Stop requested"
        case .notSent: "Stop not carried out"
        case .notRunning: "Task wasn't running"
        case .terminationObserved: "Task ended"
        }
    }
}

// A validated SessionStopResult. Only fields the phone relies on are kept.
public struct StopReceipt: Codable, Equatable, Sendable {
    public let status: String
    public let definitelyNotSent: Bool
    public let signalAttempted: Bool
    public let signalAcknowledged: Bool
    public let terminationObserved: Bool
    public let terminationCursor: Int64?
    public let reservedCursor: Int64
    public let processAlive: Bool
    public let retried: Bool
    public let causality: String

    private static let statuses: Set<String> = ["unconfirmed", "requested", "not_sent", "not_running", "termination_observed"]

    // Every identity and semantic field must match the saved intent exactly and
    // the flags must be self-consistent; otherwise the outcome stays uncertain.
    static func validated(_ result: JSONValue, for stop: PendingStop) -> (StopReceipt, String?)? {
        let execution = stop.execution
        guard let provider = execution.providerSessionID,
              result["conversation_id"] == .string(execution.conversation.conversationID),
              result["session"] == .string(execution.sessionID),
              let r = result["receipt"],
              r["request_id"] == .string(stop.requestID),
              r["conversation_id"] == .string(execution.conversation.conversationID),
              r["session_id"] == .string(execution.sessionID),
              r["provider_session_id"] == .string(provider),
              r["semantics"] == .string(StopCapability.semantics),
              let status = r["status"]?.string, statuses.contains(status),
              let notSent = bool(r["definitely_not_sent"]), let attempted = bool(r["signal_attempted"]),
              let acknowledged = bool(r["signal_acknowledged"]), let observed = bool(r["termination_observed"]),
              let alive = bool(r["process_alive"]), let retried = bool(r["retried"]),
              let reserved = integer(r["reserved_cursor"]), reserved >= stop.expectedStateCursor,
              let causality = r["causality"]?.string, causality.hasPrefix("unverified") else { return nil }
        guard notSent == (status == "not_sent" || status == "not_running"),
              !(notSent && (attempted || acknowledged || observed)),
              !(acknowledged && !attempted),
              observed == (status == "termination_observed"),
              status != "requested" || acknowledged else { return nil }
        let source = r["termination_source"]?.string, cursor = integer(r["termination_cursor"])
        if observed {
            // Only a native egg process exit (the egg records completed or failed)
            // after this stop's reservation. process_alive is a typed diagnostic
            // of the supervising egg, which records the provider's exit before it
            // shuts down, so true here is valid and never contradicts the exit.
            guard source == "egg_process", let cursor, cursor > reserved,
                  let state = r["termination_state"]?.string, state == "completed" || state == "failed" else { return nil }
        } else {
            guard source == nil, cursor == nil, r["termination_state"] == nil else { return nil }
        }
        let receipt = StopReceipt(status: status, definitelyNotSent: notSent, signalAttempted: attempted, signalAcknowledged: acknowledged,
                                  terminationObserved: observed, terminationCursor: cursor, reservedCursor: reserved, processAlive: alive,
                                  retried: retried, causality: causality)
        return (receipt, r["reason"]?.string)
    }

    var progress: StopProgress {
        switch status {
        case "requested": .requested
        case "not_sent": .notSent
        case "not_running": .notRunning
        case "termination_observed": .terminationObserved
        default: .unconfirmed
        }
    }

    private static func bool(_ value: JSONValue?) -> Bool? { if case .bool(let item)? = value { return item }; return nil }
    private static func integer(_ value: JSONValue?) -> Int64? { if case .integer(let item)? = value { return item }; return nil }
}

// One immutable whole-execution Stop intent. Its id is the request_id; the
// target, cursor and wait bound never change, so an explicit Check resends the
// identical request and the home never acts on it twice.
public struct PendingStop: Codable, Equatable, Sendable, Identifiable {
    public static let timeoutSeconds: Int64 = 5
    public let id: UUID
    public let execution: ExecutionReference
    public let expectedStateCursor: Int64
    public let timeoutSeconds: Int64
    public let savedAt: Date
    public private(set) var progress: StopProgress
    public private(set) var receipt: StopReceipt?
    // Bounded plain text from the home or the connection, shown only as detail.
    public private(set) var detail: String?

    public init(id: UUID = UUID(), execution: ExecutionReference, expectedStateCursor: Int64, savedAt: Date = Date()) throws {
        guard execution.providerSessionID?.isEmpty == false, !execution.sessionID.isEmpty, expectedStateCursor >= 0 else { throw ClientError.staleReference }
        self.id = id; self.execution = execution; self.expectedStateCursor = expectedStateCursor
        timeoutSeconds = Self.timeoutSeconds; self.savedAt = savedAt; progress = .unconfirmed
    }

    public var requestID: String { id.uuidString }
    public var arguments: [String: JSONValue] {
        ["conversation_id": .string(execution.conversation.conversationID), "session": .string(execution.sessionID),
         "expected_provider_session_id": .string(execution.providerSessionID ?? ""), "request_id": .string(requestID),
         "expected_state_cursor": .integer(expectedStateCursor), "timeout_seconds": .integer(timeoutSeconds)]
    }

    public func sameIntent(_ other: PendingStop) -> Bool {
        id == other.id && execution == other.execution && expectedStateCursor == other.expectedStateCursor && timeoutSeconds == other.timeoutSeconds
    }

    // Progress only moves forward; a final outcome is never replaced.
    mutating func apply(response: JSONValue) {
        guard let validated = StopReceipt.validated(response, for: self) else {
            detail = "Your computer's reply didn't match this stop request, so its outcome is still unconfirmed."
            return
        }
        let (receipt, reason) = validated
        let next = receipt.progress
        if progress.isFinal, next != progress {
            detail = "Your computer sent a reply that conflicts with an earlier final result. The earlier result is kept."
            return
        }
        guard next.rank >= progress.rank else { return }
        progress = next; self.receipt = receipt; detail = reason.map(Self.bounded)
    }

    mutating func markUncertain(_ error: any Error) { detail = Self.bounded(error.localizedDescription) }

    private static func bounded(_ text: String) -> String { String(text.prefix(500)) }
}
