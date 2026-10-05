import Foundation
import WingthingCore

// Read-only acceptance of the native core against an explicit, already
// browser-authorized preview roost on this same Mac. It takes no credential,
// launches, prompts and stops nothing, and prints only IDs, cursors and the
// final native assistant text for the root and each child task.

private func finish(_ code: Int32, _ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8)); exit(code)
}

#if DEBUG
private let usage = "usage: WingthingLivePreview --origin http://127.0.0.1:PORT --user local --wing WING_ID --public-key BASE64 --root CONVERSATION_ID"
private let flags = ["--origin", "--user", "--wing", "--public-key", "--root"]

private func parse(_ argv: [String]) -> [String: String] {
    var values: [String: String] = [:], rest = argv[...]
    while let flag = rest.popFirst() {
        let lowered = flag.lowercased()
        if ["token", "bearer", "credential", "cookie", "password", "secret", "auth", "session-key"].contains(where: { lowered.contains($0) }) {
            finish(2, "Credentials are not accepted; local preview never sends one.")
        }
        guard flags.contains(flag) else { finish(2, "Unknown argument \(flag.prefix(40)).\n\(usage)") }
        guard values[flag] == nil else { finish(2, "Duplicate argument \(flag).") }
        guard let value = rest.popFirst(), !value.hasPrefix("--"), !value.isEmpty else { finish(2, "Missing value for \(flag).") }
        values[flag] = value
    }
    guard values.count == flags.count else { finish(2, usage) }
    return values
}

private struct TaskReceipt: Encodable {
    let conversationID: String
    let parentConversationID: String?
    let sessionID: String
    var providerSessionID: String?
    var state: String?
    var stateSource: String?
    var cursor: Int64?
    var headCursor: Int64?
    var pages: Int?
    var nativeMessageCount: Int?
    var finalAssistantText: String?
    var error: String?
    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id", parentConversationID = "parent_conversation_id", sessionID = "session_id"
        case providerSessionID = "provider_session_id", state, stateSource = "state_source", cursor, headCursor = "head_cursor", pages
        case nativeMessageCount = "native_message_count", finalAssistantText = "final_assistant_text", error
    }
}

private struct Receipt: Encodable {
    let ok: Bool
    let origin: String
    let userID: String
    let wingID: String
    let rootConversationID: String
    let pageLimit: Int64
    let tasks: [TaskReceipt]
    let error: String?
    enum CodingKeys: String, CodingKey {
        case ok, origin, userID = "user_id", wingID = "wing_id", rootConversationID = "root_conversation_id", pageLimit = "session_read_page_limit", tasks, error
    }
}

private func describe(_ error: any Error) -> String {
    String(((error as? LocalizedError)?.errorDescription ?? "\(type(of: error))").prefix(300))
}

private func emit(_ receipt: Receipt) -> Never {
    let encoder = JSONEncoder(); encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
    FileHandle.standardOutput.write((try? encoder.encode(receipt)) ?? Data("{\"ok\":false}".utf8))
    FileHandle.standardOutput.write(Data("\n".utf8))
    exit(receipt.ok ? 0 : 1)
}

let arguments = parse(Array(CommandLine.arguments.dropFirst()))
let origin = arguments["--origin"]!, root = arguments["--root"]!
@MainActor private func configure() -> (HomeProfile, HomeClient) {
    do {
        guard let url = URL(string: origin) else { throw ClientError.invalidConfiguration("Invalid origin.") }
        let home = try HomeProfile.localPreview(origin: url, expectedUserID: arguments["--user"]!, homeWingID: arguments["--wing"]!, homeWingPublicKey: arguments["--public-key"]!)
        return (home, try HomeClient(localPreview: home))
    } catch { finish(2, describe(error)) }
}
let (home, client) = configure()

@MainActor private func run() async -> Receipt {
    var tasks: [TaskReceipt] = []
    func failed(_ error: any Error) -> Receipt {
        Receipt(ok: false, origin: origin, userID: home.expectedUserID, wingID: home.homeWingID, rootConversationID: root, pageLimit: HomeClient.sessionPageLimit, tasks: tasks, error: describe(error))
    }
    do {
        _ = try await client.verifyHome()
        let reference = ConversationReference(profileID: home.id, userID: home.expectedUserID, wingID: home.homeWingID, conversationID: root)
        let tree = try await client.conversation(reference)
        guard tree.conversation.isRoot else { throw ClientError.response("The selected conversation is not a root.") }
        // Parent first, then each child, all from the same tree read.
        let ordered = tree.tasks.filter { $0.conversation.conversationID == root } + tree.tasks.filter { $0.conversation.conversationID != root }
        for task in ordered {
            let conversation = task.conversation
            var receipt = TaskReceipt(conversationID: conversation.conversationID, parentConversationID: conversation.parentConversationID, sessionID: conversation.sessionID)
            if conversation.sessionID.isEmpty {
                receipt.error = "This task has no session."
            } else {
                do {
                    let child = ConversationReference(profileID: home.id, userID: home.expectedUserID, wingID: conversation.wingID, conversationID: conversation.conversationID)
                    let history = try await client.readToHead(ExecutionReference(conversation: child, sessionID: conversation.sessionID))
                    let view = history.lifecycle
                    receipt.providerSessionID = view.providerSessionID; receipt.state = view.state; receipt.stateSource = view.stateSource
                    receipt.cursor = view.cursor; receipt.headCursor = view.headCursor; receipt.pages = history.pages
                    receipt.nativeMessageCount = history.nativeMessages.count
                    receipt.finalAssistantText = history.finalAssistantText.map { String($0.prefix(4000)) }
                } catch { receipt.error = describe(error) }
            }
            tasks.append(receipt)
        }
    } catch { return failed(error) }
    let ok = !tasks.isEmpty && tasks.allSatisfy { $0.error == nil }
    return Receipt(ok: ok, origin: origin, userID: home.expectedUserID, wingID: home.homeWingID, rootConversationID: root, pageLimit: HomeClient.sessionPageLimit, tasks: tasks, error: ok ? nil : "One or more task reads did not complete.")
}

emit(await run())
#else
finish(2, "WingthingLivePreview is available only in debug builds.")
#endif
