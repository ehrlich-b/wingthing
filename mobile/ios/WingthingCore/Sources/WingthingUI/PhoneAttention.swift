import Foundation
import UserNotifications
import WingthingCore

@MainActor public protocol PhoneAttentionDelivery {
    var enabled: Bool { get }
    func enable() async throws -> Bool
    func disable()
    func notify(_ identity: PhoneSessionIdentity) async throws
}

@MainActor public final class PhoneAttentionNotifications: PhoneAttentionDelivery {
    public private(set) var enabled = false
    private var center: UNUserNotificationCenter { .current() }
    public init() {}
    public func enable() async throws -> Bool {
        enabled = try await center.requestAuthorization(options: [.alert, .sound])
        return enabled
    }
    public func disable() { enabled = false; center.removeAllPendingNotificationRequests() }
    public func notify(_ identity: PhoneSessionIdentity) async throws {
        guard enabled else { return }
        let content = UNMutableNotificationContent()
        content.title = "Wingthing"; content.body = "Wingthing needs your attention."
        content.userInfo = ["origin_wing_id": identity.originWingID, "session_id": identity.sessionID]
        try await center.add(UNNotificationRequest(identifier: identity.key, content: content, trigger: nil))
    }
}
