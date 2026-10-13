import Foundation
import Testing
import WingthingUI
@testable import WingthingCore

private func paired(_ id: String, _ name: String) -> PairedWing {
    PairedWing(wingID: id, name: name, key: Data(repeating: 1, count: 32).base64EncodedString(), deviceID: "device-" + id, local: URL(string: "http://127.0.0.1:6078")!, relay: nil)
}
private func session(_ id: String = "same", origin: String? = nil, machine: String? = nil, pinned: Bool = false, status: String = "idle") throws -> PhoneSession {
    var fields: [String: JSONValue] = ["id": .string(id), "name": .string(pinned ? "wing" : "Session"), "agent": .string("claude"), "status": .string(status), "blocked": .bool(false), "unread": .bool(true), "pinned": .bool(pinned), "role": .string(pinned ? "coordinator" : "")]
    if let origin { fields["origin_wing_id"] = .string(origin) }
    if let machine { fields["machine"] = .string(machine) }
    return try JSONDecoder().decode(PhoneSession.self, from: JSONEncoder().encode(fields))
}

@Suite struct PhoneFleetTests {
    @Test func equalSessionIDsOnDifferentMachinesRemainDistinct() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session()], healthy: true), PhoneInventory(wing: paired("b", "Server"), sessions: [try session()], healthy: true)])
        #expect(rows.count == 2); #expect(Set(rows.map(\.id)).count == 2)
        #expect(Set(rows.compactMap { $0.identity?.originWingID }) == ["a", "b"])
        #expect(rows.allSatisfy { !$0.possibleDuplicate && $0.preferredRoute?.direct == true })
    }
    @Test func provenRemoteMergesAndPrefersDirect() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(origin: "b", machine: "Server")], healthy: true), PhoneInventory(wing: paired("b", "Server"), sessions: [try session(origin: "b", machine: "Server")], healthy: true)])
        #expect(rows.count == 1); #expect(rows[0].routes.count == 2)
        #expect(rows[0].preferredRoute?.wingID == "b"); #expect(rows[0].routes.map(\.label) == ["direct", "via Mac"])
    }
    @Test func failedDirectRetainsRowAndUsesHealthyViaRoute() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(origin: "b", machine: "Server", pinned: true)], healthy: true), PhoneInventory(wing: paired("b", "Server"), sessions: [try session(origin: "b", machine: "Server", pinned: true)], healthy: false)])
        #expect(rows.count == 1); #expect(rows[0].coordinator)
        #expect(rows[0].preferredRoute?.label == "via Mac"); #expect(rows[0].routes.count == 2)
    }
    @Test func missingOriginNeverMergesWithKnownIdentityOrEnablesActions() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(machine: "Server")], healthy: true), PhoneInventory(wing: paired("b", "Server"), sessions: [try session(origin: "b", machine: "Server")], healthy: true)])
        #expect(rows.count == 2); #expect(rows.allSatisfy { $0.possibleDuplicate })
        #expect(rows.filter { $0.identity == nil }.count == 1)
    }
    @Test func matchingMachineNamesDoNotProveAReportedOrigin() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(machine: "Mac")], healthy: true)])
        #expect(rows.count == 1); #expect(rows[0].identity == nil); #expect(rows[0].possibleDuplicate)
    }
    @Test func unprovenNamespaceCannotCollideWithARealOrigin() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(machine: "Server")], healthy: true), PhoneInventory(wing: paired("unproven:a:Server", "Server"), sessions: [try session()], healthy: true)])
        #expect(rows.count == 2); #expect(rows.filter { $0.identity != nil }.count == 1)
    }
    @Test func olderDirectInventoryDoesNotEraseVerifiedCoordinatorMetadata() throws {
        let old = try JSONDecoder().decode(PhoneSession.self, from: Data("{\"id\":\"same\",\"name\":\"renamed\",\"agent\":\"claude\",\"status\":\"idle\",\"blocked\":false,\"unread\":false}".utf8))
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session(origin: "b", machine: "Server", pinned: true)], healthy: true), PhoneInventory(wing: paired("b", "Server"), sessions: [old], healthy: true)])
        #expect(rows.count == 1); #expect(rows[0].coordinator); #expect(rows[0].preferredRoute?.direct == true)
    }
    @Test func delimiterCharactersCannotCollideQualifiedIdentityKeys() {
        #expect(PhoneSessionIdentity(originWingID: "a:b", sessionID: "c").key != PhoneSessionIdentity(originWingID: "a", sessionID: "b:c").key)
    }
    @Test func pinnedCoordinatorIsFirstEvenWhenNameDoesNotSortFirst() throws {
        let rows = PhoneFleet.merge([PhoneInventory(wing: paired("a", "Mac"), sessions: [try session("ordinary"), try session("coordinator", pinned: true)], healthy: true)])
        #expect(rows.count == 2); #expect(rows[0].coordinator)
    }
}

