import SwiftUI
import WingthingCore

struct MessageReadingFramesKey: PreferenceKey {
    static let defaultValue: [MessageReadingFrame] = []
    static func reduce(value: inout [MessageReadingFrame], nextValue: () -> [MessageReadingFrame]) { value += nextValue() }
}

#if os(iOS)
import UIKit

// Own the native scroll view rather than guessing an anchor fraction or
// depending on SwiftUI's private scroll-view hierarchy. The hosted content is
// still the same selectable, Dynamic Type-aware SwiftUI transcript.
struct NativeConversationScrollView: UIViewControllerRepresentable {
    let content: AnyView
    let frames: [MessageReadingFrame]
    let messageIDs: Set<String>
    let initialPosition: ConversationReadingPosition?
    let latestRequest: Int
    let onPosition: (ConversationReadingPosition, Bool) -> Void
    func makeUIViewController(context: Context) -> NativeConversationScrollController {
        NativeConversationScrollController(content: content, initialPosition: initialPosition)
    }
    func updateUIViewController(_ controller: NativeConversationScrollController, context: Context) {
        controller.update(content: content, frames: frames, messageIDs: messageIDs, latestRequest: latestRequest, onPosition: onPosition)
    }
}

final class NativeConversationScrollController: UIViewController, UIScrollViewDelegate {
    private let scroll = UIScrollView()
    private let hosting: UIHostingController<AnyView>
    private var frames: [MessageReadingFrame] = []
    private var messageIDs: Set<String> = []
    private var position: ConversationReadingPosition?
    private var following = true
    private var latestRequest = 0
    private var restoring = false
    private var layoutPending = false
    private var onPosition: ((ConversationReadingPosition, Bool) -> Void)?
    private var observation: NSKeyValueObservation?

    init(content: AnyView, initialPosition: ConversationReadingPosition?) {
        hosting = UIHostingController(rootView: content)
        position = initialPosition; following = initialPosition?.followingLatest ?? true
        super.init(nibName: nil, bundle: nil)
    }
    required init?(coder: NSCoder) { fatalError("Native transcript uses an explicit model.") }
    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .clear; scroll.backgroundColor = .clear
        scroll.delegate = self; scroll.alwaysBounceVertical = true
        scroll.keyboardDismissMode = .interactive; scroll.contentInsetAdjustmentBehavior = .never
        scroll.accessibilityIdentifier = "conversation-history"
        scroll.translatesAutoresizingMaskIntoConstraints = false; view.addSubview(scroll)
        addChild(hosting); hosting.view.backgroundColor = .clear
        hosting.sizingOptions = .intrinsicContentSize
        hosting.view.translatesAutoresizingMaskIntoConstraints = false; scroll.addSubview(hosting.view)
        hosting.didMove(toParent: self)
        NSLayoutConstraint.activate([
            scroll.topAnchor.constraint(equalTo: view.topAnchor), scroll.bottomAnchor.constraint(equalTo: view.bottomAnchor),
            scroll.leadingAnchor.constraint(equalTo: view.leadingAnchor), scroll.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            hosting.view.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor),
            hosting.view.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor),
            hosting.view.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor),
            hosting.view.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor),
            hosting.view.widthAnchor.constraint(equalTo: scroll.frameLayoutGuide.widthAnchor)
        ])
        observation = scroll.observe(\.contentSize) { [weak self] _, _ in
            Task { @MainActor [weak self] in self?.requestLayout() }
        }
    }
    func update(content: AnyView, frames: [MessageReadingFrame], messageIDs: Set<String>, latestRequest: Int,
                onPosition: @escaping (ConversationReadingPosition, Bool) -> Void) {
        self.onPosition = onPosition; self.frames = frames; self.messageIDs = messageIDs
        if self.latestRequest != latestRequest { position = nil; following = true; self.latestRequest = latestRequest }
        requestLayout()
        hosting.rootView = content
    }
    private func requestLayout() {
        guard !layoutPending else { return }
        layoutPending = true
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.layoutPending = false; self.settleLayout()
        }
    }
    override func viewDidLayoutSubviews() { super.viewDidLayoutSubviews(); requestLayout() }
    private func settleLayout() {
        guard isViewLoaded, !scroll.isDragging, !scroll.isDecelerating, !frames.isEmpty,
              hasCurrentGeometry else { return }
        let height = Double(scroll.bounds.height), content = Double(scroll.contentSize.height)
        let wanted = following ? max(0, content - height) : position?.restoredOffset(frames: frames, viewportHeight: height, contentHeight: content)
        guard let wanted else { return } // Missing message waits for its history; never jumps to another message ID.
        restoring = true
        if abs(scroll.contentOffset.y - wanted) > 0.25 { scroll.setContentOffset(CGPoint(x: 0, y: wanted), animated: false) }
        restoring = false
        capture(persist: false)
    }
    // During reconnect/relaunch the same screen can briefly show empty
    // content while its previous preference frames remain. That geometry must
    // never replace a saved reading anchor or turn following back on.
    private var hasCurrentGeometry: Bool {
        scroll.bounds.height > 0 && scroll.contentSize.height > 0 && !frames.isEmpty &&
        Set(frames.map(\.id)) == messageIDs &&
        frames.allSatisfy { $0.minY + $0.height <= Double(scroll.contentSize.height) + 1 }
    }
    private func capture(persist: Bool) {
        guard hasCurrentGeometry else { return }
        guard let saved = ConversationReadingPosition.capture(frames: frames, offset: Double(scroll.contentOffset.y),
            viewportHeight: Double(scroll.bounds.height), contentHeight: Double(scroll.contentSize.height)) else { return }
        position = saved; following = saved.followingLatest
        onPosition?(saved, persist)
    }
    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        guard !restoring, !layoutPending, scroll.isDragging || scroll.isDecelerating else { return }
        capture(persist: false)
    }
    func scrollViewDidEndDragging(_ scrollView: UIScrollView, willDecelerate decelerate: Bool) {
        if !decelerate { capture(persist: true) }
    }
    func scrollViewDidEndDecelerating(_ scrollView: UIScrollView) { capture(persist: true) }
    override func viewWillDisappear(_ animated: Bool) { capture(persist: true); super.viewWillDisappear(animated) }
}
#endif
