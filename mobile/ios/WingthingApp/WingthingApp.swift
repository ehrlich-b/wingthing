import SwiftUI
import WingthingCore
import WingthingUI

@main struct WingthingApp: App {
    @StateObject private var model = WingthingModel(credentialStore: KeychainCredentialStore())
    var body: some Scene {
        WindowGroup {
            WingthingRootView(model: model)
                .task {
                    #if DEBUG && targetEnvironment(simulator)
                    let environment = ProcessInfo.processInfo.environment
                    if environment["WT_IOS_FIXTURE_ENABLED"] == "1", environment["WT_IOS_PREVIEW_ENABLED"] == nil {
                        await model.openInteractiveFixture(scenario: environment["WT_IOS_FIXTURE_SCENARIO"] ?? "normal", persistenceID: environment["WT_IOS_FIXTURE_PERSISTENCE_ID"])
                    } else if environment["WT_IOS_PREVIEW_ENABLED"] == "1" {
                        await model.inspectLocalPreview(environment: environment)
                    } else { await model.restoreHome() }
                    #else
                    await model.restoreHome()
                    #endif
                }
        }
    }
}
