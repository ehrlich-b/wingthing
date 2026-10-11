import CryptoKit
import Foundation
import Security
import Testing
import WingthingUI
@testable import WingthingCore

private actor PairingFailureWire: PhoneWire {
    let reply: JSONValue?
    private(set) var count = 0
    init(reply: JSONValue?) { self.reply = reply }
    func request(_ value: JSONValue, local: URL, relay: URL?, wingID: String, wingKey: String) async throws -> JSONValue {
        count += 1
        if let reply { return reply }; throw URLError(.cannotConnectToHost)
    }
}
private func fixturePairingLink(expires: Date = Date().addingTimeInterval(299), local: String = "http://192.168.1.40:6078", key: String = Data(repeating: 1, count: 32).base64EncodedString(), relay: String = "") throws -> String {
    let value: [String: String] = ["wing_id":"fixture-wing","name":"EhrlichMac","key":key,"local":local,"relay":relay,"id":"test-invitation-long-enough","secret":"apple bridge cedar delta ember frost","expires_at":ISO8601DateFormatter().string(from: expires)]
    var fields = value.mapValues(JSONValue.string); fields["v"] = .integer(1)
    let json = String(decoding: try JSONEncoder().encode(JSONValue.object(fields)), as: UTF8.self)
    return "wingthing://pair#" + json.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed)!
}

