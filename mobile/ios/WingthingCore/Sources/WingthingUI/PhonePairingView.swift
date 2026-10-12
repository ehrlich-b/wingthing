import Foundation
import SwiftUI
import WingthingCore
#if os(iOS)
import UIKit
import AVFoundation
#endif

@MainActor public final class PhonePairingModel: ObservableObject {
    @Published public private(set) var wings: [PairedWing] = []
    @Published public private(set) var selected: PairedWing?
    @Published public private(set) var invitation: PairingInvitation?
    @Published public private(set) var emoji: String?
    @Published public private(set) var sessions: [PhoneSession] = []
    @Published public private(set) var busy = false
    @Published public private(set) var waitingForTerminal = false
    @Published public private(set) var error: String?
    @Published public private(set) var hardStop = false
    @Published public private(set) var scanning = true
    @Published public private(set) var attached: PhoneSession?
    @Published public private(set) var output = ""
    @Published public private(set) var online = false
    private let vault: any PhoneSecretStore
    private let file: URL
    private var enrollment: PhoneEnrollment?
    private var connection: PhoneConnection?
    private var epoch = UUID()
    private var restored = false
    private var readTask: Task<Void, Never>?
    private var confirmTask: Task<Void, Never>?
    public init(vault: any PhoneSecretStore = PhoneKeychainStore(), file: URL? = nil) {
        self.vault = vault
        self.file = file ?? FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Wingthing/paired-wings.json")
    }
    public func restore() async {
        guard !restored else { return }; restored = true
        do {
            if FileManager.default.fileExists(atPath: file.path) {
                wings = try JSONDecoder().decode([PairedWing].self, from: Data(contentsOf: file))
                guard Set(wings.map(\.id)).count == wings.count, wings.allSatisfy({ PairingInvitation.localAllowed($0.local) && ($0.endpoints?.allSatisfy(PairingInvitation.localAllowed) ?? true) && ($0.relay.map(PairingInvitation.relayAllowed) ?? true) }) else { throw PairingError.invalidQR }
            }
            if let first = wings.first { scanning = false; await select(first) }
        } catch { self.error = "Could not restore paired machines. \(error.localizedDescription)" }
    }
    public func scan(_ link: String) async {
        guard !busy, invitation == nil else { return }
        epoch = UUID(); let request = epoch; error = nil; hardStop = false; busy = true
        do {
            let q = try PairingInvitation(link: link); invitation = q
            let enrollment = try PhoneEnrollment(invitation: q); self.enrollment = enrollment
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
        let request = epoch; busy = true; waitingForTerminal = true
        confirmTask = Task {
            do {
                var result = try await enrollment.confirm(emoji: emoji)
                while result == nil {
                    try await Task.sleep(for: .seconds(1)); try Task.checkCancellation()
                    result = try await enrollment.status()
                }
                guard epoch == request, let (wing, secret) = result else { return }
                // Save secrets first; no public profile is published before its vault write.
                try vault.save(secret, for: wing)
                var saved = wings; saved.removeAll { $0.wingID == wing.wingID }; saved.append(wing)
                do {
                    try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
                    try JSONEncoder().encode(saved).write(to: file, options: .atomic)
                } catch { try? vault.forget(wing); throw error }
                wings = saved; invitation = nil; self.emoji = nil; self.enrollment = nil; busy = false; waitingForTerminal = false; scanning = false
                await select(wing)
            } catch {
                guard epoch == request else { return }; fail(error); busy = false; waitingForTerminal = false
                try? await enrollment.cancel(); self.enrollment = nil
            }
        }
    }
    public func cancel() async {
        epoch = UUID(); confirmTask?.cancel(); confirmTask = nil
        if let enrollment { try? await enrollment.cancel() }
        enrollment = nil; invitation = nil; emoji = nil; busy = false; waitingForTerminal = false; error = nil; hardStop = false
        scanning = true
    }
    public func scanAnother() async {
        await detach(); await connection?.close(); connection = nil; selected = nil; online = false; sessions = []
        await cancel()
    }
    public func select(_ wing: PairedWing) async {
        epoch = UUID(); let request = epoch; readTask?.cancel(); await connection?.close(); connection = nil
        selected = wing; scanning = false; error = nil; output = ""; attached = nil; online = false; busy = true
        do {
            guard let secret = try vault.load(for: wing) else { throw PairingError.revoked }
            let client = PhoneConnection(wing: wing, secret: secret); connection = client
            try await client.connect(); let inventory = try await client.sessions()
            guard epoch == request else { await client.close(); return }; sessions = inventory; online = true; busy = false
            let refreshed = await client.wing
            if refreshed != wing {
                var saved = wings; if let index = saved.firstIndex(where: { $0.wingID == wing.wingID }) { saved[index] = refreshed }
                try JSONEncoder().encode(saved).write(to: file, options: .atomic); wings = saved; selected = refreshed
            }
        } catch { guard epoch == request else { return }; fail(error); busy = false }
    }
    public func refresh() async {
        guard !busy, let connection else { return }; busy = true; let request = epoch
        do { let list = try await connection.sessions(); guard epoch == request else { return }; sessions = list; online = true }
        catch { guard epoch == request else { return }; fail(error) }; busy = false
    }
    public func attach(_ session: PhoneSession) async {
        guard !busy, let connection else { return }; busy = true; error = nil; let request = epoch
        do {
            _ = try await connection.command("attach", session: session.id)
            guard epoch == request else { return }; attached = session; output = ""; busy = false
            readTask = Task {
                do {
                    while !Task.isCancelled {
                        if busy { try await Task.sleep(for: .milliseconds(250)); continue }
                        let result = try await connection.command("read", session: session.id)
                        guard epoch == request, attached?.id == session.id else { return }
                        if let screen = result["screen"]?.string { output = screen } else { output += result["output"]?.string ?? "" }; if output.utf8.count > 128 << 10 { output = String(output.suffix(64 << 10)) }
                        if result["truncated"] == .bool(true) { error = "Some output was skipped while this phone was away." }
                        if result["ended"] == .bool(true) { error = "This session ended."; return }
                        try await Task.sleep(for: .milliseconds(500))
                    }
                } catch {
                    guard epoch == request else { return }
                    if !(error is CancellationError) { fail(error) }
                }
            }
        } catch { guard epoch == request else { return }; fail(error); busy = false }
    }
    public func input(_ text: String) async -> Bool {
        guard !busy, let connection, let attached else { return false }; busy = true; let request = epoch
        do { _ = try await connection.command("input", session: attached.id, input: text + "\r"); guard epoch == request else { return false }; busy = false; return true }
        catch { guard epoch == request else { return false }; fail(error); self.error = "Input delivery is unconfirmed. Reconnect and inspect the session before sending again. \(error.localizedDescription)"; busy = false; return false }
    }
    public func stop() async {
        guard !busy, let connection, let attached else { return }; busy = true; let request = epoch
        do { _ = try await connection.command("stop", session: attached.id); guard epoch == request else { return }; readTask?.cancel(); self.attached = nil; output = "" }
        catch { guard epoch == request else { return }; fail(error) }; busy = false
    }
    public func detach() async {
        let request = epoch; busy = true
        // Queue after an outstanding read, then cancel the idle poller.
        if let attached { _ = try? await connection?.command("detach", session: attached.id) }
        guard epoch == request else { return }
        readTask?.cancel(); readTask = nil; attached = nil; output = ""; busy = false
    }
    private func fail(_ error: Error) {
        self.error = error.localizedDescription; online = false
        if error as? PairingError == .identityMismatch || error as? PairingError == .badProof { hardStop = true }
    }
}

public struct PhonePairingView: View {
    @ObservedObject var model: PhonePairingModel
    let legacySetup: () -> Void
    @State private var paste = ""
    @State private var showingPaste = false
    @State private var input = ""
    @State private var confirmingStop = false
    public init(model: PhonePairingModel, legacySetup: @escaping () -> Void) { self.model = model; self.legacySetup = legacySetup }
    public var body: some View {
        VStack(spacing: 16) {
            HStack {
                if model.attached != nil { Button { Task { await model.detach(); await model.refresh() } } label: { Label("Sessions", systemImage: "chevron.left") } }
                Text(model.scanning ? "Scan to pair" : model.selected?.name ?? "Machines").font(.title2.bold())
                Spacer()
                Menu {
                    ForEach(model.wings) { wing in Button(wing.name) { Task { await model.select(wing) } } }
                    Button("Scan another wing") { Task { await model.scanAnother() } }
                    Button("Paste pairing link") { showingPaste = true }
                    Button("Existing Home setup", action: legacySetup)
                } label: { Image(systemName: "ellipsis.circle").font(.title2).frame(width: 44, height: 44) }.accessibilityLabel("Machines and fallback setup")
            }.padding(.horizontal)
            if model.hardStop {
                VStack(spacing: 20) { Image(systemName: "xmark.shield.fill").font(.system(size: 50)); Text("Identity check failed").font(.title.bold()); Text(model.error ?? "Pairing stopped."); Button("Cancel") { Task { await model.cancel() } } }.padding(24).foregroundStyle(.white).background(.red, in: RoundedRectangle(cornerRadius: 18)).padding()
            } else if let invitation = model.invitation {
                ScrollView {
                    VStack(spacing: 20) {
                        Text("Pair with \(invitation.name)?").font(.largeTitle.bold())
                        Text("See and control the sessions on this machine.")
                        Text("Wing fingerprint").font(.caption).foregroundStyle(.secondary)
                        Text(invitation.fingerprint).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                        if let emoji = model.emoji {
                            Text(emoji).font(.system(size: 38)).accessibilityLabel("Check code: \(emoji)")
                            Text("Compare all four emoji with the terminal on \(invitation.name).")
                            if model.waitingForTerminal { ProgressView(); Text("Waiting for Enter on \(invitation.name)…") }
                            else { Button("Emoji match · Pair") { model.confirm() }.buttonStyle(.borderedProminent).disabled(model.busy) }
                        } else if model.busy { ProgressView("Checking the wing…") }
                        if let error = model.error { Text(error).foregroundStyle(.red) }
                        Button("Cancel · emoji differ") { Task { await model.cancel() } }.buttonStyle(.bordered)
                    }.padding(24).multilineTextAlignment(.center)
                }
            } else if model.scanning {
                if let error = model.error { Text(error).foregroundStyle(.red).padding(.horizontal) }
                #if os(iOS)
                PairingCamera { link in Task { await model.scan(link) } }.clipShape(RoundedRectangle(cornerRadius: 20)).padding(.horizontal)
                #else
                Image(systemName: "qrcode.viewfinder").resizable().scaledToFit().padding(40)
                #endif
                Text(model.wings.isEmpty ? "On EhrlichMac, run **wt pair**.\nPoint at the QR in the terminal." : "On the computer you want to pair, run **wt pair**.\nPoint at its QR in the terminal.").multilineTextAlignment(.center).padding()
                if !model.wings.isEmpty { Button("Back to machines") { if let first = model.wings.first { Task { await model.select(first) } } } }
            } else if let attached = model.attached {
                Text(attached.title).font(.headline)
                if let error = model.error { Text(error).foregroundStyle(.red).padding(.horizontal) }
                ScrollView([.horizontal, .vertical]) { Text(model.output.isEmpty ? "Waiting for session output…" : model.output).font(.system(.footnote, design: .monospaced)).fixedSize(horizontal: true, vertical: false).textSelection(.enabled).padding() }
                HStack { TextField("Type into this session", text: $input, axis: .vertical).textFieldStyle(.roundedBorder); Button("Send") { let text = input; Task { if await model.input(text), input == text { input = "" } } }.disabled(model.busy || !model.online || input.isEmpty) }.padding(.horizontal)
                Button("Stop session", role: .destructive) { confirmingStop = true }.disabled(model.busy || !model.online).padding(.bottom)
            } else {
                List {
                    Section {
                        Label(model.online ? "Connected · this machine only" : "Disconnected", systemImage: model.online ? "lock.fill" : "wifi.slash")
                        if let error = model.error { Text(error).foregroundStyle(.red) }
                        if !model.online, let wing = model.selected { Button("Reconnect") { Task { await model.select(wing) } }.disabled(model.busy) }
                    }
                    Section("Sessions") {
                        if model.sessions.isEmpty { Text(model.busy ? "Loading sessions…" : "No active sessions on this wing.").foregroundStyle(.secondary) }
                        ForEach(model.sessions) { session in
                            Button { Task { await model.attach(session) } } label: {
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(session.title).font(.headline)
                                    Text("\(session.agent) · \(session.status)").font(.caption).foregroundStyle(.secondary)
                                    HStack { if session.blocked { Label("Blocked", systemImage: "exclamationmark.bubble").foregroundStyle(.orange) }; if session.unread { Label("Unread", systemImage: "circle.fill").foregroundStyle(.blue) } }.font(.caption)
                                }.foregroundStyle(.primary)
                            }.disabled(model.busy || !model.online)
                        }
                    }
                    Section("Machines") { ForEach(model.wings) { wing in Button(wing.name) { Task { await model.select(wing) } } }; Button("Scan another wing") { Task { await model.scanAnother() } } }
                }.refreshable { await model.refresh() }
            }
            Spacer(minLength: 0)
        }
        .task { await model.restore() }
        .sheet(isPresented: $showingPaste) { NavigationStack { Form { TextField("wingthing://pair#…", text: $paste, axis: .vertical).autocorrectionDisabled(); Button("Read pairing link") { showingPaste = false; Task { await model.scan(paste); paste = "" } } }.navigationTitle("Pairing fallback").toolbar { Button("Cancel") { showingPaste = false; paste = "" } } } }
        .confirmationDialog("Stop this session on \(model.selected?.name ?? "the wing")?", isPresented: $confirmingStop, titleVisibility: .visible) { Button("Stop session", role: .destructive) { Task { await model.stop() } } }
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
