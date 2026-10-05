import SwiftUI
import WingthingCore
#if os(iOS)
import UIKit
#elseif os(macOS)
import AppKit
#endif

private struct ConversationPalette {
    let background, foreground, secondary, surface, border, userBubble, accentText, accentFill, onAccent: Color
    init(_ scheme: ColorScheme) {
        let dark = scheme == .dark
        func tone(_ light: UInt32, _ night: UInt32) -> Color {
            let n = dark ? night : light
            return Color(.sRGB, red: Double((n >> 16) & 255) / 255, green: Double((n >> 8) & 255) / 255, blue: Double(n & 255) / 255, opacity: 1)
        }
        background = tone(0xFFFFFF, 0x131516); foreground = tone(0x202224, 0xF1F3F3)
        secondary = tone(0x6B7074, 0xA2A8AC); surface = tone(0xF3F4F4, 0x202426)
        border = tone(0xE7E9EA, 0x353B3E); userBubble = tone(0xEDF1F2, 0x253138)
        accentText = tone(0x00779F, 0x68D0F2); accentFill = tone(0x08B7ED, 0x39C3EF)
        onAccent = tone(0x043345, 0x102D37)
    }
}

public struct WingthingRootView: View {
    @ObservedObject private var model: WingthingModel
    @Environment(\.colorScheme) private var scheme
    @State private var showingHome = false
    @State private var showingNew = false
    @State private var homeAfterNew = false
    @State private var conversationStates: [ConversationReference: ConversationViewState] = [:]
    public init(model: WingthingModel) { self.model = model }
    private var palette: ConversationPalette { .init(scheme) }
    public var body: some View {
        NavigationStack {
            Group {
                if let selected = model.selected {
                    ConversationScreen(model: model, navigation: Binding(
                        get: { conversationStates[selected] ?? ConversationViewState() },
                        set: { conversationStates[selected] = $0 }
                    )).id(selected)
                } else if model.profile != nil {
                    VStack(spacing: 0) {
                        HStack {
                            Text("Conversations").font(.headline).fixedSize(horizontal: false, vertical: true)
                            Spacer()
                            Button { showingNew = true } label: {
                                Image(systemName: "square.and.pencil").font(.system(size: 22)).frame(width: 44, height: 44)
                            }.accessibilityLabel("New conversation").accessibilityIdentifier("new-conversation")
                            Button { showingHome = true } label: {
                                Image(systemName: "slider.horizontal.3").font(.system(size: 22)).frame(width: 44, height: 44)
                            }.accessibilityLabel("Home connection and settings")
                        }.padding(.horizontal, 12).frame(minHeight: 56)
                        TaskInventoryView(model: model, onConnect: { showingHome = true }, onOpen: {})
                            .accessibilityIdentifier("configured-conversation-list")
                    }.foregroundStyle(palette.foreground).background(palette.background.ignoresSafeArea())
                } else {
                    VStack(spacing: 20) {
                        Spacer()
                        Circle().stroke(palette.accentFill, lineWidth: 7).frame(width: 52, height: 52).accessibilityHidden(true)
                        Text("Wingthing").font(.largeTitle.bold())
                        Text("Your conversations, on your computer.").font(.body).multilineTextAlignment(.center)
                        Button("Connect a home") { showingHome = true }.font(.headline).padding(16)
                            .foregroundStyle(palette.onAccent).frame(maxWidth: .infinity)
                            .background(palette.accentFill, in: RoundedRectangle(cornerRadius: 16))
                        Spacer()
                    }.padding(24).foregroundStyle(palette.foreground)
                        .background(palette.background.ignoresSafeArea())
                }
            }
            .hideConversationNavigationBar()
        }.tint(palette.accentText)
            .sheet(isPresented: $showingHome) { NavigationStack { HomeConnectionView(model: model) } }
            .sheet(isPresented: $showingNew, onDismiss: {
                if homeAfterNew { homeAfterNew = false; showingHome = true }
            }) { NavigationStack { NewConversationView(model: model) } }
            .onOpenURL { url in
                model.receiveHomeSetupLink(url.absoluteString)
                if showingNew { homeAfterNew = true; showingNew = false }
                else { showingHome = true }
            }
    }
}

