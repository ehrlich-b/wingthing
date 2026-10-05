import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private let previewOrigin = URL(string: "http://127.0.0.1:61428")!

private func preview(origin: URL = previewOrigin, user: String = "local", wing: String = "mac", key: String = wingPublic) throws -> HomeProfile {
    try HomeProfile.localPreview(origin: origin, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key)
}

private func nativeEvent(_ sequence: Int64, type: String, content: JSONValue, source: String = "claude_transcript") -> JSONValue {
    .object(["sequence": .integer(sequence), "type": .string("provider_event"), "source": .string(source),
             "raw": .object(["type": .string(type), "message": .object(["role": .string(type), "content": content])])])
}

// A credential-free preview roost: real-shaped /api/app/me and /api/app/wings,
// and an encrypted wing that pages session_read exactly as the egg does.
private actor PreviewWire: HomeWire {
    var calls: [URL] = []
    var bearers: [String] = []
    var requests: [JSONValue] = []
    var me: [String: JSONValue] = ["id": .string("local"), "release_channel": .string("preview"), "provider": .string("local")]
    var wings: [JSONValue] = [.object(["wing_id": .string("mac"), "public_key": .string(wingPublic), "user_id": .string("local")])]
    var events: [JSONValue] = []
    var pending = false
    var stallAt: Int64?
    var replaceProviderAfter: Int64?
    var routes: [String: JSONValue] = [:]
    func route(_ routes: [String: JSONValue]) { self.routes = routes }
    func set(me: [String: JSONValue]? = nil, wings: [JSONValue]? = nil, events: [JSONValue]? = nil, pending: Bool? = nil, stallAt: Int64? = nil, replaceProviderAfter: Int64? = nil) {
        if let me { self.me = me }; if let wings { self.wings = wings }; if let events { self.events = events }; if let pending { self.pending = pending }
        self.stallAt = stallAt; self.replaceProviderAfter = replaceProviderAfter
    }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        calls.append(url); bearers.append(bearer)
        guard bearer.isEmpty else { return .init(status: 400, data: Data()) }
        let value: JSONValue
        switch url.path {
        case "/api/app/me": value = .object(me)
        case "/api/app/wings": value = .array(wings)
        default: return .init(status: 404, data: Data())
        }
        return HTTPReply(status: 200, data: try JSONEncoder().encode(value))
    }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        calls.append(url); bearers.append(bearer)
        let envelope = try JSONDecoder().decode(JSONValue.self, from: request)
        let cipher = try TunnelIdentity(rawPrivateKey: Data(base64Encoded: wingPrivate)!).cipher(peerPublicKey: try unwrap(envelope["sender_pub"]?.string))
        let inner = try JSONDecoder().decode(JSONValue.self, from: cipher.open(try unwrap(envelope["payload"]?.string)))
        requests.append(inner)
        let operation = inner["operation"]?.string ?? ""
        let target = inner["arguments"]?["conversation_id"]?.string ?? ""
        let result: JSONValue
        if let routed = routes[operation + ":" + target] { result = routed }
        else if operation == "session_read" { result = try page(inner["arguments"] ?? .null) }
        else { result = .object(["ok": .bool(true)]) }
        let reply: JSONValue = .object(["type": .string("tunnel.res"), "request_id": try unwrap(envelope["request_id"]), "payload": .string(try cipher.seal(JSONEncoder().encode(result)))])
        return try JSONEncoder().encode(reply)
    }
    private func page(_ arguments: JSONValue) throws -> JSONValue {
        guard case .integer(let after)? = arguments["after_cursor"], case .integer(let limit)? = arguments["limit"] else { throw ClientError.response("bad page") }
        let sequences = events.compactMap { event -> Int64? in if case .integer(let n)? = event["sequence"] { n } else { nil } }
        let head = sequences.max() ?? 0
        var delivered: [JSONValue] = [], cursor = after, more = pending
        if stallAt.map({ after >= $0 }) != true {
            for (event, sequence) in zip(events, sequences) where sequence > after {
                if Int64(delivered.count) < limit { delivered.append(event); cursor = sequence } else { more = true }
            }
        } else { more = true }
        let provider = replaceProviderAfter.map { after >= $0 } == true ? "replacement-id" : "native-id"
        return .object(["lifecycle": .object(["session_id": .string("same-session"), "agent": .string("claude"), "provider_session_id": .string(provider), "state": .string("idle"),
                                              "state_source": .string("claude_hook"), "ready": .bool(true), "process_alive": .bool(true), "cursor": .integer(cursor),
                                              "head_cursor": .integer(head), "has_more": .bool(more), "events": .array(delivered)])])
    }
}

