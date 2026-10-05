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

public actor URLSessionHomeWire: HomeWire {
    private let session: URLSession
    private var socket: URLSessionWebSocketTask?
    private var socketURL: URL?
    private var socketBearer: String?
    private var exchangeTail: Task<Void, Never>?
    public init() {
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil; config.httpShouldSetCookies = false
        config.timeoutIntervalForRequest = 30; config.timeoutIntervalForResource = 30
        session = URLSession(configuration: config, delegate: NoRedirects(), delegateQueue: nil)
    }
    deinit { session.invalidateAndCancel() }
    // An empty bearer exists only for a verified localPreview client; it sends
    // no Authorization header rather than an empty one.
    static func authorize(_ request: inout URLRequest, bearer: String) {
        if !bearer.isEmpty { request.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }
    }
    public func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        var request = URLRequest(url: url); Self.authorize(&request, bearer: bearer)
        let (data, response) = try await session.data(for: request)
        guard data.count <= 2 << 20, let response = response as? HTTPURLResponse, response.url == url else { throw ClientError.response("The home returned an oversized or redirected response.") }
        return HTTPReply(status: response.statusCode, data: data)
    }
    public func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        // Serialize exchanges on the authenticated socket. History pagination
        // must not create an HTTP upgrade for every event; the relay bounds
        // upgrades independently from the typed requests carried over them.
        let previous = exchangeTail
        let work = Task {
            await previous?.value
            try Task.checkCancellation()
            return try await self.exchangeNow(url, bearer: bearer, request: request)
        }
        exchangeTail = Task { _ = try? await work.value }
        return try await withTaskCancellationHandler {
            try await work.value
        } onCancel: { work.cancel() }
    }

    private func exchangeNow(_ url: URL, bearer: String, request: Data) async throws -> Data {
        var outgoing = URLRequest(url: url); Self.authorize(&outgoing, bearer: bearer)
        if socketURL != url || socketBearer != bearer {
            socket?.cancel(with: .normalClosure, reason: nil)
            socket = nil
        }
        let connection: URLSessionWebSocketTask
        if let socket { connection = socket }
        else {
            connection = session.webSocketTask(with: outgoing)
            connection.maximumMessageSize = 2 << 20
            socket = connection; socketURL = url; socketBearer = bearer
            connection.resume()
        }
        do {
            return try await withTaskCancellationHandler {
                try await withThrowingTaskGroup(of: Data.self) { group in
                    group.addTask {
                        try await connection.send(.string(String(decoding: request, as: UTF8.self)))
                        switch try await connection.receive() {
                        case .data(let data): return data
                        case .string(let text): return Data(text.utf8)
                        @unknown default: throw ClientError.response("Unsupported WebSocket response.")
                        }
                    }
                    group.addTask { try await Task.sleep(for: .seconds(30)); connection.cancel(with: .goingAway, reason: nil); throw ClientError.timeout }
                    defer { group.cancelAll() }
                    return try await group.next()!
                }
            } onCancel: { connection.cancel(with: .goingAway, reason: nil) }
        } catch {
            connection.cancel(with: .goingAway, reason: nil)
            socket = nil; socketURL = nil; socketBearer = nil
            if let response = connection.response as? HTTPURLResponse, response.statusCode != 101 {
                throw ClientError.response("The home WebSocket returned HTTP \(response.statusCode).")
            }
            // The request may have been delivered. Never reconnect and resend.
            throw error
        }
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

public struct SessionHistory: Sendable {
    public let lifecycle: SessionLifecycle
    public let events: [SessionEvent]
    public let pages: Int

    // Native provider JSON is data. Only user/assistant text blocks become
    // messages; PTY output, thinking, tool input/results and hooks never do.
    public var nativeMessages: [NativeMessage] {
        events.compactMap { event in
            guard event.source == "claude_transcript", let raw = event.raw, let type = raw["type"]?.string, ["user", "assistant"].contains(type),
                  raw["message"]?["role"]?.string == type else { return nil }
            let content = raw["message"]?["content"]
            let text = content?.string ?? (content?.array ?? []).filter { $0["type"]?.string == "text" }.compactMap { $0["text"]?.string }.joined(separator: "\n")
            return text.isEmpty ? nil : NativeMessage(sequence: event.sequence, role: type, text: text)
        }
    }
    public var finalAssistantText: String? { nativeMessages.last { $0.role == "assistant" }?.text }
}

public struct NativeMessage: Equatable, Sendable {
    public let sequence: Int64
    public let role: String
    public let text: String
}

public actor HomeClient {
    public let profile: HomeProfile
    private let bearer: String
    private let wire: any HomeWire
    private let identity: TunnelIdentity
    private let pinnedWings: [String: String]
    private var homeEvidence: HomeEvidence?
    // Stop advertisements seen by this connection since its last verification.
    // Only the newest read issued for an execution, completing within the same
    // connection epoch, may publish; actor reentrancy lets older reads finish late.
    private var stopEvidence: [ExecutionReference: StopCapability] = [:]
    private var continuationEvidence: [ExecutionReference: ContinuationAvailability] = [:]
    private var creationEvidence: ConversationCreationOptions?
    private var creationSequence: UInt64 = 0
    private var stopsInFlight: Set<UUID> = []
    private var epoch: UInt64 = 0
    private var readSequence: UInt64 = 0
    private var latestRead: [ExecutionReference: UInt64] = [:] // Keyed without provider.
    public init(profile: HomeProfile, existingBearer: String, wire: any HomeWire = URLSessionHomeWire(), identity: TunnelIdentity = TunnelIdentity(), additionalWingPins: [String: String] = [:]) throws {
        guard profile.mode == .remote else { throw ClientError.invalidConfiguration("A local preview profile never takes a credential.") }
        guard !existingBearer.isEmpty else { throw ClientError.invalidConfiguration("An existing authorized connection is required. Native pairing and sign-in are not implemented.") }
        try self.init(checked: profile, bearer: existingBearer, wire: wire, identity: identity, additionalWingPins: additionalWingPins)
    }

    #if DEBUG
    // Debug-only, credential-free client for an explicit localPreview profile.
    // Any supplied credential, including whitespace, is refused rather than
    // sent or ignored. Only the profile's single pinned wing is trusted.
    public init(localPreview profile: HomeProfile, suppliedCredential: String = "", wire: any HomeWire = URLSessionHomeWire(), identity: TunnelIdentity = TunnelIdentity()) throws {
        guard profile.mode == .localPreview else { throw ClientError.invalidConfiguration("Local preview requires an explicit local preview profile.") }
        guard suppliedCredential.isEmpty else { throw ClientError.invalidConfiguration("Local preview takes no credential. Nothing was sent.") }
        try self.init(checked: profile, bearer: "", wire: wire, identity: identity, additionalWingPins: [:])
    }
    #endif

    private init(checked profile: HomeProfile, bearer: String, wire: any HomeWire, identity: TunnelIdentity, additionalWingPins: [String: String]) throws {
        var pins = additionalWingPins
        if let key = pins[profile.homeWingID], key != profile.homeWingPublicKey { throw ClientError.identityMismatch("Conflicting home wing identity.") }
        pins[profile.homeWingID] = profile.homeWingPublicKey
        guard pins.values.allSatisfy({ Data(base64Encoded: $0)?.count == 32 }) else { throw ClientError.identityMismatch("Invalid pinned wing identity.") }
        self.profile = profile; self.bearer = bearer; self.wire = wire; self.identity = identity; pinnedWings = pins
    }

    public func verifyHome() async throws -> HomeEvidence {
        homeEvidence = nil; resetStopEvidence()
        switch profile.mode {
        case .remote:
            let health = try await read("/health")
            guard health["ok"] == .bool(true) else { throw ClientError.response("The configured home health check failed.") }
            let account = try await read("/auth/check")
            guard account["ok"] == .bool(true), account["user_id"]?.string == profile.expectedUserID else { throw ClientError.identityMismatch("The configured home returned a different account.") }
        case .localPreview:
            // The real single-user preview account, not a synthesized auth check.
            guard HomeProfile.localPreviewCompiled, bearer.isEmpty else { throw ClientError.unsupported("Local preview mode exists only in debug builds.") }
            let account = try await read("/api/app/me")
            guard account["id"]?.string == profile.expectedUserID, account["release_channel"]?.string == "preview", account["provider"]?.string == "local" else {
                throw ClientError.identityMismatch("The local preview home returned a different account or release channel.")
            }
        }
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

    public func disconnect() { homeEvidence = nil; resetStopEvidence() }

    // A new epoch invalidates every read still in flight.
    private func resetStopEvidence() { epoch &+= 1; stopEvidence = [:]; continuationEvidence = [:]; creationEvidence = nil; latestRead = [:] }

    private func read(_ path: String) async throws -> JSONValue {
        let reply = try await wire.get(profile.endpoint(path), bearer: bearer)
        guard reply.status == 200 else { throw ClientError.response("Configured home returned HTTP \(reply.status).") }
        return try JSONDecoder().decode(JSONValue.self, from: reply.data)
    }

    // Generic reads and the explicit prompt only. conversation_stop is reachable
    // solely through submitStop, behind this connection's exact advertisement.
    public func control(_ reference: ConversationReference, operation: String, arguments: [String: JSONValue]) async throws -> JSONValue {
        let allowed = ["conversation_list", "conversation_read", "session_read", "session_status", "session_prompt"]
        guard allowed.contains(operation) else { throw ClientError.unsupported("This native draft has no adapter for \(operation).") }
        return try await dispatch(reference, operation: operation, arguments: arguments)
    }

    private func dispatch(_ reference: ConversationReference, operation: String, arguments: [String: JSONValue]) async throws -> JSONValue {
        let inner = JSONValue.object(["type": .string("session.control"), "operation": .string(operation), "arguments": .object(arguments)])
        return try await tunnel(reference, inner: inner)
    }

    private func tunnel(_ reference: ConversationReference, inner: JSONValue) async throws -> JSONValue {
        try reference.validate(profile)
        guard let evidence = homeEvidence else { throw ClientError.offline }
        guard let pinned = pinnedWings[reference.wingID], evidence.wings.contains(where: { $0.wingID == reference.wingID && $0.publicKey == pinned }) else { throw ClientError.identityMismatch("The selected execution wing has not been explicitly verified.") }
        let data = try JSONEncoder().encode(inner)
        guard data.count <= 1 << 20 else { throw ClientError.response("The control request exceeds 1 MiB.") }
        let cipher = try identity.cipher(peerPublicKey: pinned)
        let id = UUID().uuidString
        let purpose = inner["type"] == .string("wing.info") ? "wing-discovery" : "wing-control"
        let envelope = RelayEnvelope(type: "tunnel.req", wingID: reference.wingID, requestID: id, purpose: purpose, senderPub: identity.publicKey, payload: try cipher.seal(data), message: nil)
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
        // The relay envelope is unauthenticated. Mutation receipts must bind
        // their immutable intent and destination inside the decrypted payload.
        if let operation = inner["operation"]?.string, ["agent_start", "session_prompt"].contains(operation) {
            let receipt = operation == "session_prompt" ? result["receipt"] ?? result["prompt"] ?? result : result
            guard receipt["request_id"] != nil else {
                throw ClientError.unsupported("Your computer didn't echo the request ID inside its encrypted reply. Update Wingthing on that computer. Delivery remains unconfirmed.")
            }
            guard receipt["request_id"] == inner["arguments"]?["request_id"] else { throw ClientError.staleReference }
            if operation == "session_prompt" {
                guard receipt["session_id"] != nil else {
                    throw ClientError.unsupported("Your computer didn't echo the session ID inside its encrypted reply. Update Wingthing on that computer. Delivery remains unconfirmed.")
                }
                guard receipt["session_id"] == inner["arguments"]?["session"] else { throw ClientError.staleReference }
            }
        }
        return result
    }

    public func creationOptions() async throws -> ConversationCreationOptions {
        guard profile.mode == .remote else { throw ClientError.unsupported("Local preview is inspection only.") }
        creationEvidence = nil; let requestedEpoch = epoch
        creationSequence &+= 1; let issued = creationSequence
        let reference = ConversationReference(profileID: profile.id, userID: profile.expectedUserID, wingID: profile.homeWingID, conversationID: "inventory")
        let result = try await tunnel(reference, inner: .object(["type": .string("wing.info")]))
        let options = try ConversationCreationOptions(result: result, profile: profile)
        guard requestedEpoch == epoch, issued == creationSequence, homeEvidence != nil else { throw ClientError.staleReference }
        creationEvidence = options; return options
    }

    public func submitConversationLaunch(_ launch: PendingConversationLaunch, firstAttempt: Bool) async throws -> PendingConversationLaunch {
        try launch.validate(profile)
        guard profile.mode == .remote else { throw ClientError.unsupported("Local preview is inspection only.") }
        if firstAttempt, creationEvidence?.covers(launch) != true { throw ClientError.staleReference }
        let reference = ConversationReference(profileID: profile.id, userID: profile.expectedUserID, wingID: launch.wingID, conversationID: "inventory")
        let result = try await dispatch(reference, operation: "agent_start", arguments: launch.arguments)
        var updated = launch; updated.apply(result); return updated
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
        try await observe(execution, after: after).lifecycle
    }

    // Reads lifecycle and, only if exactly bound to what this read returned,
    // the Stop advertisement. Any read replaces that execution's evidence.
    public func observe(_ execution: ExecutionReference, after: Int64) async throws -> ExecutionObservation {
        let key = ExecutionReference(conversation: execution.conversation, sessionID: execution.sessionID)
        readSequence &+= 1
        let issued = readSequence, issuedEpoch = epoch
        latestRead[key] = issued
        stopEvidence = stopEvidence.filter { $0.key.conversation != key.conversation || $0.key.sessionID != key.sessionID }
        continuationEvidence = continuationEvidence.filter { $0.key.conversation != key.conversation || $0.key.sessionID != key.sessionID }
        let result = try await control(execution.conversation, operation: "session_read", arguments: ["session": .string(execution.sessionID), "after_cursor": .integer(after), "limit": .integer(Self.sessionPageLimit)])
        guard let value = result["lifecycle"] else { throw ClientError.unsupported("This wing has no native conversation reader.") }
        let lifecycle = try JSONDecoder().decode(SessionLifecycle.self, from: JSONEncoder().encode(value))
        guard lifecycle.sessionID == execution.sessionID, execution.providerSessionID == nil || execution.providerSessionID == lifecycle.providerSessionID else { throw ClientError.staleReference }
        let bound = ExecutionReference(conversation: execution.conversation, sessionID: lifecycle.sessionID, providerSessionID: lifecycle.providerSessionID)
        // A read superseded by reconnect, disconnect or a newer read of this
        // execution (including one that saw a replaced provider) never publishes.
        let newest = issuedEpoch == epoch && latestRead[key] == issued
        let capability = newest ? StopCapability(result: result, execution: bound) : nil
        if let capability { stopEvidence[bound] = capability }
        let continuation = newest ? ContinuationAvailability(result: result, execution: bound, lifecycle: lifecycle) : nil
        if let continuation { continuationEvidence[bound] = continuation }
        return ExecutionObservation(lifecycle: lifecycle, stopCapability: capability, continuation: continuation)
    }

    // A separate closed agent_start adapter for an exact existing continuation.
    // No model, workspace, provider, launch argv or authority override is sent.
    public func submitContinuation(_ pending: PendingContinuation, firstAttempt: Bool) async throws -> PendingContinuation {
        guard profile.mode == .remote else { throw ClientError.unsupported("Local preview is inspection only.") }
        if firstAttempt, continuationEvidence[pending.source]?.covers(pending.source) != true { throw ClientError.staleReference }
        let response = try await dispatch(pending.source.conversation, operation: "agent_start", arguments: pending.arguments)
        var updated = pending; updated.apply(response); return updated
    }

    // One event per session_read page: a single large native event can fill
    // the unchanged coordination response cap, so batching is not safe.
    public static let sessionPageLimit: Int64 = 1

    // Pages forward to the journal head. Every page must keep the same session
    // and provider identity and advance the cursor with strictly increasing
    // sequences; a stall below head, a page bound or a deadline is an error.
    public func readToHead(_ execution: ExecutionReference, after: Int64 = 0, maxPages: Int = 500, timeLimit: Duration = .seconds(120)) async throws -> SessionHistory {
        guard after >= 0, maxPages > 0 else { throw ClientError.invalidConfiguration("Invalid history bound.") }
        let clock = ContinuousClock(), deadline = clock.now.advanced(by: timeLimit)
        var target = execution, cursor = after, events: [SessionEvent] = [], pages = 0
        while true {
            guard pages < maxPages else { throw ClientError.response("Session history did not reach its head within \(maxPages) pages.") }
            guard clock.now < deadline else { throw ClientError.timeout }
            try Task.checkCancellation()
            let page = try await observe(target, after: cursor).lifecycle
            pages += 1
            target = ExecutionReference(conversation: execution.conversation, sessionID: page.sessionID, providerSessionID: page.providerSessionID)
            var last = cursor
            for event in page.events ?? [] {
                guard event.sequence > last, event.sequence <= page.cursor else { throw ClientError.response("Session history page is out of order.") }
                last = event.sequence; events.append(event)
            }
            guard page.cursor >= cursor, page.headCursor.map({ page.cursor <= $0 }) ?? true else { throw ClientError.response("Session history cursor moved backwards.") }
            let advanced = page.cursor > cursor
            cursor = page.cursor
            if page.hasMore != true { return SessionHistory(lifecycle: page, events: events, pages: pages) }
            if !advanced {
                // A pending journal reports more at head; below head nothing moved.
                guard let head = page.headCursor, cursor >= head else { throw ClientError.response("Session history stopped advancing before its head.") }
                return SessionHistory(lifecycle: page, events: events, pages: pages)
            }
        }
    }

    // Sends one saved Stop intent, or its identical explicit retry. Without
    // current exact evidence nothing is sent. Once dispatch starts, any failure
    // or unmatched reply leaves the outcome unconfirmed rather than throwing.
    public func submitStop(_ pending: PendingStop) async throws -> PendingStop {
        try pending.execution.conversation.validate(profile)
        guard stopEvidence[pending.execution]?.covers(pending.execution) == true else {
            throw ClientError.unsupported("Your computer hasn't confirmed that it can stop this exact task.")
        }
        guard !stopsInFlight.contains(pending.id) else { throw ClientError.response("This stop request is already being sent.") }
        stopsInFlight.insert(pending.id)
        defer { stopsInFlight.remove(pending.id) }
        var updated = pending
        do {
            let result = try await dispatch(pending.execution.conversation, operation: StopCapability.operation, arguments: pending.arguments)
            updated.apply(response: result)
        } catch { updated.markUncertain(error) }
        return updated
    }
}
