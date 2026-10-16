import Foundation
import CryptoKit
import Security

public struct PairedWing: Codable, Equatable, Identifiable, Sendable {
    public var id: String { wingID }
    public let wingID, name, key, deviceID: String
    public let local: URL
    public let endpoints: [URL]?
    public let relay: URL?
    public init(wingID: String, name: String, key: String, deviceID: String, local: URL, relay: URL?, endpoints: [URL]? = nil) {
        self.wingID = wingID; self.name = name; self.key = key; self.deviceID = deviceID; self.local = local; self.relay = relay; self.endpoints = endpoints
    }
}
public struct PhoneSigningKey: Sendable {
    public let representation: Data
    public let enclave: Bool
    public init(useEnclave: Bool = true) throws {
        if useEnclave && SecureEnclave.isAvailable {
            let key = try SecureEnclave.P256.Signing.PrivateKey()
            representation = key.dataRepresentation; enclave = true
        } else { representation = P256.Signing.PrivateKey().rawRepresentation; enclave = false }
    }
    public var publicKey: String {
        get throws {
            if enclave { return try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: representation).publicKey.x963Representation.base64EncodedString() }
            return try P256.Signing.PrivateKey(rawRepresentation: representation).publicKey.x963Representation.base64EncodedString()
        }
    }
    public func sign(_ text: String) throws -> String {
        if enclave { return try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: representation).signature(for: Data(text.utf8)).derRepresentation.base64EncodedString() }
        return try P256.Signing.PrivateKey(rawRepresentation: representation).signature(for: Data(text.utf8)).derRepresentation.base64EncodedString()
    }
}
public struct PhoneSecret: Codable, Sendable {
    public let credential: String
    public let signingRepresentation: Data
    public let enclave: Bool
    public init(credential: String, signingKey: PhoneSigningKey) { self.credential = credential; signingRepresentation = signingKey.representation; enclave = signingKey.enclave }
    public func sign(_ text: String) throws -> String {
        if enclave { return try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: signingRepresentation).signature(for: Data(text.utf8)).derRepresentation.base64EncodedString() }
        return try P256.Signing.PrivateKey(rawRepresentation: signingRepresentation).signature(for: Data(text.utf8)).derRepresentation.base64EncodedString()
    }
}
public protocol PhoneSecretStore: Sendable {
    func save(_ secret: PhoneSecret, for wing: PairedWing) throws
    func load(for wing: PairedWing) throws -> PhoneSecret?
    func forget(_ wing: PairedWing) throws
}
public struct PhoneKeychainStore: PhoneSecretStore {
    public init() {}
    static func query(_ wing: PairedWing) -> [String: Any] {
        let scope = "\(wing.wingID):\(wing.key):\(wing.deviceID)"
        let account = SHA256.hash(data: Data(scope.utf8)).map { String(format: "%02x", $0) }.joined()
        return [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "dev.ehrlich.wingthing.phone-pairing-v1", kSecAttrAccount as String: account, kSecAttrSynchronizable as String: false, kSecUseDataProtectionKeychain as String: true]
    }
    public func save(_ secret: PhoneSecret, for wing: PairedWing) throws {
        let query = Self.query(wing), attributes: [String: Any] = [kSecValueData as String: try JSONEncoder().encode(secret), kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        var status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound { status = SecItemAdd(query.merging(attributes) { _, new in new } as CFDictionary, nil) }
        guard status == errSecSuccess else { throw ClientError.storage("Could not save this wing in Keychain (\(status)). Unlock this iPhone and try again.") }
    }
    public func load(for wing: PairedWing) throws -> PhoneSecret? {
        var query = Self.query(wing); query[kSecReturnData as String] = true; query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?; let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let bytes = result as? Data else { throw ClientError.storage("Unlock this iPhone to connect to \(wing.name).") }
        return try JSONDecoder().decode(PhoneSecret.self, from: bytes)
    }
    public func forget(_ wing: PairedWing) throws {
        let status = SecItemDelete(Self.query(wing) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw ClientError.storage("Could not forget this pairing in Keychain.") }
    }
}

// @MainActor avoids concurrent PAKE context access and fences replaced scans.
@MainActor public final class PhoneEnrollment {
    public let invitation: PairingInvitation
    public private(set) var emoji: String?
    private let wire: any PhoneWire
    private let signingKey: PhoneSigningKey
    private var pake: PairingPAKE?
    private var cipher: PairingCipher?
    private var cancelled = false
    private var phoneConfirmed = false
    public init(invitation: PairingInvitation, wire: any PhoneWire = URLSessionPhoneWire(), signingKey: PhoneSigningKey? = nil) throws {
        self.invitation = invitation; self.wire = wire; self.signingKey = try signingKey ?? PhoneSigningKey()
    }
    public func start() async throws {
        try invitation.validate(now: Date())
        await wire.configure(endpoints: invitation.endpoints ?? [])
        let exchange = try PairingPAKE(invitation: invitation); pake = exchange
        let reply = try await send(.object(["op": .string("pair.start"), "id": .string(invitation.id), "message": .string(exchange.message)]))
        guard reply["key"]?.string == invitation.key else { throw PairingError.identityMismatch }
        guard let message = reply["message"]?.string else { throw PairingError.protocolFailure }
        let channel = try exchange.finish(message, invitation: invitation); cipher = channel; pake = nil
        if cancelled { try? await cancel(); throw PairingError.cancelled }
        let proved = try await encrypted("prove", extras: ["name": .string("My iPhone"), "public_key": .string(try signingKey.publicKey)])
        guard proved["op"]?.string == "proved", proved["emoji"]?.string == channel.emoji else { try? await cancel(); throw PairingError.emojiMismatch }
        guard !cancelled else { throw PairingError.cancelled }; emoji = channel.emoji
    }
    public func confirm(emoji: String) async throws -> (PairedWing, PhoneSecret)? {
        guard let expected = self.emoji, emoji == expected else { try? await cancel(); throw PairingError.emojiMismatch }
        phoneConfirmed = true
        return try result(await encrypted("confirm", extras: ["emoji": .string(emoji)]))
    }
    public func status() async throws -> (PairedWing, PhoneSecret)? {
        guard phoneConfirmed else { throw PairingError.denied }
        return try result(await encrypted("status"))
    }
    public func cancel() async throws {
        cancelled = true; emoji = nil; pake = nil
        if cipher != nil { _ = try await encrypted("cancel", allowCancelled: true) }
        cipher = nil
    }
    private func result(_ reply: JSONValue) throws -> (PairedWing, PhoneSecret)? {
        guard !cancelled else { throw PairingError.cancelled }
        if reply["state"]?.string == "cancelled" { throw PairingError.cancelled }
        guard reply["op"]?.string == "paired" else { return nil }
        guard reply["wing_id"]?.string == invitation.wingID, reply["key"]?.string == invitation.key,
              let id = reply["device_id"]?.string, let credential = reply["credential"]?.string, !id.isEmpty, credential.count >= 40 else { throw PairingError.identityMismatch }
        return (PairedWing(wingID: invitation.wingID, name: invitation.name, key: invitation.key, deviceID: id, local: invitation.local, relay: invitation.relay, endpoints: invitation.endpoints), PhoneSecret(credential: credential, signingKey: signingKey))
    }
    private func encrypted(_ operation: String, extras: [String: JSONValue] = [:], allowCancelled: Bool = false) async throws -> JSONValue {
        guard (!cancelled || allowCancelled), let cipher else { throw PairingError.cancelled }
        var fields = extras; fields["op"] = .string(operation); fields["id"] = .string(invitation.id)
        let reply = try await send(.object(["op": .string("pair.\(operation)"), "id": .string(invitation.id), "payload": .string(try cipher.seal(.object(fields)))]))
        guard let payload = reply["payload"]?.string else { throw PairingError.protocolFailure }
        let value = try cipher.open(payload)
        guard value["id"]?.string == invitation.id else { throw PairingError.badProof }; return value
    }
    private func send(_ request: JSONValue) async throws -> JSONValue {
        do {
            let reply = try await wire.request(request, local: invitation.local, relay: invitation.relay, wingID: invitation.wingID, wingKey: invitation.key)
            if let error = reply["error"]?.string { throw PairingError.code(error, name: invitation.name) }; return reply
        } catch PairingError.unreachable { throw PairingError.unreachable(invitation.name) } catch let error as URLError {
            if [.cannotConnectToHost, .cannotFindHost, .networkConnectionLost, .notConnectedToInternet, .timedOut].contains(error.code) { throw PairingError.unreachable(invitation.name) }; throw error
        }
    }
}

public struct PhoneSession: Codable, Identifiable, Sendable {
    public let id, name, agent, status: String
    public let blocked, unread: Bool
    public var title: String { name.isEmpty ? id : name }
}
public actor PhoneConnection {
    public private(set) var wing: PairedWing
    private let secret: PhoneSecret
    private let wire: any PhoneWire
    private let identity = TunnelIdentity()
    private var cipher: TunnelCipher?
    private var connectionID: String?
    private var sequence: Int64 = 0
    private var flight = false
    private var commandTail: Task<Void, Never>?
    public init(wing: PairedWing, secret: PhoneSecret, wire: any PhoneWire = URLSessionPhoneWire()) { self.wing = wing; self.secret = secret; self.wire = wire }
    public func connect() async throws {
        guard !flight else { throw PairingError.denied }; flight = true; defer { flight = false }
        connectionID = nil; sequence = 0
        await wire.configure(endpoints: wing.endpoints ?? [])
        let channel = try identity.cipher(peerPublicKey: wing.key, domain: "wt-phone-v1")
        let hello = try await send(.object(["op": .string("device.hello"), "sender_pub": .string(identity.publicKey)]))
        guard let payload = hello["payload"]?.string else { throw PairingError.badProof }
        let challenge = try JSONDecoder().decode(JSONValue.self, from: channel.open(payload))
        guard challenge["wing_id"]?.string == wing.wingID, challenge["key"]?.string == wing.key, let id = challenge["connection_id"]?.string, let nonce = challenge["challenge"]?.string else { throw PairingError.identityMismatch }
        let signature = try secret.sign("wt-device-v1\n\(wing.wingID)\n\(id)\n\(nonce)\n\(identity.publicKey)")
        let auth: JSONValue = .object(["connection_id": .string(id), "device_id": .string(wing.deviceID), "credential": .string(secret.credential), "signature": .string(signature)])
        let reply = try await send(.object(["op": .string("device.auth"), "connection_id": .string(id), "payload": .string(try channel.seal(JSONEncoder().encode(auth)))]))
        guard let encoded = reply["payload"]?.string else { throw PairingError.badProof }
        let verified = try JSONDecoder().decode(JSONValue.self, from: channel.open(encoded))
        guard verified["connection_id"]?.string == id, verified["ok"] == .bool(true), verified["sequence"] == .integer(0) else { throw PairingError.badProof }
        if let values = challenge["endpoints"]?.array {
            let direct = values.compactMap { $0.string.flatMap(URL.init(string:)) }
            let remote = challenge["relay"]?.string.flatMap { $0.isEmpty ? nil : URL(string: $0) }
            guard !direct.isEmpty, direct.count <= 9, direct.allSatisfy(PairingInvitation.localAllowed), remote.map(PairingInvitation.relayAllowed) ?? true else { throw PairingError.protocolFailure }
            wing = PairedWing(wingID: wing.wingID, name: wing.name, key: wing.key, deviceID: wing.deviceID, local: direct[0], relay: remote, endpoints: Array(direct.dropFirst()))
        }
        cipher = channel; connectionID = id
    }
    public func sessions() async throws -> [PhoneSession] { try JSONDecoder().decode([PhoneSession].self, from: JSONEncoder().encode(await command("sessions"))) }
    public func command(_ op: String, session: String = "", input: String = "") async throws -> JSONValue {
        let previous = commandTail
        let task = Task { await previous?.value; try Task.checkCancellation(); return try await self.execute(op, session: session, input: input) }
        commandTail = Task { _ = try? await task.value }
        return try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
    }
    private func execute(_ op: String, session: String, input: String) async throws -> JSONValue {
        guard !flight, let cipher, let id = connectionID, ["sessions", "attach", "read", "input", "stop", "detach", "close"].contains(op) else { throw PairingError.denied }
        flight = true; defer { flight = false }; sequence += 1; let seq = sequence
        let value: JSONValue = .object(["op": .string(op), "connection_id": .string(id), "sequence": .integer(seq), "session_id": .string(session), "input": .string(input)])
        do {
            let reply = try await send(.object(["op": .string("device.command"), "connection_id": .string(id), "payload": .string(try cipher.seal(JSONEncoder().encode(value)))]))
            guard let payload = reply["payload"]?.string else { throw PairingError.badProof }
            let response = try JSONDecoder().decode(JSONValue.self, from: cipher.open(payload))
            guard response["connection_id"]?.string == id, response["sequence"] == .integer(seq) else { throw PairingError.badProof }
            if response["error"] != nil { throw PairingError.protocolFailure }; return response["result"] ?? .null
        } catch { connectionID = nil; self.cipher = nil; throw error }
    }
    public func close() async { _ = try? await command("close"); connectionID = nil; cipher = nil }
    private func send(_ value: JSONValue) async throws -> JSONValue {
        do {
            let reply = try await wire.request(value, local: wing.local, relay: wing.relay, wingID: wing.wingID, wingKey: wing.key)
            if let error = reply["error"]?.string { throw PairingError.code(error, name: wing.name) }; return reply
        } catch is URLError { throw PairingError.unreachable(wing.name) } catch PairingError.unreachable { throw PairingError.unreachable(wing.name) }
    }
}
