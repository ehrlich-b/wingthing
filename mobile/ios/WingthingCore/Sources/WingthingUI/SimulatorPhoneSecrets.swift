#if DEBUG && targetEnvironment(simulator)
import CryptoKit
import Foundation
import WingthingCore

// Unsigned simulator builds cannot use device Keychain entitlements. This store
// accepts only generated loopback pairings in an explicit, disposable fixture.
struct SimulatorPhoneSecrets: PhoneSecretStore {
    let directory: URL
    private func file(_ wing: PairedWing) throws -> URL {
        guard wing.wingID.hasPrefix("fixture-"), wing.local.host == "127.0.0.1", wing.relay.map({ $0.host == "127.0.0.1" && PairingInvitation.relayAllowed($0) }) ?? true,
              wing.endpoints?.allSatisfy({ $0.host == "127.0.0.1" }) ?? true else { throw PairingError.invalidQR }
        let scope = wing.wingID + ":" + wing.key + ":" + wing.deviceID
        let key = SHA256.hash(data: Data(scope.utf8)).map { String(format: "%02x", $0) }.joined()
        return directory.appendingPathComponent(key + ".json")
    }
    func save(_ secret: PhoneSecret, for wing: PairedWing) throws {
        let file = try file(wing)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try JSONEncoder().encode(secret).write(to: file, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
    }
    func load(for wing: PairedWing) throws -> PhoneSecret? {
        let file = try file(wing)
        guard FileManager.default.fileExists(atPath: file.path) else { return nil }
        return try JSONDecoder().decode(PhoneSecret.self, from: Data(contentsOf: file))
    }
    func forget(_ wing: PairedWing) throws {
        let file = try file(wing)
        if FileManager.default.fileExists(atPath: file.path) { try FileManager.default.removeItem(at: file) }
    }
}
#endif
