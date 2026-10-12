import Foundation
import CryptoKit
import CCryptoBoringSSL

public enum PairingError: Error, LocalizedError, Equatable, Sendable {
    case invalidQR, expired, used, cancelled, identityMismatch, emojiMismatch, revoked, badProof, unreachable(String), denied, protocolFailure
    public var errorDescription: String? {
        switch self {
        case .invalidQR: "This is not a Wingthing pairing QR. On your computer, run wt pair."
        case .expired: "This QR expired. On the computer, press Enter for a fresh QR."
        case .used: "This QR was already used. On the computer, press Enter for a fresh QR."
        case .cancelled: "Pairing cancelled on both sides. No access granted."
        case .identityMismatch: "The wing fingerprint does not match the QR. Pairing stopped. Check the computer before scanning a fresh QR."
        case .emojiMismatch: "The check emoji differ. Pairing cancelled on both sides."
        case .revoked: "This phone's access was revoked. Scan a fresh QR to pair again."
        case .badProof: "The private pairing check failed. Scan a fresh QR."
        case .unreachable(let name): "\(name) isn't reachable from here. On the Mac, run `wt login` once to enable remote access."
        case .denied: "This wing refused the request."
        case .protocolFailure: "The wing returned an invalid pairing response."
        }
    }
    static func code(_ text: String, name: String) -> PairingError {
        switch text {
        case "expired": .expired
        case "used": .used
        case "cancelled": .cancelled
        case "identity_mismatch": .identityMismatch
        case "emoji_mismatch": .emojiMismatch
        case "revoked": .revoked
        case "bad_proof", "bad_key": .badProof
        case "unreachable": .unreachable(name)
        default: .denied
        }
    }
}

