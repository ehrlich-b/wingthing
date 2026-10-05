import Foundation
import Testing
import WingthingCore
import WingthingUI

@Suite @MainActor struct ConversationCreationJourneyTests {
    private func setup(_ wire: ConversationTransportFixture, file: URL) async throws -> WingthingModel {
        let model = WingthingModel(), home = try await wire.profile()
        try await model.configure(profile: home, existingBearer: wire.bearer, cacheFile: file, wire: wire)
        await model.refresh(); await model.prepareConversationCreation()
        return model
    }
    private func create(_ model: WingthingModel, name: String = "phone-parent") async {
        await model.createConversation(label: name, workspace: "/synthetic/workspace", model: "claude-opus-5-5", input: "Inspect this project 🦉")
    }
    private func directory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("root-launch-\(UUID())") }

    @Test func emptyHomeCreatesExactlyOneQualifiedParentThroughEncryptedTransport() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let wire = ConversationTransportFixture(scenario: "empty-home"), model = try await setup(wire, file: dir.appendingPathComponent("state.json"))
        expectTrue(model.roots.isEmpty); expectNil(model.selected); expectTrue(model.canCreateConversation)
        await create(model)
        expectEqual(model.selected?.conversationID, "fixture-created-1"); expectEqual(model.execution?.sessionID, "fixture-created-session-1")
        expectEqual(model.execution?.providerSessionID, wire.providerID); expectEqual(model.parent?.reference, model.selected)
        expectNil(model.pendingLaunch); expectNil(model.error)
        let counts = await wire.counts(); expectEqual(counts.launches, 1); expectEqual(counts.requests, 1); expectEqual(counts.stops, 0)
        let intents = await wire.intents(), args = try unwrap(intents.first)
        expectEqual(args["conversation_role"], .string("parent")); expectNil(args["parent_conversation_id"]); expectNil(args["resume_session"])
        expectEqual(args["model"], .string("claude-opus-5-5")); expectEqual(args["cwd"], .string("/synthetic/workspace"))
    }

    @Test func lostAcknowledgementRestartAndReconnectNeverResendAndExplicitReplayKeepsExactIntent() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("state.json"), wire = ConversationTransportFixture(scenario: "creation-lost-reply")
        let first = try await setup(wire, file: file)
        await create(first)
        let pending = try unwrap(first.pendingLaunch)
        expectEqual(pending.progress, .unconfirmed); expectNil(first.selected)
        let restarted = try await setup(wire, file: file)
        expectEqual(restarted.pendingLaunch?.id, pending.id); expectNil(restarted.selected)
        let before = await wire.counts(); expectEqual(before.launches, 1); expectEqual(before.requests, 1)
        await restarted.createConversation(label: "changed", workspace: "/different", model: "claude-other-1", input: "Different input")
        let after = await wire.counts(); expectEqual(after.launches, 1); expectEqual(after.requests, 2)
        expectEqual(restarted.selected?.conversationID, "fixture-created-1"); expectNil(restarted.pendingLaunch)
        expectEqual(await wire.intents(), [.object(pending.arguments)])
        let bytes = try String(contentsOf: file, encoding: .utf8)
        expectFalse(bytes.contains(wire.bearer)); expectFalse(bytes.contains("private_key"))
    }

    @Test func failedSaveDispatchesNothingAndPreservesTheExistingConversationDraft() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let wire = ConversationTransportFixture(scenario: "normal"), model = try await setup(wire, file: dir.appendingPathComponent("state.json"))
        let home = try await wire.profile()
        await model.open(reference(home, wing: wire.wingID, task: wire.rootID)); model.draft = "Existing conversation draft"
        await model.prepareConversationCreation()
        try FileManager.default.removeItem(at: dir); try Data("not a directory".utf8).write(to: dir)
        await create(model)
        expectEqual(model.selected?.conversationID, wire.rootID); expectEqual(model.draft, "Existing conversation draft")
        expectNil(model.pendingLaunch); expectTrue(model.creationNotice != nil)
        let counts = await wire.counts(); expectEqual(counts.requests, 0)
    }

    @Test func confirmedFailureRequiresAnExplicitNewAttemptAndCreatesNoPhantomSelection() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let wire = ConversationTransportFixture(scenario: "creation-failed"), model = try await setup(wire, file: dir.appendingPathComponent("state.json"))
        await create(model)
        let failed = try unwrap(model.pendingLaunch)
        expectEqual(failed.progress, .failed); expectNil(model.selected)
        await create(model); expectEqual((await wire.counts()).requests, 1)
        model.newConversationAttempt(); await create(model)
        expectNil(model.pendingLaunch); expectEqual(model.selected?.conversationID, "fixture-created-2")
        let intents = await wire.intents(); expectEqual(intents.count, 2)
        expectTrue(intents.contains { $0["request_id"] != .string(failed.id.uuidString) })
    }

    @Test func explicitCreateRechecksProjectEvidenceBeforeSavingOrDispatching() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let wire = ConversationTransportFixture(scenario: "empty-home"), model = try await setup(wire, file: dir.appendingPathComponent("state.json"))
        expectTrue(model.canCreateConversation)
        await wire.hideProjects(); await create(model)
        expectEqual((await wire.counts()).requests, 0); expectNil(model.pendingLaunch); expectNil(model.selected)
        expectTrue(model.creationOptions?.projects.isEmpty == true)
        expectTrue(model.creationNotice?.contains("no longer advertised") == true)
    }

    @Test func delayedCreationAcknowledgementCannotRetargetANewSelectionOrItsDraft() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let wire = ConversationTransportFixture(scenario: "normal"), model = try await setup(wire, file: dir.appendingPathComponent("state.json"))
        await wire.holdNextCreation()
        let launch = Task { await create(model) }
        for _ in 0..<500 { if (await wire.counts()).requests > 0 { break }; await Task.yield() }
        expectEqual((await wire.counts()).requests, 1)
        let home = try await wire.profile()
        await model.open(reference(home, wing: wire.wingID, task: wire.rootID)); model.draft = "Keep reading here"
        await wire.releaseCreation(); await launch.value
        expectEqual(model.selected?.conversationID, wire.rootID); expectEqual(model.draft, "Keep reading here")
        await model.prepareConversationCreation(); expectNil(model.pendingLaunch)
        expectEqual((await wire.counts()).launches, 1)
    }

    @Test func freshProjectEvidenceIsRequiredAndGenericAgentStartRemainsUnavailable() async throws {
        let wire = ConversationTransportFixture(scenario: "normal"), home = try await wire.profile()
        let client = try HomeClient(profile: home, existingBearer: wire.bearer, wire: wire)
        _ = try await client.verifyHome()
        let intent = try PendingConversationLaunch(profile: home, label: "parent", workspace: "/synthetic/workspace", model: "claude-opus-5-5", input: "First message")
        do { _ = try await client.submitConversationLaunch(intent, firstAttempt: true); fail("No project observation") } catch { expectEqual(error as? ClientError, .staleReference) }
        _ = try await client.creationOptions(); _ = try await client.verifyHome()
        do { _ = try await client.submitConversationLaunch(intent, firstAttempt: true); fail("Reconnect reused stale project evidence") } catch { expectEqual(error as? ClientError, .staleReference) }
        do { _ = try await client.control(reference(home, wing: wire.wingID), operation: "agent_start", arguments: intent.arguments); fail("Generic launch exposed") } catch { expectTrue(error is ClientError) }
        _ = try await client.creationOptions()
        let other = try PendingConversationLaunch(profile: home, label: "parent", workspace: "/not-advertised", model: "claude-opus-5-5", input: "First message")
        do { _ = try await client.submitConversationLaunch(other, firstAttempt: true); fail("Unadvertised project") } catch { expectEqual(error as? ClientError, .staleReference) }
        expectEqual((await wire.counts()).requests, 0)
        let unsupported = ConversationTransportFixture(scenario: "no-creation")
        let second = try HomeClient(profile: home, existingBearer: unsupported.bearer, wire: unsupported)
        _ = try await second.verifyHome()
        do { _ = try await second.creationOptions(); fail("Unsupported personal capability") } catch { expectTrue(error is ClientError) }
    }

    @Test func receiptRequiresQualifiedRootAndClosedTemplateIncludesExactFirstMessageAndModel() throws {
        let home = try profile(), intent = try PendingConversationLaunch(profile: home, label: "parent", workspace: "/work", model: "claude-opus-5-5", input: "Exact first message 🦉")
        let good: [String: JSONValue] = ["session": .string("new-session"), "conversation_id": .string("new-root"), "root_conversation_id": .string("new-root"),
            "agent": .string("claude"), "wing_id": .string(home.homeWingID), "label": .string(intent.label), "cwd": .string(intent.workspace), "launch_state": .string("started")]
        for change: [String: JSONValue] in [["wing_id": .string("other")], ["agent": .string("other")], ["cwd": .string("/other")], ["label": .string("other")],
            ["root_conversation_id": .string("other")], ["parent_conversation_id": .string("other")], ["session": .string(" ")], ["request_id": .string("other")]] {
            var value = intent; value.apply(.object(good.merging(change) { _, new in new }))
            expectEqual(value.progress, .unconfirmed); expectNil(value.target)
        }
        var started = intent; started.apply(.object(good)); started.markUnconfirmed(); expectEqual(started.progress, .started)
        let argv = try unwrap(intent.arguments["args"]?.array?.compactMap(\.string))
        expectEqual(Array(argv.prefix(13)), ["-p", intent.input, "--output-format", "stream-json", "--verbose", "--restricted", "--setting-sources=", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--max-turns", "40"])
        expectEqual(argv[13], "--allowedTools"); expectEqual(argv[15], "--settings")
        let policy = try JSONDecoder().decode(JSONValue.self, from: Data(argv[16].utf8))
        expectEqual(policy["availableModels"], .array([.string(intent.model)])); expectEqual(policy["enforceAvailableModels"], .bool(true))
        expectEqual(argv[14], ["agent_start", "session_wait", "session_read", "session_status", "conversation_read", "conversation_checkpoint", "wingthing_capabilities"].map { "mcp__wingthing__" + $0 }.joined(separator: ","))
        expectFalse(argv.contains("--tools")); expectFalse(argv.contains("--safe-mode")); expectFalse(argv.contains("--dangerously-skip-permissions"))
        expectThrows(try PendingConversationLaunch(profile: home, label: "bad name", workspace: "/work", model: intent.model, input: "First"))
        expectThrows(try PendingConversationLaunch(profile: home, label: "parent", workspace: "/work", model: "opus", input: "First"))
        expectThrows(try PendingConversationLaunch(profile: home, label: "parent", workspace: "/work", model: intent.model, input: "-flag"))
    }

    @Test func storeRejectsMutatedIntentAndSecondUnconfirmedLaunchAndFinalStateCannotRegress() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let home = try profile(), file = dir.appendingPathComponent("state.json"), store = try LocalConversationStore(profile: home, file: file)
        let first = try PendingConversationLaunch(profile: home, label: "parent", workspace: "/work", model: "claude-opus-5-5", input: "First")
        try await store.saveLaunch(first)
        let changed = try PendingConversationLaunch(id: first.id, profile: home, label: "parent", workspace: "/work", model: first.model, input: "Changed")
        do { try await store.saveLaunch(changed); fail("Mutated saved intent") } catch { expectTrue(error is ClientError) }
        let second = try PendingConversationLaunch(profile: home, label: "other", workspace: "/work", model: first.model, input: "Second")
        do { try await store.saveLaunch(second); fail("Second uncertain launch") } catch { expectTrue(error is ClientError) }
        var failed = first; failed.apply(.object(["session": .string("session"), "conversation_id": .string("root"), "root_conversation_id": .string("root"),
            "agent": .string("claude"), "wing_id": .string(home.homeWingID), "label": .string(first.label), "cwd": .string(first.workspace), "launch_state": .string("failed")]))
        try await store.saveLaunch(failed); try await store.saveLaunch(first)
        expectNil(try await store.unfinishedLaunch())
        try await store.saveLaunch(second); expectEqual(try await store.unfinishedLaunch()?.id, second.id)
    }
}
