import Foundation
import Testing
@testable import WingthingCore

actor FixtureWire: HomeWire {
    var calls: [URL] = []
    var requests: [JSONValue] = []
    var user = "fixture-user"
    var key = wingPublic
    var controlReply: JSONValue = .object(["ok": .bool(true)])
    var operationReplies: [String: JSONValue] = [:]
    var wrongRequestID = false
    var echoPromptIDs = true
    func configure(user: String? = nil, key: String? = nil, reply: JSONValue? = nil, wrongRequestID: Bool? = nil, echoPromptIDs: Bool? = nil) {
        if let user { self.user = user }; if let key { self.key = key }; if let reply { controlReply = reply }; if let wrongRequestID { self.wrongRequestID = wrongRequestID }
        if let echoPromptIDs { self.echoPromptIDs = echoPromptIDs }
    }
    func route(_ replies: [String: JSONValue]) { operationReplies = replies }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        calls.append(url)
        guard bearer == "synthetic-existing-token" else { return .init(status: 401, data: Data()) }
        let value: JSONValue
        switch url.path {
        case "/health": value = .object(["ok": .bool(true)])
        case "/auth/check": value = .object(["ok": .bool(true), "user_id": .string(user)])
        case "/api/app/wings": value = .array([.object(["wing_id": .string("mac"), "public_key": .string(key)])])
        default: throw ClientError.response("Unexpected API call: \(url.path)")
        }
        return HTTPReply(status: 200, data: try JSONEncoder().encode(value))
    }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        calls.append(url)
        guard bearer == "synthetic-existing-token" else { throw ClientError.response("Fixture token missing") }
        let envelope = try JSONDecoder().decode(JSONValue.self, from: request)
        let sender = try unwrap(envelope["sender_pub"]?.string)
        let wing = try TunnelIdentity(rawPrivateKey: Data(base64Encoded: wingPrivate)!)
        let cipher = try wing.cipher(peerPublicKey: sender)
        let inner = try JSONDecoder().decode(JSONValue.self, from: cipher.open(try unwrap(envelope["payload"]?.string)))
        requests.append(inner)
        expectEqual(envelope["type"], .string("tunnel.req"))
        expectEqual(envelope["purpose"], .string("wing-control"))
        expectEqual(envelope["wing_id"], .string("mac"))
        let operation = inner["operation"]?.string ?? ""
        let target = inner["arguments"]?["conversation_id"]?.string ?? inner["arguments"]?["session"]?.string ?? ""
        var result = operationReplies[operation + ":" + target] ?? controlReply
        if echoPromptIDs, operation == "session_prompt", case .object(var fields)? = result["receipt"], case .object(var reply) = result {
            if fields["request_id"] == nil { fields["request_id"] = inner["arguments"]?["request_id"] }
            if fields["session_id"] == nil { fields["session_id"] = inner["arguments"]?["session"] }
            reply["receipt"] = .object(fields); result = .object(reply)
        }
        let response: JSONValue = .object(["type": .string("tunnel.res"), "request_id": .string(wrongRequestID ? "old-request" : try unwrap(envelope["request_id"]?.string)), "payload": .string(try cipher.seal(JSONEncoder().encode(result)))])
        return try JSONEncoder().encode(response)
    }
}