@Suite @MainActor struct PhonePairingTests {
    @Test func qrValidationAndExpiredSecretMakeNoRequest() async throws {
        let link = try fixturePairingLink(), q = try PairingInvitation(link: link)
        #expect(q.name == "EhrlichMac"); #expect(q.secret.split(separator: " ").count == 6); #expect(q.relay == nil)
        #expect(q.fingerprint.count == 64)
        #expect(throws: PairingError.expired) { try PairingInvitation(link: fixturePairingLink(expires: Date().addingTimeInterval(-1))) }
        for origin in ["http://example.com:6078", "https://192.168.1.40:6078", "http://192.168.1.40:6078/path", "http://user:pass@192.168.1.40:6078", "http://127.1:6078", "http://10.0.0.1.evil:6078"] {
            #expect(throws: PairingError.invalidQR) { try PairingInvitation(link: fixturePairingLink(local: origin)) }
        }
        #expect(throws: PairingError.invalidQR) { try PairingInvitation(link: fixturePairingLink(relay: "http://wingthing.ai")) }
        #expect(throws: PairingError.invalidQR) { try PairingInvitation(link: "wingthing://home#bad") }
    }
    @Test func lanThenTailnetRoutesNeedNoAccount() throws {
        let local = URL(string: "http://192.168.1.40:6078")!, tail = URL(string: "http://100.91.2.3:6078")!, magic = URL(string: "http://ehrlichmac.fixture.ts.net:6078")!
        #expect(PairingInvitation.localAllowed(tail)); #expect(PairingInvitation.localAllowed(magic))
        #expect(!PairingInvitation.localAllowed(URL(string: "http://100.128.2.3:6078")!))
        #expect(!PairingInvitation.localAllowed(URL(string: "http://ehrlichmac.fixture.ts.net.evil:6078")!))
        #expect(URLSessionPhoneWire.orderedDirect(tail, endpoints: [magic, local, tail]) == [local, tail, magic])
        let fields: JSONValue = .array([.integer(1), .string("fixture-wing"), .string("EhrlichMac"), .string(Data(repeating: 1, count: 32).base64EncodedString()), .array([.string(local.absoluteString), .string(tail.absoluteString), .string(magic.absoluteString)]), .string(""), .string("test-invitation-long-enough"), .string("apple bridge cedar delta ember frost"), .integer(Int64(Date().addingTimeInterval(299).timeIntervalSince1970))])
        let base64 = try JSONEncoder().encode(fields).base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        let qr = try PairingInvitation(link: "wingthing://pair#v1." + base64)
        #expect(qr.local == local); #expect(qr.endpoints == [tail, magic]); #expect(qr.relay == nil)
    }
    @Test func expiredUsedCancelledAndUnreachableHaveSpecificFailures() async throws {
        let q = try PairingInvitation(link: fixturePairingLink())
        for (code, expected) in [("expired", PairingError.expired), ("used", .used), ("cancelled", .cancelled), ("revoked", .revoked)] {
            let wire = PairingFailureWire(reply: .object(["error": .string(code)]))
            let pairing = try PhoneEnrollment(invitation: q, wire: wire, signingKey: PhoneSigningKey(useEnclave: false))
            do { try await pairing.start(); Issue.record("Accepted \(code)") } catch { #expect(error as? PairingError == expected) }
            #expect(await wire.count == 1)
        }
        let unreachable = try PhoneEnrollment(invitation: q, wire: PairingFailureWire(reply: nil), signingKey: PhoneSigningKey(useEnclave: false))
        do { try await unreachable.start(); Issue.record("Accepted unreachable wing") } catch { #expect(error as? PairingError == .unreachable("EhrlichMac")); #expect(error.localizedDescription.contains("wt login")) }
    }
    @Test func fingerprintMismatchStopsBeforePublicKeyOrCredential() async throws {
        let wire = PairingFailureWire(reply: .object(["key": .string(Data(repeating: 2, count: 32).base64EncodedString()), "message": .string("irrelevant")]))
        let pairing = try PhoneEnrollment(invitation: PairingInvitation(link: fixturePairingLink()), wire: wire, signingKey: PhoneSigningKey(useEnclave: false))
        do { try await pairing.start(); Issue.record("Accepted changed wing identity") } catch { #expect(error as? PairingError == .identityMismatch) }
        #expect(await wire.count == 1); #expect(pairing.emoji == nil)
    }
    @Test func emojiMismatchAndCancellationCannotConfirm() async throws {
        let wire = PairingFailureWire(reply: .object(["error": .string("cancelled")]))
        let pairing = try PhoneEnrollment(invitation: PairingInvitation(link: fixturePairingLink()), wire: wire, signingKey: PhoneSigningKey(useEnclave: false))
        do { _ = try await pairing.confirm(emoji: "🌲 🐙 🚲 🌙"); Issue.record("Confirmed without a matching PAKE emoji") } catch { #expect(error as? PairingError == .emojiMismatch) }
        #expect(await wire.count == 0)
        do { _ = try await pairing.status(); Issue.record("Got a credential without phone confirmation") } catch { #expect(error as? PairingError == .denied) }
    }
    @Test func encryptedCheckCodeAndCredentialKeyAreDomainBound() throws {
        let key = SymmetricKey(data: Data(repeating: 7, count: 32)), channel = PairingCipher(key: key)
        #expect(channel.emoji.split(separator: " ").count == 4)
        let encrypted = try channel.seal(.object(["credential": .string("fixture-secret")]))
        #expect(!encrypted.contains("fixture-secret")); #expect(try channel.open(encrypted)["credential"]?.string == "fixture-secret")
        let wrong = PairingCipher(key: SymmetricKey(data: Data(repeating: 8, count: 32)))
        #expect(throws: PairingError.badProof) { try wrong.open(encrypted) }
        let signing = try PhoneSigningKey(useEnclave: false), secret = PhoneSecret(credential: "fixture", signingKey: signing)
        let publicKey = try P256.Signing.PublicKey(x963Representation: Data(base64Encoded: signing.publicKey)!)
        #expect(try publicKey.isValidSignature(P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: secret.sign("wing A"))!), for: Data("wing A".utf8)))
        #expect(try !publicKey.isValidSignature(P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: secret.sign("wing A"))!), for: Data("wing B".utf8)))
    }
    @Test func metadataContainsNoDeviceSecretAndScopesEachWing() throws {
        let a = PairedWing(wingID: "ehrlichmac", name: "EhrlichMac", key: "pin-a", deviceID: "phone-a", local: URL(string: "http://192.168.1.40:6078")!, relay: nil)
        let b = PairedWing(wingID: "forge", name: "forge", key: "pin-b", deviceID: "phone-b", local: URL(string: "http://192.168.1.41:6078")!, relay: nil)
        #expect(a.id != b.id)
        let json = String(decoding: try JSONEncoder().encode([a,b]), as: UTF8.self)
        #expect(!json.contains("credential")); #expect(!json.contains("signingRepresentation"))
        let query = PhoneKeychainStore.query(a)
        #expect(query[kSecAttrSynchronizable as String] as? Bool == false)
        #expect(query[kSecAttrAccount as String] as? String != PhoneKeychainStore.query(b)[kSecAttrAccount as String] as? String)
    }
}

#if os(macOS)
private final class PairingPeer {
    let process = Process(), input = Pipe(), output = Pipe()
    init(binary: String) throws {
        process.executableURL = URL(fileURLWithPath: binary); process.standardInput = input; process.standardOutput = output; process.standardError = FileHandle.nullDevice
        try process.run()
    }
    func line() throws -> JSONValue {
        var bytes = Data()
        while let byte = try output.fileHandleForReading.read(upToCount: 1), !byte.isEmpty {
            if byte[0] == 10 { return try JSONDecoder().decode(JSONValue.self, from: bytes) }; bytes.append(byte)
            if bytes.count > 8192 { throw PairingError.protocolFailure }
        }; throw PairingError.protocolFailure
    }
    func control(_ action: String) throws { try input.fileHandleForWriting.write(contentsOf: Data((action + "\n").utf8)); let reply = try line(); guard reply["ok"] == .bool(true) else { throw PairingError.protocolFailure } }
    deinit { try? input.fileHandleForWriting.write(contentsOf: Data("stop\n".utf8)); try? input.fileHandleForWriting.close(); if process.isRunning { process.terminate() }; process.waitUntilExit() }
}
@Suite @MainActor struct PhonePairingInteroperabilityTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["WT_PAIRING_TEST_PEER"] != nil))
    func nativePAKEAndDeviceProofTalkToRealGoWing() async throws {
        let peer = try PairingPeer(binary: ProcessInfo.processInfo.environment["WT_PAIRING_TEST_PEER"]!)
        let start = try peer.line(); let q = try PairingInvitation(link: start["link"]!.string!)
        let enrollment = try PhoneEnrollment(invitation: q, signingKey: PhoneSigningKey(useEnclave: false))
        try await enrollment.start(); #expect(enrollment.emoji != nil)
        #expect(try await enrollment.confirm(emoji: enrollment.emoji!) == nil)
        try peer.control("allow")
        let paired = try await enrollment.status(); #expect(paired != nil)
        let (wing, secret) = paired!
        #expect(wing.name == "EhrlichMac"); #expect(wing.wingID == "fixture-main-wing")
        let client = PhoneConnection(wing: wing, secret: secret)
        try await client.connect()
        async let firstList = client.sessions()
        async let secondList = client.sessions()
        let (first, second) = try await (firstList, secondList)
        #expect(first.count == second.count)
        let sessions = try await client.sessions()
        #expect(sessions.count == 1); #expect(sessions[0].blocked); #expect(sessions[0].unread)
        _ = try await client.command("attach", session: sessions[0].id)
        #expect(try await client.command("input", session: sessions[0].id, input: "hello\r")["ok"] == .bool(true))
        #expect(try await client.command("read", session: sessions[0].id)["output"]?.string == "fixture output\r\n")
        try peer.control("revoke")
        do { _ = try await client.command("input", session: sessions[0].id, input: "never delivered"); Issue.record("Lost phone still controlled a session") } catch { #expect(error as? PairingError == .revoked) }
    }
}
#endif
