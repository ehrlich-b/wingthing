import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private let syntheticToken = "synthetic-existing-token"
private let homeOrigin = "https://personal-home.example"

// Wraps the encrypted fixture wing. Records every attempted path (including
// ones that fail offline) and can hold one call open to interleave work.
private actor FlowWire: HomeWire {
    let fixture = FixtureWire()
    private(set) var attempts: [String] = []
    private var offline = false
    private let holdAt: Int?
    private var held: CheckedContinuation<Void, Never>?
    private var arrival: CheckedContinuation<Void, Never>?
    private var arrived = false
    init(holdAt: Int? = nil, offline: Bool = false) { self.holdAt = holdAt; self.offline = offline }
    func setOffline(_ value: Bool) { offline = value }
    func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        try await pass(url)
        return try await fixture.get(url, bearer: bearer)
    }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        try await pass(url)
        return try await fixture.exchange(url, bearer: bearer, request: request)
    }
    private func pass(_ url: URL) async throws {
        attempts.append(url.path)
        if attempts.count == holdAt {
            arrived = true; arrival?.resume(); arrival = nil
            await withCheckedContinuation { held = $0 }
        }
        if offline { throw URLError(.notConnectedToInternet) }
    }
    func waitUntilHeld() async {
        guard !arrived else { return }
        await withCheckedContinuation { arrival = $0 }
    }
    func release() { held?.resume(); held = nil }
    func operations(_ name: String) async -> [JSONValue] { await fixture.requests.filter { $0["operation"] == .string(name) } }
}

private func scratchDirectory() -> URL {
    FileManager.default.temporaryDirectory.appendingPathComponent("wingthing-home-flow-\(UUID())")
}

private func savedFiles(_ directory: URL) -> [URL] {
    (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)) ?? []
}

private func routeHome(_ wire: FlowWire, rootTitle: String = "root") async throws {
    let root = conversationJSON(rootTitle), child = conversationJSON("child", parent: rootTitle)
    let rootLifecycle = try lifecycleJSON(lifecycle("idle"))
    let tasks: JSONValue = .array([.object(["conversation": root, "lifecycle": rootLifecycle]), .object(["conversation": child])])
    await wire.fixture.route([
        "conversation_list:": .object(["conversations": .array([root, child])]),
        "conversation_read:\(rootTitle)": .object(["conversation": root, "tasks": tasks]),
        "session_read:same-session": .object(["lifecycle": rootLifecycle]),
    ])
}

@MainActor private func connect(_ model: WingthingModel, _ wire: any HomeWire, origin: String = homeOrigin, user: String = "fixture-user", wing: String = "mac", key: String = wingPublic, token: String = syntheticToken) async {
    await model.connect(origin: origin, transport: .userOwnedEndpoint, userID: user, wingID: wing, wingPublicKey: key, existingBearer: token, wire: wire)
}

@MainActor private func rootReference(_ model: WingthingModel) throws -> ConversationReference {
    try unwrap(model.reference(for: try unwrap(model.roots.first)))
}

private func isFailed(_ phase: HomeConnectionPhase) -> Bool { if case .failed = phase { true } else { false } }
private func isOffline(_ phase: HomeConnectionPhase) -> Bool { if case .offline = phase { true } else { false } }

@Suite @MainActor struct HomeConnectionFlowPartnerTests {
    @Test func connectVerifiesIdentityThenLoadsEncryptedInventoryOnly() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire(holdAt: 1)
        try await routeHome(wire)
        expectEqual(model.phase, .notConfigured); expectFalse(model.connected); expectFalse(model.canDisconnect)

        let connecting = Task { await connect(model, wire) }
        await wire.waitUntilHeld()
        expectEqual(model.phase, .connecting); expectTrue(model.busy); expectFalse(model.connected)
        expectTrue(model.canDisconnect); expectFalse(model.canReconnect)
        await wire.release(); await connecting.value

