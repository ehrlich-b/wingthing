import Foundation

public struct PhoneSessionIdentity: Hashable, Codable, Sendable {
    public let originWingID, sessionID: String
    public init(originWingID: String, sessionID: String) { self.originWingID = originWingID; self.sessionID = sessionID }
    public var key: String { Data(try! JSONEncoder().encode([originWingID, sessionID])).base64EncodedString() }
}

public struct PhoneInventory: Sendable {
    public let wing: PairedWing
    public let sessions: [PhoneSession]
    public let healthy: Bool
    public init(wing: PairedWing, sessions: [PhoneSession], healthy: Bool) { self.wing = wing; self.sessions = sessions; self.healthy = healthy }
}

public struct PhoneRoute: Identifiable, Equatable, Sendable {
    public var id: String { wingID }
    public let wingID, name: String
    public let direct, healthy: Bool
    public var label: String { direct ? "direct" : "via " + name }
}

public struct PhoneFleetSession: Identifiable, Equatable, Sendable {
    public let identity: PhoneSessionIdentity?
    public let session: PhoneSession
    public let machine: String
    public let routes: [PhoneRoute]
    public let possibleDuplicate: Bool
    public let id: String
    public let coordinator: Bool
    public var preferredRoute: PhoneRoute? {
        routes.filter(\.healthy).sorted { a, b in a.direct != b.direct ? a.direct : a.wingID < b.wingID }.first
    }
}

public enum PhoneFleet {
    public static func merge(_ inventories: [PhoneInventory]) -> [PhoneFleetSession] {
        struct Observation {
            let identity: PhoneSessionIdentity?
            let session: PhoneSession
            let route: PhoneRoute
            let machine: String
            let key: String
        }
        var observations: [Observation] = []
        for inventory in inventories.sorted(by: { $0.wing.wingID < $1.wing.wingID }) {
            for session in inventory.sessions {
                // A legacy row without a machine label belongs to its authenticated wing.
                // A labeled remote without an origin remains unproven, even if names match.
                let remoteLabel = session.machine?.isEmpty == false
                let origin = session.originWingID?.isEmpty == false ? session.originWingID : remoteLabel ? nil : inventory.wing.wingID
                let identity = origin.map { PhoneSessionIdentity(originWingID: $0, sessionID: session.id) }
                let key = identity.map { "known:" + $0.key } ?? "unproven:" + (try! JSONEncoder().encode([inventory.wing.wingID, session.machine ?? "", session.id])).base64EncodedString()
                observations.append(Observation(identity: identity, session: session,
                    route: PhoneRoute(wingID: inventory.wing.wingID, name: inventory.wing.name, direct: origin == inventory.wing.wingID, healthy: inventory.healthy),
                    machine: session.machine ?? inventory.wing.name, key: key))
            }
        }
        let groups = Dictionary(grouping: observations, by: \.key)
        return groups.map { key, values in
            let sorted = values.sorted { a, b in
                if a.route.healthy != b.route.healthy { return a.route.healthy }
                if a.route.direct != b.route.direct { return a.route.direct }
                return a.route.wingID < b.route.wingID
            }
            let first = sorted[0]
            let uncertain = observations.contains { other in
                other.key != key && other.session.id == first.session.id && (other.identity == nil || first.identity == nil)
            }
            var routeIDs = Set<String>()
            let routes = sorted.map(\.route).filter { routeIDs.insert($0.id).inserted }
            let pin = sorted.first { $0.session.pinned != nil && $0.session.role != nil }
            return PhoneFleetSession(identity: first.identity, session: first.session, machine: first.machine, routes: routes,
                possibleDuplicate: first.identity == nil || uncertain, id: key,
                coordinator: first.identity != nil && pin?.session.pinned == true && pin?.session.role == "coordinator")
        }.sorted { a, b in
            if a.coordinator != b.coordinator { return a.coordinator }
            if a.machine != b.machine { return a.machine.localizedStandardCompare(b.machine) == .orderedAscending }
            if a.session.blocked != b.session.blocked { return a.session.blocked }
            if a.session.title != b.session.title { return a.session.title.localizedStandardCompare(b.session.title) == .orderedAscending }
            return a.id < b.id
        }
    }
}

public protocol PhoneSessionTransport: Sendable {
    func connect() async throws
    func sessions() async throws -> [PhoneSession]
    func command(_ op: String, session: String, input: String, originWingID: String?) async throws -> JSONValue
    func close() async
}
extension PhoneConnection: PhoneSessionTransport {}
