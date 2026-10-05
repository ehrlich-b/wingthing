import CryptoKit
import Foundation

// Fresh observation of the existing backend's headless_continuation field.
// This is capability evidence, not a grant, and is never cached as live.
public struct ContinuationAvailability: Equatable, Sendable {
    public let execution: ExecutionReference
    public let model: String
    public init?(result: JSONValue, execution: ExecutionReference, lifecycle: SessionLifecycle) {
        guard !lifecycle.processAlive, lifecycle.agent == "claude", lifecycle.sessionID == execution.sessionID,
              let provider = execution.providerSessionID, !provider.isEmpty, provider == lifecycle.providerSessionID,
              let value = result["headless_continuation"], value["available"] == .bool(true),
              value["source_session"] == .string(execution.sessionID), value["conversation_id"] == .string(execution.conversation.conversationID),
              value["provider_session_id"] == .string(provider), let model = value["model"]?.string,
              model.hasPrefix("claude-"), model.count <= 128, model.rangeOfCharacter(from: .controlCharacters) == nil else { return nil }
        self.execution = execution; self.model = model
    }
    public func covers(_ target: ExecutionReference?) -> Bool { target == execution }
}

public enum ContinuationProgress: String, Codable, Sendable {
    case savedLocally, unconfirmed, started, failed
    public var final: Bool { self == .started || self == .failed }
}

public struct PendingContinuation: Codable, Equatable, Sendable, Identifiable {
    public let id: UUID
    public let source: ExecutionReference
    public let input: String
    public let savedAt: Date
    public private(set) var progress: ContinuationProgress = .savedLocally
    public private(set) var targetSessionID: String?
    public private(set) var detail: String?
    public init(id: UUID = UUID(), source: ExecutionReference, input: String, savedAt: Date = Date()) throws {
        guard source.providerSessionID?.isEmpty == false, !source.sessionID.isEmpty else { throw ClientError.staleReference }
        guard !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, input.utf8.count <= 64 << 10, !input.hasPrefix("-"), !input.contains("\0") else {
            throw ClientError.invalidConfiguration("Enter a message of at most 64 KiB that does not begin with '-'.")
        }
        self.id = id; self.source = source; self.input = input; self.savedAt = savedAt
    }
    public var requestID: String { id.uuidString }
    public var inputSHA256: String { SHA256.hash(data: Data(input.utf8)).map { String(format: "%02x", $0) }.joined() }
    public var arguments: [String: JSONValue] {
        ["resume_session": .string(source.sessionID), "conversation_role": .string("parent"), "input": .string(input), "request_id": .string(requestID)]
    }
    public func sameIntent(_ other: PendingContinuation) -> Bool { id == other.id && source == other.source && input == other.input }
    public mutating func markUnconfirmed(_ detail: String? = nil) {
        guard !progress.final else { return }
        progress = .unconfirmed; self.detail = detail.map { String($0.prefix(500)) }
    }
    public mutating func apply(_ result: JSONValue) {
        guard !progress.final else { return }
        let root = source.conversation.conversationID
        guard result["request_id"] == .string(requestID), result["source_session"] == .string(source.sessionID),
              result["wing_id"] == .string(source.conversation.wingID), result["conversation_id"] == .string(root),
              result["root_conversation_id"] == .string(root), result["parent_conversation_id"] == nil || result["parent_conversation_id"] == .null || result["parent_conversation_id"] == .string(""),
              result["new_turn"]?["input_sha256"] == .string(inputSHA256),
              let target = result["session"]?.string, !target.isEmpty, target == target.trimmingCharacters(in: .whitespacesAndNewlines),
              target != source.sessionID, target.utf8.count <= 256, target.rangeOfCharacter(from: .controlCharacters) == nil else {
            markUnconfirmed("The reply did not match this exact follow-up request."); return
        }
        let provider = result["provider_session_id"]?.string ?? ""
        guard provider == source.providerSessionID || (provider.isEmpty && result["launch_state"] == .string("failed")) else {
            markUnconfirmed("The reply did not match this follow-up's provider."); return
        }
        if result["launch_state"] == .string("started"), result["continuation_state"] == .string("started"),
           provider == source.providerSessionID {
            progress = .started; targetSessionID = target; detail = nil
        } else if result["launch_state"] == .string("failed"), result["continuation_state"] == .string("failed") {
            progress = .failed; detail = String((result["launch_error"]?.string ?? "The follow-up did not start.").prefix(500))
        } else { markUnconfirmed("Follow-up unconfirmed. Check reuses this saved message.") }
    }
}
