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

// These are configured identities. A relay URL and wing ID are not HomeRoostID.
// No default vendor address, discovery service, fallback, or enrollment exists.
public struct HomeProfile: Codable, Hashable, Sendable, Identifiable {
    public let id: UUID
    public let origin: URL
    public let transport: HomeTransport
    public let expectedUserID: String
    public let homeWingID: String
    public let homeWingPublicKey: String
    public let homeRoostID: String?

    public init(id: UUID = UUID(), origin: URL, transport: HomeTransport, expectedUserID: String, homeWingID: String, homeWingPublicKey: String, homeRoostID: String? = nil) throws {
        guard let parts = URLComponents(url: origin, resolvingAgainstBaseURL: false), parts.scheme == "https", parts.host != nil,
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.path.isEmpty || parts.path == "/" else {
            throw ClientError.invalidConfiguration("Choose an exact HTTPS home origin without a path or embedded credentials.")
        }
        guard !expectedUserID.isEmpty, !homeWingID.isEmpty, Data(base64Encoded: homeWingPublicKey)?.count == 32 else {
            throw ClientError.invalidConfiguration("The home user and pinned wing identity are required. Pairing is not implemented yet.")
        }
        self.id = id; self.origin = origin; self.transport = transport; self.expectedUserID = expectedUserID
        self.homeWingID = homeWingID; self.homeWingPublicKey = homeWingPublicKey; self.homeRoostID = homeRoostID
    }

    enum CodingKeys: String, CodingKey { case id, origin, transport, expectedUserID, homeWingID, homeWingPublicKey, homeRoostID }
    public init(from decoder: any Decoder) throws {
        let value = try decoder.container(keyedBy: CodingKeys.self)
        try self.init(id: value.decode(UUID.self, forKey: .id), origin: value.decode(URL.self, forKey: .origin), transport: value.decode(HomeTransport.self, forKey: .transport), expectedUserID: value.decode(String.self, forKey: .expectedUserID), homeWingID: value.decode(String.self, forKey: .homeWingID), homeWingPublicKey: value.decode(String.self, forKey: .homeWingPublicKey), homeRoostID: value.decodeIfPresent(String.self, forKey: .homeRoostID))
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
        parts.scheme = "wss"; parts.queryItems = [URLQueryItem(name: "wing_id", value: wingID)]
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