private func rejects(_ body: () async throws -> Void) async -> Bool {
    do { try await body(); return false } catch { return true }
}

private func execution(_ home: HomeProfile) -> ExecutionReference {
    ExecutionReference(conversation: reference(home), sessionID: "same-session")
}

private struct ReleaseDecoded: Decodable {
    let profile: HomeProfile
    init(from decoder: any Decoder) throws { profile = try HomeProfile(decoding: decoder, compiled: false) }
}

@Suite struct LocalPreviewPartnerTests {
    @Test @MainActor func simulatorModelInspectsThePinnedNativeTreeWithoutSendOrStop() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-preview-ui-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = PreviewWire()
        let root = conversationJSON("root"), child = conversationJSON("child", parent: "root")
        await wire.route(["conversation_list:": .object(["conversations": .array([root, child])]),
                          "conversation_read:root": .object(["conversation": root, "tasks": .array([.object(["conversation": root]), .object(["conversation": child])])])])
        await wire.set(events: [nativeEvent(1, type: "assistant", content: .string("A real-shaped native reply"))])
        let env = ["WT_IOS_PREVIEW_ENABLED": "1", "WT_IOS_PREVIEW_ORIGIN": previewOrigin.absoluteString, "WT_IOS_PREVIEW_USER": "local",
                   "WT_IOS_PREVIEW_WING": "mac", "WT_IOS_PREVIEW_KEY": wingPublic, "WT_IOS_PREVIEW_ROOT": "root"]
        await model.inspectLocalPreview(environment: env, wire: wire)
        expectTrue(model.connected); expectTrue(model.inspectionOnly); expectEqual(model.parent?.reference.conversationID, "root")
        expectEqual(model.tasks.count, 2); expectTrue(model.items.contains { $0.content == "A real-shaped native reply" })
        expectFalse(model.inputReady); expectFalse(model.canStop); expectFalse(model.canRetryStop)
        model.draft = "Must never leave this phone"
        await model.sendOrCheck(); await model.requestStop(); await model.retryStop(); await model.refresh()
        let requests = await wire.requests
        expectTrue(requests.allSatisfy { ["conversation_list", "conversation_read", "session_read"].contains($0["operation"]?.string ?? "") })
        expectTrue(await wire.bearers.allSatisfy(\.isEmpty))
    }

    @Test @MainActor func simulatorInspectionIsOptInAndRejectsCredentialOrIncompleteMetadataBeforeNetwork() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-preview-refusal-\(UUID())")
        defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = PreviewWire()
        await model.inspectLocalPreview(environment: [:], wire: wire)
        expectNil(model.profile); expectEqual(model.phase, .notConfigured)
        let fields = ["WT_IOS_PREVIEW_ENABLED": "1", "WT_IOS_PREVIEW_ORIGIN": previewOrigin.absoluteString, "WT_IOS_PREVIEW_USER": "local",
                      "WT_IOS_PREVIEW_WING": "mac", "WT_IOS_PREVIEW_KEY": wingPublic, "WT_IOS_PREVIEW_ROOT": "root"]
        var secret = fields; secret["WT_IOS_PREVIEW_BEARER"] = "synthetic-secret"
        var missing = fields; missing.removeValue(forKey: "WT_IOS_PREVIEW_KEY")
        var wrongOrigin = fields; wrongOrigin["WT_IOS_PREVIEW_ORIGIN"] = "http://localhost:61428"
        for env in [secret, missing, wrongOrigin] {
            await model.inspectLocalPreview(environment: env, wire: wire)
            expectFalse(model.connected); expectNil(model.profile)
        }
        expectTrue(await wire.calls.isEmpty)
        expectFalse(FileManager.default.fileExists(atPath: directory.path))
    }

    @Test func onlyLiteralLoopbackWithExplicitPortAndLocalUserIsAccepted() throws {
        let home = try preview()
        expectEqual(home.mode, .localPreview); expectEqual(home.transport, .localNetwork); expectNil(home.homeRoostID)
        let tunnel = try home.tunnelURL(wingID: "mac")
        expectEqual(tunnel.scheme, "ws"); expectEqual(tunnel.host, "127.0.0.1"); expectEqual(tunnel.port, 61428)
        expectEqual(try home.endpoint("/api/app/me").absoluteString, "http://127.0.0.1:61428/api/app/me")
        expectEqual(try preview(origin: URL(string: "http://127.0.0.1:61428/")!).mode, .localPreview)

        for origin in ["http://127.0.0.1", "http://localhost:61428", "http://127.0.0.2:61428", "http://[::1]:61428", "http://0.0.0.0:61428",
                       "http://192.168.1.20:61428", "https://127.0.0.1:61428", "ws://127.0.0.1:61428", "http://127.0.0.1:61428/app",
                       "http://local:pw@127.0.0.1:61428", "http://127.0.0.1:61428?next=1", "http://127.0.0.1:61428#x", "http://127.0.0.1:0",
                       "http://127.0.0.1:061428", "HTTP://127.0.0.1:61428", "http://2130706433:61428"] {
            expectThrows(try preview(origin: URL(string: origin)!))
        }
        for user in ["", "Local", "local ", "fixture-user"] { expectThrows(try preview(user: user)) }
        expectThrows(try preview(wing: ""))
        expectThrows(try preview(key: Data(repeating: 7, count: 31).base64EncodedString()))
        // Construction is impossible when the mode is not compiled in.
        expectThrows(try HomeProfile(localPreview: UUID(), origin: previewOrigin, expectedUserID: "local", homeWingID: "mac", homeWingPublicKey: wingPublic, compiled: false))
    }

    @Test func strictRemoteContractIsUnchanged() throws {
        expectThrows(try HomeProfile(origin: previewOrigin, transport: .localNetwork, expectedUserID: "local", homeWingID: "mac", homeWingPublicKey: wingPublic))
        let remote = try profile()
        expectEqual(remote.mode, .remote); expectEqual(try remote.tunnelURL(wingID: "mac").scheme, "wss")
        let encoded = try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(remote))
        expectNil(encoded["mode"]) // Saved remote profiles are byte-for-byte the old shape.
        expectEqual(try JSONDecoder().decode(HomeProfile.self, from: JSONEncoder().encode(remote)), remote)
        expectEqual(try JSONDecoder().decode(ReleaseDecoded.self, from: JSONEncoder().encode(remote)).profile, remote)
        // The existing-bearer client still demands a token and refuses a preview profile.
        expectThrows(try HomeClient(profile: remote, existingBearer: ""))
        expectThrows(try HomeClient(profile: try preview(), existingBearer: "synthetic-existing-token"))
        expectThrows(try HomeClient(profile: try preview(), existingBearer: ""))

        var request = URLRequest(url: previewOrigin)
        URLSessionHomeWire.authorize(&request, bearer: "")
        expectNil(request.value(forHTTPHeaderField: "Authorization"))
        URLSessionHomeWire.authorize(&request, bearer: "synthetic-existing-token")
        expectEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-existing-token")
    }

    @Test func savedPreviewProfileIsRevalidatedAndFailsInRelease() throws {
        let home = try preview()
        let data = try JSONEncoder().encode(home)
        let encoded = try JSONDecoder().decode(JSONValue.self, from: data)
        expectEqual(encoded["mode"], .string("localPreview"))
        expectEqual(try JSONDecoder().decode(HomeProfile.self, from: data), home)
        expectThrows(try JSONDecoder().decode(ReleaseDecoded.self, from: data))

        guard case .object(let fields) = encoded else { fail("Profile is not an object"); return }
        let tampered: [(String, JSONValue)] = [("origin", .string("https://127.0.0.1:61428")), ("origin", .string("http://192.168.1.20:61428")),
                                              ("expectedUserID", .string("someone")), ("transport", .string("userOwnedEndpoint")),
                                              ("homeRoostID", .string("root")), ("homeWingPublicKey", .string("short")), ("mode", .string("unknown"))]
        for (key, value) in tampered {
            var copy = fields; copy[key] = value
            expectThrows(try JSONDecoder().decode(HomeProfile.self, from: JSONEncoder().encode(JSONValue.object(copy))))
        }
    }

    @Test func credentialsAreRefusedBeforeAnyNetwork() async throws {
        let wire = PreviewWire()
        for credential in ["synthetic-existing-token", " ", "\n"] {
            expectThrows(try HomeClient(localPreview: try preview(), suppliedCredential: credential, wire: wire))
        }
        expectThrows(try HomeClient(localPreview: try profile(), wire: wire))
        let calls = await wire.calls
        expectTrue(calls.isEmpty)
    }

    @Test func verifiesRealAccountAndPinnedWingThenUsesEncryptedControlWithoutAuthorization() async throws {
        let home = try preview(), wire = PreviewWire()
        let client = try HomeClient(localPreview: home, wire: wire)
        let evidence = try await client.verifyHome()
        expectEqual(evidence.userID, "local"); expectEqual(evidence.wings.map(\.wingID), ["mac"])
        _ = try await client.control(reference(home), operation: "conversation_read", arguments: ["conversation_id": .string("root")])
        let calls = await wire.calls, bearers = await wire.bearers
        expectEqual(calls.map(\.path), ["/api/app/me", "/api/app/wings", "/ws/relay"])
        expectTrue(calls.allSatisfy { $0.host == "127.0.0.1" && $0.port == 61428 })
        expectEqual(calls.last?.scheme, "ws")
        expectTrue(bearers.allSatisfy(\.isEmpty))
        let requests = await wire.requests
        expectEqual(requests.map { $0["operation"] }, [.string("conversation_read")])
        // Launch, prompt-free mutation and stop stay unreachable here.
        expectTrue(await rejects { _ = try await client.control(reference(home), operation: "conversation_launch", arguments: [:]) })
    }

    @Test func changedAccountChannelProviderOrKeyNeverReachesTheWing() async throws {
        let good: [String: JSONValue] = ["id": .string("local"), "release_channel": .string("preview"), "provider": .string("local")]
        var accounts: [[String: JSONValue]] = []
        for (key, value) in [("id", "someone"), ("release_channel", "stable"), ("provider", "github")] { var copy = good; copy[key] = .string(value); accounts.append(copy) }
        var copy = good; copy["release_channel"] = nil; accounts.append(copy)
        let entry: (String, String) -> JSONValue = { .object(["wing_id": .string($0), "public_key": .string($1)]) }
        let rosters: [[JSONValue]] = [[], [entry("mac", clientPrivate)], [entry("mac", wingPublic), entry("mac", wingPublic)], [entry("linux", wingPublic)]]
        let cases: [([String: JSONValue], [JSONValue])] = accounts.map { ($0, [entry("mac", wingPublic)]) } + rosters.map { (good, $0) }
        for (me, wings) in cases {
            let home = try preview(), wire = PreviewWire()
            await wire.set(me: me, wings: wings)
            let client = try HomeClient(localPreview: home, wire: wire)
            expectTrue(await rejects { _ = try await client.verifyHome() })
            expectTrue(await rejects { _ = try await client.control(reference(home), operation: "conversation_read", arguments: ["conversation_id": .string("root")]) })
            let calls = await wire.calls
            expectFalse(calls.contains { $0.path == "/ws/relay" }); expectFalse(calls.contains { $0.path == "/auth/check" })
        }
    }

    @Test func historyPagesOneEventAtATimeToHeadAndKeepsOnlyNativeText() async throws {
        let home = try preview(), wire = PreviewWire()
        await wire.set(events: [
            nativeEvent(1, type: "user", content: .string("Summarize the child")),
            nativeEvent(2, type: "assistant", content: .array([.object(["type": .string("thinking"), "thinking": .string("private reasoning")]),
                                                               .object(["type": .string("tool_use"), "name": .string("Bash"), "input": .object(["command": .string("cat secrets")])])])),
            nativeEvent(3, type: "user", content: .array([.object(["type": .string("tool_result"), "content": .string("tool output")])])),
            .object(["sequence": .integer(4), "type": .string("pty_output"), "source": .string("pty"), "text": .string("terminal screen")]),
            nativeEvent(5, type: "assistant", content: .array([.object(["type": .string("text"), "text": .string("Final answer")])])),
            nativeEvent(6, type: "assistant", content: .array([.object(["type": .string("text"), "text": .string("PTY lookalike")])]), source: "pty"),
        ])
        let client = try HomeClient(localPreview: home, wire: wire)
        _ = try await client.verifyHome()
        let history = try await client.readToHead(execution(home))
        expectEqual(history.pages, 6); expectEqual(history.events.map(\.sequence), [1, 2, 3, 4, 5, 6])
        expectEqual(history.lifecycle.cursor, 6); expectEqual(history.lifecycle.headCursor, 6)
        expectEqual(history.nativeMessages, [NativeMessage(sequence: 1, role: "user", text: "Summarize the child"), NativeMessage(sequence: 5, role: "assistant", text: "Final answer")])
        expectEqual(history.finalAssistantText, "Final answer")
        let pages = await wire.requests
        expectTrue(pages.allSatisfy { $0["operation"] == .string("session_read") && $0["arguments"]?["limit"] == .integer(1) })
        expectEqual(pages.map { $0["arguments"]?["after_cursor"] }, (Int64(0)...5).map { JSONValue.integer($0) })

        // A pending journal at head ends cleanly; resuming from a cursor works.
        await wire.set(pending: true)
        let resumed = try await client.readToHead(execution(home), after: 4)
        expectEqual(resumed.events.map(\.sequence), [5, 6]); expectEqual(resumed.pages, 3)
    }

    @Test func historyBoundsStallsAndReplacedProviderAreErrors() async throws {
        let home = try preview(), wire = PreviewWire()
        await wire.set(events: (1...5).map { nativeEvent($0, type: "user", content: .string("m\($0)")) })
        let client = try HomeClient(localPreview: home, wire: wire)
        _ = try await client.verifyHome()
        expectTrue(await rejects { _ = try await client.readToHead(execution(home), maxPages: 3) })
        expectTrue(await rejects { _ = try await client.readToHead(execution(home), timeLimit: .zero) })
        await wire.set(stallAt: 2)
        expectTrue(await rejects { _ = try await client.readToHead(execution(home)) })
        await wire.set(replaceProviderAfter: 2)
        do { _ = try await client.readToHead(execution(home)); fail("Replaced provider accepted") } catch { expectEqual(error as? ClientError, .staleReference) }
        expectTrue(await rejects { _ = try await client.readToHead(execution(home), after: -1) })
    }
}
