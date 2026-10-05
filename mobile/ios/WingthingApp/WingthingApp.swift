import SwiftUI
import WingthingUI

@main struct WingthingApp: App {
    @StateObject private var model = WingthingModel()
    var body: some Scene { WindowGroup { WingthingRootView(model: model) } }
}