private func phaseColor(_ phase: HomeConnectionPhase) -> Color {
    switch phase {
    case .online: .green
    case .connecting: .blue
    case .offline: .orange
    case .failed: .red
    default: .secondary
    }
}

private struct ConnectionStatusRow: View {
    let phase: HomeConnectionPhase
    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Circle().fill(phaseColor(phase)).frame(width: 10, height: 10)
            VStack(alignment: .leading, spacing: 3) {
                Text(phase.title).font(.headline)
                if let detail = phase.detail { Text(detail).font(.footnote).foregroundStyle(.secondary) }
            }
        }.accessibilityElement(children: .combine)
    }
}

private func statusColor(_ state: ObservedStatus) -> Color {
    switch state {
    case .working: .blue
    case .needsInput: .orange
    case .ready, .turnCompleted: .green
    case .failed: .red
    default: .secondary
    }
}

private struct TaskInventoryView: View {
    @ObservedObject var model: WingthingModel
    let onConnect: () -> Void
    let onOpen: () -> Void
    var body: some View {
        List {
            if let error = model.error { Section { Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.orange) } }
            if model.profile == nil {
                Section {
                    Label("Start with your own home", systemImage: "house.circle").font(.title3)
                    Button("Connect a home", action: onConnect).frame(minHeight: 44)
                    Text("Connect to the computer where your conversations run.").foregroundStyle(.secondary)
                    Text("Use your existing home address and access. Pairing and sign-in aren't available yet.").font(.footnote).foregroundStyle(.secondary)
                }
            } else {
                if !model.connected { Section { ConnectionStatusRow(phase: model.phase) } }
                if model.roots.isEmpty && model.tasks.isEmpty {
                    Section { Text(model.connected ? "This home has no parent conversations yet." : "No saved tasks for this home yet.").foregroundStyle(.secondary) }
                }
            }
            if !model.connected && !model.tasks.isEmpty { Section { Label("Saved tasks · not live", systemImage: "wifi.slash").foregroundStyle(.secondary) } }
            if !model.roots.isEmpty {
                Section("Parents") {
                    ForEach(model.roots) { root in
                        Button {
                            if let reference = model.reference(for: root) { Task { await model.open(reference); onOpen() } }
                        } label: {
                            HStack(alignment: .top, spacing: 12) {
                                Image(systemName: "circle.fill").accessibilityHidden(true)
                                Text(root.displayTitle).fixedSize(horizontal: false, vertical: true)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }.foregroundStyle(.primary)
                        }
                        .disabled(model.busy)
                        .accessibilityIdentifier("conversation-root-" + root.conversationID)
                    }
                }
            }
            if !model.tasks.isEmpty {
                Section("Linked tasks") {
                    ForEach(orderedTaskTree(model.tasks)) { row in
                        VStack(alignment: .leading, spacing: 7) {
                            Button {
                                if let reference = model.reference(for: row.task.conversation) { Task { await model.open(reference); onOpen() } }
                            } label: {
                                HStack {
                                    Image(systemName: row.depth == 0 ? "circle.fill" : "arrow.turn.down.right").foregroundStyle(statusColor(model.status(for: row.task)))
                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(row.task.conversation.displayTitle).font(.headline).foregroundStyle(.primary)
                                        Text("\(row.task.conversation.agent) · \(model.status(for: row.task).label)").font(.caption).foregroundStyle(.secondary)
                                        Text(row.task.conversation.cwd).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                    }
                                    Spacer(); Image(systemName: "chevron.right").foregroundStyle(.secondary)
                                }.padding(.leading, CGFloat(row.depth) * 10)
                            }.disabled(model.busy).accessibilityIdentifier("task-link-" + row.task.conversation.conversationID)
                            if let issue = row.task.lifecycleError ?? row.task.conversation.launchError {
                                DisclosureGroup("Inspect state issue") { Text(issue).font(.footnote).textSelection(.enabled) }
                            }
                            if row.task.historyUnavailable == true { Text("Some earlier execution history is unavailable.").font(.footnote).foregroundStyle(.secondary) }
                        }.padding(.vertical, 5)
                    }
                }
            }
        }.refreshable { await model.refresh() }
    }
}

private struct ConversationViewState {
    var expandedChildren: Set<String> = []
    var following = true
}

