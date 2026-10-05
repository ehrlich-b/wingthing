import CryptoKit
import Foundation

// Matches internal/auth/crypto.go and web/src/tunnel.js exactly. The identity is
// in memory; no credential, private key, grant, or identity pin is auto-created
// in the user's existing Wingthing configuration.
public struct TunnelIdentity: Sendable {
    private let key: Curve25519.KeyAgreement.PrivateKey
    public init() { key = Curve25519.KeyAgreement.PrivateKey() }
    public init(rawPrivateKey: Data) throws { key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: rawPrivateKey) }
    public var publicKey: String { key.publicKey.rawRepresentation.base64EncodedString() }

    public func cipher(peerPublicKey: String, domain: String = "wt-tunnel") throws -> TunnelCipher {
        guard let bytes = Data(base64Encoded: peerPublicKey), bytes.count == 32 else { throw ClientError.identityMismatch("Invalid wing public key.") }
        let peer = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: bytes)
        let shared = try key.sharedSecretFromKeyAgreement(with: peer)
        let symmetric = shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: Data(repeating: 0, count: 32), sharedInfo: Data(domain.utf8), outputByteCount: 32)
        return TunnelCipher(key: symmetric)
    }
}

public struct TunnelCipher: Sendable {
    private let key: SymmetricKey
    init(key: SymmetricKey) { self.key = key }
    public func seal(_ plaintext: Data) throws -> String {
        let box = try AES.GCM.seal(plaintext, using: key)
        guard let bytes = box.combined else { throw ClientError.response("Could not encode the encrypted request.") }
        return bytes.base64EncodedString() // nonce12 || ciphertext || tag16
    }
    public func open(_ encoded: String) throws -> Data {
        guard let bytes = Data(base64Encoded: encoded), bytes.count >= 28 else { throw ClientError.response("Malformed encrypted response.") }
        return try AES.GCM.open(AES.GCM.SealedBox(combined: bytes), using: key)
    }
}
