import Foundation

public enum ClientError: Error, Equatable, LocalizedError, Sendable {
    case invalidConfiguration(String), identityMismatch(String), unsupported(String)
    case response(String), timeout, offline, staleReference, storage(String)
    public var errorDescription: String? {
        switch self {
        case .invalidConfiguration(let message), .identityMismatch(let message), .unsupported(let message), .response(let message), .storage(let message): message
        case .timeout: "The configured home did not respond. Delivery may be unconfirmed."
        case .offline: "The configured home is unavailable. Cached information is not live state."
        case .staleReference: "This request belongs to a different home, user, or execution."
        }
    }
}

public enum HomeTransport: String, Codable, CaseIterable, Sendable {
    case localNetwork, userOwnedEndpoint, existingVPN, explicitHostedRoost
}

// Remote is the only shipping mode. localPreview is a DEBUG-only way to read a
// browser-authorized single-user preview roost on this same Mac: literal
// http://127.0.0.1 with an explicit port, user "local", no credential. It is
// never chosen automatically and never relaxes the HTTPS contract.
public enum HomeProfileMode: String, Codable, Hashable, Sendable {
    case remote, localPreview
}

// These are configured identities. A relay URL and wing ID are not HomeRoostID.
// The hosted preset fills only the origin; account, wing and pin stay explicit.
public struct HomeProfile: Codable, Hashable, Sendable, Identifiable {
    public static let hostedPresetOrigin = "https://wingthing.ai"
    public let id: UUID
    public let origin: URL
    public let transport: HomeTransport
    public let expectedUserID: String
    public let homeWingID: String
    public let homeWingPublicKey: String
    public let homeRoostID: String?
    public let mode: HomeProfileMode

    public init(id: UUID = UUID(), origin: URL, transport: HomeTransport, expectedUserID: String, homeWingID: String, homeWingPublicKey: String, homeRoostID: String? = nil) throws {
        guard let parts = URLComponents(url: origin, resolvingAgainstBaseURL: false), parts.scheme == "https", let host = parts.host, !host.isEmpty,
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.path.isEmpty || parts.path == "/" else {
            throw ClientError.invalidConfiguration("Choose an exact HTTPS home origin without a path or embedded credentials.")
        }
        try Self.checkIdentity(expectedUserID, homeWingID, homeWingPublicKey)
        self.id = id; self.origin = origin; self.transport = transport; self.expectedUserID = expectedUserID
        self.homeWingID = homeWingID; self.homeWingPublicKey = homeWingPublicKey; self.homeRoostID = homeRoostID; mode = .remote
    }

    #if DEBUG
    static let localPreviewCompiled = true
    #else
    static let localPreviewCompiled = false
    #endif
    public static let localPreviewUserID = "local"

    // Shared by manual entry and setup links; neither path creates authority.
    public static func formInput(origin: String, transport: HomeTransport, userID: String, wingID: String, wingPublicKey: String, mode: HomeProfileMode = .remote) throws -> HomeProfile {
        guard let address = URL(string: origin.trimmingCharacters(in: .whitespacesAndNewlines)) else {
            throw ClientError.invalidConfiguration("Choose an exact HTTPS home origin without a path or embedded credentials.")
        }
        let user = userID.trimmingCharacters(in: .whitespacesAndNewlines), wing = wingID.trimmingCharacters(in: .whitespacesAndNewlines)
        let key = wingPublicKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if mode == .localPreview {
            return try localPreview(origin: address, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key)
        }
        return try HomeProfile(origin: address, transport: transport, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key)
    }

    // Explicit debug entry point. Release builds have no way to construct or
    // restore this mode.
    public static func localPreview(id: UUID = UUID(), origin: URL, expectedUserID: String, homeWingID: String, homeWingPublicKey: String) throws -> HomeProfile {
        try HomeProfile(localPreview: id, origin: origin, expectedUserID: expectedUserID, homeWingID: homeWingID, homeWingPublicKey: homeWingPublicKey)
    }

    init(localPreview id: UUID, origin: URL, expectedUserID: String, homeWingID: String, homeWingPublicKey: String, compiled: Bool = localPreviewCompiled) throws {
        guard compiled else { throw ClientError.unsupported("Local preview mode exists only in debug builds.") }
        guard let parts = URLComponents(url: origin, resolvingAgainstBaseURL: false), parts.scheme == "http", parts.host == "127.0.0.1",
              let port = parts.port, (1...65535).contains(port), origin.absoluteString.hasPrefix("http://127.0.0.1:\(port)"),
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.path.isEmpty || parts.path == "/" else {
            throw ClientError.invalidConfiguration("Local preview requires the literal origin http://127.0.0.1 with an explicit port.")
        }
        guard expectedUserID == Self.localPreviewUserID else { throw ClientError.invalidConfiguration("Local preview requires the expected user \"local\".") }
        try Self.checkIdentity(expectedUserID, homeWingID, homeWingPublicKey)
        self.id = id; self.origin = origin; transport = .localNetwork; self.expectedUserID = expectedUserID
        self.homeWingID = homeWingID; self.homeWingPublicKey = homeWingPublicKey; homeRoostID = nil; mode = .localPreview
    }

    private static func checkIdentity(_ user: String, _ wing: String, _ key: String) throws {
        guard !user.isEmpty, !wing.isEmpty, Data(base64Encoded: key)?.count == 32 else {
            throw ClientError.invalidConfiguration("The home user and pinned wing identity are required. Pairing is not implemented yet.")
        }
    }