public struct PairingInvitation: Codable, Equatable, Sendable {
    public let v: Int
    public let wingID: String
    public let name: String
    public let key: String
    public let local: URL
    public let endpoints: [URL]?
    public let relay: URL?
    public let id: String
    public let secret: String
    public let expiresAt: Date
    enum CodingKeys: String, CodingKey { case v, name, key, local, endpoints, relay, id, secret; case wingID = "wing_id", expiresAt = "expires_at" }
    public init(link: String, now: Date = Date()) throws {
        guard link.utf8.count < 4096, let url = URL(string: link), url.scheme == "wingthing", url.host == "pair", url.path.isEmpty, url.query == nil,
              let fragment = url.fragment?.removingPercentEncoding else { throw PairingError.invalidQR }
        let bytes: Data
        if fragment.hasPrefix("v1.") || fragment.hasPrefix("v1z.") {
            let compressed = fragment.hasPrefix("v1z.")
            var base64 = String(fragment.dropFirst(compressed ? 4 : 3)).replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
            base64 += String(repeating: "=", count: (4 - base64.count % 4) % 4)
            guard let raw = Data(base64Encoded: base64) else { throw PairingError.invalidQR }
            let compact: Data
            if compressed {
                guard let decoded = try? (raw as NSData).decompressed(using: .zlib), decoded.length <= 4096 else { throw PairingError.invalidQR }
                compact = decoded as Data
            } else { compact = raw }
            guard case .array(let fields) = try? JSONDecoder().decode(JSONValue.self, from: compact), fields.count == 9, case .integer(let expires) = fields[8] else { throw PairingError.invalidQR }
            let names = ["v", "wing_id", "name", "key", "local", "relay", "id", "secret"]
            var mapped = Dictionary(uniqueKeysWithValues: zip(names, fields.prefix(8)))
            if case .array(let direct) = fields[4] {
                guard !direct.isEmpty else { throw PairingError.invalidQR }
                mapped["local"] = direct[0]; mapped["endpoints"] = .array(Array(direct.dropFirst()))
            }
            mapped["expires_at"] = .string(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: Double(expires))))
            bytes = try JSONEncoder().encode(JSONValue.object(mapped))
        } else { guard let data = fragment.data(using: .utf8) else { throw PairingError.invalidQR }; bytes = data }
        let decoder = JSONDecoder(); decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            let iso = ISO8601DateFormatter(); iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = iso.date(from: text) { return date }; iso.formatOptions = [.withInternetDateTime]
            guard let date = iso.date(from: text) else { throw PairingError.invalidQR }; return date
        }
        do { self = try decoder.decode(Self.self, from: bytes) } catch { throw PairingError.invalidQR }
        try validate(now: now)
    }
    // Empty relay strings emitted by a local-only wing decode as nil.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        v = try c.decode(Int.self, forKey: .v); wingID = try c.decode(String.self, forKey: .wingID)
        name = try c.decode(String.self, forKey: .name); key = try c.decode(String.self, forKey: .key)
        guard let endpoint = URL(string: try c.decode(String.self, forKey: .local)) else { throw PairingError.invalidQR }; local = endpoint
        endpoints = try c.decodeIfPresent([URL].self, forKey: .endpoints)
        let remote = try c.decodeIfPresent(String.self, forKey: .relay) ?? ""; relay = remote.isEmpty ? nil : URL(string: remote)
        if !remote.isEmpty && relay == nil { throw PairingError.invalidQR }
        id = try c.decode(String.self, forKey: .id); secret = try c.decode(String.self, forKey: .secret); expiresAt = try c.decode(Date.self, forKey: .expiresAt)
    }
    public func validate(now: Date) throws {
        guard v == 1, !wingID.isEmpty, wingID.count <= 100, !name.isEmpty, name.count <= 80, !name.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
              Data(base64Encoded: key)?.count == 32, id.count >= 20, id.count <= 100,
              secret.split(separator: " ").count == 6, secret.utf8.count <= 120,
              (endpoints?.count ?? 0) <= 8, (endpoints?.allSatisfy(Self.localAllowed) ?? true), Self.localAllowed(local), relay.map(Self.relayAllowed) ?? true else { throw PairingError.invalidQR }
        guard expiresAt > now else { throw PairingError.expired }
        guard expiresAt.timeIntervalSince(now) <= 305 else { throw PairingError.invalidQR }
    }
    public static func localAllowed(_ url: URL) -> Bool {
        guard url.scheme == "http", let host = url.host?.lowercased(), url.port != nil, cleanOrigin(url) else { return false }
        if host.hasSuffix(".local") || host.hasSuffix(".ts.net") { return true }
        let components = host.split(separator: ".", omittingEmptySubsequences: false)
        guard components.count == 4, components.allSatisfy({ !$0.isEmpty && $0.allSatisfy({ $0 >= "0" && $0 <= "9" }) }) else { return false }
        let octets = components.compactMap { Int($0) }
        guard octets.count == 4, octets.allSatisfy({ (0...255).contains($0) }) else { return false }
        return (octets[0] == 100 && (64...127).contains(octets[1])) || octets[0] == 10 || octets[0] == 127 || (octets[0] == 192 && octets[1] == 168) || (octets[0] == 172 && (16...31).contains(octets[1])) || (octets[0] == 169 && octets[1] == 254)
    }
    public static func relayAllowed(_ url: URL) -> Bool { url.scheme == "https" && url.host != nil && cleanOrigin(url) }
    private static func cleanOrigin(_ url: URL) -> Bool { url.user == nil && url.password == nil && url.query == nil && url.fragment == nil && (url.path.isEmpty || url.path == "/") }
    public var fingerprint: String { Self.fingerprint(key) }
    public static func fingerprint(_ key: String) -> String { SHA256.hash(data: Data(base64Encoded: key) ?? Data()).map { String(format: "%02x", $0) }.joined() }
    var identity: String { "wt-wing-v1:\(wingID):\(fingerprint):\(id)" }
}