        expectEqual(model.phase, .online); expectTrue(model.connected); expectFalse(model.busy); expectNil(model.error)
        expectEqual(model.roots.map(\.conversationID), ["root"])
        let attempts = await wire.attempts
        expectEqual(attempts, ["/health", "/auth/check", "/api/app/wings", "/ws/relay"])
        let calls = await wire.fixture.calls
        expectTrue(calls.allSatisfy { $0.host == "personal-home.example" })
        let tunnel = try unwrap(calls.last)
        expectEqual(tunnel.scheme, "wss")
        expectEqual(URLComponents(url: tunnel, resolvingAgainstBaseURL: false)?.queryItems, [URLQueryItem(name: "wing_id", value: "mac")])
        // The fixture wing could only decrypt this with its pinned private key.
        let requests = await wire.fixture.requests
        expectEqual(requests.map { $0["operation"] }, [.string("conversation_list")])
        let prompts = await wire.operations("session_prompt")
        expectTrue(prompts.isEmpty)
        expectTrue(savedFiles(directory).isEmpty) // Listing alone writes nothing.
    }

    @Test func invalidSetupFailsBeforeAnyNetworkOrFileAccess() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let shortKey = Data(repeating: 7, count: 31).base64EncodedString()
        let cases: [(origin: String, user: String, wing: String, key: String, token: String)] = [
            ("http://personal-home.example", "fixture-user", "mac", wingPublic, syntheticToken),
            ("https://personal-home.example/app", "fixture-user", "mac", wingPublic, syntheticToken),
            ("https://someone:secret@personal-home.example", "fixture-user", "mac", wingPublic, syntheticToken),
            ("https://personal-home.example?next=1", "fixture-user", "mac", wingPublic, syntheticToken),
            ("personal-home.example", "fixture-user", "mac", wingPublic, syntheticToken),
            ("", "fixture-user", "mac", wingPublic, syntheticToken),
            (homeOrigin, " ", "mac", wingPublic, syntheticToken),
            (homeOrigin, "fixture-user", "", wingPublic, syntheticToken),
            (homeOrigin, "fixture-user", "mac", shortKey, syntheticToken),
            (homeOrigin, "fixture-user", "mac", "not base64", syntheticToken),
            (homeOrigin, "fixture-user", "mac", wingPublic, ""),
            (homeOrigin, "fixture-user", "mac", wingPublic, "  \n"),
        ]
        for item in cases {
            let model = WingthingModel(cacheDirectory: directory), wire = FlowWire()
            await connect(model, wire, origin: item.origin, user: item.user, wing: item.wing, key: item.key, token: item.token)
            expectTrue(isFailed(model.phase)); expectFalse(model.connected); expectFalse(model.busy)
            expectNil(model.profile); expectFalse(model.canReconnect); expectFalse(model.canDisconnect)
            let attempts = await wire.attempts
            expectTrue(attempts.isEmpty)
        }
        expectTrue(savedFiles(directory).isEmpty)
    }

    @Test func failedReplacementNeverKeepsThePreviousClient() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), first = FlowWire()
        try await routeHome(first)
        await connect(model, first)
        await model.open(try rootReference(model))
        expectEqual(model.phase, .online); expectTrue(model.parent != nil)

        await connect(model, FlowWire(), token: "")
        expectTrue(isFailed(model.phase)); expectNil(model.profile); expectNil(model.parent)
        expectTrue(model.roots.isEmpty); expectTrue(model.tasks.isEmpty); expectFalse(model.canReconnect)
        let before = await first.attempts
        await model.refresh(); await model.pollTranscript(); await model.sendOrCheck()
        let after = await first.attempts
        expectEqual(before, after)

        // The existing provisioning seam has the same guarantee.
        await connect(model, first)
        expectEqual(model.phase, .online)
        let home = try profile()
        do {
            try await model.configure(profile: home, existingBearer: "", cacheFile: directory.appendingPathComponent("other.json"), wire: FlowWire())
            fail("Empty token accepted")
        } catch {}
        expectTrue(isFailed(model.phase)); expectNil(model.profile); expectFalse(model.canReconnect)
        let settled = await first.attempts
        await model.refresh()
        let unchanged = await first.attempts
        expectEqual(settled, unchanged)

        // A valid configure still restores without contacting the home.
        let restoring = FlowWire()
        try await model.configure(profile: home, existingBearer: syntheticToken, cacheFile: directory.appendingPathComponent("other.json"), wire: restoring)
        expectEqual(model.phase, .unverified); expectFalse(model.connected); expectTrue(model.canReconnect)
        let untouched = await restoring.attempts
        expectTrue(untouched.isEmpty)
    }

    @Test func wrongAccountChangedKeyOrStaleTunnelReplyNeverGoesOnline() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire()
        try await routeHome(wire)
        await wire.fixture.configure(user: "someone-else")
        await connect(model, wire)
        expectEqual(model.phase, .failed("The configured home returned a different account."))
        expectFalse(model.connected); expectFalse(model.busy); expectTrue(model.roots.isEmpty)
        expectTrue(model.canReconnect) // Explicit retry stays available; nothing retries by itself.
        var attempts = await wire.attempts
        expectEqual(attempts, ["/health", "/auth/check"])

        await wire.fixture.configure(user: "fixture-user", key: clientPrivate)
        await model.refresh()
        expectEqual(model.phase, .failed("The configured home wing is missing or its identity changed."))
        attempts = await wire.attempts
        expectFalse(attempts.contains("/ws/relay"))
        let requests = await wire.fixture.requests
        expectTrue(requests.isEmpty)

        await wire.fixture.configure(key: wingPublic, wrongRequestID: true)
        await model.refresh()
        expectEqual(model.phase, .failed(ClientError.staleReference.localizedDescription)); expectTrue(model.roots.isEmpty)

        await wire.fixture.configure(wrongRequestID: false)
        await model.refresh()
        expectEqual(model.phase, .online); expectEqual(model.roots.count, 1)
    }

    @Test func passkeyRequiredWingIsReportedAsUnsupported() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire()
        await wire.fixture.configure(reply: .object(["error": .string("passkey_required")]))
        await connect(model, wire)
        guard case .failed(let message) = model.phase else { fail("Expected a failed state, got \(model.phase)"); return }
        expectTrue(message.contains("passkey")); expectFalse(model.connected); expectTrue(model.roots.isEmpty)
    }

    @Test func unreachableHomeShowsSavedTasksScopedToExactIdentity() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let live = WingthingModel(cacheDirectory: directory), wire = FlowWire()
        try await routeHome(wire)
        await connect(live, wire)
        let rootRef = try rootReference(live)
        await live.open(rootRef)
        let identity = try unwrap(live.profile).id
        let savedTranscript = live.transcript
        expectEqual(live.tasks.count, 2); expectEqual(live.currentStatus, .ready)
        await live.disconnect()
        expectEqual(savedFiles(directory).map(\.lastPathComponent), ["home-\(identity.uuidString.lowercased()).json"])

        let offline = WingthingModel(cacheDirectory: directory), unreachable = FlowWire(offline: true)
        await connect(offline, unreachable)
        expectTrue(isOffline(offline.phase)); expectFalse(offline.connected); expectFalse(offline.busy)
        expectEqual(offline.profile?.id, identity)
        expectEqual(offline.parent?.reference, rootRef); expectEqual(offline.selected, rootRef)
        expectEqual(offline.tasks.map(\.conversation.conversationID), ["root", "child"])
        expectEqual(offline.transcript.events, savedTranscript.events); expectEqual(offline.transcript.cursor, savedTranscript.cursor)
        expectTrue(offline.roots.isEmpty) // No live inventory is claimed.
        expectEqual(offline.currentStatus, .offline); expectEqual(offline.parentStatus, .offline)
        expectTrue(offline.tasks.allSatisfy { offline.status(for: $0) == .offline })
        expectFalse(offline.inputReady)
        await offline.open(rootRef); await offline.openParent()
        let attempts = await unreachable.attempts
        expectEqual(attempts, ["/health"])
        expectEqual(offline.selected, rootRef); expectTrue(isOffline(offline.phase))

        // Same home spelled differently resolves to the same saved state.
        let respelled = WingthingModel(cacheDirectory: directory)
        await connect(respelled, FlowWire(offline: true), origin: " https://PERSONAL-Home.example:443/ ", user: " fixture-user ", key: " \(wingPublic) ")
        expectEqual(respelled.profile?.id, identity); expectEqual(respelled.parent?.reference, rootRef)

        // Any changed identity field is a different home with nothing restored.
        for (origin, user, wing, key) in [(homeOrigin, "fixture-user", "mac", clientPrivate), (homeOrigin, "other-user", "mac", wingPublic),
                                          (homeOrigin, "fixture-user", "linux", wingPublic), ("https://personal-home.example:8443", "fixture-user", "mac", wingPublic)] {
            let other = WingthingModel(cacheDirectory: directory)
            await connect(other, FlowWire(offline: true), origin: origin, user: user, wing: wing, key: key)
            expectTrue(other.profile != nil); expectTrue(other.profile?.id != identity)
            expectNil(other.parent); expectTrue(other.tasks.isEmpty); expectTrue(other.transcript.events.isEmpty)
            await other.open(rootRef) // A reference from another identity is refused.
            expectNil(other.selected); expectTrue(other.tasks.isEmpty)
        }

        // Field boundaries are unambiguous, not delimiter concatenation.
        let left = WingthingModel(cacheDirectory: directory), right = WingthingModel(cacheDirectory: directory)
        await connect(left, FlowWire(offline: true), user: "ab", wing: "c")
        await connect(right, FlowWire(offline: true), user: "a", wing: "bc")
        expectTrue(left.profile?.id != nil && left.profile?.id != right.profile?.id)
        expectEqual(savedFiles(directory).count, 1)
    }

    @Test func reconnectOnlyReadsAndKeepsTheExactPendingInput() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire()
        try await routeHome(wire)
        await connect(model, wire)
        await model.open(try rootReference(model))
        model.draft = "Summarize the linked child task"
        await wire.fixture.configure(reply: .object(["receipt": .object(["status": .string("unconfirmed")])]))
        await model.sendOrCheck()
        let pending = try unwrap(model.pending)
        expectEqual(pending.delivery, .unconfirmed)
        var prompts = await wire.operations("session_prompt")
        expectEqual(prompts.count, 1)

        await wire.setOffline(true)
        await model.refresh()
        expectTrue(isOffline(model.phase)); expectEqual(model.pending?.id, pending.id)
        await model.sendOrCheck() // Not connected: nothing is sent.
        await wire.setOffline(false)
        await model.refresh()
        expectEqual(model.phase, .online); expectEqual(model.pending?.id, pending.id); expectEqual(model.pending?.delivery, .unconfirmed)
        prompts = await wire.operations("session_prompt")
        expectEqual(prompts.count, 1)

        // A full disconnect and a fresh connect restore the same saved input.
        await model.disconnect()
        await connect(model, wire)
        expectEqual(model.phase, .online); expectEqual(model.pending?.id, pending.id); expectEqual(model.pending?.input, pending.input)
        prompts = await wire.operations("session_prompt")
        expectEqual(prompts.count, 1)

        // Only the explicit Check receipt action repeats the identical request.
        await wire.fixture.configure(reply: .object(["receipt": .object(["status": .string("native_receipt_observed"), "native_receipt_observed": .bool(true)])]))
        await model.sendOrCheck()
        prompts = await wire.operations("session_prompt")
        expectEqual(prompts.count, 2); expectEqual(prompts[0]["arguments"], prompts[1]["arguments"])
        expectEqual(prompts[1]["arguments"]?["request_id"], .string(pending.id.uuidString))
        expectNil(model.pending)

        // The token is never written to disk or exposed in published state.
        let secret = Data(syntheticToken.utf8)
        let files = savedFiles(directory)
        expectFalse(files.isEmpty)
        for file in files {
            expectFalse(try Data(contentsOf: file).range(of: secret) != nil)
            expectFalse(file.lastPathComponent.contains(syntheticToken))
        }
        let encodedProfile = try JSONEncoder().encode(try unwrap(model.profile))
        expectNil(encodedProfile.range(of: secret))
        expectFalse(String(reflecting: model.profile).contains(syntheticToken))
        expectFalse(String(reflecting: model.phase).contains(syntheticToken))
    }

    @Test func disconnectDropsTheClientButKeepsSavedTasksReadable() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire()
        try await routeHome(wire)
        await connect(model, wire)
        let rootRef = try rootReference(model)
        await model.open(rootRef)
        let events = model.transcript.events
        expectEqual(model.currentStatus, .ready)
        let before = await wire.attempts

        await model.disconnect()
        expectEqual(model.phase, .disconnected); expectFalse(model.connected); expectFalse(model.busy)
        expectFalse(model.canReconnect); expectFalse(model.canDisconnect)
        expectEqual(model.parent?.reference, rootRef); expectEqual(model.tasks.count, 2); expectEqual(model.transcript.events, events)
        expectEqual(model.currentStatus, .offline); expectEqual(model.parentStatus, .offline)
        model.draft = "Should never leave the phone"
        await model.refresh(); await model.open(rootRef); await model.openParent(); await model.pollTranscript(); await model.sendOrCheck()
        let after = await wire.attempts
        expectEqual(before, after)
        expectEqual(model.phase, .disconnected); expectEqual(model.selected, rootRef); expectNil(model.pending)

        // Reconnecting needs the token entered again.
        await connect(model, wire, token: "")
        expectTrue(isFailed(model.phase))
        let unchanged = await wire.attempts
        expectEqual(before, unchanged)
    }

    @Test func disconnectDuringConnectDiscardsTheLateResult() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory), wire = FlowWire(holdAt: 1)
        try await routeHome(wire)
        let connecting = Task { await connect(model, wire) }
        await wire.waitUntilHeld()
        await model.disconnect()
        expectEqual(model.phase, .disconnected); expectFalse(model.busy)
        await wire.release(); await connecting.value

        expectEqual(model.phase, .disconnected); expectFalse(model.busy); expectTrue(model.roots.isEmpty)
        let attempts = await wire.attempts
        expectFalse(attempts.contains("/ws/relay")) // No inventory request on the dropped client.
        await model.refresh()
        let after = await wire.attempts
        expectEqual(attempts, after)
    }

    @Test func newerConnectWinsOverAnInFlightConnect() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory)
        let older = FlowWire(holdAt: 1), newer = FlowWire()
        try await routeHome(older); try await routeHome(newer, rootTitle: "second-root")
        let connecting = Task { await connect(model, older) }
        await older.waitUntilHeld()
        await connect(model, newer, origin: "https://second-home.example")
        expectEqual(model.phase, .online); expectEqual(model.roots.map(\.conversationID), ["second-root"])
        await older.release(); await connecting.value

        expectEqual(model.profile?.origin.host, "second-home.example")
        expectEqual(model.phase, .online); expectFalse(model.busy)
        expectEqual(model.roots.map(\.conversationID), ["second-root"])
        let olderAttempts = await older.attempts
        expectFalse(olderAttempts.contains("/ws/relay"))
    }

    @Test func navigationResultFromAReplacedHomeIsDiscarded() async throws {
        let directory = scratchDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let model = WingthingModel(cacheDirectory: directory)
        let first = FlowWire(holdAt: 5), second = FlowWire()
        try await routeHome(first); try await routeHome(second, rootTitle: "second-root")
        await connect(model, first)
        let opening = Task { await model.open(try rootReference(model)) }
        await first.waitUntilHeld()
        expectTrue(model.busy)
        await connect(model, second, origin: "https://second-home.example")
        await first.release(); _ = await opening.result

        expectEqual(model.profile?.origin.host, "second-home.example"); expectEqual(model.phase, .online); expectFalse(model.busy)
        expectNil(model.selected); expectNil(model.parent); expectNil(model.execution); expectTrue(model.tasks.isEmpty)
        expectEqual(model.roots.map(\.conversationID), ["second-root"])
        expectTrue(savedFiles(directory).isEmpty) // The stale read never reached the first home's cache.
    }
}
