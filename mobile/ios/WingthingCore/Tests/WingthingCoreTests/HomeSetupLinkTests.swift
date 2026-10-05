import Combine
import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

#if DEBUG
private actor SetupPreviewWire: HomeWire {
    private let encrypted = FixtureWire()
    private(set) var bearers: [String] = []
    func get(_ url: URL, bearer: String) async throws -> HTTPReply {
        bearers.append(bearer)
        guard bearer.isEmpty else { throw ClientError.response("Preview received a credential") }
        if url.path == "/api/app/me" {
            return HTTPReply(status: 200, data: Data(#"{"id":"local","release_channel":"preview","provider":"local"}"#.utf8))
        }
        // Reuse the encrypted fixture's roster, not its remote auth contract.
        return try await encrypted.get(url, bearer: "synthetic-existing-token")
    }
    func exchange(_ url: URL, bearer: String, request: Data) async throws -> Data {
        bearers.append(bearer)
        guard bearer.isEmpty else { throw ClientError.response("Preview received a credential") }
        await encrypted.route(["conversation_list:": .object(["conversations": .array([])])])
        return try await encrypted.exchange(url, bearer: "synthetic-existing-token", request: request)
    }
}
#endif

@Suite @MainActor struct HomeSetupLinkTests {
    private func link(origin: String = "https://personal-home.example", account: String = "fixture-user", wing: String = "mac", key: String = wingPublic, token: String? = "synthetic-existing-token", version: String = "1") -> String {
        var parts = URLComponents(string: "wingthing://home")!
        parts.queryItems = [URLQueryItem(name: "v", value: version), URLQueryItem(name: "origin", value: origin),
                            URLQueryItem(name: "account", value: account), URLQueryItem(name: "wing", value: wing), URLQueryItem(name: "key", value: key)]
        if let token { parts.queryItems!.append(URLQueryItem(name: "token", value: token)) }
        return parts.string!
    }

    @Test func validLinkUsesManualFormValidatorsAndKeepsTokenSeparate() throws {
        let setup = try HomeSetupLink(link())
        let manual = try HomeProfile.formInput(origin: "https://personal-home.example", transport: .userOwnedEndpoint, userID: "fixture-user", wingID: "mac", wingPublicKey: wingPublic)
        expectEqual(setup.profile.origin, manual.origin)
        expectEqual(setup.profile.expectedUserID, manual.expectedUserID)
        expectEqual(setup.profile.homeWingID, manual.homeWingID)
        expectEqual(setup.profile.homeWingPublicKey, manual.homeWingPublicKey)
        expectEqual(setup.token, "synthetic-existing-token")
        expectNil(try JSONEncoder().encode(setup.profile).range(of: Data(setup.token!.utf8)))
        expectEqual(try HomeSetupLink(link(origin: HomeProfile.hostedPresetOrigin)).profile.transport, .explicitHostedRoost)
        expectNil(try HomeSetupLink(link(token: nil)).token)
        expectEqual(try HomeSetupLink(" \n" + link() + "\n").profile.expectedUserID, "fixture-user")
    }

    @Test func goPercentEncodingPreservesPlusSlashEqualsAndSpaces() throws {
        let key = Data(repeating: 0xfb, count: 32).base64EncodedString()
        // Go query escaping followed by replacing form '+' with '%20'.
        let encodedKey = key.replacingOccurrences(of: "+", with: "%2B").replacingOccurrences(of: "/", with: "%2F").replacingOccurrences(of: "=", with: "%3D")
        let text = "wingthing://home?v=1&account=account%20%2B%26%2F%3D%3F&key=\(encodedKey)&origin=https%3A%2F%2Fhome.example%3A8443&token=private%20%2B%26%2F%3D%3F%25%23&wing=wing%20%2B%26%2F%3D%3F"
        let setup = try HomeSetupLink(text)
        expectEqual(setup.profile.expectedUserID, "account +&/=?")
        expectEqual(setup.profile.homeWingID, "wing +&/=?")
        expectEqual(setup.profile.homeWingPublicKey, key)
        expectEqual(setup.token, "private +&/=?%#")
    }

    @Test func wrongOrMissingVersionIsRejected() {
        for version in ["0", "2", "01", "", "1.0"] { expectThrows(try HomeSetupLink(link(version: version))) }
        expectThrows(try HomeSetupLink(link().replacingOccurrences(of: "v=1&", with: "")))
    }

    @Test func duplicateAndExtraParametersAreRejected() {
        for suffix in ["&v=1", "&origin=https%3A%2F%2Fother.example", "&account=fixture-user", "&wing=mac", "&key=\(wingPublic)",
                       "&token=second-token", "&%74oken=encoded-duplicate", "&extra=ignored", "&token", "&"] {
            expectThrows(try HomeSetupLink(link() + suffix))
        }
    }

    @Test func onlyExactHomeLinkEndpointIsAccepted() {
        for endpoint in ["https://home", "wingthing://other", "wingthing://home/", "wingthing://user@home", "wingthing://home:80", "wingthing://%68ome"] {
            expectThrows(try HomeSetupLink(link().replacingOccurrences(of: "wingthing://home", with: endpoint)))
        }
        expectThrows(try HomeSetupLink(link() + "#fragment"))
        expectThrows(try HomeSetupLink(link() + "&token=%ZZ"))
    }

    @Test func httpOriginAndNonOriginURLsAreRejected() {
        for origin in ["https://", "https://:443", "http://home.example", "http://localhost:8080", "ftp://home.example", "https://home.example/path", "https://home.example?x=1",
                       "https://home.example#fragment", "https://user:secret@home.example"] {
            expectThrows(try HomeSetupLink(link(origin: origin)))
        }
    }

    @Test func badKeyMissingIdentityAndEmptyTokenAreRejected() {
        for key in ["not-base64", Data(repeating: 1, count: 31).base64EncodedString(), Data(repeating: 1, count: 33).base64EncodedString(), ""] {
            expectThrows(try HomeSetupLink(link(key: key)))
        }
        expectThrows(try HomeSetupLink(link(account: " \n")))
        expectThrows(try HomeSetupLink(link(wing: "")))
        expectThrows(try HomeSetupLink(link(token: " \n")))
        for field in ["origin", "account", "wing", "key"] {
            var parts = URLComponents(string: link())!
            parts.queryItems!.removeAll { $0.name == field }
            expectThrows(try HomeSetupLink(parts.string!))
        }
    }

    @Test func oversizedInputIsRejectedBeforeParsing() {
        expectThrows(try HomeSetupLink(link(token: String(repeating: "x", count: HomeSetupLink.maximumBytes))))
        expectThrows(try HomeSetupLink(String(repeating: "💾", count: HomeSetupLink.maximumBytes / 2)))
    }

    @Test func previewUsesExistingDebugRulesAndNeverAcceptsCredentials() throws {
        #if DEBUG
        let setup = try HomeSetupLink(link(origin: "http://127.0.0.1:9999", account: "local", token: nil))
        expectEqual(setup.profile.mode, .localPreview)
        expectEqual(setup.profile.transport, .localNetwork)
        expectThrows(try HomeSetupLink(link(origin: "http://127.0.0.1:9999", account: "other", token: nil)))
        expectThrows(try HomeSetupLink(link(origin: "http://127.0.0.1", account: "local", token: nil)))
        #else
        expectThrows(try HomeSetupLink(link(origin: "http://127.0.0.1:9999", account: "local", token: nil)))
        #endif
        for token in ["secret", "", " "] {
            expectThrows(try HomeSetupLink(link(origin: "http://127.0.0.1:9999", account: "local", token: token)))
        }
    }

    @Test func importingOnlyFillsFormAndExplicitConnectStillChecksIdentityBeforeKeychain() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("home-setup-\(UUID())")
        defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials), wire = FixtureWire()
        model.receiveHomeSetupLink(link())
        let setup = try unwrap(model.takeHomeSetupLink())
        expectNil(model.profile); expectFalse(model.connected); expectTrue(credentials.saves.isEmpty)
        expectFalse(FileManager.default.fileExists(atPath: dir.path))
        await model.restoreHome(wire: wire)
        expectTrue(await wire.calls.isEmpty)
        await wire.configure(user: "other-account")
        let home = setup.profile
        await model.connect(origin: home.origin.absoluteString, transport: home.transport, userID: home.expectedUserID, wingID: home.homeWingID, wingPublicKey: home.homeWingPublicKey, existingBearer: setup.token!, wire: wire)
        expectFalse(model.connected); expectTrue(credentials.saves.isEmpty); expectTrue(await wire.requests.isEmpty)
        let verified = FixtureWire()
        await verified.route(["conversation_list:": .object(["conversations": .array([])])])
        await model.connect(origin: home.origin.absoluteString, transport: home.transport, userID: home.expectedUserID, wingID: home.homeWingID, wingPublicKey: home.homeWingPublicKey, existingBearer: setup.token!, wire: verified)
        expectTrue(model.connected); expectEqual(credentials.saves.count, 1)
        expectEqual(await verified.calls.map(\.path), ["/health", "/auth/check", "/api/app/wings", "/ws/relay"])
    }

    @Test func invalidImportPreservesConnectionAndNeverEchoesCredentialInError() {
        let model = WingthingModel(), secret = "private-link-credential"
        model.receiveHomeSetupLink(link(token: secret, version: "2"))
        expectNil(model.pendingHomeSetup); expectNil(model.profile); expectEqual(model.phase, .notConfigured)
        expectTrue(model.homeSetupError != nil); expectFalse(model.homeSetupError!.contains(secret))
    }

    @Test func openingLinkNeverRestoresAnExistingSavedHomeAutomatically() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("home-setup-launch-\(UUID())")
        defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), home = try profile(), wire = FixtureWire()
        try credentials.save("synthetic-existing-token", for: home)
        try SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json")).save(home)
        let model = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
        model.receiveHomeSetupLink(link())
        // Consuming the transient form data must not re-enable launch restore.
        expectTrue(model.takeHomeSetupLink() != nil)
        await model.restoreHome(wire: wire)
        expectNil(model.profile); expectFalse(model.connected); expectTrue(await wire.calls.isEmpty)
        expectEqual(credentials.saves.count, 1)
    }

    @Test func linkArrivingDuringLaunchRestorationFencesThePreviousHome() async throws {
        for beforeRefresh in [false, true] {
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent("home-setup-race-\(UUID())")
            defer { try? FileManager.default.removeItem(at: dir) }
            let credentials = MemoryCredentials(), home = try profile(), wire = FixtureWire()
            try credentials.save("synthetic-existing-token", for: home)
            let selected = SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json"))
            try selected.save(home)
            await wire.route(["conversation_list:": .object(["conversations": .array([])])])
            let model = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
            let incoming = link(origin: "https://replacement-home.example")
            var started = false, arrived = false
            var observation: AnyCancellable?
            if beforeRefresh {
                // Deliver immediately after installation's actor suspensions,
                // before restoration can begin its network refresh.
                observation = model.$busy.sink { busy in
                    if busy { started = true }
                    if started && !busy && !arrived {
                        arrived = true
                        model.receiveHomeSetupLink(incoming)
                    }
                }
                await model.restoreHome(wire: wire)
            } else {
                let restoring = Task(priority: .background) { await model.restoreHome(wire: wire) }
                await withCheckedContinuation { ready in
                    observation = model.$busy.first(where: { $0 }).sink { _ in ready.resume() }
                }
                // The restoring task has yielded inside installation.
                expectTrue(model.busy)
                model.receiveHomeSetupLink(incoming); arrived = true
                await restoring.value
            }
            observation?.cancel()
            expectTrue(arrived); expectFalse(model.connected); expectFalse(model.busy)
            expectTrue(await wire.calls.isEmpty); expectTrue(await wire.requests.isEmpty)
            expectEqual(try selected.load(), home); expectEqual(credentials.saves.count, 1)
            let setup = try unwrap(model.takeHomeSetupLink())
            await model.restoreHome(wire: wire)
            expectTrue(await wire.calls.isEmpty)

            // Only the explicit Connect action may contact the imported home.
            await model.connect(origin: setup.profile.origin.absoluteString, transport: setup.profile.transport, userID: setup.profile.expectedUserID, wingID: setup.profile.homeWingID, wingPublicKey: setup.profile.homeWingPublicKey, existingBearer: setup.token!, wire: wire)
            expectTrue(model.connected); expectEqual(model.profile?.origin.host, "replacement-home.example")
            expectTrue(await wire.calls.allSatisfy { $0.host == "replacement-home.example" })
        }
    }

    @Test func importingAnotherHomeDoesNotReplaceLiveConnectionOrSaveItsToken() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("home-setup-live-\(UUID())")
        defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials), wire = FixtureWire()
        await wire.route(["conversation_list:": .object(["conversations": .array([])])])
        await model.connect(origin: "https://personal-home.example", transport: .userOwnedEndpoint, userID: "fixture-user", wingID: "mac", wingPublicKey: wingPublic, existingBearer: "synthetic-existing-token", wire: wire)
        let original = model.profile, calls = await wire.calls.count
        model.receiveHomeSetupLink(link(origin: "https://other.example", account: "other-account", token: "other-token"))
        expectEqual(model.profile, original); expectTrue(model.connected)
        expectEqual(await wire.calls.count, calls); expectEqual(credentials.saves.count, 1)
        model.receiveHomeSetupLink(link(token: nil))
        expectNil(model.takeHomeSetupLink()?.token)
    }

    #if DEBUG
    @Test func explicitPreviewConnectRemainsCredentialFreeAndInspectionOnly() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("home-setup-preview-\(UUID())")
        defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials), wire = SetupPreviewWire()
        let setup = try HomeSetupLink(link(origin: "http://127.0.0.1:9999", account: "local", token: nil)), home = setup.profile
        await model.connect(origin: home.origin.absoluteString, transport: home.transport, userID: home.expectedUserID, wingID: home.homeWingID, wingPublicKey: home.homeWingPublicKey, existingBearer: "", mode: home.mode, wire: wire)
        expectTrue(model.connected); expectTrue(model.inspectionOnly); expectTrue(credentials.saves.isEmpty)
        expectTrue(await wire.bearers.allSatisfy(\.isEmpty))
        expectFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent("selected-home.json").path))
        let quiet = FixtureWire()
        await model.connect(origin: home.origin.absoluteString, transport: home.transport, userID: home.expectedUserID, wingID: home.homeWingID, wingPublicKey: home.homeWingPublicKey, existingBearer: " ", mode: home.mode, wire: quiet)
        expectFalse(model.connected); expectTrue(await quiet.calls.isEmpty); expectTrue(credentials.saves.isEmpty)
    }
    #endif
}
