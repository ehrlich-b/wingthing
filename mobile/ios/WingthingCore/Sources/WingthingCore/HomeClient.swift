import Foundation

public struct HTTPReply: Sendable {
    public let status: Int
    public let data: Data
    public init(status: Int, data: Data) { self.status = status; self.data = data }
}

public protocol HomeWire: Sendable {
    func get(_ url: URL, bearer: String) async throws -> HTTPReply
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data
}

private final class NoRedirects: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) { completionHandler(nil) }
}

public final class URLSessionHomeWire: HomeWire, Sendable {
    private let session: URLSession
    public init() {
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil; config.httpShouldSetCookies = false
        config.timeoutIntervalForRequest = 30; config.timeoutIntervalForResource = 30
        session = URLSession(configuration: config, delegate: NoRedirects(), delegateQueue: nil)
    }
    public func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        var request = URLRequest(url: url); request.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await session.data(for: request)
        guard data.count <= 2 << 20, let response = response as? HTTPURLResponse, response.url == url else { throw ClientError.response("The home returned an oversized or redirected response.") }
        return HTTPReply(status: response.statusCode, data: data)
    }
    public func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        var outgoing = URLRequest(url: url); outgoing.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        let socket = session.webSocketTask(with: outgoing); socket.maximumMessageSize = 2 << 20
        socket.resume(); defer { socket.cancel(with: .normalClosure, reason: nil) }
        return try await withTaskCancellationHandler {
            try await withThrowingTaskGroup(of: Data.self) { group in
                group.addTask {
                    try await socket.send(.string(String(decoding: request, as: UTF8.self)))
                    switch try await socket.receive() {
                    case .data(let data): return data
                    case .string(let text): return Data(text.utf8)
                    @unknown default: throw ClientError.response("Unsupported WebSocket response.")
                    }
                }
                group.addTask { try await Task.sleep(for: .seconds(30)); socket.cancel(with: .goingAway, reason: nil); throw ClientError.timeout }
                defer { group.cancelAll() }
                return try await group.next()!
            }
        } onCancel: { socket.cancel(with: .goingAway, reason: nil) }
    }
}

private struct RelayEnvelope: Codable {
    let type: String
    let wingID: String?
    let requestID: String?
    let purpose: String?
    let senderPub: String?
    let payload: String?
    let message: String?
    enum CodingKeys: String, CodingKey { case type, wingID = "wing_id", requestID = "request_id", purpose, senderPub = "sender_pub", payload, message }
}

