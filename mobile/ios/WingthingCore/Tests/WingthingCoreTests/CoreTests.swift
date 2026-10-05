import CryptoKit
import Foundation
import Testing
@testable import WingthingCore

// Deliberately synthetic fixed keys, generated independently through the
// existing browser's noble X25519/HKDF/AES-GCM implementation.
let wingPublic = "WGmv9FBUlzLLqu1eXfmzCm2jHLDldCutWtShp2jxpns="
let wingPrivate = "ISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0A="
let clientPrivate = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="

func profile(_ id: UUID = UUID(), user: String = "fixture-user") throws -> HomeProfile {
    try HomeProfile(id: id, origin: URL(string: "https://personal-home.example")!, transport: .userOwnedEndpoint, expectedUserID: user, homeWingID: "mac", homeWingPublicKey: wingPublic)
}
func reference(_ home: HomeProfile, wing: String = "mac", task: String = "root") -> ConversationReference {
    ConversationReference(profileID: home.id, userID: home.expectedUserID, wingID: wing, conversationID: task)
}
func lifecycle(_ state: String = "working", alive: Bool = true, source: String = "claude_hook", cursor: Int64 = 1, events: [JSONValue] = []) throws -> SessionLifecycle {
    let json: JSONValue = .object(["session_id": .string("same-session"), "agent": .string("claude"), "provider_session_id": .string("native-id"), "state": .string(state), "state_source": .string(source), "ready": .bool(true), "process_alive": .bool(alive), "cursor": .integer(cursor), "events": .array(events)])
    return try JSONDecoder().decode(SessionLifecycle.self, from: JSONEncoder().encode(json))
}

@Suite struct CoreTests {
    @Test func testCryptoMatchesRealBrowserContractAndSeparatesPTYDomain() throws {
        let client = try TunnelIdentity(rawPrivateKey: Data(base64Encoded: clientPrivate)!)
        expectEqual(client.publicKey, "B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9/AsrhtHHw=")
        let cipher = try client.cipher(peerPublicKey: wingPublic)
        let payload = "AAECAwQFBgcICQoLwvf7WD0Q94BzQHmuXo2j9RBrNHbF2S7kcfY2HYhLoSzMvmg6ZtTjYQeAC/MDhrYiWNLO5bSUOyidEZ+gua1SkDco+SCbWJhDzQtqgFZf0znLb3aT5K1Yir0+uXjAn7Bhpi4u/TveqTpVFf9j2/4uRV1aBGln3TBcVw=="
        let plain = try cipher.open(payload)
        expectEqual(String(decoding: plain, as: UTF8.self), "{\"type\":\"session.control\",\"operation\":\"conversation_read\",\"arguments\":{\"conversation_id\":\"fixture-root\"}}")
        let wing = try TunnelIdentity(rawPrivateKey: Data(base64Encoded: wingPrivate)!)
        let peer = try wing.cipher(peerPublicKey: client.publicKey)
        expectEqual(try peer.open(cipher.seal(plain)), plain)
        expectThrows(try client.cipher(peerPublicKey: wingPublic, domain: "wt-pty").open(payload))
        expectThrows(try cipher.open("short"))
    }

    @Test func testExactHomeIdentityAndNoOriginOrVendorFallback() throws {
        let home = try profile()
        expectEqual(try home.tunnelURL(wingID: "wing a&other").host, "personal-home.example")
        expectEqual(URLComponents(url: try home.tunnelURL(wingID: "wing a&other"), resolvingAgainstBaseURL: false)?.queryItems?.first?.value, "wing a&other")
        expectThrows(try HomeProfile(origin: URL(string: "https://personal-home.example/path")!, transport: .localNetwork, expectedUserID: "u", homeWingID: "w", homeWingPublicKey: wingPublic))
        expectThrows(try HomeProfile(origin: URL(string: "http://personal-home.example")!, transport: .localNetwork, expectedUserID: "u", homeWingID: "w", homeWingPublicKey: wingPublic))
        expectThrows(try home.endpoint("//other.example"))
        expectThrows(try HomeEvidence(profileID: home.id, userID: "other", wings: [.init(wingID: "mac", publicKey: wingPublic)]).validate(home))
        expectThrows(try HomeEvidence(profileID: home.id, userID: home.expectedUserID, wings: [.init(wingID: "mac", publicKey: clientPrivate)]).validate(home))
        expectNoThrow(try HomeEvidence(profileID: home.id, userID: home.expectedUserID, wings: [.init(wingID: "mac", publicKey: wingPublic)]).validate(home))
        expectNil(home.homeRoostID)
        let loopback = try HomeProfile(origin: URL(string: "https://127.0.0.1:8181")!, transport: .localNetwork, expectedUserID: "u", homeWingID: "w", homeWingPublicKey: wingPublic)
        expectTrue(loopback.phoneReachabilityNote!.contains("phone itself"))
    }

    @Test func testNativeTranscriptReplayToolsAndExitAreNotTaskCompletion() throws {
        let home = try profile(), target = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let event: JSONValue = .object(["sequence": .integer(1), "type": .string("provider_record"), "raw": .object(["type": .string("assistant"), "message": .object(["role": .string("assistant"), "content": .array([.object(["type": .string("tool_use"), "name": .string("Read"), "input": .object(["path": .string("README.md")])])])])])])
        var transcript = TranscriptState()
        try transcript.apply(lifecycle(events: [event]), target: target)
        try transcript.apply(lifecycle(events: [event]), target: target)
        expectEqual(transcript.events.count, 1)
        expectEqual(TranscriptItem.items(from: transcript.events[0])[0].title, "Tool: Read")
        expectEqual(transcript.status(connected: true), .working)
        expectFalse(transcript.canSend(connected: true))
        try transcript.apply(lifecycle("completed", alive: false, source: "egg_process", cursor: 2), target: target)
        expectEqual(transcript.status(connected: true), .archived)
        try transcript.apply(lifecycle("completed", alive: true, cursor: 3), target: target)
        expectEqual(transcript.status(connected: true), .turnCompleted)
        expectTrue(transcript.canSend(connected: true))
        try transcript.apply(lifecycle(cursor: 1), target: target)
        expectEqual(transcript.cursor, 3)
        let restored = try JSONDecoder().decode(TranscriptState.self, from: JSONEncoder().encode(transcript))
        expectEqual(restored.events, transcript.events)
        expectEqual(restored.status(connected: true), .unknown)
        expectEqual(transcript.status(connected: false), .offline)
        transcript.markUnavailable(); expectEqual(transcript.status(connected: true), .unknown)
        expectThrows(try transcript.apply(lifecycle(), target: .init(conversation: reference(home), sessionID: "different")))
        expectThrows(try transcript.apply(lifecycle(), target: .init(conversation: reference(home), sessionID: "same-session", providerSessionID: "replaced")))
    }

    @Test func testUnknownNativeApprovalNeverOffersAutoApproval() throws {
        let home = try profile(), target = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        let attention = try unwrap(HumanAttention(execution: target, lifecycle: lifecycle("needs_input")))
        expectFalse(attention.nativeApprovalDecisionSupported)
        expectEqual(attention.execution, target)
        expectNil(HumanAttention(execution: target, lifecycle: try lifecycle("needs_input", alive: false)))
        expectNil(HumanAttention(execution: target, lifecycle: try lifecycle("needs_input", source: "unsupported")))
        expectNil(HumanAttention(execution: .init(conversation: reference(home), sessionID: "same-session", providerSessionID: "another-provider"), lifecycle: try lifecycle("needs_input")))
    }
}
