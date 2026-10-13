import Foundation
import SwiftUI
import WingthingCore
#if os(iOS)
import UIKit
import AVFoundation
#endif

public struct PhonePairingView: View {
    @ObservedObject var model: PhonePairingModel
    let legacySetup: () -> Void
    @State private var paste = ""
    @State private var showingPaste = false
    @State private var input = ""
    @State private var drafts: [String: String] = [:]
    @State private var draftID: String?
    @FocusState private var composing: Bool
    @FocusState private var searching: Bool
    @State private var stopTarget: PhoneFleetSession?
    @State private var showingReach = false
    @State private var showingNotifications = false
    @State private var showingCloud = false
    @State private var search = ""
    public init(model: PhonePairingModel, legacySetup: @escaping () -> Void) { self.model = model; self.legacySetup = legacySetup }
    private var visible: [PhoneFleetSession] {
        (search.isEmpty ? model.fleet : model.searchResults ?? model.fleet).filter { row in
            let machineMatches = model.selected == nil || row.routes.contains { $0.wingID == model.selected?.id }
            let terms = [row.session.title, row.session.agent, row.machine, row.session.cwd ?? "", row.session.id].joined(separator: " ")
            return machineMatches && (search.isEmpty || model.searchResults != nil || terms.localizedCaseInsensitiveContains(search))
        }
    }
    private var globalCoordinator: PhoneFleetSession? { model.fleet.first(where: \.coordinator) }
    private var groupedRows: [PhoneFleetSession] { visible.filter { $0.id != globalCoordinator?.id } }
    private func checkNames(_ emoji: String) -> String {
        emoji.split(separator: " ").map { glyph in
            String(glyph).applyingTransform(.toUnicodeName, reverse: false)?.replacingOccurrences(of: "\\N{", with: "").replacingOccurrences(of: "}", with: "").lowercased() ?? String(glyph)
        }.joined(separator: " · ")
    }
    private func timestamp(_ item: TranscriptItem) -> String? {
        guard let event = model.thread.events.first(where: { item.id.hasPrefix("\($0.sequence):") }),
              let stamp = event.timestamp else { return nil }
        let parser = ISO8601DateFormatter(); parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let fractional = parser.date(from: stamp); parser.formatOptions = [.withInternetDateTime]
        guard let date = fractional ?? parser.date(from: stamp) else { return nil }
        return date.formatted(date: .abbreviated, time: .shortened)
    }
    private var messages: [TranscriptItem] {
        model.thread.events.flatMap(TranscriptItem.items).filter { ["user", "assistant"].contains($0.kind) && !$0.content.isEmpty }
    }
    public var body: some View {
        VStack(spacing: 0) {
            header
            if model.hardStop { identityFailure }
            else if let invitation = model.invitation { pairing(invitation) }
            else if model.scanning { scanner }
            else if let row = model.attached { session(row) }
            else { inventory }
        }
        .task {
            await model.restore()
            #if DEBUG && targetEnvironment(simulator)
            if let link = ProcessInfo.processInfo.environment["WT_IOS_PHONE_TEST_LINK"],
               ProcessInfo.processInfo.environment["WT_IOS_PHONE_FIXTURE_ID"] != nil {
                await model.scanAnother(); await model.scan(link)
            }
            #endif
        }
        .task(id: search + ":" + String(model.inventoryRevision)) {
            do { try await Task.sleep(for: .milliseconds(350)); try Task.checkCancellation(); await model.search(search) }
            catch {}
        }
        .onChange(of: model.attached?.id) { newID in
            if let draftID { drafts[draftID] = input }
            draftID = newID
            input = newID.flatMap { drafts[$0] } ?? ""
        }
        .onChange(of: input) { value in if let draftID { drafts[draftID] = value } }
        .sheet(isPresented: $showingPaste) {
            NavigationStack {
                Form {
                    Text("Paste the complete one-time invitation from the computer. It includes its address, public identity and six-word code.")
                    TextField("wingthing://pair#…", text: $paste, axis: .vertical).autocorrectionDisabled().accessibilityIdentifier("phone-pairing-link")
                    Button("Read pairing link") { showingPaste = false; let link = paste; paste = ""; Task { await model.scan(link) } }.accessibilityIdentifier("phone-read-link")
                }.navigationTitle("Pairing invitation").toolbar { Button("Cancel") { showingPaste = false; paste = "" } }
            }
        }
        .sheet(isPresented: $showingReach) { reach }
        .sheet(isPresented: $showingNotifications) { notifications }
        .sheet(isPresented: $showingCloud) { cloud }
        .confirmationDialog("Stop \(stopTarget?.session.title ?? "session") on \(stopTarget?.machine ?? "machine")?", isPresented: Binding(get: { stopTarget != nil }, set: { if !$0 { stopTarget = nil } }), titleVisibility: .visible) {
            if let target = stopTarget { Button("Stop this session", role: .destructive) { Task { await model.stop(expectedRowID: target.id) }; stopTarget = nil } }
            Button("Keep running", role: .cancel) { stopTarget = nil }
        } message: { Text("Other sessions keep running. Detach only closes this view.") }
    }
    private var header: some View {
        HStack(spacing: 8) {
            if model.attached != nil {
                Button { Task { await model.detach(); await model.refresh() } } label: { Image(systemName: "chevron.left").frame(width: 44, height: 44) }.accessibilityLabel("Sessions").accessibilityIdentifier("phone-sessions-back")
            }
            Text(model.scanning ? "Scan to pair" : model.attached?.coordinator == true ? "wing" : model.attached?.session.title ?? model.selected?.name ?? "Everywhere").font(.title2.bold()).lineLimit(1)
            Spacer()
            if model.attached?.coordinator == true {
                Button { if model.threadVisible { Task { await model.showTerminal() } } else { model.showThread() } } label: {
                    Image(systemName: model.threadVisible ? "terminal" : "bubble.left.and.bubble.right").frame(width: 44, height: 44)
                }.accessibilityLabel(model.threadVisible ? "Open terminal" : "Open wing thread").accessibilityIdentifier("phone-thread-terminal")
            }
            Menu {
                Button("Everywhere") { Task { await model.showAll() } }
                ForEach(model.wings) { wing in Button(wing.name) { Task { await model.select(wing) } } }
                Button("Scan another wing") { Task { await model.scanAnother() } }
                Button("Paste pairing link") { showingPaste = true }
                Button("Connection and privacy") { showingReach = true }
                Button("Notifications") { showingNotifications = true }
                #if DEBUG && targetEnvironment(simulator)
                if ProcessInfo.processInfo.environment["WT_IOS_PAID_FIXTURE_ENABLED"] == "1", model.attached?.coordinator == true {
                    Button("Run in cloud") { showingCloud = true }
                }
                #endif
                Button("Existing Home setup", action: legacySetup)
            } label: { Image(systemName: "ellipsis.circle").font(.title2).frame(width: 44, height: 44) }.accessibilityLabel("Machines and fallback setup").accessibilityIdentifier("phone-machines-menu")
        }.padding(.horizontal, 16).padding(.vertical, 6)
    }
    private var identityFailure: some View {
        ScrollView {
            VStack(spacing: 20) {
                Image(systemName: "xmark.shield.fill").font(.system(size: 50))
                Text("Identity check failed").font(.title.bold())
                Text(model.error ?? "Pairing stopped.")
                Button("Cancel") { Task { await model.cancel() } }.buttonStyle(.bordered)
            }.padding(24).foregroundStyle(.white).background(.red, in: RoundedRectangle(cornerRadius: 18)).padding()
        }
    }
    private func pairing(_ invitation: PairingInvitation) -> some View {
        ScrollView {
            VStack(spacing: 20) {
                if model.simulatorFixture { Text("Simulator fixture · software key storage").font(.caption).foregroundStyle(.secondary) }
                Image(systemName: "lock.shield").font(.system(size: 44)).foregroundStyle(.tint)
                Text(model.savingPairing ? "Saving this machine securely…" : model.recoverablePairing ? "Finishing pairing" : "Pair with \(invitation.name)?").font(.largeTitle.bold())
                Text("See and control the sessions on this machine.")
                Text("Wing fingerprint").font(.caption).foregroundStyle(.secondary)
                Text(invitation.fingerprint).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                if model.savingPairing { ProgressView(); Text("Your private key stays on this iPhone.") }
                else if let emoji = model.emoji {
                    Text(emoji).font(.system(size: 32))
                            Text(checkNames(emoji)).font(.footnote).foregroundStyle(.secondary).accessibilityLabel("Check code: \(emoji)").accessibilityIdentifier("phone-pairing-emoji")
                    Text("Compare all four emoji with the terminal on \(invitation.name).")
                    if model.waitingForTerminal { ProgressView(); Text("Waiting for Enter on \(invitation.name)…").accessibilityIdentifier("phone-waiting-terminal") }
                    else if model.recoverablePairing {
                        Button("Check pairing result") { model.checkPairingResult() }.buttonStyle(.borderedProminent).accessibilityIdentifier("phone-check-pairing")
                    } else {
                        Button("Emoji match · Pair") { model.confirm() }.buttonStyle(.borderedProminent).disabled(model.busy).accessibilityIdentifier("phone-confirm-pairing")
                    }
                } else if model.busy { ProgressView("Checking the wing…") }
                if let error = model.error { Text(error).foregroundStyle(.red).accessibilityIdentifier("phone-error") }
                Button(model.emoji == nil ? "Scan again" : "Cancel · emoji differ") { Task { await model.cancel() } }.buttonStyle(.bordered).accessibilityIdentifier("phone-cancel-pairing")
            }.padding(24).multilineTextAlignment(.center)
        }
    }
    private var scanner: some View {
        VStack(spacing: 16) {
            if let error = model.error { Text(error).foregroundStyle(.red).padding(.horizontal).accessibilityIdentifier("phone-error") }
            #if os(iOS)
            PairingCamera { link in Task { await model.scan(link) } }.clipShape(RoundedRectangle(cornerRadius: 20)).padding(.horizontal).accessibilityIdentifier("phone-camera")
            #else
            Image(systemName: "qrcode.viewfinder").resizable().scaledToFit().padding(40)
            #endif
            Text("On the computer you want to pair, run **wt pair**.\nPoint at its QR in the terminal.").multilineTextAlignment(.center).padding(.horizontal)
            HStack {
                Button("Enter invitation details") { showingPaste = true }.accessibilityIdentifier("phone-manual-pairing")
                #if os(iOS)
                Button("Open Settings") { if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) } }
                #endif
            }.font(.footnote).padding(.bottom)
            if !model.wings.isEmpty { Button("Back to machines") { Task { await model.showAll() } }.padding(.bottom) }
        }
    }
    private var inventory: some View {
        List {
            Section {
                HStack {
                    Label(model.online ? "Connected" : "Disconnected", systemImage: model.online ? "lock.fill" : "wifi.slash")
                    Spacer()
                    Button { Task { await model.refresh() } } label: { Image(systemName: "arrow.clockwise").frame(width: 44, height: 44) }.accessibilityLabel("Refresh machines").accessibilityValue("\(model.fleet.count) sessions").accessibilityIdentifier("phone-refresh").disabled(model.busy)
                }
                if let notice = model.inventoryNotice { Text(notice).font(.caption).foregroundStyle(.secondary) }
                if let carrier = model.carrier { Text(carrier).font(.caption).foregroundStyle(.secondary) }
                if let error = model.error { Text(error).foregroundStyle(.red).accessibilityIdentifier("phone-error") }
                TextField("Search names, projects or transcript", text: $search).accessibilityIdentifier("phone-search").autocorrectionDisabled().focused($searching).submitLabel(.search).onSubmit { searching = false }
            }
            if model.searchBusy { ProgressView("Searching native messages…") }
            if !search.isEmpty, !model.searchBusy, let results = model.searchResults { Text("\(results.count) matching sessions").font(.caption).foregroundStyle(.secondary).accessibilityIdentifier("phone-search-status") }
            if let notice = model.searchNotice, !search.isEmpty { Text(notice).font(.caption).foregroundStyle(.secondary) }
            if let coordinator = globalCoordinator {
                Section { row(coordinator) }
            }
            ForEach(Array(Set(groupedRows.map(\.machine))).sorted(), id: \.self) { machine in
                Section(machine) { ForEach(groupedRows.filter { $0.machine == machine }) { row($0) } }
            }
            if visible.isEmpty { Text(model.busy ? "Loading sessions…" : search.isEmpty ? "No active sessions on these wings." : "No matching sessions.").foregroundStyle(.secondary) }
            Section("Machines · paired separately") {
                ForEach(model.wings) { wing in
                    Button { Task { await model.select(wing) } } label: {
                        HStack { Label(wing.name, systemImage: "desktopcomputer"); Spacer(); Image(systemName: "lock.shield").foregroundStyle(.secondary) }
                    }.accessibilityIdentifier("phone-machine-" + wing.wingID)
                }
                Button("Scan another wing") { Task { await model.scanAnother() } }
            }
        }.phoneInventoryStyle().refreshable { await model.refresh() }
    }
    private func row(_ row: PhoneFleetSession) -> some View {
        Button { Task { await model.attach(row) } } label: {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 12) {
                    if row.coordinator { Image(systemName: "bubble.left.and.bubble.right.fill").font(.title2).foregroundStyle(.tint) }
                    Text(row.coordinator ? "wing" : row.session.title).font(.headline)
                    Spacer()
                    if row.coordinator { Image(systemName: "pin.fill").foregroundStyle(.secondary) }
                }
                Text("\(row.session.agent) · \(row.session.status)\(row.preferredRoute == nil ? " · last known" : "")\(row.coordinator ? " · " + row.machine : "")").font(.subheadline).foregroundStyle(.secondary)
                if row.coordinator { Text("Message your coordinator").font(.subheadline).foregroundStyle(.secondary) }
                HStack {
                    ForEach(row.routes) { route in
                        Text(route.label + (route.healthy ? "" : " · offline")).font(.caption).padding(.horizontal, 8).padding(.vertical, 4).background(.quaternary, in: Capsule())
                    }
                }
                if row.possibleDuplicate { Label(row.identity == nil ? "Possible duplicate · identity unverified" : "Possible duplicate · unverified copy", systemImage: "questionmark.circle").font(.caption).foregroundStyle(.orange) }
                if let preview = model.searchPreviews[row.id], !search.isEmpty { Text(preview).font(.footnote).lineLimit(3).foregroundStyle(.secondary) }
                HStack {
                    if row.session.blocked { Label("Needs you", systemImage: "exclamationmark.bubble").foregroundStyle(.orange) }
                    if row.session.unread { Label("Unread", systemImage: "circle.fill").foregroundStyle(.blue) }
                }.font(.caption)
            }.padding(.vertical, 5).foregroundStyle(.primary).frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
        }.buttonStyle(.plain).disabled(model.busy || row.preferredRoute == nil || row.identity == nil).accessibilityIdentifier("phone-session-" + row.id)
    }
    private func session(_ row: PhoneFleetSession) -> some View {
        VStack(spacing: 12) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text(row.machine + " · " + (model.activeRoute?.label ?? "disconnected")).font(.caption).foregroundStyle(.secondary)
                    if let carrier = model.activeCarrier { Text(carrier).font(.caption2).foregroundStyle(.secondary) }
                    if model.simulatorFixture && model.activeCarrier?.hasPrefix("Relay fixture") == true { Text("Dormant · loopback relay fixture").font(.caption2).foregroundStyle(.orange) }
                }
                Spacer()
                if !model.online { Button("Reconnect") { Task { await model.reconnectAttached() } }.accessibilityIdentifier("phone-reconnect") }
            }.padding(.horizontal)
            if let error = model.error { Text(error).foregroundStyle(.red).font(.footnote).padding(.horizontal).accessibilityIdentifier("phone-error") }
            if model.threadVisible {
                ScrollViewReader { proxy in
                    ScrollView {
                        VStack(alignment: .leading, spacing: 18) {
                            if messages.isEmpty { Text("No native messages yet. Open the terminal to inspect this session.").foregroundStyle(.secondary) }
                            ForEach(messages) { item in
                                HStack {
                                    if item.kind == "user" { Spacer(minLength: 48) }
                                    VStack(alignment: .leading, spacing: 6) {
                                        Text(item.content).textSelection(.enabled).accessibilityIdentifier("phone-message-" + item.id)
                                        Text((item.kind == "user" ? "You" : row.coordinator ? "wing" : row.session.agent) + (timestamp(item).map { " · " + $0 } ?? "")).font(.caption2).foregroundStyle(.secondary)
                                    }.padding(14).background(item.kind == "user" ? Color.accentColor.opacity(0.12) : Color.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 18))
                                    if item.kind != "user" { Spacer(minLength: 32) }
                                }.id(item.id)
                            }
                        }.padding()
                    }.accessibilityIdentifier("phone-wing-thread").onChange(of: messages.last?.id) { id in if let id { proxy.scrollTo(id, anchor: .bottom) } }
                }
            } else {
                ScrollView([.horizontal, .vertical]) { Text(model.output.isEmpty ? "Waiting for session output…" : model.output).font(.system(.footnote, design: .monospaced)).fixedSize(horizontal: true, vertical: false).textSelection(.enabled).padding() }.accessibilityIdentifier("phone-terminal")
            }
            if row.session.status == "archived" { Text("Archived session · transcript only").font(.footnote).foregroundStyle(.secondary) }
            HStack(alignment: .bottom) {
                TextField(model.threadVisible ? "Message wing" : "Type into this session", text: $input, axis: .vertical).lineLimit(1...6).padding(12).background(.quaternary, in: RoundedRectangle(cornerRadius: 18)).accessibilityIdentifier("phone-input").focused($composing)
                Button { let text = input; Task { if await model.input(text), input == text { input = ""; composing = false } } } label: { Image(systemName: "arrow.up.circle.fill").font(.system(size: 32)).frame(width: 44, height: 44) }.accessibilityLabel("Send").accessibilityIdentifier("phone-send").disabled(model.busy || !model.online || input.isEmpty || row.session.status == "archived")
            }.padding(.horizontal)
            Button("Stop session", role: .destructive) { stopTarget = row }.disabled(model.busy || !model.online || row.session.status == "archived").accessibilityIdentifier("phone-stop").padding(.bottom, 10)
        }
    }
    private var notifications: some View {
        NavigationStack {
            Form {
                Section("Attention") {
                    Text("Wingthing needs your attention.").font(.headline)
                    Text("Lock-screen notifications hide session names, messages and project details.")
                    if model.notificationsEnabled {
                        Button("Disable notifications") { model.disableNotifications() }
                    } else {
                        Button("Enable notifications") { Task { await model.enableNotifications() } }.accessibilityIdentifier("phone-enable-notifications")
                    }
                    if let notice = model.notificationNotice { Text(notice).font(.footnote).foregroundStyle(.secondary) }
                }
                Section { Text("This phone can notify you about new attention while Wingthing is open. Background delivery is not available yet.") }
            }.navigationTitle("Notifications").toolbar { Button("Done") { showingNotifications = false } }
        }
    }
    private var cloud: some View {
        NavigationStack {
            Form {
                Section { Label("Dormant · development fixture", systemImage: "moon.zzz").foregroundStyle(.orange) }
                Section("Run in cloud") {
                    Text((model.attached?.machine ?? "Machine") + " · coordinator")
                    LabeledContent("Approved template", value: "workspace / Claude / standard")
                    LabeledContent("Spending cap", value: "Owner preauthorized in fixture")
                    Text("The phone sends a request to the paired coordinator. Cloud launch and spending remain subject to its preauthorized template and cap.")
                    Button("Send cloud request") {
                        Task {
                            if await model.input("Request cloud work using the owner-preauthorized workspace / Claude / standard template and its existing spending cap. Do not raise the cap.") { showingCloud = false }
                        }
                    }.disabled(model.busy || !model.online).accessibilityIdentifier("phone-cloud-request")
                }
            }.navigationTitle("Run in cloud").toolbar { Button("Cancel") { showingCloud = false } }
        }
    }
    private var reach: some View {
        NavigationStack {
            List {
                Section("Connection") {
                    Text(model.activeCarrier ?? model.carrier ?? "No route connected")
                    if let latency = model.latencyMilliseconds { Text("Last inventory request (\(model.latencyWingName ?? "machine")): \(latency) ms").font(.footnote) }
                    Text("The phone tries LAN, then your existing tailnet, then a registered relay. LAN and tailnet need no Wingthing account.")
                }
                Section("Privacy") {
                    Label("Pinned wing identity", systemImage: "lock.shield")
                    Text("Session commands and results are encrypted to the paired wing. A relay sees routing IDs, traffic size and timing. Your sessions stay on their machines.")
                    Text("Each machine has its own pairing. Revoke a lost phone on every paired wing with wt devices revoke.")
                }
                Section("When disconnected") { Text("Reconnect only reads state. An unconfirmed message stays in the composer; inspect the session before sending it again.") }
            }.navigationTitle("Connection and privacy").toolbar { Button("Done") { showingReach = false } }
        }
    }
}
#if os(iOS)
private struct PairingCamera: UIViewControllerRepresentable {
    let scan: (String) -> Void
    func makeUIViewController(context: Context) -> PairingCameraController { PairingCameraController(scan: scan) }
    func updateUIViewController(_ uiViewController: PairingCameraController, context: Context) {}
    static func dismantleUIViewController(_ controller: PairingCameraController, coordinator: ()) { controller.engine.stop() }
}
@MainActor private final class PairingCameraController: UIViewController {
    let engine = PairingCameraEngine()
    private let scan: (String) -> Void
    private var preview: AVCaptureVideoPreviewLayer?
    init(scan: @escaping (String) -> Void) { self.scan = scan; super.init(nibName: nil, bundle: nil) }
    required init?(coder: NSCoder) { fatalError("init(coder:) is unavailable") }
    override func viewDidLoad() {
        super.viewDidLoad(); view.backgroundColor = .black
        let preview = AVCaptureVideoPreviewLayer(session: engine.session); preview.videoGravity = .resizeAspectFill; view.layer.addSublayer(preview); self.preview = preview
        engine.scan = scan
        AVCaptureDevice.requestAccess(for: .video) { [weak self] allowed in
            Task { @MainActor in
                guard let self else { return }
                if allowed { self.engine.start { [weak self] in Task { @MainActor in self?.showMessage("Camera unavailable. Use Machines and fallback setup to paste a pairing link.") } } }
                else { self.showMessage("Allow Camera in iPhone Settings to scan the terminal QR. You can also use the hidden pairing-link fallback.") }
            }
        }
    }
    override func viewDidLayoutSubviews() { super.viewDidLayoutSubviews(); preview?.frame = view.bounds }
    private func showMessage(_ text: String) { let label = UILabel(); label.text = text; label.textColor = .white; label.numberOfLines = 0; label.textAlignment = .center; label.translatesAutoresizingMaskIntoConstraints = false; view.addSubview(label); NSLayoutConstraint.activate([label.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 24),label.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -24),label.centerYAnchor.constraint(equalTo: view.centerYAnchor)]) }
}
private final class PairingCameraEngine: NSObject, AVCaptureMetadataOutputObjectsDelegate, @unchecked Sendable {
    let session = AVCaptureSession()
    private let queue = DispatchQueue(label: "dev.ehrlich.wingthing.pairing-camera")
    @MainActor var scan: ((String) -> Void)?
    @MainActor private var last = ""
    func start(failed: @escaping @Sendable () -> Void) {
        queue.async { [self] in
            guard let camera = AVCaptureDevice.default(for: .video), let input = try? AVCaptureDeviceInput(device: camera), session.canAddInput(input) else { failed(); return }
            session.beginConfiguration(); session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else { session.commitConfiguration(); failed(); return }
            session.addOutput(output); output.setMetadataObjectsDelegate(self, queue: .main); output.metadataObjectTypes = [.qr]; session.commitConfiguration(); session.startRunning()
        }
    }
    func stop() { queue.async { [self] in if session.isRunning { session.stopRunning() } } }
    func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject], from connection: AVCaptureConnection) {
        guard let text = (objects.first as? AVMetadataMachineReadableCodeObject)?.stringValue else { return }
        Task { @MainActor in guard text != last else { return }; last = text; scan?(text) }
    }
}
#endif

private extension View {
    @ViewBuilder func phoneInventoryStyle() -> some View {
        #if os(iOS)
        self.listStyle(.insetGrouped)
        #else
        self.listStyle(.inset)
        #endif
    }
}