public actor HomeClient {
    public let profile: HomeProfile
    private let bearer: String
    private let wire: any HomeWire
    private let identity: TunnelIdentity
    private let pinnedWings: [String: String]
    private var homeEvidence: HomeEvidence?
    public init(profile: HomeProfile, existingBearer: String, wire: any HomeWire = URLSessionHomeWire(), identity: TunnelIdentity = TunnelIdentity(), additionalWingPins: [String: String] = [:]) throws {
        guard !existingBearer.isEmpty else { throw ClientError.invalidConfiguration("An existing authorized connection is required. Native pairing and sign-in are not implemented.") }
        var pins = additionalWingPins
        if let key = pins[profile.homeWingID], key != profile.homeWingPublicKey { throw ClientError.identityMismatch("Conflicting home wing identity.") }
        pins[profile.homeWingID] = profile.homeWingPublicKey
        guard pins.values.allSatisfy({ Data(base64Encoded: $0)?.count == 32 }) else { throw ClientError.identityMismatch("Invalid pinned wing identity.") }
        self.profile = profile; bearer = existingBearer; self.wire = wire; self.identity = identity; pinnedWings = pins
    }

    public func verifyHome() async throws -> HomeEvidence {
        homeEvidence = nil
        let health = try await read("/health")
        guard health["ok"] == .bool(true) else { throw ClientError.response("The configured home health check failed.") }
        let account = try await read("/auth/check")
        guard account["ok"] == .bool(true), account["user_id"]?.string == profile.expectedUserID else { throw ClientError.identityMismatch("The configured home returned a different account.") }
        let roster = try await wire.get(profile.endpoint("/api/app/wings"), bearer: bearer)
        guard roster.status == 200 else { throw ClientError.response("Home wing inventory returned HTTP \(roster.status).") }
        let wings = try JSONDecoder().decode([WingIdentity].self, from: roster.data)
        let evidence = HomeEvidence(profileID: profile.id, userID: profile.expectedUserID, wings: wings)
        try evidence.validate(profile)
        for wing in wings {
            if let pinned = pinnedWings[wing.wingID], pinned != wing.publicKey { throw ClientError.identityMismatch("An execution wing identity changed.") }
        }
        homeEvidence = evidence
        return evidence
    }

    public func disconnect() { homeEvidence = nil }

    private func read(_ path: String) async throws -> JSONValue {
        let reply = try await wire.get(profile.endpoint(path), bearer: bearer)
        guard reply.status == 200 else { throw ClientError.response("Configured home returned HTTP \(reply.status).") }
        return try JSONDecoder().decode(JSONValue.self, from: reply.data)
    }

    public func control(_ reference: ConversationReference, operation: String, arguments: [String: JSONValue]) async throws -> JSONValue {
        try reference.validate(profile)
        guard let evidence = homeEvidence else { throw ClientError.offline }
        guard let pinned = pinnedWings[reference.wingID], evidence.wings.contains(where: { $0.wingID == reference.wingID && $0.publicKey == pinned }) else { throw ClientError.identityMismatch("The selected execution wing has not been explicitly verified.") }
        let allowed = ["conversation_list", "conversation_read", "session_read", "session_status", "session_prompt"]
        guard allowed.contains(operation) else { throw ClientError.unsupported("This native draft has no adapter for \(operation).") }
        let inner = JSONValue.object(["type": .string("session.control"), "operation": .string(operation), "arguments": .object(arguments)])
        let data = try JSONEncoder().encode(inner)
        guard data.count <= 1 << 20 else { throw ClientError.response("The control request exceeds 1 MiB.") }
        let cipher = try identity.cipher(peerPublicKey: pinned)
        let id = UUID().uuidString
        let envelope = RelayEnvelope(type: "tunnel.req", wingID: reference.wingID, requestID: id, purpose: "wing-control", senderPub: identity.publicKey, payload: try cipher.seal(data), message: nil)
        let reply = try await wire.exchange(profile.tunnelURL(wingID: reference.wingID), bearer: bearer, request: JSONEncoder().encode(envelope))
        guard reply.count <= 2 << 20 else { throw ClientError.response("The control response exceeds 2 MiB.") }
        let response = try JSONDecoder().decode(RelayEnvelope.self, from: reply)
        guard response.requestID == id else { throw ClientError.staleReference }
        if response.type == "error" { throw ClientError.response(String((response.message ?? "Home control request denied.").prefix(1000))) }
        guard response.type == "tunnel.res", let encrypted = response.payload else { throw ClientError.response("Unsupported control response.") }
        let result = try JSONDecoder().decode(JSONValue.self, from: cipher.open(encrypted))
        if let error = result["error"]?.string {
            if error == "passkey_required" { throw ClientError.unsupported("This wing requires its existing passkey authorization. Native passkey approval is not connected yet.") }
            throw ClientError.response(String(error.prefix(1000)))
        }
        return result
    }

    public func conversations(on wingID: String) async throws -> [Conversation] {
        let reference = ConversationReference(profileID: profile.id, userID: profile.expectedUserID, wingID: wingID, conversationID: "inventory")
        let result = try await control(reference, operation: "conversation_list", arguments: [:])
        guard let items = result["conversations"] else { throw ClientError.response("The wing has no native conversation inventory.") }
        return try JSONDecoder().decode([Conversation].self, from: JSONEncoder().encode(items))
    }

    public func conversation(_ reference: ConversationReference) async throws -> ConversationRead {
        let result = try await control(reference, operation: "conversation_read", arguments: ["conversation_id": .string(reference.conversationID)])
        let view = try JSONDecoder().decode(ConversationRead.self, from: JSONEncoder().encode(result))
        guard view.conversation.conversationID == reference.conversationID, view.conversation.wingID == reference.wingID,
              view.tasks.contains(where: { $0.conversation.conversationID == reference.conversationID && $0.conversation.wingID == reference.wingID }) else { throw ClientError.staleReference }
        return view
    }

    public func transcript(_ execution: ExecutionReference, after: Int64) async throws -> SessionLifecycle {
        let result = try await control(execution.conversation, operation: "session_read", arguments: ["session": .string(execution.sessionID), "after_cursor": .integer(after), "limit": .integer(100)])
        guard let value = result["lifecycle"] else { throw ClientError.unsupported("This wing has no native conversation reader.") }
        let lifecycle = try JSONDecoder().decode(SessionLifecycle.self, from: JSONEncoder().encode(value))
        guard lifecycle.sessionID == execution.sessionID, execution.providerSessionID == nil || execution.providerSessionID == lifecycle.providerSessionID else { throw ClientError.staleReference }
        return lifecycle
    }
}