private struct ConversationScreen: View {
    @ObservedObject var model: WingthingModel
    @Binding var navigation: ConversationViewState
    @Environment(\.scenePhase) private var phase
    @Environment(\.dynamicTypeSize) private var textSize
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @ScaledMetric(relativeTo: .headline) private var toolbarSize: CGFloat = 18
    #if os(iOS)
    @Environment(\.verticalSizeClass) private var verticalSize
    private var compactHeight: Bool { verticalSize == .compact }
    #else
    private var compactHeight: Bool { false }
    #endif
    @State private var showingDetails = false
    @State private var showingTasks = false
    @State private var showingHome = false
    @State private var confirmingStop = false
    @State private var detailsStopTarget: ExecutionReference?
    @State private var confirmingComposerStop = false
    @State private var composerStopTarget: ExecutionReference?
    @State private var showingNew = false
    @FocusState private var composerFocused: Bool
    @State private var readingFrames: [MessageReadingFrame] = []
    @State private var latestRequest = 0
    private var palette: ConversationPalette { .init(scheme) }
    private var isChild: Bool { model.selected != model.parent?.reference }
    private var presented: NativeTranscriptPresentation { NativeTranscriptPresentation(model.items) }
    private var sendLabel: String { model.pendingContinuation != nil ? "Retry follow-up" : model.pending == nil ? "Send" : "Check receipt" }
    private var composerUsesStop: Bool { model.pendingContinuation == nil && model.pending == nil && (model.canStop || model.canRetryStop) }
    private var sendDisabled: Bool {
        model.busy || !model.connected || (model.pending == nil && model.pendingContinuation == nil &&
            (!model.inputReady || model.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .center, spacing: 8) {
                if isChild {
                    Button { composerFocused = false; Task { await model.openParent() } } label: { toolbarIcon("chevron.left") }
                        .accessibilityLabel("Back to \(model.parentTitle)").accessibilityIdentifier("parent-dot")
                        .accessibilityValue(model.fixtureDiagnostics ?? model.phase.title)
                } else {
                    Menu {
                        Button("Conversations") { composerFocused = false; showingTasks = true }
                        Button("Settings") { composerFocused = false; showingHome = true }
                        Button("Conversation details") { composerFocused = false; showingDetails = true }
                            .accessibilityIdentifier("conversation-details")
                        Button("Reconnect to your home") { Task { await model.refresh() } }.disabled(!model.canReconnect)
                    } label: { toolbarIcon("line.3.horizontal") }
                        .accessibilityLabel("Conversations and settings").accessibilityIdentifier("related-task-menu")
                }
                VStack(alignment: .leading, spacing: 2) {
                    if isChild {
                        Text(model.title).font(.system(size: toolbarSize, weight: .semibold))
                            .lineLimit(textSize.isAccessibilitySize ? 2 : 1)
                        Text("From \(model.parentTitle)").font(.caption).foregroundStyle(palette.secondary).lineLimit(1)
                    } else {
                        Button { Task { await model.openParent() } } label: {
                            Text("Wingthing").font(.system(size: toolbarSize, weight: .semibold)).foregroundStyle(palette.foreground)
                        }.buttonStyle(.plain)
                            .accessibilityLabel("Open parent: \(model.parentTitle), \(model.parentStatus.label)")
                            .accessibilityValue(model.fixtureDiagnostics ?? model.phase.title).accessibilityIdentifier("parent-dot")
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
                if isChild {
                    Button { composerFocused = false; showingDetails = true } label: { toolbarIcon("ellipsis") }
                        .accessibilityLabel("Conversation details").accessibilityIdentifier("conversation-details")
                } else {
                    Button { composerFocused = false; showingNew = true } label: { toolbarIcon("square.and.pencil") }
                        .accessibilityLabel("New conversation").accessibilityIdentifier("new-conversation")
                }
            }.foregroundStyle(palette.foreground).padding(.horizontal, 12).frame(minHeight: 56).padding(.bottom, 10)

            GeometryReader { geometry in
                #if os(iOS)
                let expected = model.execution
                NativeConversationScrollView(content: AnyView(transcriptContent(width: geometry.size.width)),
                    frames: readingFrames, messageIDs: Set(presented.messages.map(\.id)), initialPosition: model.readingPosition, latestRequest: latestRequest) { position, persist in
                        guard let expected else { return }
                        model.rememberReadingPosition(position, for: expected, persist: persist)
                        if navigation.following != position.followingLatest { navigation.following = position.followingLatest }
                    }.id(expected)
                    .overlay(alignment: .bottomTrailing) { latestButton }
                #else
                ScrollViewReader { proxy in
                    ScrollView { transcriptContent(width: geometry.size.width) }
                        .accessibilityIdentifier("conversation-history")
                        .simultaneousGesture(DragGesture().onChanged { _ in navigation.following = false })
                        .onAppear { if navigation.following { proxy.scrollTo("conversation-end", anchor: .bottom) } }
                        .onChange(of: presented.messages.last?.id) { _ in if navigation.following { proxy.scrollTo("conversation-end", anchor: .bottom) } }
                        .onChange(of: model.pendingContinuation?.id) { _ in if navigation.following { proxy.scrollTo("conversation-end", anchor: .bottom) } }
                        .onChange(of: geometry.size) { _ in if navigation.following { proxy.scrollTo("conversation-end", anchor: .bottom) } }
                        .onChange(of: latestRequest) { _ in proxy.scrollTo("conversation-end", anchor: .bottom) }
                        .overlay(alignment: .bottomTrailing) { latestButton }
                }
                #endif
            }

        }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(palette.background.ignoresSafeArea()).foregroundStyle(palette.foreground)
        .safeAreaInset(edge: .bottom, spacing: 0) { composerContent }
        .sheet(isPresented: $showingNew) {
            NavigationStack { NewConversationView(model: model) }
        }
        .sheet(isPresented: $showingTasks) {
            NavigationStack {
                TaskInventoryView(model: model, onConnect: { showingTasks = false; showingHome = true }, onOpen: { showingTasks = false })
                    .navigationTitle("Conversations")
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { showingTasks = false } } }
            }
        }
        .sheet(isPresented: $showingHome) { NavigationStack { HomeConnectionView(model: model) } }
        .sheet(isPresented: $showingDetails) {
            NavigationStack {
                List {
                    Section("Conversation") {
                        Text(model.title)
                        Text(model.canContinue ? "Ready for follow-up" : model.currentStatus.label).font(.footnote)
                        if !model.connected { Text("Saved copy").font(.footnote) }
                        if let error = model.error { Text(error).font(.footnote) }
                        Button("Reconnect to your home") { Task { await model.refresh() } }.disabled(!model.canReconnect)
                    }
                    if model.canStop {
                        Section {
                            Button("Stop task", role: .destructive) { detailsStopTarget = model.execution; confirmingStop = true }
                                .confirmationDialog("Stop this whole task?", isPresented: $confirmingStop, titleVisibility: .visible) {
                                    Button("Stop task", role: .destructive) {
                                        guard let target = detailsStopTarget else { return }
                                        Task { await model.requestStop(expectedExecution: target) }
                                    }
                                    Button("Keep running", role: .cancel) {}
                                } message: { Text(WingthingModel.stopConfirmation) }
                        }
                    }
                    if let notice = model.stopNotice { Section("Task control") { Text(notice).font(.footnote) } }
                    if let execution = model.execution {
                        Section("Execution") {
                            Text("Wing: \(execution.conversation.wingID)\nSession: \(execution.sessionID)\nProvider: \(execution.providerSessionID ?? "unknown")")
                                .font(.caption).textSelection(.enabled)
                        }
                    }
                    if !presented.activity.isEmpty {
                        Section("Activity") {
                            ForEach(presented.activity) { item in
                                DisclosureGroup(item.title) {
                                    Text(item.content).font(.system(.footnote, design: .monospaced)).textSelection(.enabled)
                                    if item.truncated { Text("Provider record truncated").font(.caption) }
                                }
                            }
                        }
                    }
                    if let diagnostics = model.fixtureDiagnostics {
                        Section("Synthetic fixture") {
                            Text("No network or provider calls").font(.footnote)
                            Text(diagnostics).font(.footnote).fixedSize(horizontal: false, vertical: true)
                                .accessibilityIdentifier("fixture-metrics")
                        }
                    }
                }.navigationTitle("Conversation details").inlineConversationTitle()
                    .toolbar { ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { showingDetails = false }.accessibilityIdentifier("close-details")
                    } }
            }
        }
        .onChange(of: phase) { next in if next != .active { model.persistReadingPosition() } }
        .task(id: model.execution) {
            while !Task.isCancelled {
                if phase == .active { await model.pollTranscript(); await model.pollTaskTree() }
                do { try await Task.sleep(for: .seconds(2)) } catch { break }
            }
        }
    }

    private func transcriptContent(width: CGFloat) -> some View {
                        VStack(alignment: .leading, spacing: 24) {
                            ForEach(presented.messages) { item in
                                HStack(alignment: .top, spacing: 0) {
                                    if item.kind == "user" { Spacer(minLength: 0) }
                                    VStack(alignment: .leading, spacing: 0) {
                                        ForEach(NativeMessageBlock.readingBlocks(item.content, maximumCharacters: textSize.isAccessibilitySize ? 32 : 240)) { block in
                                            Text(block.content).font(.body).lineSpacing(6).foregroundStyle(palette.foreground).textSelection(.enabled)
                                                .fixedSize(horizontal: false, vertical: true)
                                                .accessibilityLabel((item.kind == "user" ? "Your message: " : "Assistant message: ") + block.content)
                                                .accessibilityIdentifier("native-message-" + item.id + "-part-" + String(block.id))
                                        }
                                        if item.truncated { Text("This message is truncated.").font(.caption).foregroundStyle(palette.secondary) }
                                    }.padding(.horizontal, item.kind == "user" ? 17 : 0).padding(.vertical, item.kind == "user" ? 13 : 0)
                                        .frame(maxWidth: item.kind == "user" ? (width - 44) * 0.88 : .infinity, alignment: .leading)
                                        .background(item.kind == "user" ? palette.userBubble : .clear, in: RoundedRectangle(cornerRadius: 22))
                                    if item.kind != "user" { Spacer(minLength: 0) }
                                }.background(GeometryReader { frame in
                                    let rect = frame.frame(in: .named("transcript-content"))
                                    Color.clear.preference(key: MessageReadingFramesKey.self, value: [MessageReadingFrame(id: item.id, minY: rect.minY, height: rect.height)])
                                })
                            }
                            if presented.messages.isEmpty { Text("Your conversation will appear here.").foregroundStyle(palette.foreground).font(.body) }
                            if let continuation = model.pendingContinuation {
                                VStack(alignment: .leading, spacing: 8) {
                                    Text("Follow-up unconfirmed").font(.footnote).fixedSize(horizontal: false, vertical: true)
                                        .accessibilityIdentifier("follow-up-pending")
                                    Text(continuation.input).font(.body).fixedSize(horizontal: false, vertical: true)
                                        .accessibilityIdentifier("saved-follow-up")
                                    Text("Retry reuses this saved message. Reconnect only checks your home.")
                                        .font(.caption).fixedSize(horizontal: false, vertical: true)
                                }.foregroundStyle(palette.foreground).padding(16).frame(maxWidth: .infinity, alignment: .leading)
                                    .background(palette.surface, in: RoundedRectangle(cornerRadius: 22))
                            } else if let notice = model.continuationNotice {
                                Text(notice).font(.footnote).foregroundStyle(palette.foreground).fixedSize(horizontal: false, vertical: true)
                                    .accessibilityIdentifier("follow-up-notice")
                            }
                            if let pending = model.pending {
                                Text(pending.delivery.label + " · Check receipt reuses your saved message.")
                                    .font(.footnote).foregroundStyle(palette.foreground).fixedSize(horizontal: false, vertical: true)
                            }
                            if let stop = model.pendingStop {
                                HStack {
                                    Text(stop.progress.label).font(.footnote).foregroundStyle(palette.foreground)
                                    Spacer()
                                    if model.canRetryStop {
                                        Button("Retry stop") { Task { await model.retryStop() } }.accessibilityHint(WingthingModel.stopRetryHint)
                                    }
                                    if model.canDismissStop {
                                        Button("Dismiss") { Task { await model.dismissStop() } }.accessibilityHint(WingthingModel.stopDismissHint)
                                    }
                                }
                            }
                            if let attention = model.attention {
                                VStack(alignment: .leading, spacing: 8) {
                                    Label("Needs your attention", systemImage: "hand.raised")
                                    Text(attention.reason).font(.footnote)
                                    Text("Open Wingthing on your computer to respond.").font(.caption)
                                }.foregroundStyle(palette.foreground).padding(16)
                                    .background(palette.surface, in: RoundedRectangle(cornerRadius: 22))
                            }
                            if !isChild && !model.relatedTasks.isEmpty {
                                ForEach(model.relatedTasks) { task in
                                    let childID = task.conversation.conversationID
                                    VStack(alignment: .leading, spacing: 12) {
                                        Button {
                                            if navigation.expandedChildren.contains(childID) { navigation.expandedChildren.remove(childID) }
                                            else { navigation.expandedChildren.insert(childID) }
                                        } label: {
                                            childHeader(task, expanded: navigation.expandedChildren.contains(childID))
                                                .accessibilityHidden(true) // The named button is the one VoiceOver focus.
                                        }.buttonStyle(.plain).accessibilityIdentifier("child-link-" + childID)
                                            .accessibilityLabel("\(task.conversation.displayTitle), \(model.status(for: task).label)")
                                            .accessibilityValue(navigation.expandedChildren.contains(childID) ? "Expanded" : "Collapsed")
                                        if navigation.expandedChildren.contains(childID) {
                                            Text("Open this task to read its messages.").font(.footnote).foregroundStyle(palette.secondary)
                                            Button("Open task") {
                                                composerFocused = false
                                                if let reference = model.reference(for: task.conversation) { Task { await model.open(reference) } }
                                            }.frame(minHeight: 44).accessibilityIdentifier("open-task-" + childID)
                                        }
                                    }.padding(14).foregroundStyle(palette.foreground)
                                        .overlay(RoundedRectangle(cornerRadius: 16).stroke(palette.border, lineWidth: 1))
                                }
                            }
                            Color.clear.frame(height: 1).id("conversation-end")
                        }.padding(.horizontal, 22).padding(.top, 20).padding(.bottom, 20)
                        .fixedSize(horizontal: false, vertical: true)
                        .coordinateSpace(name: "transcript-content")
                        .onPreferenceChange(MessageReadingFramesKey.self) { readingFrames = $0 }
    }

    @ViewBuilder private var latestButton: some View {
        if !navigation.following {
            Button { navigation.following = true; latestRequest += 1 } label: {
                Image(systemName: "arrow.down").font(.system(size: 18, weight: .semibold)).frame(width: 44, height: 44)
            }.foregroundStyle(palette.foreground).background(palette.surface, in: Circle()).padding()
                .accessibilityLabel("Latest").accessibilityIdentifier("jump-latest")
        }
    }

    private var composerContent: some View {
        Group {
            if model.inspectionOnly {
                if !compactHeight {
                    Text("Read-only preview").font(.footnote).foregroundStyle(palette.foreground)
                        .accessibilityIdentifier("read-only-preview").padding(.vertical, 12)
                }
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    if let issue = model.localSaveError { Text(issue).font(.footnote).foregroundStyle(palette.foreground) }
                    TextField(isChild ? "Message this task" : "Message Wingthing", text: $model.draft,
                              prompt: Text(isChild ? "Message this task" : "Message Wingthing").foregroundColor(palette.secondary), axis: .vertical)
                        .font(.body).foregroundStyle(palette.foreground)
                        .lineLimit(1...6)
                        .textFieldStyle(.plain).disabled(model.execution == nil || model.pending != nil || model.pendingContinuation != nil || model.busy).focused($composerFocused)
                        .accessibilityIdentifier("message-composer")
                    HStack {
                        Spacer()
                        Button {
                            composerFocused = false
                            if composerUsesStop {
                                if model.canRetryStop { Task { await model.retryStop() } }
                                else { composerStopTarget = model.execution; confirmingComposerStop = true }
                            } else { Task { await model.sendOrCheck() } }
                        } label: {
                            Image(systemName: composerUsesStop ? model.canRetryStop ? "arrow.clockwise" : "stop.fill" :
                                model.pendingContinuation != nil || model.pending != nil ? "arrow.clockwise" : "arrow.up")
                                .font(.system(size: 22, weight: .semibold)).frame(width: 44, height: 44)
                        }.buttonStyle(.plain).foregroundStyle(!composerUsesStop && sendDisabled ? palette.secondary : palette.onAccent)
                            .background(!composerUsesStop && sendDisabled ? palette.border : palette.accentFill, in: Circle())
                            .disabled(!composerUsesStop && sendDisabled)
                            .accessibilityLabel(composerUsesStop ? model.canRetryStop ? "Retry stop" : "Stop whole task" : sendLabel)
                            .accessibilityHint(composerUsesStop ? model.canRetryStop ? WingthingModel.stopRetryHint : WingthingModel.stopConfirmation :
                                model.pendingContinuation != nil ?
                                "Retries the same saved request. If it never arrived, this can send it." :
                                model.canContinue ? "Starts a new execution from this saved conversation." : "")
                            .accessibilityIdentifier(composerUsesStop ? "stop-task-composer" : "send-message")
                            .confirmationDialog("Stop this whole task?", isPresented: $confirmingComposerStop, titleVisibility: .visible) {
                                Button("Stop task", role: .destructive) {
                                    guard let target = composerStopTarget else { return }
                                    Task { await model.requestStop(expectedExecution: target) }
                                }
                                Button("Keep running", role: .cancel) {}
                            } message: { Text(WingthingModel.stopConfirmation) }
                    }
                }.padding(.horizontal, 13).padding(.top, 13).padding(.bottom, 8)
                    .background(palette.surface, in: RoundedRectangle(cornerRadius: 27))
                    .overlay(RoundedRectangle(cornerRadius: 27).stroke(palette.border, lineWidth: 1))
                    .padding(.horizontal, 12).padding(.top, 10).padding(.bottom, 8)
            }
        }
    }

    @ViewBuilder private func childHeader(_ task: ConversationTask, expanded: Bool) -> some View {
        let icon = Image(systemName: "arrow.triangle.branch").font(.system(size: 22))
            .frame(width: 32, height: 32).accessibilityHidden(true)
        let disclosure = Image(systemName: expanded ? "chevron.down" : "chevron.right")
            .font(.system(size: 18)).accessibilityHidden(true)
        let title = VStack(alignment: .leading, spacing: 4) {
            Text(task.conversation.displayTitle).font(.subheadline.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
            Text(model.status(for: task).label).font(.caption).foregroundStyle(palette.secondary)
        }.frame(maxWidth: .infinity, alignment: .leading)
        if textSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 8) {
                HStack { icon; Spacer(); disclosure }
                title
            }.frame(minHeight: 44)
        } else {
            HStack(spacing: 12) { icon; title; disclosure }.frame(minHeight: 44)
        }
    }

    private func toolbarIcon(_ name: String) -> some View {
        Image(systemName: name).font(.system(size: 22, weight: .regular)).foregroundStyle(palette.foreground)
            .frame(width: 44, height: 44)
    }
    private func relatedButton(_ task: ConversationTask) -> some View {
        Button {
            if let reference = model.reference(for: task.conversation) { Task { await model.open(reference) } }
        } label: { Text(task.conversation.displayTitle) }
            .disabled(model.busy)
            .accessibilityLabel("Open child: \(task.conversation.displayTitle), \(model.status(for: task).label)")
            .accessibilityIdentifier("child-link-" + task.conversation.conversationID)
    }
}