@Suite @MainActor struct ProtocolTests {
    @Test func testNativeExistingBearerPathAndEncryptedWingQualifiedControl() async throws {
        let home = try profile(), wire = FixtureWire()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: wire)
        let evidence = try await client.verifyHome()
        expectEqual(evidence.userID, home.expectedUserID)
        let result = try await client.control(reference(home), operation: "conversation_read", arguments: ["conversation_id": .string("root")])
        expectEqual(result["ok"], .bool(true))
        let calls = await wire.calls
        expectEqual(calls.map(\.path), ["/health", "/auth/check", "/api/app/wings", "/ws/relay"])
        expectTrue(calls.allSatisfy { $0.host == home.origin.host })
        expectFalse(calls.contains { $0.path == "/api/app/me" })
        let requests = await wire.requests
        expectEqual(requests[0]["arguments"]?["conversation_id"], .string("root"))
        do { _ = try await client.control(reference(home, wing: "unverified-remote"), operation: "conversation_read", arguments: [:]); fail("Unpinned wing accepted") } catch {}
        let requestCount = await wire.requests.count
        expectEqual(requestCount, 1)
    }

    @Test func testChangedUserWingKeyAndRequestNeverFallBackOrApply() async throws {
        let home = try profile(), wire = FixtureWire()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: wire)
        await wire.configure(user: "wrong-user")
        do { _ = try await client.verifyHome(); fail("Wrong account accepted") } catch {}
        let initialCalls = await wire.calls
        expectEqual(initialCalls.count, 2)
        await wire.configure(user: home.expectedUserID, key: clientPrivate)
        do { _ = try await client.verifyHome(); fail("Changed wing identity accepted") } catch {}
        await wire.configure(key: wingPublic)
        _ = try await client.verifyHome()
        await wire.configure(wrongRequestID: true)
        do { _ = try await client.control(reference(home), operation: "conversation_read", arguments: [:]); fail("Stale response accepted") } catch { expectEqual(error as? ClientError, .staleReference) }
        await wire.configure(reply: .object(["error": .string("passkey_required")]), wrongRequestID: false)
        do { _ = try await client.control(reference(home), operation: "conversation_read", arguments: [:]); fail("Missing native passkey treated as ready") } catch { expectTrue(error.localizedDescription.contains("passkey")) }
        let before = await wire.requests.count
        await client.disconnect()
        do { _ = try await client.control(reference(home), operation: "conversation_read", arguments: [:]); fail("Offline control accepted") } catch { expectEqual(error as? ClientError, .offline) }
        let after = await wire.requests.count
        expectEqual(before, after)
    }

    @Test func testUncertainInputCheckReusesExactExecutionAndReceiptIDWithoutReconnectResend() async throws {
        let home = try profile(), wire = FixtureWire()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: wire)
        _ = try await client.verifyHome()
        let execution = ExecutionReference(conversation: reference(home), sessionID: "same-session", providerSessionID: "native-id")
        var pending = try PendingInput(execution: execution, input: "Inspect the child evidence")
        await wire.configure(reply: .object(["receipt": .object(["status": .string("accepted")])]))
        pending = try await client.submit(pending)
        expectEqual(pending.delivery, .unconfirmed)
        let before = await wire.requests.count
        _ = try await client.verifyHome()
        let after = await wire.requests.count
        expectEqual(before, after) // Reconnect never resends.
        await wire.configure(reply: .object(["receipt": .object(["status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)])]))
        pending = try await client.submit(pending) // Explicit Check receipt action.
        expectEqual(pending.delivery, .nativeReceiptObserved)
        let requests = await wire.requests
        expectEqual(requests.count, 2)
        expectEqual(requests[0]["arguments"], requests[1]["arguments"])
        expectEqual(requests[1]["arguments"]?["session"], .string("same-session"))
    }
    @Test func encryptedPromptCorrelationRejectsMissingOrReplayedInnerIDs() async throws {
        let home = try profile(), wire = FixtureWire()
        let client = try HomeClient(profile: home, existingBearer: "synthetic-existing-token", wire: wire)
        _ = try await client.verifyHome()
        let intent = try PendingInput(execution: ExecutionReference(conversation: reference(home), sessionID: "same-session"), input: "hello")
        let good: [String: JSONValue] = ["request_id": .string(intent.id.uuidString), "session_id": .string("same-session"),
            "status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)]
        for key in ["request_id", "session_id"] {
            for value: JSONValue? in [nil, .string("earlier-success")] {
                var fields = good; fields[key] = value
                await wire.configure(reply: .object(["receipt": .object(fields)]), echoPromptIDs: false)
                do { _ = try await client.submit(intent); fail("Accepted unrelated encrypted success") }
                catch {
                    if value == nil { expectTrue(error.localizedDescription.contains("Update Wingthing")) }
                    else { expectEqual(error as? ClientError, .staleReference) }
                }
            }
        }
        await wire.configure(reply: .object(["receipt": .object(good)]))
        expectEqual(try await client.submit(intent).delivery, .nativeReceiptObserved)
    }

}
