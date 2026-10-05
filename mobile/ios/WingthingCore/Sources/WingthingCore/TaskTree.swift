import Foundation

public struct TaskTreeRow: Identifiable, Sendable {
    public let task: ConversationTask
    public let depth: Int
    public var id: String { task.id }
}

public func orderedTaskTree(_ tasks: [ConversationTask]) -> [TaskTreeRow] {
    var result: [TaskTreeRow] = [], seen: Set<String> = []
    func visit(_ task: ConversationTask, depth: Int) {
        guard seen.insert(task.id).inserted else { return }
        result.append(TaskTreeRow(task: task, depth: depth))
        for child in tasks where child.conversation.wingID == task.conversation.wingID && child.conversation.parentConversationID == task.conversation.conversationID {
            visit(child, depth: min(depth + 1, 6))
        }
    }
    for task in tasks where task.conversation.isRoot { visit(task, depth: 0) }
    for task in tasks where !seen.contains(task.id) { visit(task, depth: 0) } // Retain inspectable orphans/cycles.
    return result
}