// All elliptic-curve PAKE arithmetic is inside the pinned BoringSSL library.
public final class PairingPAKE {
    private var context: OpaquePointer?
    public let message: String
    public init(invitation: PairingInvitation) throws {
        let phone = Array("wt-phone-v1".utf8), wing = Array(invitation.identity.utf8), password = Array(invitation.secret.utf8)
        context = phone.withUnsafeBufferPointer { p in wing.withUnsafeBufferPointer { w in
            CCryptoBoringSSL_SPAKE2_CTX_new(spake2_role_alice, p.baseAddress, p.count, w.baseAddress, w.count)
        } }
        guard let context else { throw PairingError.badProof }
        var bytes = [UInt8](repeating: 0, count: 32), size = 0
        let ok = bytes.withUnsafeMutableBufferPointer { b in password.withUnsafeBufferPointer { p in
            CCryptoBoringSSL_SPAKE2_generate_msg(context, b.baseAddress, &size, b.count, p.baseAddress, p.count)
        } }
        guard ok == 1 else { CCryptoBoringSSL_SPAKE2_CTX_free(context); self.context = nil; throw PairingError.badProof }
        message = Data(bytes.prefix(size)).base64EncodedString()
    }
    deinit { if let context { CCryptoBoringSSL_SPAKE2_CTX_free(context) } }
    public func finish(_ peer: String, invitation: PairingInvitation) throws -> PairingCipher {
        guard let context, let remote = Data(base64Encoded: peer) else { throw PairingError.badProof }
        defer { CCryptoBoringSSL_SPAKE2_CTX_free(context); self.context = nil }
        var output = [UInt8](repeating: 0, count: 64), size = 0
        let ok = output.withUnsafeMutableBufferPointer { out in remote.withUnsafeBytes { data in
            CCryptoBoringSSL_SPAKE2_process_msg(context, out.baseAddress, &size, out.count, data.bindMemory(to: UInt8.self).baseAddress, data.count)
        } }
        guard ok == 1 else { throw PairingError.badProof }
        let transcript = "wt-pair-v1\n\(invitation.wingID)\n\(invitation.key)\n\(invitation.id)\n\(message)\n\(peer)"
        let key = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: output.prefix(size)), salt: Data(SHA256.hash(data: Data(transcript.utf8))), info: Data("wt-pair-v1".utf8), outputByteCount: 32)
        return PairingCipher(key: key)
    }
}

public struct PairingCipher: Sendable {
    let key: SymmetricKey
    public init(key: SymmetricKey) { self.key = key }
    public var emoji: String { Array(HMAC<SHA256>.authenticationCode(for: Data("wt-emoji-v1".utf8), using: key)).prefix(4).map { pairingEmoji[Int($0)] }.joined(separator: " ") }
    public func seal(_ value: JSONValue) throws -> String {
        let box = try AES.GCM.seal(JSONEncoder().encode(value), using: key)
        guard let combined = box.combined else { throw PairingError.protocolFailure }; return combined.base64EncodedString()
    }
    public func open(_ encoded: String) throws -> JSONValue {
        guard let data = Data(base64Encoded: encoded) else { throw PairingError.badProof }
        do { return try JSONDecoder().decode(JSONValue.self, from: AES.GCM.open(AES.GCM.SealedBox(combined: data), using: key)) } catch { throw PairingError.badProof }
    }
}

