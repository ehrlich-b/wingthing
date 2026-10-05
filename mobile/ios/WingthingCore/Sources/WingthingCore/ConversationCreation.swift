import Foundation

public struct ConversationProject: Codable, Equatable, Sendable, Identifiable {
    public let name: String
    public let path: String
    public var id: String { path }
}

// Authenticated wing.info evidence, never a grant or a persisted live claim.
public struct ConversationCreationOptions: Equatable, Sendable {
    public let profileID: UUID
    public let wingID: String
    public let projects: [ConversationProject]
    public let observedAt: Date
    public init(result: JSONValue, profile: HomeProfile, now: Date = Date()) throws {
        guard result["capabilities"]?.array?.contains(.string("conversation.personal.v1")) == true,
              result["agents"]?.array?.contains(.string("claude")) == true else {
            throw ClientError.unsupported("This computer doesn't advertise personal Claude conversations. Create one on your computer, then open it here.")
        }
        let decoded = try JSONDecoder().decode([ConversationProject].self, from: JSONEncoder().encode(result["projects"] ?? .array([])))
        projects = decoded.filter { Self.validPath($0.path) && !$0.name.isEmpty }.reduce(into: []) { values, project in
            if !values.contains(where: { $0.path == project.path }) { values.append(project) }
        }
        profileID = profile.id; wingID = profile.homeWingID; observedAt = now
    }
    public func covers(_ launch: PendingConversationLaunch, now: Date = Date()) -> Bool {
        launch.profileID == profileID && launch.wingID == wingID && projects.contains { $0.path == launch.workspace } &&
        now.timeIntervalSince(observedAt) >= 0 && now.timeIntervalSince(observedAt) < 60
    }
    static func validPath(_ value: String) -> Bool {
        value.hasPrefix("/") && value.utf8.count <= 4096 && value == value.trimmingCharacters(in: .whitespacesAndNewlines) &&
        value.rangeOfCharacter(from: .controlCharacters) == nil && !value.split(separator: "/").contains("..")
    }
}

public enum ConversationLaunchProgress: String, Codable, Sendable {
    case savedLocally, unconfirmed, started, failed
    public var final: Bool { self == .started || self == .failed }
}

// One immutable, home-qualified first message. Only the closed existing
// headless parent template can be rendered; arbitrary provider argv is absent.
public struct PendingConversationLaunch: Codable, Equatable, Sendable, Identifiable {
    public let id: UUID
    public let profileID: UUID
    public let userID: String
    public let wingID: String
    public let label: String
    public let workspace: String
    public let model: String
    public let input: String
    public let savedAt: Date
    public private(set) var progress: ConversationLaunchProgress = .savedLocally
    public private(set) var target: ExecutionReference?
    public private(set) var detail: String?
    public init(id: UUID = UUID(), profile: HomeProfile, label: String, workspace: String, model: String, input: String, savedAt: Date = Date()) throws {
        guard !label.isEmpty, label.utf8.count <= 64,
              label.range(of: "^[A-Za-z0-9_][A-Za-z0-9_.-]*$", options: .regularExpression) != nil else {
            throw ClientError.invalidConfiguration("Use a name of up to 64 letters, numbers, dots, underscores or hyphens, starting with a letter, number or underscore.")
        }
        guard ConversationCreationOptions.validPath(workspace) else { throw ClientError.invalidConfiguration("Choose an existing project on your computer.") }
        guard model.utf8.count <= 128, model.range(of: "^claude-[a-z0-9]+(?:[.-][a-z0-9]+)+$", options: .regularExpression) != nil else {
            throw ClientError.invalidConfiguration("Enter an exact Claude model ID.")
        }
        guard !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, input.utf8.count <= 64 << 10, !input.hasPrefix("-"), !input.contains("\0") else {
            throw ClientError.invalidConfiguration("Enter a first message of at most 64 KiB that does not begin with '-'.")
        }
        self.id = id; profileID = profile.id; userID = profile.expectedUserID; wingID = profile.homeWingID
        self.label = label; self.workspace = workspace; self.model = model; self.input = input; self.savedAt = savedAt
    }
    public func validate(_ profile: HomeProfile) throws {
        guard profileID == profile.id, userID == profile.expectedUserID, wingID == profile.homeWingID else { throw ClientError.staleReference }
        _ = try Self(id: id, profile: profile, label: label, workspace: workspace, model: model, input: input, savedAt: savedAt)
    }
    public var arguments: [String: JSONValue] {
        let tools = ["agent_start", "session_wait", "session_read", "session_status", "conversation_read", "conversation_checkpoint", "wingthing_capabilities"]
        // Exact template shared with web/src/conversation-headless.js. The
        // runtime adds bound MCP, hooks and session ID. No built-in tool grant.
        let settings = JSONValue.object(["availableModels": .array([.string(model)]), "enforceAvailableModels": .bool(true)])
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        let policy = String(decoding: try! encoder.encode(settings), as: UTF8.self)
        let argv = ["-p", input, "--output-format", "stream-json", "--verbose", "--restricted", "--setting-sources=", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--max-turns", "40", "--allowedTools", tools.map { "mcp__wingthing__" + $0 }.joined(separator: ","), "--settings", policy]
        return ["agent": .string("claude"), "label": .string(label), "cwd": .string(workspace), "conversation_role": .string("parent"),
                "model": .string(model), "args": .array(argv.map(JSONValue.string)), "request_id": .string(id.uuidString)]
    }
    public func sameIntent(_ other: Self) -> Bool {
        id == other.id && profileID == other.profileID && userID == other.userID && wingID == other.wingID &&
        label == other.label && workspace == other.workspace && model == other.model && input == other.input
    }
    public mutating func markUnconfirmed(_ message: String? = nil) {
        guard !progress.final else { return }; progress = .unconfirmed; detail = message.map { String($0.prefix(500)) }
    }
    public mutating func apply(_ result: JSONValue) {
        guard !progress.final else { return }
        guard result["request_id"] != nil else {
            markUnconfirmed("Your computer didn't echo the request ID inside its encrypted reply. Update Wingthing on that computer. Creation remains unconfirmed."); return
        }
        guard result["request_id"] == .string(id.uuidString),
              result["wing_id"] == .string(wingID), result["agent"] == .string("claude"), result["label"] == .string(label), result["cwd"] == .string(workspace),
              let root = result["conversation_id"]?.string, Self.validID(root), result["root_conversation_id"] == .string(root),
              result["parent_conversation_id"] == nil || result["parent_conversation_id"] == .null || result["parent_conversation_id"] == .string(""),
              let session = result["session"]?.string, Self.validID(session) else {
            markUnconfirmed("The reply didn't identify this exact new parent conversation."); return
        }
        if result["launch_state"] == .string("started") {
            progress = .started
            target = ExecutionReference(conversation: .init(profileID: profileID, userID: userID, wingID: wingID, conversationID: root), sessionID: session)
            detail = nil
        } else if result["launch_state"] == .string("failed") {
            progress = .failed; detail = String((result["launch_error"]?.string ?? "The conversation didn't start.").prefix(500))
        } else { markUnconfirmed("Creation isn't confirmed. Retry sends this same saved launch.") }
    }
    private static func validID(_ value: String) -> Bool {
        !value.isEmpty && value.utf8.count <= 256 && value == value.trimmingCharacters(in: .whitespacesAndNewlines) && value.rangeOfCharacter(from: .controlCharacters) == nil
    }
}
