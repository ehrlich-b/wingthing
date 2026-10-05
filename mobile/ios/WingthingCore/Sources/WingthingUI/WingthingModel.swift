import CryptoKit
import Foundation
import SwiftUI
import WingthingCore

// What the app can truthfully say about the configured home right now.
public enum HomeConnectionPhase: Equatable, Sendable {
    case notConfigured, unverified, connecting, online, offline(String), failed(String), disconnected
    public var title: String {
        switch self {
        case .notConfigured: "Not connected"
        case .unverified: "Not verified yet"
        case .connecting: "Connecting…"
        case .online: "Online"
        case .offline: "Can't reach your home"
        case .failed: "Couldn't connect"
        case .disconnected: "Disconnected"
        }
    }
    public var detail: String? {
        switch self {
        case .notConfigured: "Enter your home's address and identity, then connect with access you already have."
        case .unverified: "Reconnect to check this home before showing live tasks."
        case .connecting: "Checking your home, your account, and the home's pinned identity."
        case .online: nil
        case .offline(let message): "Showing saved tasks, which may be out of date. \(message)"
        case .failed(let message): message
        case .disconnected: "Saved tasks stay readable. Enter your access token again to reconnect."
        }
    }
}

@MainActor public final class WingthingModel: ObservableObject {
    @Published public private(set) var profile: HomeProfile?
    @Published public private(set) var parent: ParentSelection?
    @Published public private(set) var roots: [Conversation] = []
    @Published public private(set) var tasks: [ConversationTask] = []
    @Published public private(set) var selected: ConversationReference?
    @Published public private(set) var execution: ExecutionReference?
    @Published public private(set) var transcript = TranscriptState()
    @Published public private(set) var parentTranscript = TranscriptState()
    @Published public private(set) var phase = HomeConnectionPhase.notConfigured
    @Published public private(set) var busy = false
    @Published public private(set) var pending: PendingInput?
    @Published public private(set) var error: String?
    @Published public var draft = ""
    // The only holder of the existing token. Released on disconnect or replacement.
    private var client: HomeClient?
    private var store: LocalConversationStore?
    private var generation = UUID()
    private var treeObservedAt: Date?
    private let cacheDirectory: URL?

    // Empty/default app performs no network, file, or credential action.
    public init(cacheDirectory: URL? = nil) { self.cacheDirectory = cacheDirectory }

    public var connected: Bool { phase == .online }
    public var canReconnect: Bool { client != nil && !busy }
    public var canDisconnect: Bool { client != nil || phase == .connecting }

    // Explicit user connection to an already authorized home. Fields are checked
    // before any file or network access; no pairing, grant, or token is created.
    public func connect(origin: String, transport: HomeTransport, userID: String, wingID: String, wingPublicKey: String, existingBearer: String, wire: any HomeWire = URLSessionHomeWire()) async {
        let (requested, released) = beginReplacement()
        phase = .connecting; busy = true
        await released?.disconnect()
        do {
            let home = try Self.pinnedProfile(origin: origin, transport: transport, userID: userID, wingID: wingID, wingPublicKey: wingPublicKey)
            let bearer = existingBearer.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !bearer.isEmpty else { throw ClientError.invalidConfiguration("Enter the access token you already use for this home.") }
            guard try await install(home, bearer: bearer, cacheFile: cacheFile(for: home), wire: wire, requested: requested) else { return }
        } catch {
            guard requested == generation else { return }
            phase = classify(error); self.error = error.localizedDescription; busy = false
            return
        }
        busy = false
        await refresh()
    }

    // Drops the client and its token. Saved tasks and transcript stay readable.
    public func disconnect() async {
        generation = UUID()
        let released = client
        client = nil; busy = false
        transcript.markUnavailable(); parentTranscript.markUnavailable()
        phase = profile == nil ? .notConfigured : .disconnected
        await released?.disconnect()
    }

    // Provisioning seam only. Native sign-in/identity QR approval is not wired;
    // the caller must explicitly provide an already authorized exact profile.
    public func configure(profile: HomeProfile, existingBearer: String, cacheFile: URL, wire: any HomeWire = URLSessionHomeWire()) async throws {
        let (requested, released) = beginReplacement()
        await released?.disconnect()
        do {
            guard try await install(profile, bearer: existingBearer, cacheFile: cacheFile, wire: wire, requested: requested) else { return }
            phase = .unverified
        } catch {
            if requested == generation { phase = classify(error) }
            throw error
        }
    }