private struct HomeConnectionView: View {
    @ObservedObject var model: WingthingModel
    @State private var address = ""
    @State private var transport = HomeTransport.localNetwork
    @State private var userID = ""
    @State private var wingID = ""
    @State private var wingKey = ""
    @State private var token = ""
    @State private var setupMode = HomeProfileMode.remote
    @State private var importedSetup = false
    private var formComplete: Bool {
        [address, userID, wingID, wingKey].allSatisfy { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
            && (setupMode == .localPreview || !token.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }
    var body: some View {
        Form {
            Section("Status") {
                ConnectionStatusRow(phase: model.phase)
                if model.canDisconnect {
                    Button("Reconnect") { Task { await model.refresh() } }.disabled(!model.canReconnect)
                        .accessibilityHint("Checks your home again and reloads tasks. Nothing is sent.")
                    Button("Disconnect", role: .destructive) { Task { await model.disconnect() } }
                        .accessibilityHint("Forgets the access token on this device. Saved tasks stay readable.")
                }
            }
            Section {
                Button("Paste setup link") {
                    #if os(iOS)
                    let text = UIPasteboard.general.string
                    #elseif os(macOS)
                    let text = NSPasteboard.general.string(forType: .string)
                    #else
                    let text: String? = nil
                    #endif
                    model.receiveHomeSetupLink(text ?? "")
                    applySetupLink()
                }.accessibilityIdentifier("paste-home-setup-link")
                if let error = model.homeSetupError { Text(error).foregroundStyle(.red) }
                Button("wingthing.ai") { address = HomeProfile.hostedPresetOrigin; transport = .explicitHostedRoost; setupMode = .remote }
                    .accessibilityIdentifier("wingthing-ai-preset")
                Picker("Connection", selection: $transport) {
                    Text("Local network").tag(HomeTransport.localNetwork)
                    Text("My reachable endpoint").tag(HomeTransport.userOwnedEndpoint)
                    Text("Existing VPN / Tailscale").tag(HomeTransport.existingVPN)
                    Text("Hosted home I choose").tag(HomeTransport.explicitHostedRoost)
                }
                TextField("HTTPS home address", text: $address).plainEntry(url: true)
                TextField("Account ID", text: $userID).plainEntry()
                TextField("Home wing ID", text: $wingID).plainEntry()
                TextField("Home wing public key", text: $wingKey).plainEntry()
            } header: { Text("Your home") } footer: {
                Text("Use the details your home already shows for this account and wing. The wing key is pinned: if it changes, the app won't connect.")
            }
            Section {
                SecureField("Existing access token", text: $token).plainEntry()
                Button("Connect") {
                    let secret = token
                    token = ""
                    Task { await model.connect(origin: address, transport: transport, userID: userID, wingID: wingID, wingPublicKey: wingKey, existingBearer: secret, mode: setupMode) }
                }.disabled(model.busy || !formComplete)
            } header: { Text("Access") } footer: {
                Text("Use access you already have for this home. After its identity is checked, the token is saved in this device's Keychain and restored when you reopen the app. Disconnect forgets it.")
            }
            Section {
                Button("Scan identity QR") {}.disabled(true)
            } footer: {
                Text("Pairing, phone approval, and sign-in aren't available yet. Connecting creates no account or permission and contacts only the address you enter.")
            }
            if let profile = model.profile {
                Section("Connected identity") {
                    Text(profile.origin.absoluteString).textSelection(.enabled)
                    Text("Account: \(profile.expectedUserID)").font(.footnote)
                    Text("Home wing: \(profile.homeWingID)").font(.footnote)
                    if let note = profile.phoneReachabilityNote { Text(note).font(.footnote).foregroundStyle(.secondary) }
                }
            }
            Section("Connection limits") {
                Text("Sharing your home's identity doesn't make it reachable from the internet. Away from your local network, choose your own reachable endpoint or an existing VPN. A hosted home is a separate choice.")
                Text("The preview home only listens on its own computer. Reaching it from a phone needs an endpoint you've set up and approved; this app doesn't change that.")
                Text("A message saved on this phone isn't confirmed until your home reports a receipt. Reconnecting never resends it.")
            }
        }.navigationTitle("Home connection")
            .task(id: model.profile?.id) {
                applySetupLink()
                if !importedSetup, let profile = model.profile, profile.mode == .remote {
                    address = profile.origin.absoluteString; transport = profile.transport
                    userID = profile.expectedUserID; wingID = profile.homeWingID; wingKey = profile.homeWingPublicKey
                }
            }
            .onChange(of: model.pendingHomeSetup?.profile.id) { _ in applySetupLink() }
            .onDisappear { token = ""; _ = model.takeHomeSetupLink() }
    }

    private func applySetupLink() {
        guard let setup = model.takeHomeSetupLink() else { return }
        importedSetup = true
        address = setup.profile.origin.absoluteString; transport = setup.profile.transport
        userID = setup.profile.expectedUserID; wingID = setup.profile.homeWingID; wingKey = setup.profile.homeWingPublicKey
        token = setup.token ?? ""; setupMode = setup.profile.mode
    }
}

extension View {
    @ViewBuilder func hideConversationNavigationBar() -> some View {
        #if os(iOS)
        self.toolbar(.hidden, for: .navigationBar)
        #else
        self
        #endif
    }
    func inlineConversationTitle() -> some View {
        #if os(iOS)
        return self.navigationBarTitleDisplayMode(.inline)
        #else
        return self
        #endif
    }

    func plainEntry(url: Bool = false) -> some View {
        #if os(iOS)
        return self.keyboardType(url ? .URL : .asciiCapable).textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return self.autocorrectionDisabled()
        #endif
    }
}