private final class MemoryPhoneVault: PhoneSecretStore, @unchecked Sendable {
    let secret = try! PhoneSecret(credential: "fixture-credential", signingKey: PhoneSigningKey(useEnclave: false))
    func save(_ secret: PhoneSecret, for wing: PairedWing) throws {}
    func load(for wing: PairedWing) throws -> PhoneSecret? { secret }
    func forget(_ wing: PairedWing) throws {}
}
private actor FleetTransport: PhoneSessionTransport {
    let rows: [PhoneSession]
    let threadText: String?
    var down = false
    var uncertainInput = false
    var brokenAfterInput = false
    private(set) var calls: [String] = []
    init(_ rows: [PhoneSession], threadText: String? = nil) { self.rows = rows; self.threadText = threadText }
    func setDown() { down = true }
    func loseInputReply() { uncertainInput = true }
    func connect() throws { if down { throw PairingError.unreachable("fixture") } }
    func sessions() throws -> [PhoneSession] { if down { throw PairingError.unreachable("fixture") }; return rows }
    func command(_ op: String, session: String, input: String, originWingID: String?) throws -> JSONValue {
        calls.append("\(op)|\(originWingID ?? "")|\(session)|\(input)")
        if op == "read" && brokenAfterInput { throw PairingError.denied }
        if down || op == "input" && uncertainInput { brokenAfterInput = op == "input"; throw PairingError.unreachable("fixture") }
        if op == "thread" {
            return .object(["session_id": .string(session), "agent": .string("claude"), "state": .string("idle"), "state_source": .string("claude_hook"), "ready": .bool(true), "process_alive": .bool(true), "cursor": .integer(threadText == nil ? 0 : 1), "events": .array(threadText.map { [.object(["sequence": .integer(1), "type": .string("message"), "role": .string("assistant"), "text": .string($0)])] } ?? [])])
        }
        return .object(["ok": .bool(true), "screen": .string("fixture")])
    }
    func close() {}
}
@Suite @MainActor struct PhoneFleetModelTests {
    private func model(_ a: FleetTransport, _ b: FleetTransport? = nil) throws -> PhonePairingModel {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("phone-fleet-\(UUID()).json")
        try JSONEncoder().encode(b == nil ? [paired("a", "Mac")] : [paired("a", "Mac"), paired("b", "Server")]).write(to: file)
        return PhonePairingModel(vault: MemoryPhoneVault(), file: file, makeConnection: { wing, _ in wing.id == "a" ? a : b! })
    }
    @Test func coordinatorSelectionReadsThreadWithoutClaimingInputLease() async throws {
        let transport = FleetTransport([try session("coordinator", pinned: true)]), model = try model(transport)
        await model.restore(); await model.attach(model.fleet[0])
        #expect(model.threadVisible); #expect(model.attached?.coordinator == true)
        #expect(await transport.calls.filter { $0.hasPrefix("attach|") }.isEmpty)
        await model.detach()
    }
    @Test func archivedCoordinatorNeverClaimsTerminalOrDispatchesMutations() async throws {
        let transport = FleetTransport([try session("former-coordinator", pinned: true, status: "archived")]), model = try model(transport)
        await model.restore(); await model.attach(model.fleet[0])
        await model.showTerminal()
        #expect(model.threadVisible)
        #expect(!(await model.input("do not send")))
        await model.stop(expectedRowID: model.fleet[0].id)
        await model.detach()
        #expect(await transport.calls.contains { $0.hasPrefix("thread|") })
        #expect(await transport.calls.allSatisfy { $0.hasPrefix("thread|") })
    }
    @Test func fallbackSelectionAndInputKeepExactRemoteOrigin() async throws {
        let a = FleetTransport([try session("same", origin: "b", machine: "Server")]), b = FleetTransport([try session()])
        let model = try model(a,b); await model.restore(); await b.setDown(); await model.refresh()
        #expect(model.fleet.count == 1); #expect(model.fleet[0].preferredRoute?.wingID == "a")
        await model.attach(model.fleet[0]); #expect(model.activeRoute?.label == "via Mac")
        #expect(await model.input("hello"))
        #expect(await a.calls.contains("input|b|same|hello\r"))
        #expect(await b.calls.filter { $0.hasPrefix("input|") }.isEmpty)
        await model.detach()
    }
    @Test func uncertainInputNeverReplaysOnReconnectOrViaRoute() async throws {
        let a = FleetTransport([try session("same", origin: "b", machine: "Server")]), b = FleetTransport([try session()])
        let model = try model(a,b); await model.restore(); await model.attach(model.fleet[0]); await b.loseInputReply()
        #expect(!(await model.input("send once"))); #expect(model.error?.contains("unconfirmed") == true)
        await model.reconnectAttached()
        #expect(await b.calls.filter { $0.hasPrefix("input|") }.count == 1)
        #expect(await a.calls.filter { $0.hasPrefix("input|") }.isEmpty)
        await model.detach()
    }
    @Test func coldTranscriptSearchSeparatesSameIDsAndSendsOnlyReads() async throws {
        let a = FleetTransport([try session()], threadText: "Pairing coverage is ready."), b = FleetTransport([try session()], threadText: "The resolver retries are fixed.")
        let model = try model(a,b); await model.restore(); await model.search("resolver retries")
        #expect(model.searchResults?.count == 1)
        #expect(model.searchResults?.first?.identity?.originWingID == "b")
        #expect(model.searchPreviews.values.first?.contains("resolver retries") == true)
        #expect(await a.calls.allSatisfy { $0.hasPrefix("thread|") })
        #expect(await b.calls.allSatisfy { $0.hasPrefix("thread|") })
        #expect(model.attached == nil)
    }
    @Test func unprovenTranscriptSearchDoesNotSendAnAmbiguousID() async throws {
        let transport = FleetTransport([try session(machine: "Server")], threadText: "resolver retries"), model = try model(transport)
        await model.restore(); await model.search("resolver retries")
        #expect(model.searchResults?.isEmpty == true); #expect(model.searchNotice != nil)
        #expect(await transport.calls.isEmpty)
    }
    @Test func pollFailureCannotEraseUnconfirmedInputNotice() async throws {
        let transport = FleetTransport([try session()]), model = try model(transport)
        await model.restore(); await model.attach(model.fleet[0]); await transport.loseInputReply()
        #expect(!(await model.input("send once")))
        try await Task.sleep(for: .milliseconds(1250))
        #expect(await transport.calls.contains { $0.hasPrefix("read|") })
        #expect(model.error?.contains("unconfirmed") == true)
        #expect(await transport.calls.filter { $0.hasPrefix("input|") }.count == 1)
        await model.detach()
    }
    @Test func staleStopConfirmationCannotAffectNewSelection() async throws {
        let transport = FleetTransport([try session("first"), try session("second")]), model = try model(transport)
        await model.restore(); let old = model.fleet[0]; await model.attach(old)
        await model.attach(model.fleet[1]); await model.stop(expectedRowID: old.id)
        #expect(await transport.calls.filter { $0.hasPrefix("stop|") }.isEmpty)
        await model.detach()
    }
    @Test func unprovenRemoteCannotAttach() async throws {
        let transport = FleetTransport([try session(machine: "Server")]), model = try model(transport)
        await model.restore(); await model.attach(model.fleet[0])
        #expect(model.attached == nil); #expect(await transport.calls.isEmpty)
    }
}