    public var currentStatus: ObservedStatus { transcript.status(connected: connected) }
    public var parentStatus: ObservedStatus { parentTranscript.status(connected: connected) }
    public var inputReady: Bool { !busy && pending == nil && transcript.canSend(connected: connected) }
    public var items: [TranscriptItem] { transcript.events.flatMap(TranscriptItem.items) }
    public var title: String { tasks.first(where: { $0.conversation.conversationID == selected?.conversationID && $0.conversation.wingID == selected?.wingID })?.conversation.title ?? "Conversation" }
    public var attention: HumanAttention? {
        guard currentStatus == .needsInput, let execution, let lifecycle = transcript.lifecycle else { return nil }
        return HumanAttention(execution: execution, lifecycle: lifecycle)
    }

    // The home's native control API has no stop for one selected conversation's
    // run, so Stop is never enabled and nothing is sent. Dropping this connection
    // sends nothing either: work keeps running on the wing.
    public var canStop: Bool { false }
    public static let stopUnavailableHint = "Stopping from this phone isn't available yet. Leaving this screen or disconnecting doesn't stop the task."
    public var stopNotice: String? {
        guard selected != nil else { return nil }
        switch currentStatus {
        case .working, .starting, .needsInput:
            return "Stopping from this phone isn't available yet. This task keeps running on your computer, even if you leave this screen or disconnect. To stop it, use Wingthing on that computer."
        case .offline:
            return "Disconnecting doesn't stop a task that's running on your computer. Stopping from this phone isn't available yet."
        default:
            return nil
        }
    }

    // Explicit reconnect. Reads only; a pending input is never resent here.
    public func refresh() async {
        guard let client, let profile, !busy else { return }
        busy = true; error = nil; phase = .connecting; let requested = generation
        do {
            _ = try await client.verifyHome()
            guard requested == generation else { return } // Superseded: never list on a dropped client.
            let inventory = try await client.conversations(on: profile.homeWingID)
            guard requested == generation else { return }
            roots = inventory.filter(\.isRoot); phase = .online; busy = false
            if let selected { await open(selected) }
            else if let parent { await open(parent.reference) }
        } catch {
            guard requested == generation else { return }
            phase = classify(error); transcript.markUnavailable(); parentTranscript.markUnavailable(); self.error = error.localizedDescription; busy = false
        }
    }

    public func reference(for node: Conversation) -> ConversationReference? {
        guard let profile else { return nil }
        return ConversationReference(profileID: profile.id, userID: profile.expectedUserID, wingID: node.wingID, conversationID: node.conversationID)
    }

    public func openParent() async { if let parent { await open(parent.reference) } }
    public func open(_ reference: ConversationReference) async {
        guard let profile, let store else { return }
        guard phase == .online, let client else { await openSaved(reference, from: store); return }
        generation = UUID(); let requested = generation
        busy = true; error = nil
        do {
            try reference.validate(profile)
            let view = try await client.conversation(reference)
            guard requested == generation else { return }
            guard let task = view.tasks.first(where: { $0.conversation.conversationID == reference.conversationID && $0.conversation.wingID == reference.wingID }) else { throw ClientError.staleReference }
            // An unknown provider binding in the tree is the same execution, not a
            // new one: keep its draft, transcript cursor, and known binding.
            let observed = task.lifecycle?.providerSessionID
            let continuing = execution.map { $0.conversation == reference && $0.sessionID == task.conversation.sessionID && (observed == nil || $0.providerSessionID == nil || $0.providerSessionID == observed) } ?? false
            let target = ExecutionReference(conversation: reference, sessionID: task.conversation.sessionID, providerSessionID: observed ?? (continuing ? execution?.providerSessionID : nil))
            if !continuing { transcript = TranscriptState(); draft = "" }
            selected = reference; execution = target; tasks = view.tasks; treeObservedAt = Date()
            let rootID = task.conversation.rootConversationID
            if let root = view.tasks.first(where: { $0.conversation.conversationID == rootID && $0.conversation.wingID == reference.wingID && $0.conversation.isRoot }), let rootRef = self.reference(for: root.conversation) {
                let selection = ParentSelection(reference: rootRef, title: root.conversation.title, context: root.coordinatorContext ?? root.conversation.coordinatorContext ?? view.coordinatorContext)
                try await store.selectParent(selection)
                guard requested == generation else { return }
                parent = selection
                parentTranscript = TranscriptState()
                if root.lifecycleError == nil, let lifecycle = root.lifecycle {
                    try parentTranscript.apply(lifecycle, target: ExecutionReference(conversation: rootRef, sessionID: root.conversation.sessionID, providerSessionID: lifecycle.providerSessionID))
                }
            }
            if let issue = task.lifecycleError { throw ClientError.response(issue) }
            let lifecycle = try await client.transcript(target, after: transcript.cursor)
            guard requested == generation else { return }
            let bound = ExecutionReference(conversation: reference, sessionID: target.sessionID, providerSessionID: lifecycle.providerSessionID)
            execution = bound
            try transcript.apply(lifecycle, target: bound); phase = .online
            pending = await store.pending(for: bound)
            if pending?.delivery == .nativeReceiptObserved || pending?.delivery == .definitelyNotSent { pending = nil }
            try await store.cache(CachedConversation(reference: reference, tasks: view.tasks, transcript: transcript))
        } catch {
            guard requested == generation else { return }
            transcript.markUnavailable(); parentTranscript.markUnavailable(); phase = classify(error); self.error = error.localizedDescription
            if selected != reference, let cached = try? await store.cached(reference) {
                tasks = cached.tasks; transcript = cached.transcript; selected = reference; execution = nil; pending = nil
            }
        }
        if requested == generation { busy = false }
    }

