import Foundation
import WingthingCore

public extension Conversation {
    var displayTitle: String {
        if title.isEmpty || title.range(of: "^web-coordinator-[a-f0-9]+$", options: .regularExpression) != nil { return "Conversation" }
        return title
    }
}

// Preserve exact native records, including their sequence-qualified IDs.
// Messages are prominent; tool/provider/thinking records stay inspectable.
public struct NativeTranscriptPresentation {
    public let messages: [TranscriptItem]
    public let activity: [TranscriptItem]
    public init(_ items: [TranscriptItem]) {
        messages = items.filter { ["user", "assistant"].contains($0.kind) && !$0.content.isEmpty }
        activity = items.filter { !["user", "assistant"].contains($0.kind) || $0.content.isEmpty }
    }
}

// A long selectable Text produces one accessibility frame spanning many
// screens. Keep the original message in the model and expose smaller reading
// blocks; concatenating these blocks is always the exact native content.
public struct NativeMessageBlock: Identifiable, Equatable {
    public let id: Int
    public let content: String

    public static func readingBlocks(_ content: String, maximumCharacters: Int) -> [Self] {
        precondition(maximumCharacters > 0)
        var blocks: [Self] = [], start = content.startIndex
        while start < content.endIndex {
            var end = content.index(start, offsetBy: maximumCharacters, limitedBy: content.endIndex) ?? content.endIndex
            if end < content.endIndex,
               let boundary = content[start..<end].lastIndex(where: { $0.isWhitespace }) {
                end = content.index(after: boundary)
            }
            blocks.append(Self(id: blocks.count, content: String(content[start..<end])))
            start = end
        }
        return blocks
    }
}