private actor SlowPhoneRead: PhoneSessionTransport {
    let rows: [PhoneSession]
    private var valid = true
    private var started = false
    private var waiter: CheckedContinuation<Void, Never>?
    init(_ rows: [PhoneSession]) { self.rows = rows }
    func connect() { valid = true }
    func sessions() throws -> [PhoneSession] { guard valid else { throw PairingError.denied }; return rows }
    func waitForRead() async { if !started { await withCheckedContinuation { waiter = $0 } } }
    func command(_ op: String, session: String, input: String, originWingID: String?) async throws -> JSONValue {
        if op == "read" {
            started = true; waiter?.resume(); waiter = nil
            do { try await Task.sleep(for: .milliseconds(100)) }
            catch { valid = false; throw error }
        }
        guard valid else { throw PairingError.denied }
        return .object(["ok": .bool(true), "screen": .string("fixture")])
    }
    func close() { valid = false }
}
@Suite @MainActor struct PhoneReadRetirementTests {
    @Test func leavingDuringAReadKeepsThePairedConnectionUsable() async throws {
        let transport = SlowPhoneRead([try session()])
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("phone-read-\(UUID()).json")
        try JSONEncoder().encode([paired("a", "Mac")]).write(to: file)
        let model = PhonePairingModel(vault: MemoryPhoneVault(), file: file, makeConnection: { _, _ in transport })
        await model.restore(); await model.attach(model.fleet[0]); await transport.waitForRead()
        await model.detach(); await model.refresh()
        #expect(model.online); #expect(model.fleet[0].preferredRoute != nil)
        #expect(model.error == nil)
    }
}