    public func pollTranscript() async {
        guard connected, !busy, let client, let execution else { return }
        let requested = generation
        do {
            let view = try await client.transcript(execution, after: transcript.cursor)
            guard requested == generation, self.execution == execution else { return }
            try transcript.apply(view, target: execution)
            // Pin the first observed provider session, as open() does, so input
            // saved from here stays bound to it.
            if execution.providerSessionID == nil, let bound = view.providerSessionID, !bound.isEmpty {
                self.execution = ExecutionReference(conversation: execution.conversation, sessionID: execution.sessionID, providerSessionID: bound)
            }
            if parent?.reference == execution.conversation { parentTranscript = transcript }
            if let store, let selected { try await store.cache(CachedConversation(reference: selected, tasks: tasks, transcript: transcript)) }
        } catch {
            guard requested == generation else { return }
            transcript.markUnavailable(); phase = classify(error); self.error = error.localizedDescription
        }
    }

    public func status(for task: ConversationTask) -> ObservedStatus {
        guard connected else { return .offline }
        guard task.lifecycleError == nil else { return .unavailable }
        guard let treeObservedAt, let view = task.lifecycle, let reference = reference(for: task.conversation) else { return .unknown }
        var state = TranscriptState()
        try? state.apply(view, target: ExecutionReference(conversation: reference, sessionID: task.conversation.sessionID, providerSessionID: view.providerSessionID), now: treeObservedAt)
        return state.status(connected: true)
    }

    public func sendOrCheck() async {
        guard connected, !busy, let client, let store, let execution else { return }
        let requested = generation; busy = true; error = nil
        do {
            var input: PendingInput
            if let pending { guard pending.execution == execution else { throw ClientError.staleReference }; input = pending }
            else {
                guard transcript.canSend(connected: connected) else { throw ClientError.response("The current native execution is not ready for input.") }
                input = try PendingInput(execution: execution, input: draft)
                try await store.savePending(input) // Must be saved before sending.
                guard requested == generation else { return }
                pending = input
            }
            input.markUnconfirmed(); try await store.savePending(input)
            guard requested == generation else { return }
            pending = input
            let result = try await client.submit(input)
            try await store.savePending(result)
            guard requested == generation else { return }
            if result.delivery == .nativeReceiptObserved { pending = nil; draft = "" }
            else if result.delivery == .definitelyNotSent { pending = nil; draft = result.input; error = result.notice }
            else { pending = result }
        } catch {
            guard requested == generation else { return }
            self.error = error.localizedDescription
        }
        if requested == generation { busy = false }
    }

