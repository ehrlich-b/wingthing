import Foundation

// Transient form input only. Parsing never persists, logs, or connects.
public struct HomeSetupLink: Sendable {
    public static let maximumBytes = 16 << 10
    public let profile: HomeProfile
    public let token: String?

    public init(_ text: String) throws {
        let invalid = ClientError.invalidConfiguration("Invalid home setup link. Copy the link from wt phone link again.")
        guard text.utf8.count <= Self.maximumBytes else {
            throw ClientError.invalidConfiguration("The home setup link is too large.")
        }
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard text.removingPercentEncoding != nil, let parts = URLComponents(string: text),
              parts.scheme == "wingthing", parts.host == "home", parts.percentEncodedHost == "home",
              parts.user == nil, parts.password == nil, parts.port == nil, parts.path.isEmpty, parts.fragment == nil,
              let items = parts.queryItems else { throw invalid }
        let allowed: Set<String> = ["v", "origin", "account", "wing", "key", "token"]
        var fields: [String: String] = [:]
        for item in items {
            guard allowed.contains(item.name), fields[item.name] == nil, let value = item.value else { throw invalid }
            fields[item.name] = value
        }
        guard fields["v"] == "1", let origin = fields["origin"], let user = fields["account"],
              let wing = fields["wing"], let key = fields["key"] else { throw invalid }
        let transport: HomeTransport = origin == HomeProfile.hostedPresetOrigin ? .explicitHostedRoost : .userOwnedEndpoint
        let mode: HomeProfileMode = URLComponents(string: origin)?.scheme == "http" ? .localPreview : .remote
        // This invokes the existing DEBUG-only literal-loopback, explicit-port,
        // user "local" rules. Release cannot construct a preview profile.
        profile = try HomeProfile.formInput(origin: origin, transport: transport, userID: user, wingID: wing, wingPublicKey: key, mode: mode)
        if let supplied = fields["token"] {
            let bearer = supplied.trimmingCharacters(in: .whitespacesAndNewlines)
            guard profile.mode == .remote, !bearer.isEmpty else { throw invalid }
            token = bearer
        } else { token = nil }
    }
}
