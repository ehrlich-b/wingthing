#if DEBUG
import CryptoKit
import Foundation

// Synthetic transport only. Every URL is refused unless it belongs to the
// reserved .invalid fixture origin. This type never uses URLSession, credentials,
// provider processes, Keychain, or an actual Wingthing inventory.
public actor ConversationTransportFixture: HomeWire {
    public nonisolated let identity = try! TunnelIdentity(rawPrivateKey: Data(repeating: 1, count: 32))
    public nonisolated let userID = "fixture-user"
    public nonisolated let wingID = "fixture-wing"
    public nonisolated let rootID = "fixture-root"
    public nonisolated let providerID = "fixture-provider"
    public nonisolated let bearer = "synthetic-fixture-token"
    private struct Turn { var input: String; var alive: Bool; var stopped = false; var reads = 0 }
    private var turns: [String: Turn] = ["fixture-source": Turn(input: "Welcome to the fixture", alive: false)]
    private var current = "fixture-source"
    private var starts: [String: JSONValue] = [:]
    private var startArguments: [String: JSONValue] = [:]
    private var stops: [String: JSONValue] = [:]
    private var createdRoots: [String: JSONValue] = [:]
    private var heldCreation: CheckedContinuation<Void, Never>?
    private var holdCreation = false
    private var advertiseProjects = true
    private var launchRequests = 0
    private var stopRequests = 0
    private let scenario: String
    public init(scenario: String = "lost-reply") { self.scenario = scenario }
    public func profile() throws -> HomeProfile {
        try HomeProfile(id: UUID(uuidString: "5B6E69A3-DF43-4F6C-86C8-B4CB4FE577F0")!, origin: URL(string: "https://wingthing-fixture.invalid")!, transport: .userOwnedEndpoint,
                        expectedUserID: userID, homeWingID: wingID, homeWingPublicKey: identity.publicKey)
    }
    public func diagnostics() -> String { "Launches \(turns.count - 1) · requests \(launchRequests) · stops \(stops.count)" }
    public func counts() -> (launches: Int, requests: Int, stops: Int) { (turns.count - 1, launchRequests, stops.count) }
    public func intents() -> [JSONValue] { Array(startArguments.values) }
    public func holdNextCreation() { holdCreation = true }
    public func releaseCreation() { holdCreation = false; heldCreation?.resume(); heldCreation = nil }
    public func hideProjects() { advertiseProjects = false }
    private func validate(_ url: URL, _ token: String, scheme: String = "https") throws {
        guard url.scheme == scheme, url.host == "wingthing-fixture.invalid", token == bearer else { throw ClientError.response("Fixture refused a real connection.") }
    }
    public func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        try validate(url, bearer)
        let value: JSONValue
        switch url.path {
        case "/health": value = .object(["ok": .bool(true)])
        case "/auth/check": value = .object(["ok": .bool(true), "user_id": .string(userID)])
        case "/api/app/wings": value = .array([.object(["wing_id": .string(wingID), "public_key": .string(identity.publicKey)])])
        default: throw ClientError.response("Unexpected fixture HTTP request.")
        }
        return HTTPReply(status: 200, data: try JSONEncoder().encode(value))
    }
    public func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        try validate(url, bearer, scheme: "wss")
        guard url.path == "/ws/relay" else { throw ClientError.response("Unexpected fixture tunnel.") }
        let outer = try JSONDecoder().decode(JSONValue.self, from: request)
        guard outer["wing_id"] == .string(wingID), let sender = outer["sender_pub"]?.string, let payload = outer["payload"]?.string,
              let id = outer["request_id"]?.string else { throw ClientError.staleReference }
        let cipher = try identity.cipher(peerPublicKey: sender)
        let inner = try JSONDecoder().decode(JSONValue.self, from: cipher.open(payload))
        guard outer["purpose"] == .string(inner["type"] == .string("wing.info") ? "wing-discovery" : "wing-control") else { throw ClientError.staleReference }
        let operation: String, result: JSONValue
        if inner["type"] == .string("wing.info") {
            operation = "wing.info"
            result = .object(["capabilities": .array(scenario == "no-creation" ? [] : [.string("conversation.personal.v1")]), "agents": .array([.string("claude")]),
                              "projects": .array(advertiseProjects ? [.object(["name": .string("Fixture project"), "path": .string("/synthetic/workspace")])] : [])])
        } else {
            guard inner["type"] == .string("session.control"), let selectedOperation = inner["operation"]?.string, let args = inner["arguments"] else { throw ClientError.staleReference }
            operation = selectedOperation; result = try operate(operation, args)
            if holdCreation, operation == "agent_start", args["resume_session"] == nil {
                await withCheckedContinuation { heldCreation = $0 }
            }
        }
        // Lose only the first launch acknowledgement after committing it. A
        // reconnect reads the current task; only an explicit retry replays it.
        if ["lost-reply", "creation-lost-reply"].contains(scenario), operation == "agent_start", launchRequests == 1 { throw URLError(.networkConnectionLost) }
        var encryptedResult = result
        if operation == "agent_start", case .object(var fields) = result {
            if scenario == "creation-missing-echo" { fields.removeValue(forKey: "request_id") }
            if scenario == "creation-wrong-echo" { fields["request_id"] = .string("earlier-encrypted-success") }
            encryptedResult = .object(fields)
        }
        let reply: JSONValue = .object(["type": .string("tunnel.res"), "request_id": .string(id), "payload": .string(try cipher.seal(JSONEncoder().encode(encryptedResult)))])
        return try JSONEncoder().encode(reply)
    }
    private func conversation(child: Bool = false) -> JSONValue {
        .object(["conversation_id": .string(child ? "fixture-child" : rootID), "root_conversation_id": .string(rootID),
                 "parent_conversation_id": child ? .string(rootID) : .null,
                 "title": .string(child ? "Welcome screen" : ["design", "reading"].contains(scenario) ? "Welcome screen conversation" : "Fixture conversation"),
                 "agent": .string("claude"), "cwd": .string("/synthetic/workspace"),
                 "wing_id": .string(wingID), "session_id": .string(child ? "fixture-child-session" : current), "launch_state": .string("started")])
    }
    private func event(_ sequence: Int64, _ role: String, _ text: String, provider: String? = nil) -> JSONValue {
        .object(["sequence": .integer(sequence), "type": .string("message"), "source": .string("claude_transcript"), "role": .string(role),
                 "raw": .object(["type": .string(role), "sessionId": .string(provider ?? providerID), "message": .object(["role": .string(role), "content": .string(text)])])])
    }
    private func view(_ session: String, after: Int64 = 0, page: Bool = false) throws -> JSONValue {
        let child = ["design", "reading"].contains(scenario) && session == "fixture-child-session"
        guard var turn = child ? Turn(input: "Simplify the welcome screen", alive: false) : turns[session] else { throw ClientError.staleReference }
        if page && !child { turn.reads += 1; if turn.reads >= 2 && turn.input != "Keep working" { turn.alive = false }; turns[session] = turn }
        let state = turn.stopped ? "failed" : turn.alive ? "working" : "completed"
        let designed = ["design", "reading"].contains(scenario) && session == "fixture-source"
        let recordProvider = child ? "fixture-child-provider" : providerID
        var all = [event(1, "user", designed ? "Can you tidy up the welcome screen?" : turn.input, provider: recordProvider),
                   event(2, "assistant", child ? "The first screen now has one clear action. I also shortened the setup text." :
                         designed ? "Yes. I’ll simplify the copy and make the next step clearer." :
                         turn.stopped ? "Fixture task ended." : "Fixture reply: " + turn.input, provider: recordProvider)]
        if scenario == "reading", !child {
            all = (1...24).map { n in
                event(Int64(n), n % 2 == 0 ? "assistant" : "user", "Reading note \(n). " + String(repeating: "This synthetic paragraph keeps a stable native message identity while you inspect another task. ", count: 4))
            }
        }
        let head = Int64(all.count)
        let events = page && scenario == "reading" ? all.filter { if case .integer(let n)? = $0["sequence"] { return n > after }; return false } : page ? Array(all.filter { ($0["sequence"] == .integer(after + 1)) }.prefix(1)) : []
        let cursor = page && !events.isEmpty ? scenario == "reading" ? head : min(head, after + 1) : page ? head : 0
        return .object(["session_id": .string(session), "provider_session_id": .string(recordProvider), "agent": .string("claude"),
                        "state": .string(state), "state_source": .string(turn.stopped ? "egg_process" : "claude_hook"), "state_cursor": .integer(2),
                        "ready": .bool(turn.alive), "process_alive": .bool(turn.alive), "cursor": .integer(cursor), "head_cursor": .integer(head),
                        "has_more": .bool(page && cursor < head), "events": .array(events)])
    }
    private func operate(_ operation: String, _ args: JSONValue) throws -> JSONValue {
        switch operation {
        case "conversation_list":
            let existing = ["empty-home", "creation-lost-reply", "creation-failed"].contains(scenario) ? [] : [conversation()]
            return .object(["conversations": .array(existing + createdRoots.values.sorted { ($0["conversation_id"]?.string ?? "") < ($1["conversation_id"]?.string ?? "") })])
        case "conversation_read":
            if let root = args["conversation_id"]?.string, let created = createdRoots[root], let session = created["session_id"]?.string {
                return .object(["conversation": created, "tasks": .array([.object(["conversation": created, "lifecycle": try view(session)])])])
            }
            let child = ["design", "reading"].contains(scenario) && args["conversation_id"] == .string("fixture-child")
            guard child || args["conversation_id"] == .string(rootID) else { throw ClientError.staleReference }
            let root = conversation()
            var tasks: [JSONValue] = [.object(["conversation": root, "lifecycle": try view(current)])]
            if ["design", "reading"].contains(scenario) { tasks.append(.object(["conversation": conversation(child: true), "lifecycle": try view("fixture-child-session")])) }
            return .object(["conversation": child ? conversation(child: true) : root, "tasks": .array(tasks)])
        case "session_read":
            guard let session = args["session"]?.string else { throw ClientError.staleReference }
            let after: Int64; if case .integer(let n)? = args["after_cursor"] { after = n } else { after = 0 }
            let lifecycle = try view(session, after: after, page: true)
            let alive = lifecycle["process_alive"] == .bool(true)
            var fields: [String: JSONValue] = ["session": .string(session), "lifecycle": lifecycle,
                "headless_continuation": .object(["available": .bool(!alive && session != "fixture-child-session"), "source_session": .string(session), "provider_session_id": .string(providerID),
                                                  "conversation_id": .string(createdRoots.first { $0.value["session_id"] == .string(session) }?.key ?? rootID), "model": .string("claude-opus-5-5")])]
            if alive { fields[StopCapability.field] = .object(["available": .bool(true), "protocol": .string(StopCapability.protocolVersion), "operation": .string("conversation_stop"), "semantics": .string(StopCapability.semantics), "conversation_id": .string(rootID), "session_id": .string(session), "provider_session_id": .string(providerID)]) }
            return .object(fields)
        case "agent_start":
            launchRequests += 1
            if args["resume_session"] == nil {
                guard case .object(let fields) = args, Set(fields.keys) == ["agent", "label", "cwd", "conversation_role", "model", "args", "request_id"],
                      args["agent"] == .string("claude"), args["conversation_role"] == .string("parent"), args["cwd"] == .string("/synthetic/workspace"),
                      let id = args["request_id"]?.string, let argv = args["args"]?.array, argv.count == 17, argv[0] == .string("-p"), let input = argv[1].string,
                      let label = args["label"]?.string else { throw ClientError.staleReference }
                if let prior = starts[id] { guard startArguments[id] == args else { throw ClientError.staleReference }; return prior }
                let root = "fixture-created-\(starts.count + 1)", session = "fixture-created-session-\(starts.count + 1)"
                let failed = scenario == "creation-failed" && starts.isEmpty
                let state = failed ? "failed" : "started"
                let result: JSONValue = .object(["request_id": .string(id), "session": .string(session), "conversation_id": .string(root), "root_conversation_id": .string(root),
                    "parent_conversation_id": .string(""), "agent": .string("claude"), "label": .string(label), "cwd": args["cwd"]!, "wing_id": .string(wingID),
                    "launch_state": .string(state), "reused": .bool(false), "launch_error": .string("Synthetic creation failure")])
                starts[id] = result; startArguments[id] = args
                if !failed { turns[session] = Turn(input: input, alive: true) }
                createdRoots[root] = .object(["conversation_id": .string(root), "root_conversation_id": .string(root), "parent_conversation_id": .string(""),
                    "title": .string(label), "agent": .string("claude"), "cwd": args["cwd"]!, "wing_id": .string(wingID), "session_id": .string(session), "launch_state": .string(state)])
                return result
            }
            guard case .object(let fields) = args, Set(fields.keys) == ["resume_session", "conversation_role", "input", "request_id"],
                  args["conversation_role"] == .string("parent"), let source = args["resume_session"]?.string, let input = args["input"]?.string,
                  let id = args["request_id"]?.string, turns[source] != nil else { throw ClientError.staleReference }
            if let old = starts[id] { guard startArguments[id] == args else { throw ClientError.staleReference }; return old }
            guard turns[source]?.alive == false, !input.isEmpty else { throw ClientError.staleReference }
            let target = "fixture-turn-\(starts.count + 1)", failed = scenario == "failed" && starts.isEmpty
            let hash = SHA256.hash(data: Data(input.utf8)).map { String(format: "%02x", $0) }.joined()
            let result: JSONValue = .object(["launch_state": .string(failed ? "failed" : "started"), "continuation_state": .string(failed ? "failed" : "started"),
                "session": .string(target), "source_session": .string(source), "conversation_id": .string(rootID), "root_conversation_id": .string(rootID),
                "parent_conversation_id": .null, "wing_id": .string(wingID), "provider_session_id": .string(providerID), "request_id": .string(id),
                "new_turn": .object(["input_sha256": .string(hash)]), "launch_error": .string("Synthetic launch failure")])
            starts[id] = result; startArguments[id] = args
            if !failed { turns[target] = Turn(input: input, alive: true); current = target }
            return result
        case "conversation_stop":
            stopRequests += 1
            guard args["conversation_id"] == .string(rootID), args["expected_provider_session_id"] == .string(providerID),
                  let session = args["session"]?.string, let id = args["request_id"]?.string, var turn = turns[session] else { throw ClientError.staleReference }
            if let prior = stops[id] { return prior }
            turn.alive = false; turn.stopped = true; turns[session] = turn
            let receipt: JSONValue = .object(["request_id": .string(id), "conversation_id": .string(rootID), "session_id": .string(session), "provider_session_id": .string(providerID),
                "semantics": .string(StopCapability.semantics), "status": .string("termination_observed"), "definitely_not_sent": .bool(false),
                "signal_attempted": .bool(true), "signal_acknowledged": .bool(true), "termination_observed": .bool(true), "reserved_cursor": .integer(2),
                "termination_cursor": .integer(4), "termination_state": .string("failed"), "termination_source": .string("egg_process"),
                "process_alive": .bool(false), "retried": .bool(false), "causality": .string("unverified_fixture_only")])
            let result: JSONValue = .object(["conversation_id": .string(rootID), "session": .string(session), "receipt": receipt]); stops[id] = result; return result
        default: throw ClientError.unsupported("Unexpected fixture operation: \(operation)")
        }
    }
}
#endif