    enum CodingKeys: String, CodingKey { case id, origin, transport, expectedUserID, homeWingID, homeWingPublicKey, homeRoostID, mode }
    public init(from decoder: any Decoder) throws {
        try self.init(decoding: decoder, compiled: Self.localPreviewCompiled)
    }

    // A saved localPreview profile is revalidated in full and fails in release.
    init(decoding decoder: any Decoder, compiled: Bool) throws {
        let value = try decoder.container(keyedBy: CodingKeys.self)
        let id = try value.decode(UUID.self, forKey: .id), origin = try value.decode(URL.self, forKey: .origin)
        let user = try value.decode(String.self, forKey: .expectedUserID), wing = try value.decode(String.self, forKey: .homeWingID), key = try value.decode(String.self, forKey: .homeWingPublicKey)
        switch try value.decodeIfPresent(HomeProfileMode.self, forKey: .mode) ?? .remote {
        case .remote:
            try self.init(id: id, origin: origin, transport: value.decode(HomeTransport.self, forKey: .transport), expectedUserID: user, homeWingID: wing, homeWingPublicKey: key, homeRoostID: value.decodeIfPresent(String.self, forKey: .homeRoostID))
        case .localPreview:
            guard try value.decode(HomeTransport.self, forKey: .transport) == .localNetwork, try value.decodeIfPresent(String.self, forKey: .homeRoostID) == nil else {
                throw ClientError.invalidConfiguration("The saved local preview profile is not valid.")
            }
            try self.init(localPreview: id, origin: origin, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key, compiled: compiled)
        }
    }

    // Remote profiles encode exactly as before; only localPreview adds a mode.
    public func encode(to encoder: any Encoder) throws {
        var value = encoder.container(keyedBy: CodingKeys.self)
        try value.encode(id, forKey: .id); try value.encode(origin, forKey: .origin); try value.encode(transport, forKey: .transport)
        try value.encode(expectedUserID, forKey: .expectedUserID); try value.encode(homeWingID, forKey: .homeWingID)
        try value.encode(homeWingPublicKey, forKey: .homeWingPublicKey); try value.encodeIfPresent(homeRoostID, forKey: .homeRoostID)
        if mode != .remote { try value.encode(mode, forKey: .mode) }
    }

    public func endpoint(_ path: String) throws -> URL {
        guard path.hasPrefix("/"), !path.hasPrefix("//"), !path.contains("?") else { throw ClientError.invalidConfiguration("Invalid home API path.") }
        var parts = URLComponents(url: origin, resolvingAgainstBaseURL: false)!
        parts.path = path; parts.query = nil; parts.fragment = nil
        guard let result = parts.url else { throw ClientError.invalidConfiguration("Invalid home endpoint.") }
        return result
    }

    public func tunnelURL(wingID: String) throws -> URL {
        var parts = URLComponents(url: try endpoint("/ws/relay"), resolvingAgainstBaseURL: false)!
        parts.scheme = mode == .localPreview ? "ws" : "wss"; parts.queryItems = [URLQueryItem(name: "wing_id", value: wingID)]
        return parts.url!
    }

    public var phoneReachabilityNote: String? {
        let host = origin.host?.lowercased() ?? ""
        if host == "localhost" || host == "::1" || host.hasPrefix("127.") { return "This address reaches the phone itself. The preview roost is currently loopback-only; an approved reachable endpoint is required." }
        return "Reachability has not been verified. A QR code does not create a route through NAT."
    }
}

public struct WingIdentity: Codable, Equatable, Sendable, Identifiable {
    public let wingID: String
    public let publicKey: String
    public var id: String { wingID }
    enum CodingKeys: String, CodingKey { case wingID = "wing_id", publicKey = "public_key" }
    public init(wingID: String, publicKey: String) { self.wingID = wingID; self.publicKey = publicKey }
}

public struct HomeEvidence: Equatable, Sendable {
    public let profileID: UUID
    public let userID: String
    public let wings: [WingIdentity]
    public let verifiedAt: Date
    public init(profileID: UUID, userID: String, wings: [WingIdentity], verifiedAt: Date = Date()) { self.profileID = profileID; self.userID = userID; self.wings = wings; self.verifiedAt = verifiedAt }
    public func validate(_ profile: HomeProfile) throws {
        guard profileID == profile.id, userID == profile.expectedUserID else { throw ClientError.identityMismatch("The configured home returned a different account.") }
        let matches = wings.filter { $0.wingID == profile.homeWingID }
        guard matches.count == 1, matches[0].publicKey == profile.homeWingPublicKey else { throw ClientError.identityMismatch("The configured home wing is missing or its identity changed.") }
    }
}

public struct ConversationReference: Codable, Hashable, Sendable, Identifiable {
    public let profileID: UUID
    public let userID: String
    public let wingID: String
    public let conversationID: String
    public var id: String { (try? JSONEncoder().encode([profileID.uuidString, userID, wingID, conversationID]).base64EncodedString()) ?? "" }
    public init(profileID: UUID, userID: String, wingID: String, conversationID: String) { self.profileID = profileID; self.userID = userID; self.wingID = wingID; self.conversationID = conversationID }
    public func validate(_ profile: HomeProfile) throws {
        guard profileID == profile.id, userID == profile.expectedUserID, !wingID.isEmpty, !conversationID.isEmpty else { throw ClientError.staleReference }
    }
}

public struct ExecutionReference: Codable, Hashable, Sendable {
    public let conversation: ConversationReference
    public let sessionID: String
    public let providerSessionID: String?
    public init(conversation: ConversationReference, sessionID: String, providerSessionID: String? = nil) { self.conversation = conversation; self.sessionID = sessionID; self.providerSessionID = providerSessionID }
}
