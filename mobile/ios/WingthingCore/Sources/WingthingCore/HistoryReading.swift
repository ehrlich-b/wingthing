import Foundation

// Monotonic time keeps clock adjustments from extending a reading action.
// The seam lets bounded fixtures exercise deadlines without real-time sleeps.
public protocol HistoryReadClock: Sendable {
    var elapsed: Duration { get }
    func sleep(until deadline: Duration) async throws
}

public struct ContinuousHistoryReadClock: HistoryReadClock {
    private let clock = ContinuousClock()
    private let origin: ContinuousClock.Instant
    public init() { origin = clock.now }
    public var elapsed: Duration { origin.duration(to: clock.now) }
    public func sleep(until deadline: Duration) async throws {
        try await clock.sleep(until: origin.advanced(by: deadline))
    }
}

public struct HistoryReadBudget: Sendable {
    public let maxPages: Int
    public let timeLimit: Duration
    public init(maxPages: Int = 100, timeLimit: Duration = .seconds(10)) {
        precondition(maxPages > 0 && timeLimit > .zero)
        self.maxPages = maxPages; self.timeLimit = timeLimit
    }

    // The deadline also cancels an in-flight read, rather than granting another
    // full transport timeout to a page issued near the end of the budget.
    public func observe(_ client: HomeClient, execution: ExecutionReference, after: Int64,
                        deadline: Duration, clock: any HistoryReadClock) async throws -> ExecutionObservation? {
        try await withThrowingTaskGroup(of: ExecutionObservation?.self) { group in
            group.addTask { try await client.observe(execution, after: after) }
            group.addTask { try await clock.sleep(until: deadline); return nil }
            defer { group.cancelAll() }
            return try await group.next()!
        }
    }
}