public protocol PhoneWire: Sendable {
    func configure(endpoints: [URL]) async
    func request(_ value: JSONValue, local: URL, relay: URL?, wingID: String, wingKey: String) async throws -> JSONValue
}
public extension PhoneWire { func configure(endpoints: [URL]) async {} }
private final class PairingNoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) { completionHandler(nil) }
}
public actor URLSessionPhoneWire: PhoneWire {
    private let session: URLSession
    private var directExchange: (@Sendable (URLRequest) async throws -> (Data, URLResponse))?
    private var relayExchange: (@Sendable (JSONValue, URL, String) async throws -> JSONValue)?
    private var discover: @Sendable (String, String) async -> URL? = { wingID, key in
        let lookup = await PhoneBonjourLookup(wingID: wingID, key: key)
        return await lookup.resolve()
    }
    private var socket: URLSessionWebSocketTask?
    private var remoteSelected = false
    private var endpoints: [URL] = []
    private var selectedDirect: URL?
    public func configure(endpoints: [URL]) async { self.endpoints = endpoints; selectedDirect = nil; remoteSelected = false }
    public static func isTailnet(_ url: URL) -> Bool {
        guard let host = url.host?.lowercased() else { return false }; if host.hasSuffix(".ts.net") { return true }
        let octets = host.split(separator: ".").compactMap { Int($0) }
        return octets.count == 4 && octets[0] == 100 && (64...127).contains(octets[1])
    }
    public static func orderedDirect(_ local: URL, endpoints: [URL]) -> [URL] {
        var unique: [URL] = []; for url in [local] + endpoints where !unique.contains(url) { unique.append(url) }
        return unique.filter { !isTailnet($0) } + unique.filter(isTailnet)
    }
    private var tail: Task<Void, Never>?
    private let carrierIdentity = TunnelIdentity()
    private static func makeSession() -> URLSession {
        let c = URLSessionConfiguration.ephemeral; c.httpCookieStorage = nil; c.httpShouldSetCookies = false; c.connectionProxyDictionary = [:]; c.urlCredentialStorage = nil
        c.timeoutIntervalForRequest = 4; c.timeoutIntervalForResource = 10
        return URLSession(configuration: c, delegate: PairingNoRedirect(), delegateQueue: nil)
    }
    public init() { session = Self.makeSession() }
    // Exercise carrier selection without opening real LAN, Bonjour or relay routes.
    init(directExchange: @escaping @Sendable (URLRequest) async throws -> (Data, URLResponse),
         discover: @escaping @Sendable (String, String) async -> URL?,
         relayExchange: (@Sendable (JSONValue, URL, String) async throws -> JSONValue)? = nil) {
        session = Self.makeSession(); self.directExchange = directExchange
        self.discover = discover; self.relayExchange = relayExchange
    }
    deinit { session.invalidateAndCancel() }
    public func request(_ value: JSONValue, local: URL, relay: URL?, wingID: String, wingKey: String) async throws -> JSONValue {
        let previous = tail
        let task = Task { await previous?.value; try Task.checkCancellation(); return try await self.boxed(value, local: local, relay: relay, wingID: wingID, wingKey: wingKey) }
        tail = Task { _ = try? await task.value }
        return try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
    }
    private func boxed(_ value: JSONValue, local: URL, relay: URL?, wingID: String, wingKey: String) async throws -> JSONValue {
        let cipher = try carrierIdentity.cipher(peerPublicKey: wingKey, domain: "wt-phone-carrier-v1")
        let box: JSONValue = .object(["op": .string("phone.box"), "sender_pub": .string(carrierIdentity.publicKey), "payload": .string(try cipher.seal(JSONEncoder().encode(value)))])
        let response = try await perform(box, local: local, relay: relay, wingID: wingID, wingKey: wingKey, mayChangeCarrier: value["op"]?.string == "pair.start" || value["op"]?.string == "device.hello")
        guard let payload = response["payload"]?.string else { throw PairingError.badProof }
        do { return try JSONDecoder().decode(JSONValue.self, from: cipher.open(payload)) } catch { throw PairingError.badProof }
    }
    private func perform(_ value: JSONValue, local: URL, relay: URL?, wingID: String, wingKey: String, mayChangeCarrier: Bool) async throws -> JSONValue {
        guard endpoints.allSatisfy(PairingInvitation.localAllowed), PairingInvitation.localAllowed(local), relay.map(PairingInvitation.relayAllowed) ?? true else { throw PairingError.invalidQR }
        if !remoteSelected {
            let ordered = Self.orderedDirect(local, endpoints: endpoints)
            let lan = ordered.filter { !Self.isTailnet($0) }, tailnet = ordered.filter(Self.isTailnet)
            var candidates = mayChangeCarrier ? lan : selectedDirect.map { [$0] } ?? lan
            var discovered = false
            var attempted: Set<URL> = []
            var lastError: Error = URLError(.cannotConnectToHost)
            while true {
                for origin in candidates where !attempted.contains(origin) {
                    attempted.insert(origin)
                    let url = origin.appendingPathComponent("phone")
                    var request = URLRequest(url: url); request.httpMethod = "POST"; request.httpBody = try JSONEncoder().encode(value); request.setValue("application/json", forHTTPHeaderField: "Content-Type")
                    do {
                        let (data, response): (Data, URLResponse)
                        if let directExchange { (data, response) = try await directExchange(request) }
                        else { (data, response) = try await session.data(for: request) }
                        guard let http = response as? HTTPURLResponse, http.statusCode == 200, http.url == url, data.count <= 256 << 10 else { throw PairingError.protocolFailure }
                        let reply = try JSONDecoder().decode(JSONValue.self, from: data); selectedDirect = origin; return reply
                    } catch let error as URLError {
                        guard mayChangeCarrier, [.cannotConnectToHost, .cannotFindHost, .networkConnectionLost, .notConnectedToInternet, .timedOut].contains(error.code) else { throw error }
                        lastError = error
                    }
                }
                guard mayChangeCarrier else { throw lastError }
                if !discovered {
                    discovered = true
                    let found = await discover(wingID, wingKey)
                    candidates = (found.map { [$0] } ?? []) + tailnet
                    continue
                }
                guard relay != nil else { throw lastError }
                remoteSelected = true; break
            }
        }
        guard let relay else { throw PairingError.unreachable(wingID) }
        if let relayExchange { return try await relayExchange(value, relay, wingID) }
        var parts = URLComponents(url: relay.appendingPathComponent("ws/phone"), resolvingAgainstBaseURL: false)!
        parts.scheme = "wss"; parts.queryItems = [URLQueryItem(name: "wing_id", value: wingID)]
        guard let url = parts.url else { throw PairingError.invalidQR }
        let connection: URLSessionWebSocketTask
        if let socket { connection = socket } else { connection = session.webSocketTask(with: url); connection.maximumMessageSize = 256 << 10; connection.resume(); socket = connection }
        let id = UUID().uuidString
        let envelope: JSONValue = .object(["type": .string("tunnel.req"), "purpose": .string("phone"), "wing_id": .string(wingID), "request_id": .string(id), "payload": .string(String(decoding: try JSONEncoder().encode(value), as: UTF8.self))])
        do {
            return try await withThrowingTaskGroup(of: JSONValue.self) { group in
                group.addTask {
                    try await connection.send(.string(String(decoding: try JSONEncoder().encode(envelope), as: UTF8.self)))
                    let bytes: Data
                    switch try await connection.receive() { case .data(let d): bytes = d; case .string(let s): bytes = Data(s.utf8); @unknown default: throw PairingError.protocolFailure }
                    let reply = try JSONDecoder().decode(JSONValue.self, from: bytes)
                    guard reply["type"]?.string == "tunnel.res", reply["request_id"]?.string == id, let payload = reply["payload"]?.string else { throw PairingError.protocolFailure }
                    return try JSONDecoder().decode(JSONValue.self, from: Data(payload.utf8))
                }
                group.addTask { try await Task.sleep(for: .seconds(15)); connection.cancel(with: .goingAway, reason: nil); throw PairingError.unreachable(wingID) }
                defer { group.cancelAll() }; return try await group.next()!
            }
        } catch { connection.cancel(with: .goingAway, reason: nil); socket = nil; throw error }
    }
}
