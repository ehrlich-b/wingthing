import CryptoKit
import Foundation
import Security

@MainActor public protocol HomeCredentialStore {
    func token(for profile: HomeProfile) throws -> String?
    func save(_ token: String, for profile: HomeProfile) throws
    func forget(_ profile: HomeProfile) throws
}

@MainActor public final class KeychainCredentialStore: HomeCredentialStore {
    public init() {}
    // Independent of display spelling and random profile UUIDs. Changing the
    // origin, account, wing or pin selects a different credential item.
    static func query(for profile: HomeProfile) throws -> [String: Any] {
        guard profile.mode == .remote else { throw ClientError.invalidConfiguration("Local preview never stores a credential.") }
        let parts = URLComponents(url: profile.origin, resolvingAgainstBaseURL: false)!
        let fields = ["https", parts.host!.lowercased(), String(parts.port ?? 443), profile.expectedUserID, profile.homeWingID,
                      Data(base64Encoded: profile.homeWingPublicKey)!.base64EncodedString()]
        var data = Data()
        for field in fields { data.append(contentsOf: "\(field.utf8.count):".utf8); data.append(contentsOf: field.utf8) }
        let account = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        return [kSecClass as String: kSecClassGenericPassword,
                kSecAttrService as String: "dev.ehrlich.wingthing.home-token",
                kSecAttrAccount as String: account, kSecAttrSynchronizable as String: false,
                kSecUseDataProtectionKeychain as String: true]
    }
    static func attributes(_ token: String) -> [String: Any] {
        [kSecValueData as String: Data(token.utf8), kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
    }
    public func token(for profile: HomeProfile) throws -> String? {
        var query = try Self.query(for: profile)
        query[kSecReturnData as String] = true; query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        try check(status)
        guard let bytes = result as? Data, let token = String(data: bytes, encoding: .utf8), !token.isEmpty else {
            throw ClientError.storage("The saved home token is invalid. Enter it again.")
        }
        return token
    }
    public func save(_ token: String, for profile: HomeProfile) throws {
        guard !token.isEmpty else { throw ClientError.invalidConfiguration("An existing home token is required.") }
        let query = try Self.query(for: profile), attributes = Self.attributes(token)
        let status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            try check(SecItemAdd(query.merging(attributes) { _, new in new } as CFDictionary, nil))
        } else { try check(status) }
    }
    public func forget(_ profile: HomeProfile) throws {
        let status = SecItemDelete(try Self.query(for: profile) as CFDictionary)
        if status != errSecItemNotFound { try check(status) }
    }
    private func check(_ status: OSStatus) throws {
        guard status == errSecSuccess else {
            throw ClientError.storage("The home token couldn't be accessed in Keychain (\(status)). Unlock this device and try again.")
        }
    }
}

// Only selected identity metadata lives in the app's files; the token is held
// exclusively by the credential store and the authenticated connection.
public struct SelectedHomeStore: Sendable {
    private let file: URL
    public init(file: URL) { self.file = file }
    public func load() throws -> HomeProfile? {
        guard FileManager.default.fileExists(atPath: file.path) else { return nil }
        let bytes = try Data(contentsOf: file)
        guard bytes.count <= 64 << 10 else { throw ClientError.storage("The saved home profile is too large.") }
        let profile = try JSONDecoder().decode(HomeProfile.self, from: bytes)
        guard profile.mode == .remote else { throw ClientError.invalidConfiguration("Local preview is never restored as a saved home.") }
        return profile
    }
    public func save(_ profile: HomeProfile) throws {
        guard profile.mode == .remote else { throw ClientError.invalidConfiguration("Local preview is never saved as a home.") }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSONEncoder().encode(profile).write(to: file, options: .atomic)
    }
    public func clear() throws {
        guard FileManager.default.fileExists(atPath: file.path) else { return }
        try FileManager.default.removeItem(at: file)
    }
}
