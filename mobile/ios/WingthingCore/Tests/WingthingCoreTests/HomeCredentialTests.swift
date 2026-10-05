import Foundation
import Security
import Testing
import WingthingUI
@testable import WingthingCore

@MainActor final class MemoryCredentials: HomeCredentialStore {
    var tokens: [String: String] = [:]
    var saves: [HomeProfile] = []
    var forgets: [HomeProfile] = []
    var unavailable = false
    private func key(_ profile: HomeProfile) throws -> String { try KeychainCredentialStore.query(for: profile)[kSecAttrAccount as String] as! String }
    func token(for profile: HomeProfile) throws -> String? {
        if unavailable { throw ClientError.storage("Keychain is locked") }
        return tokens[try key(profile)]
    }
    func save(_ token: String, for profile: HomeProfile) throws { tokens[try key(profile)] = token; saves.append(profile) }
    func forget(_ profile: HomeProfile) throws {
        if unavailable { throw ClientError.storage("Keychain is locked") }
        tokens.removeValue(forKey: try key(profile)); forgets.append(profile)
    }
}

@Suite @MainActor struct HomeCredentialTests {
    private let token = "synthetic-existing-token"
    private func directory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("home-credentials-\(UUID())") }
    private func connect(_ model: WingthingModel, _ wire: FixtureWire) async {
        await model.connect(origin: "https://personal-home.example", transport: .userOwnedEndpoint, userID: "fixture-user", wingID: "mac", wingPublicKey: wingPublic, existingBearer: token, wire: wire)
    }
    private func wire() async -> FixtureWire {
        let wire = FixtureWire()
        await wire.route(["conversation_list:": .object(["conversations": .array([conversationJSON("root")])])])
        return wire
    }
    @Test func keychainScopeCanonicalizesOriginAndSeparatesAllPinnedIdentities() throws {
        let home = try profile()
        let query = try KeychainCredentialStore.query(for: home)
        expectEqual(query[kSecClass as String] as? String, kSecClassGenericPassword as String)
        expectEqual(query[kSecAttrSynchronizable as String] as? Bool, false)
        expectEqual(query[kSecUseDataProtectionKeychain as String] as? Bool, true)
        let attributes = KeychainCredentialStore.attributes(token)
        expectEqual(attributes[kSecAttrAccessible as String] as? String, kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
        expectEqual(attributes[kSecValueData as String] as? Data, Data(token.utf8))
        let alias = try HomeProfile(origin: URL(string: "https://PERSONAL-home.example:443/")!, transport: .existingVPN, expectedUserID: home.expectedUserID, homeWingID: home.homeWingID, homeWingPublicKey: home.homeWingPublicKey)
        let account = query[kSecAttrAccount as String] as? String
        expectEqual(try KeychainCredentialStore.query(for: alias)[kSecAttrAccount as String] as? String, account)
        for (origin, user, wing, key) in [("https://other.example", home.expectedUserID, home.homeWingID, wingPublic),
            (home.origin.absoluteString, "other", home.homeWingID, wingPublic), (home.origin.absoluteString, home.expectedUserID, "other", wingPublic),
            (home.origin.absoluteString, home.expectedUserID, home.homeWingID, clientPrivate), ("https://personal-home.example:8443", home.expectedUserID, home.homeWingID, wingPublic)] {
            let other = try HomeProfile(origin: URL(string: origin)!, transport: .userOwnedEndpoint, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key)
            expectTrue(try KeychainCredentialStore.query(for: other)[kSecAttrAccount as String] as? String != account)
        }
    }
    @Test func launchRestoresOnlyVerifiedSelectedHomeAndNeverReplaysPendingInput() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), first = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
        await connect(first, await wire())
        expectTrue(first.connected); expectEqual(credentials.saves.count, 1)
        let home = try unwrap(first.profile)
        let selected = SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json"))
        expectEqual(try selected.load(), home)
        let local = try LocalConversationStore(profile: home, file: dir.appendingPathComponent("home-\(home.id.uuidString.lowercased()).json"))
        try await local.savePending(PendingInput(execution: ExecutionReference(conversation: reference(home), sessionID: "same-session"), input: "Don't resend on launch"))
        let restoredWire = await wire(), restored = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
        await restored.restoreHome(wire: restoredWire)
        expectTrue(restored.connected); expectEqual(restored.profile, home); expectEqual(credentials.saves.count, 1)
        expectEqual(await restoredWire.calls.map(\.path), ["/health", "/auth/check", "/api/app/wings", "/ws/relay"])
        expectTrue(await restoredWire.requests.allSatisfy { $0["operation"] == .string("conversation_list") })
        for file in try FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil) {
            expectNil(try Data(contentsOf: file).range(of: Data(token.utf8)))
        }
        // Missing access still restores the saved selection as offline data.
        let root = reference(home)
        try await local.selectParent(ParentSelection(reference: root, title: "Saved parent"))
        let tasks = try JSONDecoder().decode([ConversationTask].self, from: JSONEncoder().encode(JSONValue.array([.object(["conversation": conversationJSON("root")])])))
        try await local.cache(CachedConversation(reference: root, tasks: tasks, transcript: TranscriptState()))
        await restored.disconnect()
        expectNil(try credentials.token(for: home)); expectEqual(credentials.forgets, [home]); expectEqual(try selected.load(), home)
        let afterForget = WingthingModel(cacheDirectory: dir, credentialStore: credentials), quietWire = await wire()
        await afterForget.restoreHome(wire: quietWire)
        expectEqual(afterForget.phase, .disconnected); expectEqual(afterForget.profile, home); expectTrue(await quietWire.calls.isEmpty)
        expectEqual(afterForget.tasks, tasks); expectEqual(afterForget.selected, root); expectEqual(afterForget.currentStatus, .offline)
    }
    @Test func changedAccountOrPinOnLaunchFailsBeforeEncryptedInventory() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), first = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
        await connect(first, await wire())
        for wrongAccount in [true, false] {
            let restoring = WingthingModel(cacheDirectory: dir, credentialStore: credentials), changed = await wire()
            if wrongAccount { await changed.configure(user: "other-account") }
            else { await changed.configure(key: clientPrivate) }
            await restoring.restoreHome(wire: changed)
            expectFalse(restoring.connected); expectTrue(restoring.error != nil)
            expectTrue(await changed.requests.isEmpty)
        }
        expectEqual(credentials.saves.count, 1)
    }
    @Test func unverifiedConnectAndLockedKeychainNeverContactInventoryOrSaveTokens() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials), changed = await wire()
        await changed.configure(user: "other-account"); await connect(model, changed)
        expectTrue(credentials.saves.isEmpty)
        expectNil(try SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json")).load())
        let home = try profile()
        try credentials.save(token, for: home); try SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json")).save(home)
        credentials.unavailable = true
        let locked = WingthingModel(cacheDirectory: dir, credentialStore: credentials), quiet = await wire()
        await locked.restoreHome(wire: quiet)
        expectTrue(locked.error?.contains("locked") == true); expectTrue(await quiet.calls.isEmpty)
    }
    @Test func previewNeverEntersKeychainOrSelectedHomeStorageAndPresetStillNeedsIdentity() async throws {
        let preview = try HomeProfile.localPreview(origin: URL(string: "http://127.0.0.1:9999")!, expectedUserID: "local", homeWingID: "mac", homeWingPublicKey: wingPublic)
        expectThrows(try KeychainCredentialStore.query(for: preview))
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        expectThrows(try SelectedHomeStore(file: dir.appendingPathComponent("selected-home.json")).save(preview))
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials), quiet = await wire()
        expectEqual(HomeProfile.hostedPresetOrigin, "https://wingthing.ai")
        expectNil(model.profile)
        await model.connect(origin: HomeProfile.hostedPresetOrigin, transport: .explicitHostedRoost, userID: "", wingID: "", wingPublicKey: "", existingBearer: "", wire: quiet)
        expectTrue(await quiet.calls.isEmpty); expectTrue(credentials.saves.isEmpty)
    }
    @Test func failedForgetStaysVisibleAndCanBeRetried() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let credentials = MemoryCredentials(), model = WingthingModel(cacheDirectory: dir, credentialStore: credentials)
        await connect(model, await wire())
        let home = try unwrap(model.profile)
        credentials.unavailable = true; await model.disconnect()
        expectTrue(model.error?.contains("couldn't be forgotten") == true); expectTrue(model.canDisconnect)
        expectFalse(model.connected)
        credentials.unavailable = false; await model.disconnect()
        expectEqual(model.phase, .disconnected); expectNil(try credentials.token(for: home)); expectFalse(model.canDisconnect); expectNil(model.error)
    }

}
