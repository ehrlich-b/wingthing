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
    @Published public private(set) var pendingContinuation: PendingContinuation?
    @Published public private(set) var continuationNotice: String?
    @Published public private(set) var continuationAvailability: ContinuationAvailability?
    @Published public private(set) var creationOptions: ConversationCreationOptions?
    @Published public private(set) var pendingLaunch: PendingConversationLaunch?
    @Published public private(set) var creationNotice: String?
    @Published public private(set) var fixtureDiagnostics: String?
    #if DEBUG
    private var fixtureWire: ConversationTransportFixture?
    #endif
    private var continuationObservedAt: Date?
    // From the latest read on this connection only; never restored or cached.
    @Published public private(set) var stopCapability: StopCapability?
    @Published public private(set) var pendingStop: PendingStop?
    @Published private var stopFlight: UUID?
    // Bumped by reconnect, disconnect, open and replacement: a read already in
    // flight then never publishes Stop support, even if the generation survives.
    private var readEpoch = UUID()
    @Published public private(set) var error: String?
    @Published public var draft = "" { didSet { if !presentingDraft { saveEditedDraft() } } }
    @Published public private(set) var localSaveError: String?
    private var presentingDraft = false
    private var localViews: LocalConversationViewStore?
    private var readingPositions: [ExecutionReference: ConversationReadingPosition] = [:]
    // Unsent text is saved atomically for one exact home and execution.
    // Submitted text is owned by its persisted PendingInput receipt instead.
    private var unsentDrafts: [ExecutionReference: String] = [:]
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
    public var canCreateConversation: Bool {
        !inspectionOnly && connected && !busy && pendingLaunch == nil && creationOptions?.projects.isEmpty == false
    }

    // Read-only preparation. The computer advertises both personal capability
    // and existing projects through the same authenticated encrypted tunnel.
    public func prepareConversationCreation() async {
        creationOptions = nil
        guard !inspectionOnly, connected, !busy, let client, let store else { return }
        let requested = generation
        do {
            let restored = try await store.unfinishedLaunch()
            let options = try await client.creationOptions()
            guard requested == generation else { return }
            pendingLaunch = restored; creationOptions = options
            creationNotice = options.projects.isEmpty ? "No existing projects are advertised by this computer. Add your project in Wingthing on that computer, then reconnect." : nil
        } catch {
            guard requested == generation else { return }
            creationNotice = error.localizedDescription
        }
    }

    // An uncertain launch is always retried unchanged. Only a confirmed failure
    // releases the form for a new, explicitly chosen request identity.
    public func newConversationAttempt() {
        guard pendingLaunch?.progress == .failed, !busy else { return }
        pendingLaunch = nil; creationNotice = nil
    }

    public func createConversation(label: String, workspace: String, model: String, input: String) async {
        guard !inspectionOnly, connected, !busy, let client, let store, let profile else { return }
        let requested = generation; busy = true; creationNotice = nil
        var launch: PendingConversationLaunch?
        do {
            let firstAttempt = pendingLaunch == nil
            if let pendingLaunch {
                guard !pendingLaunch.progress.final else { throw ClientError.response("Choose a new attempt after the confirmed failure.") }
                launch = pendingLaunch
            } else {
                let proposed = try PendingConversationLaunch(profile: profile, label: label, workspace: workspace, model: model, input: input)
                // The person may spend longer than the observation TTL writing
                // the first message. Recheck read-only on this explicit tap.
                let fresh = try await client.creationOptions()
                guard requested == generation else { return }
                creationOptions = fresh
                guard fresh.covers(proposed) else { throw ClientError.invalidConfiguration("This project is no longer advertised by your computer. Choose an existing project again.") }
                try await store.saveLaunch(proposed)
                launch = proposed
            }
            guard requested == generation, var intent = launch else { return }
            intent.markUnconfirmed()
            try await store.saveLaunch(intent) // A failed durable save sends nothing.
            guard requested == generation else { return }
            pendingLaunch = intent
            let updated = try await client.submitConversationLaunch(intent, firstAttempt: firstAttempt)
            try await store.saveLaunch(updated)
            guard requested == generation else { return }
            pendingLaunch = updated; creationNotice = updated.detail
            if updated.progress == .started, let target = updated.target {
                let inventory = try await client.conversations(on: profile.homeWingID)
                guard requested == generation else { return }
                roots = inventory.filter(\.isRoot); busy = false
                await open(target.conversation, expectedExecution: target)
                if selected == target.conversation && execution?.sessionID == target.sessionID {
                    pendingLaunch = nil
                } else {
                    creationNotice = "Your computer created the conversation, but its current execution couldn't be verified. Open it from Conversations."
                }
            }
        } catch {
            guard requested == generation else { return }
            creationNotice = error.localizedDescription
            // A dispatched intent remains saved even if its response or status
            // save fails. Reconnect and relaunch never replay it automatically.
            if pendingLaunch?.progress == .unconfirmed { phase = classify(error) }
        }
        guard requested == generation || pendingLaunch == nil else { return }
        busy = false
        #if DEBUG
        if let fixtureWire { fixtureDiagnostics = await fixtureWire.diagnostics() }
        #endif
    }

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
        persistReadingPosition()
        generation = UUID()
        let released = client
        client = nil; busy = false; stopCapability = nil; continuationAvailability = nil; continuationObservedAt = nil; creationOptions = nil; stopFlight = nil; readEpoch = UUID()
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
    public var parentStatus: ObservedStatus {
        guard connected else { return .offline }
        guard let parent, let root = tasks.first(where: { $0.conversation.conversationID == parent.reference.conversationID && $0.conversation.wingID == parent.reference.wingID }) else { return .unknown }
        return status(for: root)
    }
    public var inspectionOnly: Bool { profile?.mode == .localPreview }
    public var canContinue: Bool {
        !inspectionOnly && connected && !busy && pending == nil && pendingContinuation == nil &&
        tasks.contains { $0.conversation.conversationID == selected?.conversationID && $0.conversation.wingID == selected?.wingID && $0.conversation.isRoot } &&
        continuationAvailability?.covers(execution) == true &&
        continuationObservedAt.map { Date().timeIntervalSince($0) >= 0 && Date().timeIntervalSince($0) < 60 } == true
    }
    public var inputReady: Bool { !inspectionOnly && !busy && pending == nil && pendingContinuation == nil && (canContinue || transcript.canSend(connected: connected)) }
    public var items: [TranscriptItem] { transcript.events.flatMap(TranscriptItem.items) }
    public var title: String { tasks.first(where: { $0.conversation.conversationID == selected?.conversationID && $0.conversation.wingID == selected?.wingID })?.conversation.displayTitle ?? "Conversation" }
    public var parentTitle: String { tasks.first(where: { $0.conversation.conversationID == parent?.reference.conversationID && $0.conversation.wingID == parent?.reference.wingID })?.conversation.displayTitle ?? parent?.title ?? "Your parent" }
    public var relatedTasks: [ConversationTask] {
        guard let parent else { return [] }
        return tasks.filter { $0.conversation.wingID == parent.reference.wingID && $0.conversation.rootConversationID == parent.reference.conversationID &&
            !$0.conversation.isRoot && $0.conversation.conversationID != selected?.conversationID }.sorted {
                let rank: (ObservedStatus) -> Int = { state in state == .needsInput ? 0 : [.failed, .unavailable].contains(state) ? 1 : state == .working ? 2 : 3 }
                let a = rank(status(for: $0)), b = rank(status(for: $1))
                return a == b ? $0.conversation.conversationID < $1.conversation.conversationID : a < b
            }
    }
    public var attention: HumanAttention? {
        guard currentStatus == .needsInput, let execution, let lifecycle = transcript.lifecycle else { return nil }
        return HumanAttention(execution: execution, lifecycle: lifecycle)
    }

    // Stop is enabled only when this connection's latest read advertised exact
    // whole-execution Stop for the live bound execution. Dropping this
    // connection sends nothing: work keeps running on the wing.
    public var canStop: Bool {
        guard !inspectionOnly, connected, !busy, stopFlight == nil, let execution, execution.providerSessionID != nil,
              stopCapability?.covers(execution) == true,
              let lifecycle = transcript.lifecycle, lifecycle.processAlive, lifecycle.providerSessionID == execution.providerSessionID,
              [.working, .starting, .needsInput].contains(currentStatus) else { return false }
        // Only explicit no-action allows a new intent; a confirmed native exit of
        // this execution leaves nothing to stop, even before the next read.
        return pendingStop.map { $0.execution != execution || $0.progress == .notSent || $0.progress == .notRunning } ?? true
    }
    // Explicit retry of the SAME stop. It is a conversation_stop replay, not a
    // read: if the first attempt never arrived, this one is what stops the task.
    public var canRetryStop: Bool {
        guard !inspectionOnly, connected, !busy, stopFlight == nil, let execution, let pendingStop, pendingStop.execution == execution, !pendingStop.progress.isFinal else { return false }
        return stopCapability?.covers(execution) == true
    }
    public var canDismissStop: Bool { stopFlight == nil && pendingStop != nil }
    public var stopSending: Bool { stopFlight != nil }
    public var stopActionTitle: String { pendingStop.map { !$0.progress.isFinal } == true ? "Retry stop" : "Stop" }
    public static let stopUnavailableHint = "Stopping from this phone isn't available yet. Leaving this screen or disconnecting doesn't stop the task."
    public static let stopConfirmation = "This ends the whole task on your computer, including any work in progress, not just the current step. It can't be undone from this phone."
    public static let stopRetryHint = "Sends the same stop request again. If the first one never arrived, this stops the whole task now; if it did, your computer won't stop it twice."
    public static let stopDismissHint = "Removes this stop status from this phone only. It doesn't cancel a stop your computer may already have received."
    public var stopNotice: String? {
        guard selected != nil else { return nil }
        if let pendingStop, pendingStop.execution == execution {
            var notice = Self.notice(for: pendingStop.progress, sending: stopSending)
            if !pendingStop.progress.isFinal, transcript.lifecycle?.processAlive == false {
                notice += " Your computer now reports the task has ended, but this phone can't confirm the stop caused it."
            }
            if !connected { notice += " You're offline, so this can't be checked right now." }
            return notice
        }
        switch currentStatus {
        case .working, .starting, .needsInput:
            if canStop { return "Stop ends this whole task on your computer, not just the current step. Leaving this screen or disconnecting doesn't stop it." }
            return "Stopping from this phone isn't available yet. This task keeps running on your computer, even if you leave this screen or disconnect. To stop it, use Wingthing on that computer."
        case .offline:
            return "Disconnecting doesn't stop a task that's running on your computer. Stopping from this phone isn't available yet."
        default:
            return nil
        }
    }

    private static func notice(for progress: StopProgress, sending: Bool) -> String {
        switch progress {
        case .unconfirmed where sending: "Sending your stop request…"
        case .unconfirmed: "Your stop request may not have reached your computer. Retry stop sends the same stop request again: if the first never arrived, that retry ends the whole task; it can't stop it twice."
        case .requested: "Your computer accepted the stop request. The task hasn't been confirmed as ended yet. Retry stop resends the same request to get its latest result; it won't stop the task twice."
        case .terminationObserved: "Your computer reports this task's process has ended. It may also have ended for another reason."
        case .notSent: "The stop request wasn't carried out, so nothing was stopped."
        case .notRunning: "The task wasn't running, so nothing was stopped."
        }
    }

    // Saves one immutable intent before any request. A failed save sends nothing.
    public func requestStop(expectedExecution: ExecutionReference? = nil) async {
        guard canStop, let client, let store, let execution, let lifecycle = transcript.lifecycle,
              expectedExecution == nil || expectedExecution == execution else { return }
        let flight = UUID(); stopFlight = flight; let requested = generation; error = nil
        let intent: PendingStop
        do {
            intent = try PendingStop(execution: execution, expectedStateCursor: lifecycle.headCursor ?? lifecycle.cursor)
            try await store.saveStop(intent)
        } catch {
            endStopFlight(flight)
            if requested == generation { self.error = error.localizedDescription }
            return
        }
        guard requested == generation, self.execution == execution else {
            try? await store.forgetStop(intent.id) // Superseded before dispatch: never sent.
            endStopFlight(flight); return
        }
        pendingStop = intent
        await dispatchStop(intent, client: client, store: store, flight: flight, requested: requested, fresh: true)
    }

    // Only on an explicit tap; reconnect, polling and relaunch never call this.
    public func retryStop() async {
        guard canRetryStop, let client, let store, let pendingStop else { return }
        let flight = UUID(); stopFlight = flight; error = nil
        await dispatchStop(pendingStop, client: client, store: store, flight: flight, requested: generation, fresh: false)
    }

    public func dismissStop() async {
        guard canDismissStop, let store, let pendingStop else { return }
        do { try await store.forgetStop(pendingStop.id); if self.pendingStop?.id == pendingStop.id { self.pendingStop = nil } }
        catch { self.error = error.localizedDescription }
    }

    // A delayed reply is saved for its own execution but shown only if that
    // execution is still selected on the same connection generation.
    private func dispatchStop(_ stop: PendingStop, client: HomeClient, store: LocalConversationStore, flight: UUID, requested: UUID, fresh: Bool) async {
        let updated: PendingStop
        do { updated = try await client.submitStop(stop) } catch {
            // Refused before any request. A fresh intent was therefore never sent.
            if fresh {
                try? await store.forgetStop(stop.id)
                if pendingStop?.id == stop.id { pendingStop = nil }
            }
            endStopFlight(flight)
            if requested == generation { self.error = error.localizedDescription }
            return
        }
        var saveError: (any Error)?
        do { try await store.saveStop(updated) } catch { saveError = error }
        endStopFlight(flight)
        guard requested == generation, execution == updated.execution else { return }
        pendingStop = updated
        if let saveError { self.error = "The stop status couldn't be saved on this phone. \(saveError.localizedDescription)" }
    }

    private func endStopFlight(_ flight: UUID) { if stopFlight == flight { stopFlight = nil } }

    // Explicit reconnect. Reads only; a pending input is never resent here.
    public func refresh() async {
        guard let client, let profile, !busy else { return }
        busy = true; error = nil; phase = .connecting; let requested = generation
        stopCapability = nil; continuationAvailability = nil; continuationObservedAt = nil; readEpoch = UUID() // Reconnect requires a fresh advertisement.
        creationOptions = nil
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
    public func open(_ reference: ConversationReference, expectedExecution: ExecutionReference? = nil) async {
        guard let profile, let store else { return }
        rememberDraft()
        guard phase == .online, let client else { await openSaved(reference, from: store); return }
        if selected != reference { pendingContinuation = nil; continuationNotice = nil }
        generation = UUID(); let requested = generation
        busy = true; error = nil; stopCapability = nil; continuationAvailability = nil; continuationObservedAt = nil; readEpoch = UUID(); let epoch = readEpoch
        do {
            try reference.validate(profile)
            let view = try await client.conversation(reference)
            guard requested == generation else { return }
            guard let task = view.tasks.first(where: { $0.conversation.conversationID == reference.conversationID && $0.conversation.wingID == reference.wingID }) else { throw ClientError.staleReference }
            if let expectedExecution {
                guard expectedExecution.conversation == reference, task.conversation.sessionID == expectedExecution.sessionID,
                      expectedExecution.providerSessionID == nil || task.lifecycle?.providerSessionID == nil || task.lifecycle?.providerSessionID == expectedExecution.providerSessionID else { throw ClientError.staleReference }
            }
            // An unknown provider binding in the tree is the same execution, not a
            // new one: keep its draft, transcript cursor, and known binding.
            let observed = task.lifecycle?.providerSessionID ?? expectedExecution?.providerSessionID
            let continuing = execution.map { $0.conversation == reference && $0.sessionID == task.conversation.sessionID && (observed == nil || $0.providerSessionID == nil || $0.providerSessionID == observed) } ?? false
            let target = ExecutionReference(conversation: reference, sessionID: task.conversation.sessionID, providerSessionID: observed ?? (continuing ? execution?.providerSessionID : nil))
            if !continuing { transcript = TranscriptState(); presentDraft(""); pendingStop = nil }
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
            let observation = try await client.observe(target, after: transcript.cursor)
            guard requested == generation else { return }
            let lifecycle = observation.lifecycle
            let bound = ExecutionReference(conversation: reference, sessionID: target.sessionID, providerSessionID: lifecycle.providerSessionID)
            execution = bound
            try transcript.apply(lifecycle, target: bound); phase = .online
            stopCapability = epoch == readEpoch && observation.stopCapability?.covers(bound) == true ? observation.stopCapability : nil
            continuationAvailability = epoch == readEpoch ? observation.continuation : nil
            continuationObservedAt = continuationAvailability == nil ? nil : Date()
            pendingContinuation = try await store.continuation(for: reference, unfinishedOnly: true)
            pending = await store.pending(for: bound)
            if pending?.delivery == .nativeReceiptObserved || pending?.delivery == .definitelyNotSent { pending = nil }
            if pending != nil { unsentDrafts.removeValue(forKey: bound) }
            if !continuing {
                let savedDraft = try localViews?.draft(for: bound)
                presentDraft(pending?.input ?? unsentDrafts[bound] ?? savedDraft ?? "")
            }
            pendingStop = await store.pendingStop(for: bound)
            try await store.cache(CachedConversation(reference: reference, tasks: view.tasks, transcript: transcript))
        } catch {
            guard requested == generation else { return }
            stopCapability = nil
            continuationAvailability = nil; continuationObservedAt = nil
            transcript.markUnavailable(); parentTranscript.markUnavailable(); phase = classify(error); self.error = error.localizedDescription
            if selected != reference, let cached = try? await store.cached(reference) {
                tasks = cached.tasks; transcript = cached.transcript; selected = reference; execution = nil; pending = nil; pendingStop = nil
                pendingContinuation = try? await store.continuation(for: reference, unfinishedOnly: true); continuationNotice = nil
                presentDraft("")
            }
        }
        if requested == generation { busy = false }
    }

    public func pollTranscript() async {
        guard connected, !busy, let client, let execution else { return }
        let requested = generation, epoch = readEpoch
        do {
            let observation = try await client.observe(execution, after: transcript.cursor)
            guard requested == generation, self.execution == execution else { return }
            let view = observation.lifecycle
            try transcript.apply(view, target: execution)
            // Pin the first observed provider session, as open() does, so input
            // saved from here stays bound to it.
            var current = execution
            if execution.providerSessionID == nil, let bound = view.providerSessionID, !bound.isEmpty {
                current = ExecutionReference(conversation: execution.conversation, sessionID: execution.sessionID, providerSessionID: bound)
                self.execution = current
            }
            // Each read re-validates Stop support; an earlier read is never enough,
            // and a read overtaken by reconnect never republishes it.
            if epoch == readEpoch { stopCapability = observation.stopCapability?.covers(current) == true ? observation.stopCapability : nil }
            if epoch == readEpoch {
                continuationAvailability = observation.continuation
                continuationObservedAt = continuationAvailability == nil ? nil : Date()
            }
            if current != execution, let store {
                let saved = await store.pendingStop(for: current)
                guard requested == generation, self.execution == current else { return }
                pendingStop = saved
            }
            if parent?.reference == execution.conversation { parentTranscript = transcript }
            if let store, let selected { try await store.cache(CachedConversation(reference: selected, tasks: tasks, transcript: transcript)) }
        } catch {
            guard requested == generation else { return }
            stopCapability = nil
            continuationAvailability = nil; continuationObservedAt = nil
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

    // Refresh linked-task evidence without reopening or retargeting the
    // selected execution, its draft, receipt, or Stop advertisement.
    public func pollTaskTree(now: Date = Date()) async {
        #if DEBUG
        if let fixtureWire { fixtureDiagnostics = await fixtureWire.diagnostics() }
        #endif
        guard connected, !busy, let client, let parent,
              treeObservedAt.map({ now.timeIntervalSince($0) >= 5 }) ?? true else { return }
        let requested = generation, reference = parent.reference
        do {
            let view = try await client.conversation(reference)
            guard requested == generation, self.parent?.reference == reference else { return }
            tasks = view.tasks; treeObservedAt = Date()
        } catch {
            guard requested == generation else { return }
            treeObservedAt = nil
            self.error = "Linked tasks couldn't be refreshed. \(error.localizedDescription)"
        }
    }

    public func sendOrCheck() async {
        if pendingContinuation != nil || canContinue { await sendOrCheckContinuation(); return }
        guard !inspectionOnly, connected, !busy, let client, let store, let execution else { return }
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
                try localViews?.saveDraft("", for: execution)
                unsentDrafts.removeValue(forKey: execution)
            }
            input.markUnconfirmed(); try await store.savePending(input)
            guard requested == generation else { return }
            pending = input
            let result = try await client.submit(input)
            try await store.savePending(result)
            guard requested == generation else { return }
            if result.delivery == .nativeReceiptObserved { pending = nil; presentDraft(""); unsentDrafts.removeValue(forKey: execution) }
            else if result.delivery == .definitelyNotSent { pending = nil; draft = result.input; error = result.notice }
            else { pending = result }
        } catch {
            guard requested == generation else { return }
            self.error = error.localizedDescription
        }
        if requested == generation { busy = false }
    }

    private func sendOrCheckContinuation() async {
        guard !inspectionOnly, connected, !busy, let client, let store, let execution, let selected else { return }
        let requested = generation, first = pendingContinuation == nil
        guard !first || canContinue else { return }
        busy = true; error = nil
        var updated: PendingContinuation?
        do {
            var intent: PendingContinuation
            if let pendingContinuation {
                guard pendingContinuation.source.conversation == selected else { throw ClientError.staleReference }
                intent = pendingContinuation
            } else {
                intent = try PendingContinuation(source: execution, input: draft.trimmingCharacters(in: .whitespacesAndNewlines))
                try await store.saveContinuation(intent) // Saving must succeed before a dispatch.
                try localViews?.saveDraft("", for: execution)
                unsentDrafts.removeValue(forKey: execution)
            }
            guard requested == generation else { return }
            intent.markUnconfirmed(); try await store.saveContinuation(intent)
            guard requested == generation else { return }
            pendingContinuation = intent
            do { intent = try await client.submitContinuation(intent, firstAttempt: first) }
            catch {
                intent.markUnconfirmed(error.localizedDescription)
                if requested == generation { phase = classify(error) }
            }
            try await store.saveContinuation(intent)
            updated = intent
            guard requested == generation else { return }
            pendingContinuation = intent.progress.final ? nil : intent
            continuationAvailability = nil; continuationObservedAt = nil
            if intent.progress == .started {
                continuationNotice = "Follow-up started. The earlier execution stays archived."
                unsentDrafts.removeValue(forKey: intent.source)
                if self.execution == intent.source { presentDraft("") }
            } else if intent.progress == .failed {
                continuationNotice = "Follow-up did not start. Your message is saved."
                try localViews?.saveDraft(intent.input, for: intent.source)
                if self.execution == intent.source { draft = intent.input }
                else { unsentDrafts[intent.source] = intent.input }
            } else { continuationNotice = "Follow-up unconfirmed. Check reuses this saved message." }
        } catch {
            if requested == generation { self.error = error.localizedDescription }
        }
        guard requested == generation else { return }
        busy = false
        if let updated, updated.progress == .started, let target = updated.targetSessionID {
            let next = ExecutionReference(conversation: updated.source.conversation, sessionID: target, providerSessionID: updated.source.providerSessionID)
            if self.execution?.sessionID != target { await open(next.conversation, expectedExecution: next) }
            else { await pollTranscript() }
        } else if updated?.progress == .failed { await pollTranscript() }
        #if DEBUG
        if let fixtureWire { fixtureDiagnostics = await fixtureWire.diagnostics() }
        #endif
    }

    // Shows the saved copy without any network or provider action. A check in
    // flight owns the connection state, so it is not superseded from here.
    private func openSaved(_ reference: ConversationReference, from store: LocalConversationStore) async {
        guard phase != .connecting else { return }
        generation = UUID(); let requested = generation
        let cached = try? await store.cached(reference)
        guard requested == generation, let cached else { return }
        tasks = cached.tasks; transcript = cached.transcript; selected = reference; execution = nil; pending = nil
        presentDraft("") // No exact live binding is available for this saved selection.
        stopCapability = nil; pendingStop = nil
        continuationAvailability = nil; continuationObservedAt = nil; continuationNotice = nil
        pendingContinuation = try? await store.continuation(for: reference, unfinishedOnly: true)
    }

    private func presentDraft(_ text: String) {
        presentingDraft = true; draft = text; presentingDraft = false
    }
    private func saveEditedDraft() {
        guard let execution, let localViews, pending == nil, pendingContinuation?.source != execution else { return }
        do { try localViews.saveDraft(draft, for: execution); localSaveError = nil }
        catch { localSaveError = "Your draft couldn't be saved on this phone. Keep this screen open." }
    }
    public var readingPosition: ConversationReadingPosition? {
        guard let execution else { return nil }
        return readingPositions[execution] ?? (try? localViews?.reading(for: execution))
    }
    public func rememberReadingPosition(_ position: ConversationReadingPosition, for expected: ExecutionReference, persist: Bool) {
        guard execution == expected else { return }
        readingPositions[expected] = position
        if persist { persistReadingPosition() }
    }
    public func persistReadingPosition() {
        guard let execution, let position = readingPositions[execution], let localViews else { return }
        do { try localViews.saveReading(position, for: execution) }
        catch { localSaveError = "Your reading position couldn't be saved on this phone." }
    }

    private func rememberDraft() {
        persistReadingPosition()
        guard let execution else { return }
        if pending != nil || pendingContinuation?.source == execution || draft.isEmpty { unsentDrafts.removeValue(forKey: execution) }
        else { unsentDrafts[execution] = draft }
    }

    // Supersedes every in-flight result and forgets the previous home. A failed
    // replacement therefore can never leave the earlier client usable.
    private func beginReplacement() -> (UUID, HomeClient?) {
        persistReadingPosition()
        generation = UUID()
        let released = client
        client = nil; store = nil; localViews = nil; readingPositions.removeAll(); localSaveError = nil; profile = nil; parent = nil; roots = []; tasks = []; selected = nil; execution = nil
        transcript = TranscriptState(); parentTranscript = TranscriptState(); pending = nil; draft = ""; treeObservedAt = nil
        unsentDrafts.removeAll()
        stopCapability = nil; pendingStop = nil; pendingContinuation = nil; continuationNotice = nil; continuationAvailability = nil; continuationObservedAt = nil; stopFlight = nil; readEpoch = UUID()
        error = nil; busy = false; phase = .notConfigured
        creationOptions = nil; pendingLaunch = nil; creationNotice = nil
        fixtureDiagnostics = nil
        #if DEBUG
        fixtureWire = nil
        #endif
        return (generation, released)
    }

    private func install(_ home: HomeProfile, bearer: String, cacheFile: URL, wire: any HomeWire, requested: UUID) async throws -> Bool {
        let connection: HomeClient
        #if DEBUG
        if home.mode == .localPreview { connection = try HomeClient(localPreview: home, suppliedCredential: bearer, wire: wire) }
        else { connection = try HomeClient(profile: home, existingBearer: bearer, wire: wire) }
        #else
        connection = try HomeClient(profile: home, existingBearer: bearer, wire: wire)
        #endif
        let local = try LocalConversationStore(profile: home, file: cacheFile)
        let views = try LocalConversationViewStore(profile: home, file: cacheFile.appendingPathExtension("view-state"))
        let restored = await local.parent()
        let launch = try await local.unfinishedLaunch()
        let cached: CachedConversation?
        if let restored { cached = try await local.cached(restored.reference) } else { cached = nil }
        guard requested == generation else { return false }
        profile = home; store = local; localViews = views; client = connection
        parent = restored
        pendingLaunch = launch
        if let restored, let cached {
            tasks = cached.tasks; transcript = cached.transcript; selected = restored.reference
        }
        return true
    }

    #if DEBUG
    // Explicit simulator inspection of an existing, pinned, same-Mac preview.
    // Absent launch metadata leaves the ordinary empty app untouched. This
    // route accepts no credential and enables no Send or Stop operation.
    public func inspectLocalPreview(environment: [String: String], wire: any HomeWire = URLSessionHomeWire()) async {
        guard environment["WT_IOS_PREVIEW_ENABLED"] != nil else { return }
        let (requested, released) = beginReplacement()
        phase = .connecting; busy = true
        await released?.disconnect()
        let home: HomeProfile
        let root: String
        do {
            let fields = ["ENABLED", "ORIGIN", "USER", "WING", "KEY", "ROOT", "SELECTED"].map { "WT_IOS_PREVIEW_" + $0 }
            guard environment[fields[0]] == "1", environment.keys.filter({ $0.hasPrefix("WT_IOS_PREVIEW_") }).allSatisfy(fields.contains),
                  fields[1...5].allSatisfy({ environment[$0]?.isEmpty == false }),
                  let origin = URL(string: environment[fields[1]]!) else { throw ClientError.invalidConfiguration("Preview inspection requires an explicit origin, account, wing, key and parent.") }
            root = environment[fields[5]]!
            let identity = Self.stableID(["wingthing.ios-preview.v1", origin.absoluteString, environment[fields[2]]!, environment[fields[3]]!, environment[fields[4]]!])
            home = try HomeProfile.localPreview(id: identity, origin: origin, expectedUserID: environment[fields[2]]!, homeWingID: environment[fields[3]]!, homeWingPublicKey: environment[fields[4]]!)
            guard !root.isEmpty, root.utf8.count <= 256, root.rangeOfCharacter(from: .controlCharacters) == nil else { throw ClientError.invalidConfiguration("Choose an exact preview parent.") }
            guard try await install(home, bearer: "", cacheFile: cacheFile(for: home), wire: wire, requested: requested) else { return }
        } catch {
            guard requested == generation else { return }
            phase = classify(error); self.error = error.localizedDescription; busy = false
            return
        }
        busy = false
        await refresh()
        // refresh may reopen the saved parent and advance its read generation.
        // A replacement or a different user selection still fences this route.
        guard connected, profile?.id == home.id,
              requested == generation || selected == parent?.reference else { return }
        let reference = ConversationReference(profileID: home.id, userID: home.expectedUserID, wingID: home.homeWingID, conversationID: root)
        await open(reference)
        if let selected = environment["WT_IOS_PREVIEW_SELECTED"], selected != root {
            guard self.selected == reference, connected, profile?.id == home.id,
                  let child = tasks.first(where: { $0.conversation.conversationID == selected }), let childReference = self.reference(for: child.conversation) else { return }
            await open(childReference)
        }
    }

    // Only the simulator's explicit fixture launch invokes this. The synthetic
    // wire refuses real origins; no bearer is saved to disk or Keychain.
    public func openInteractiveFixture(scenario: String, persistenceID: String? = nil) async {
        guard ["lost-reply", "failed", "normal", "design", "reading", "fresh-home", "empty-home", "creation-lost-reply"].contains(scenario) else { return }
        let wire = ConversationTransportFixture(scenario: scenario == "fresh-home" ? "normal" : scenario)
        do {
            let home = try await wire.profile()
            let file: URL
            if let persistenceID {
                guard let id = UUID(uuidString: persistenceID) else { throw ClientError.invalidConfiguration("Fixture persistence requires an exact test UUID.") }
                let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("native-test-fixtures")
                file = directory.appendingPathComponent("\(id.uuidString).json")
            } else { file = FileManager.default.temporaryDirectory.appendingPathComponent("native-fixture-\(UUID()).json") }
            try await configure(profile: home, existingBearer: wire.bearer, cacheFile: file, wire: wire)
            fixtureWire = wire; fixtureDiagnostics = await wire.diagnostics()
            await refresh()
            if !["fresh-home", "empty-home", "creation-lost-reply"].contains(scenario) {
                await open(ConversationReference(profileID: home.id, userID: home.expectedUserID, wingID: home.homeWingID, conversationID: wire.rootID))
            }
        } catch { self.error = error.localizedDescription }
    }
    #endif

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
