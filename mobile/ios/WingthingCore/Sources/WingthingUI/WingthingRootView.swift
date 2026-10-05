import SwiftUI
import WingthingCore

public struct WingthingRootView: View {
    @ObservedObject private var model: WingthingModel
    @State private var selectedTab = 0
    @State private var showingConversation = false
    public init(model: WingthingModel) { self.model = model }

    public var body: some View {
        VStack(spacing: 0) {
            HStack {
                Button {
                    selectedTab = 0
                    Task { await model.openParent(); showingConversation = model.selected != nil }
                } label: {
                    HStack(spacing: 9) {
                        Circle().fill(statusColor(model.parentStatus)).frame(width: 10, height: 10)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(model.parent?.title ?? "Your parent").font(.headline).lineLimit(1)
                            Text(model.parent == nil ? "Choose a home, then select a parent" : model.parentStatus.label).font(.caption).foregroundStyle(.secondary)
                        }
                    }.padding(10).background(.thinMaterial, in: Capsule())
                }.buttonStyle(.plain).disabled(model.parent == nil || model.busy)
                    .accessibilityLabel("Open parent: \(model.parent?.title ?? "none selected"), \(model.parentStatus.label)")
                Spacer()
                if model.busy { ProgressView().controlSize(.small) }
                Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise").frame(minWidth: 44, minHeight: 44) }
                    .accessibilityLabel("Reconnect to your home").accessibilityValue(model.phase.title).disabled(!model.canReconnect)
            }.padding(.horizontal)
            Divider()
            TabView(selection: $selectedTab) {
                NavigationStack {
                    TaskInventoryView(model: model) { showingConversation = true }
                        .navigationTitle("Your tasks")
                        .navigationDestination(isPresented: $showingConversation) { ConversationScreen(model: model) }
                }.tabItem { Label("Tasks", systemImage: "list.bullet.indent") }.tag(0)
                NavigationStack { HomeConnectionView(model: model) }.tabItem { Label("Home", systemImage: "house") }.tag(1)
            }
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
    let onOpen: () -> Void
    var body: some View {
        List {
            if let error = model.error { Section { Label(error, systemImage: "exclamationmark.circle").foregroundStyle(.orange) } }
            if model.profile == nil {
                Section {
                    Label("Start with your own home", systemImage: "house.circle").font(.title3)
                    Text("In Home, enter your home's address and identity, then connect with access you already have. Nothing connects to a vendor service by default.").foregroundStyle(.secondary)
                    Text("Pairing and sign-in aren't available yet. No sample tasks are shown as live.").font(.footnote).foregroundStyle(.secondary)
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
                        } label: { Label(root.title, systemImage: "circle.fill").foregroundStyle(.primary) }
                        .disabled(model.busy)
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
                                        Text(row.task.conversation.title).font(.headline).foregroundStyle(.primary)
                                        Text("\(row.task.conversation.agent) · \(model.status(for: row.task).label)").font(.caption).foregroundStyle(.secondary)
                                        Text(row.task.conversation.cwd).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                                    }
                                    Spacer(); Image(systemName: "chevron.right").foregroundStyle(.secondary)
                                }.padding(.leading, CGFloat(row.depth) * 10)
                            }.disabled(model.busy)
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

private struct ConversationScreen: View {
    @ObservedObject var model: WingthingModel
    @Environment(\.scenePhase) private var phase
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Circle().fill(statusColor(model.currentStatus)).frame(width: 8, height: 8)
                Text(model.currentStatus.label).font(.caption)
                Spacer()
                if !model.connected { Text("Saved copy · not live").font(.caption).foregroundStyle(.secondary) }
                // Never enabled: there is no supported stop to send (see WingthingModel.canStop).
                Button {} label: { Label("Stop", systemImage: "stop.circle").frame(minHeight: 44) }
                    .disabled(!model.canStop)
                    .accessibilityHint(model.stopNotice ?? WingthingModel.stopUnavailableHint)
            }.padding(.horizontal).padding(.vertical, 8)
            if let notice = model.stopNotice {
                Label(notice, systemImage: "info.circle").font(.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal).padding(.bottom, 8)
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    if let error = model.error { Text(error).foregroundStyle(.orange).font(.footnote) }
                    ForEach(model.items) { item in
                        if item.kind == "tool_use" || item.kind == "tool_result" || item.kind == "provider_event" {
                            DisclosureGroup(item.title) {
                                Text(item.content).font(.system(.footnote, design: .monospaced)).textSelection(.enabled)
                                if item.truncated { Text("Provider record truncated").font(.caption).foregroundStyle(.secondary) }
                            }.padding(12).background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
                        } else {
                            VStack(alignment: .leading, spacing: 6) {
                                Text(item.title).font(.caption).foregroundStyle(.secondary)
                                Text(item.content).textSelection(.enabled)
                                if item.truncated { Text("Provider record truncated").font(.caption).foregroundStyle(.secondary) }
                            }.frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    if model.items.isEmpty { Text("No native messages have been read. A quiet terminal does not prove completion.").foregroundStyle(.secondary) }
                    if let attention = model.attention {
                        VStack(alignment: .leading, spacing: 8) {
                            Label("Human response needed", systemImage: "hand.raised")
                            Text(attention.reason).font(.footnote)
                            Text("Exact provider approval decisions are not supported by the current native API. Nothing is automatically approved.").font(.caption).foregroundStyle(.secondary)
                        }.padding(12).background(.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 12))
                    }
                    if let execution = model.execution {
                        DisclosureGroup("Execution details") {
                            Text("Wing: \(execution.conversation.wingID)\nSession: \(execution.sessionID)\nProvider: \(execution.providerSessionID ?? "unknown")").font(.caption).textSelection(.enabled)
                        }
                    }
                }.padding()
            }
            Divider()
            VStack(alignment: .leading, spacing: 8) {
                if let pending = model.pending { Text(pending.delivery.label + " · Check receipt uses the same execution and input request.").font(.caption).foregroundStyle(.secondary) }
                HStack(alignment: .bottom) {
                    TextField("Message your selected conversation", text: $model.draft, axis: .vertical).lineLimit(2...6).textFieldStyle(.roundedBorder).disabled(!model.inputReady)
                    Button(model.pending == nil ? "Send" : "Check receipt") { Task { await model.sendOrCheck() } }
                        .disabled(model.busy || !model.connected || (model.pending == nil && (!model.inputReady || model.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)))
                        .frame(minHeight: 44)
                }
            }.padding()
        }.navigationTitle(model.title)
        .task(id: model.execution) {
            while !Task.isCancelled {
                if phase == .active { await model.pollTranscript() }
                do { try await Task.sleep(for: .seconds(2)) } catch { break }
            }
        }
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
    private var formComplete: Bool {
        [address, userID, wingID, wingKey, token].allSatisfy { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
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
                    token = "" // Only the in-memory connection keeps it from here.
                    Task { await model.connect(origin: address, transport: transport, userID: userID, wingID: wingID, wingPublicKey: wingKey, existingBearer: secret) }
                }.disabled(model.busy || !formComplete)
            } header: { Text("Access") } footer: {
                Text("Use access you already have for this home. It stays in memory on this device and isn't saved, so you'll enter it again after you disconnect or reopen the app.")
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
    }
}

private extension View {
    func plainEntry(url: Bool = false) -> some View {
        #if os(iOS)
        return self.keyboardType(url ? .URL : .asciiCapable).textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return self.autocorrectionDisabled()
        #endif
    }
}