    // Shows the saved copy without any network or provider action. A check in
    // flight owns the connection state, so it is not superseded from here.
    private func openSaved(_ reference: ConversationReference, from store: LocalConversationStore) async {
        guard phase != .connecting else { return }
        generation = UUID(); let requested = generation
        let cached = try? await store.cached(reference)
        guard requested == generation, let cached else { return }
        tasks = cached.tasks; transcript = cached.transcript; selected = reference; execution = nil; pending = nil
    }

    // Supersedes every in-flight result and forgets the previous home. A failed
    // replacement therefore can never leave the earlier client usable.
    private func beginReplacement() -> (UUID, HomeClient?) {
        generation = UUID()
        let released = client
        client = nil; store = nil; profile = nil; parent = nil; roots = []; tasks = []; selected = nil; execution = nil
        transcript = TranscriptState(); parentTranscript = TranscriptState(); pending = nil; draft = ""; treeObservedAt = nil
        error = nil; busy = false; phase = .notConfigured
        return (generation, released)
    }

    private func install(_ home: HomeProfile, bearer: String, cacheFile: URL, wire: any HomeWire, requested: UUID) async throws -> Bool {
        let connection = try HomeClient(profile: home, existingBearer: bearer, wire: wire)
        let local = try LocalConversationStore(profile: home, file: cacheFile)
        let restored = await local.parent()
        let cached: CachedConversation?
        if let restored { cached = try await local.cached(restored.reference) } else { cached = nil }
        guard requested == generation else { return false }
        profile = home; store = local; client = connection
        parent = restored
        if let restored, let cached {
            tasks = cached.tasks; transcript = cached.transcript; selected = restored.reference
        }
        return true
    }

    private func classify(_ error: any Error) -> HomeConnectionPhase {
        if error is URLError || error as? ClientError == .timeout || error as? ClientError == .offline { return .offline(error.localizedDescription) }
        return .failed(error.localizedDescription)
    }

    private func cacheFile(for home: HomeProfile) throws -> URL {
        let directory = try cacheDirectory ?? FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: false)
            .appendingPathComponent("Wingthing", isDirectory: true).appendingPathComponent("Homes", isDirectory: true)
        return directory.appendingPathComponent("home-\(home.id.uuidString.lowercased()).json")
    }

    // The profile ID (and so the cache file) is derived only from the canonical
    // origin, account, wing, and pinned key. It never includes the token.
    private static func pinnedProfile(origin: String, transport: HomeTransport, userID: String, wingID: String, wingPublicKey: String) throws -> HomeProfile {
        let user = userID.trimmingCharacters(in: .whitespacesAndNewlines), wing = wingID.trimmingCharacters(in: .whitespacesAndNewlines)
        let key = wingPublicKey.trimmingCharacters(in: .whitespacesAndNewlines)
        let invalid = ClientError.invalidConfiguration("Choose an exact HTTPS home origin without a path or embedded credentials.")
        guard let raw = URL(string: origin.trimmingCharacters(in: .whitespacesAndNewlines)) else { throw invalid }
        let checked = try HomeProfile(origin: raw, transport: transport, expectedUserID: user, homeWingID: wing, homeWingPublicKey: key)
        guard let parts = URLComponents(url: checked.origin, resolvingAgainstBaseURL: false), let host = parts.host?.lowercased(), !host.isEmpty,
              let keyBytes = Data(base64Encoded: key) else { throw invalid }
        let pinnedKey = keyBytes.base64EncodedString()
        let id = stableID(["wingthing.home-profile.v1", "https", host, String(parts.port ?? 443), user, wing, pinnedKey])
        return try HomeProfile(id: id, origin: checked.origin, transport: transport, expectedUserID: user, homeWingID: wing, homeWingPublicKey: pinnedKey)
    }

    // Length-prefixed fields so no delimiter inside a value can collide.
    private static func stableID(_ fields: [String]) -> UUID {
        var encoded = Data()
        for field in fields {
            let bytes = Data(field.utf8)
            withUnsafeBytes(of: UInt64(bytes.count).bigEndian) { encoded.append(contentsOf: $0) }
            encoded.append(bytes)
        }
        var digest = Array(SHA256.hash(data: encoded).prefix(16))
        digest[6] = (digest[6] & 0x0F) | 0x80 // RFC 9562 version 8 (custom hash).
        digest[8] = (digest[8] & 0x3F) | 0x80
        return digest.withUnsafeBytes { UUID(uuid: $0.loadUnaligned(as: uuid_t.self)) }
    }
}
