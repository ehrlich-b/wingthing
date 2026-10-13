import Foundation
import SwiftUI
import WingthingCore

@MainActor public final class PhonePairingModel: ObservableObject {
    @Published public private(set) var wings: [PairedWing] = []
    @Published public private(set) var selected: PairedWing?
    @Published public private(set) var invitation: PairingInvitation?
    @Published public private(set) var emoji: String?
    @Published public private(set) var fleet: [PhoneFleetSession] = []
    @Published public private(set) var busy = false
    @Published public private(set) var waitingForTerminal = false
    @Published public private(set) var recoverablePairing = false
    @Published public private(set) var savingPairing = false
    @Published public private(set) var error: String?
    @Published public private(set) var hardStop = false
    @Published public private(set) var scanning = true
    @Published public private(set) var attached: PhoneFleetSession?
    @Published public private(set) var activeRoute: PhoneRoute?
    @Published public private(set) var output = ""
    @Published public private(set) var thread = TranscriptState()
    @Published public private(set) var threadVisible = false
    @Published public private(set) var online = false
    @Published public private(set) var notificationsEnabled = false
    @Published public private(set) var notificationNotice: String?
    @Published public private(set) var carrier: String?
    @Published public private(set) var latencyMilliseconds: Int?
    private var attentionSeen: Set<PhoneSessionIdentity> = []
    private let attention: any PhoneAttentionDelivery
    public let simulatorFixture: Bool
    private let vault: any PhoneSecretStore
    private let file: URL
    private let makeConnection: @Sendable (PairedWing, PhoneSecret) -> any PhoneSessionTransport
    private let makeEnrollment: @MainActor (PairingInvitation) throws -> PhoneEnrollment
    private var enrollment: PhoneEnrollment?
    private var connections: [String: any PhoneSessionTransport] = [:]
    private var inventories: [String: PhoneInventory] = [:]
    private let transcriptScope = UUID()
    private var epoch = UUID()
    private var restored = false
    private var readTask: Task<Void, Never>?
    private var confirmTask: Task<Void, Never>?
    private var terminalAttached = false
    private var unconfirmedInput = false
    public init(vault: any PhoneSecretStore = PhoneKeychainStore(), file: URL? = nil, attention: (any PhoneAttentionDelivery)? = nil,
                makeConnection: @escaping @Sendable (PairedWing, PhoneSecret) -> any PhoneSessionTransport = { PhoneConnection(wing: $0, secret: $1) },
                makeEnrollment: @escaping @MainActor (PairingInvitation) throws -> PhoneEnrollment = { try PhoneEnrollment(invitation: $0) }) {
        self.attention = attention ?? PhoneAttentionNotifications()
        self.makeConnection = makeConnection; self.makeEnrollment = makeEnrollment
        let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Wingthing")
        #if DEBUG && targetEnvironment(simulator)
        if let value = ProcessInfo.processInfo.environment["WT_IOS_PHONE_FIXTURE_ID"], let id = UUID(uuidString: value) {
            self.file = directory.appendingPathComponent("fixture-\(id.uuidString).json")
            self.vault = SimulatorPhoneSecrets(directory: directory.appendingPathComponent("fixture-secrets-\(id.uuidString)"))
            simulatorFixture = true
        } else { self.file = file ?? directory.appendingPathComponent("paired-wings.json"); self.vault = vault; simulatorFixture = false }
        #else
        self.file = file ?? directory.appendingPathComponent("paired-wings.json"); self.vault = vault; simulatorFixture = false
        #endif
    }
    public func restore() async {
        guard !restored else { return }; restored = true
        do {
            if FileManager.default.fileExists(atPath: file.path) {
                wings = try JSONDecoder().decode([PairedWing].self, from: Data(contentsOf: file))
                guard Set(wings.map(\.id)).count == wings.count, wings.allSatisfy({ PairingInvitation.localAllowed($0.local) && ($0.endpoints?.allSatisfy(PairingInvitation.localAllowed) ?? true) && ($0.relay.map(PairingInvitation.relayAllowed) ?? true) }) else { throw PairingError.invalidQR }
            }
            if !wings.isEmpty { scanning = false; await refresh() }
        } catch { self.error = "Could not restore paired machines. \(error.localizedDescription)" }
    }
    public func scan(_ link: String) async {
        guard !busy, invitation == nil else { return }
        epoch = UUID(); let request = epoch; error = nil; hardStop = false; busy = true
        do {
            let q = try PairingInvitation(link: link)
            if simulatorFixture && (q.local.host != "127.0.0.1" || q.relay != nil || !(q.endpoints?.allSatisfy { $0.host == "127.0.0.1" } ?? true)) { throw PairingError.invalidQR }
            invitation = q
            let enrollment = try makeEnrollment(q); self.enrollment = enrollment
            try await enrollment.start()
            guard epoch == request else { return }
            emoji = enrollment.emoji; busy = false
        } catch {
            guard epoch == request else { return }; fail(error); busy = false
            if let enrollment { try? await enrollment.cancel() }; self.enrollment = nil
        }
    }
    public func confirm() {
        guard !busy, let emoji, let enrollment else { return }
        finishPairing { try await enrollment.confirm(emoji: emoji) }
    }
    public func checkPairingResult() {
        guard recoverablePairing, !busy, let enrollment else { return }
        finishPairing { try await enrollment.status() }
    }
    private func finishPairing(_ operation: @escaping @MainActor () async throws -> (PairedWing, PhoneSecret)?) {
        let request = epoch; busy = true; waitingForTerminal = true; recoverablePairing = false; error = nil
        confirmTask = Task {
            do {
                var result = try await operation()
                while result == nil {
                    try await Task.sleep(for: .seconds(1)); try Task.checkCancellation()
                    guard let enrollment else { throw PairingError.cancelled }
                    result = try await enrollment.status()
                }
                guard epoch == request, let (wing, secret) = result else { return }
                savingPairing = true; waitingForTerminal = false
                // A yield lets the secure-save state render without delaying the protocol.
                await Task.yield()
                try vault.save(secret, for: wing)
                var saved = wings; saved.removeAll { $0.wingID == wing.wingID }; saved.append(wing)
                do { try persist(saved) } catch { try? vault.forget(wing); throw error }
                wings = saved; invitation = nil; emoji = nil; enrollment = nil; busy = false; savingPairing = false; scanning = false
                selected = nil; await refresh()
            } catch {
                guard epoch == request else { return }; busy = false; waitingForTerminal = false; savingPairing = false
                if error is URLError || { if case PairingError.unreachable = error { return true }; return false }() {
                    recoverablePairing = true
                    self.error = "Pairing delivery is unconfirmed. Check the original pairing result before approving another invitation."
                } else {
                    fail(error); try? await enrollment?.cancel(); enrollment = nil
                    invitation = nil; emoji = nil; scanning = true
                }
            }
        }
    }
    private func persist(_ wings: [PairedWing]) throws {
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSONEncoder().encode(wings).write(to: file, options: .atomic)
    }
    public func cancel() async {
        epoch = UUID(); confirmTask?.cancel(); confirmTask = nil
        if let enrollment { try? await enrollment.cancel() }
        enrollment = nil; invitation = nil; emoji = nil; busy = false; waitingForTerminal = false; savingPairing = false; recoverablePairing = false; hardStop = false
        error = "Pairing cancelled. Scan a fresh QR before trying again."; scanning = true
    }
    public func scanAnother() async {
        await detach(); await cancel(); selected = nil; error = nil
    }
    public func showAll() async { await detach(); selected = nil; scanning = false; await refresh() }
    public func select(_ wing: PairedWing) async { await detach(); selected = wing; scanning = false; await refresh() }
    public func refresh() async {
        guard !busy else { return }; busy = true; let request = epoch; error = nil
        for wing in wings {
            do {
                let client: any PhoneSessionTransport
                if let existing = connections[wing.wingID] { client = existing }
                else {
                    guard let secret = try vault.load(for: wing) else { throw PairingError.revoked }
                    client = makeConnection(wing, secret); try await client.connect()
                    guard epoch == request else { await client.close(); return }
                    connections[wing.wingID] = client
                }
                let start = Date(); let list = try await client.sessions()
                guard epoch == request else { return }
                latencyMilliseconds = Int(Date().timeIntervalSince(start) * 1000)
                inventories[wing.wingID] = PhoneInventory(wing: wing, sessions: list, healthy: true)
                if let client = client as? PhoneConnection {
                    let updated = await client.wing
                    if updated != wing {
                        var saved = wings
                        if let index = saved.firstIndex(where: { $0.id == updated.id }) { saved[index] = updated }
                        try persist(saved); wings = saved
                    }
                    carrier = await client.carrierDescription()
                }
            } catch {
                guard epoch == request else { return }
                inventories[wing.wingID] = PhoneInventory(wing: wing, sessions: inventories[wing.wingID]?.sessions ?? [], healthy: false)
                await connections.removeValue(forKey: wing.wingID)?.close()
                self.error = "\(wing.name): \(error.localizedDescription)"
                if error as? PairingError == .identityMismatch || error as? PairingError == .badProof { hardStop = true }
            }
        }
        fleet = PhoneFleet.merge(Array(inventories.values)); online = inventories.values.contains(where: \.healthy); busy = false
        let blocked = Set(fleet.filter { $0.session.blocked && $0.preferredRoute != nil }.compactMap(\.identity))
        if notificationsEnabled {
            for identity in blocked.subtracting(attentionSeen) { try? await attention.notify(identity) }
        }
        attentionSeen = blocked
    }
    public func attach(_ row: PhoneFleetSession) async {
        guard !busy, row.identity != nil else { return }
        await detach(); epoch = UUID(); let request = epoch; busy = true; error = nil
        for route in row.routes.sorted(by: { a, b in a.direct != b.direct ? a.direct : a.wingID < b.wingID }) where route.healthy {
            guard let client = connections[route.wingID], let identity = row.identity else { continue }
            do {
                if !row.coordinator { _ = try await client.command("attach", session: identity.sessionID, input: "", originWingID: identity.originWingID) }
                guard epoch == request else { return }
                attached = row; activeRoute = route; output = ""; thread = TranscriptState(); threadVisible = row.coordinator; terminalAttached = !row.coordinator; online = true; busy = false
                startReading(request: request); return
            } catch {
                guard epoch == request else { return }
                // Only selecting a view can fall back. Input and Stop are never replayed.
                if error as? PairingError == .identityMismatch || error as? PairingError == .badProof { fail(error); busy = false; return }
                self.error = "\(route.label): \(error.localizedDescription)"
            }
        }
        busy = false; online = false
    }
    private func startReading(request: UUID) {
        readTask?.cancel()
        readTask = Task {
            do {
                while !Task.isCancelled {
                    if !busy { try await readCurrent(request: request) }
                    try await Task.sleep(for: .seconds(threadVisible ? 2 : 1))
                }
            } catch {
                guard epoch == request, !(error is CancellationError) else { return }
                if !unconfirmedInput || error as? PairingError == .identityMismatch || error as? PairingError == .badProof { fail(error) }
                else { online = false }
            }
        }
    }
    private func readCurrent(request: UUID) async throws {
        guard let row = attached, let identity = row.identity, let route = activeRoute, let client = connections[route.wingID] else { return }
        if threadVisible {
            let result = try await client.command("thread", session: identity.sessionID, input: "{\"after\":\(thread.cursor)}", originWingID: identity.originWingID)
            guard epoch == request, attached?.id == row.id else { return }
            let lifecycle = try JSONDecoder().decode(SessionLifecycle.self, from: JSONEncoder().encode(result))
            guard lifecycle.sessionID == identity.sessionID else { throw PairingError.identityMismatch }
            let reference = ConversationReference(profileID: transcriptScope, userID: "paired-device", wingID: identity.originWingID, conversationID: identity.sessionID)
            try thread.apply(lifecycle, target: ExecutionReference(conversation: reference, sessionID: identity.sessionID))
        } else {
            let result = try await client.command("read", session: identity.sessionID, input: "", originWingID: identity.originWingID)
            guard epoch == request, attached?.id == row.id else { return }
            if let screen = result["screen"]?.string { output = screen } else { output += result["output"]?.string ?? "" }
            if output.utf8.count > 128 << 10 { output = String(output.suffix(64 << 10)) }
            if result["truncated"] == .bool(true) { error = "Some output was skipped while this phone was away." }
            if result["ended"] == .bool(true) { error = "This session ended."; readTask?.cancel() }
        }
    }
    public func showTerminal() async {
        guard !busy, let row = attached, let identity = row.identity, let route = activeRoute, let client = connections[route.wingID] else { return }
        busy = true; let request = epoch
        do {
            if !terminalAttached { _ = try await client.command("attach", session: identity.sessionID, input: "", originWingID: identity.originWingID) }
            guard epoch == request else { return }; terminalAttached = true; threadVisible = false; error = nil; busy = false; startReading(request: request)
        } catch { guard epoch == request else { return }; fail(error); busy = false }
    }
    public func showThread() { guard attached?.coordinator == true, !busy else { return }; threadVisible = true; error = nil; startReading(request: epoch) }
    public func input(_ text: String) async -> Bool {
        guard !busy, online, let row = attached, let identity = row.identity, let route = activeRoute, let client = connections[route.wingID] else { return false }
        busy = true; let request = epoch
        do {
            if !terminalAttached {
                _ = try await client.command("attach", session: identity.sessionID, input: "", originWingID: identity.originWingID)
                guard epoch == request else { return false }; terminalAttached = true
            }
            _ = try await client.command("input", session: identity.sessionID, input: text + "\r", originWingID: identity.originWingID)
            guard epoch == request else { return false }; busy = false; return true
        } catch {
            guard epoch == request else { return false }; unconfirmedInput = true; fail(error)
            self.error = "Input delivery is unconfirmed. Reconnect and inspect the session before sending again. \(error.localizedDescription)"; busy = false; return false
        }
    }
    public func stop(expectedRowID: String) async {
        guard !busy, online, let row = attached, row.id == expectedRowID, let identity = row.identity, let route = activeRoute, let client = connections[route.wingID] else { return }
        busy = true; let request = epoch
        do {
            _ = try await client.command("stop", session: identity.sessionID, input: "", originWingID: identity.originWingID)
            guard epoch == request else { return }; error = "Stop requested for \(row.session.title) on \(row.machine)."; readTask?.cancel()
        } catch { guard epoch == request else { return }; fail(error) }; busy = false
    }
    public func detach() async {
        epoch = UUID(); let request = epoch; readTask?.cancel(); readTask = nil
        let row = attached, route = activeRoute, hadTerminal = terminalAttached
        attached = nil; activeRoute = nil; terminalAttached = false; unconfirmedInput = false; output = ""; thread = TranscriptState(); busy = true
        if hadTerminal, let identity = row?.identity, let route, let client = connections[route.wingID] {
            _ = try? await client.command("detach", session: identity.sessionID, input: "", originWingID: identity.originWingID)
        }
        guard epoch == request else { return }; busy = false
    }
    public func reconnectAttached() async {
        guard let row = attached else { return }
        let route = activeRoute
        await detach()
        if let route { await connections.removeValue(forKey: route.wingID)?.close() }
        await refresh()
        if let fresh = fleet.first(where: { $0.id == row.id }) { await attach(fresh) }
    }
    public func enableNotifications() async {
        do {
            notificationsEnabled = try await attention.enable()
            notificationNotice = notificationsEnabled ? "Lock-screen notifications hide session names and content." : "Notifications are disabled. Allow them in iPhone Settings."
        } catch { notificationNotice = error.localizedDescription }
    }
    public func disableNotifications() { attention.disable(); notificationsEnabled = false }
    private func fail(_ error: Error) {
        self.error = error.localizedDescription; online = false
        if error as? PairingError == .identityMismatch || error as? PairingError == .badProof { hardStop = true }
    }
}
