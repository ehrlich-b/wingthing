import CryptoKit
import Foundation
import Testing
@testable import WingthingCore

private struct EmojiFixture: Decodable {
    struct Invitation: Decodable { let wing_id: String; let key: String; let id: String }
    struct Vector: Decodable { let name: String; let shared_hex: String; let key_hex: String; let emoji: String; let multi_emoji: String }
    let invitation: Invitation
    let alice: String
    let bob: String
    let vectors: [Vector]
    let multi_codepoint: [String]
}

private func fixtureHex(_ value: String) throws -> Data {
    var bytes = Data()
    var start = value.startIndex
    while start < value.endIndex {
        let end = value.index(start, offsetBy: 2)
        bytes.append(try #require(UInt8(value[start..<end], radix: 16)))
        start = end
    }
    return bytes
}

@Suite struct PairingEmojiVectorTests {
    @Test func sharedSecretVectorsMatchTerminal() throws {
        var root = URL(fileURLWithPath: #filePath)
        for _ in 0..<6 { root.deleteLastPathComponent() }
        let fixture = try JSONDecoder().decode(EmojiFixture.self, from: Data(contentsOf: root.appendingPathComponent("internal/phonepair/testdata/emoji_vectors.json")))
        let q = fixture.invitation
        let transcript = "wt-pair-v1\n\(q.wing_id)\n\(q.key)\n\(q.id)\n\(fixture.alice)\n\(fixture.bob)"
        for vector in fixture.vectors {
            let shared = try fixtureHex(vector.shared_hex)
            let key = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: shared), salt: Data(SHA256.hash(data: Data(transcript.utf8))), info: Data("wt-pair-v1".utf8), outputByteCount: 32)
            #expect(key.withUnsafeBytes { Data($0) } == (try fixtureHex(vector.key_hex)))
            #expect(PairingCipher(key: key).emoji == vector.emoji)
            let synthetic = Array(HMAC<SHA256>.authenticationCode(for: Data("wt-emoji-v1".utf8), using: key)).prefix(4).map { fixture.multi_codepoint[Int($0) % 4] }.joined(separator: " ")
            #expect(synthetic == vector.multi_emoji)
        }
        // Synthetic future vocabulary entries; the protocol's current 256
        // entries are single codepoints. Splitting must preserve graphemes.
        #expect(fixture.multi_codepoint.count == 4)
        #expect(fixture.multi_codepoint.allSatisfy { $0.unicodeScalars.count > 1 })
        #expect(fixture.multi_codepoint.joined(separator: " ").split(separator: " ").map(String.init) == fixture.multi_codepoint)
    }
}
