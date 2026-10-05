import XCTest

// Every control request terminates in the app's in-process encrypted fixture.
// The reserved .invalid origin never reaches a network or provider process.
final class NativeInteractiveTests: XCTestCase {
    @MainActor private func fixture(_ scenario: String) -> XCUIApplication {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication()
        app.launchEnvironment["WT_IOS_FIXTURE_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_FIXTURE_SCENARIO"] = scenario
        app.launch()
        XCTAssertTrue(app.buttons["send-message"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["read-only-preview"].exists)
        XCTAssertTrue(app.buttons["parent-dot"].waitForExistence(timeout: 5))
        return app
    }

    @MainActor private func wait(_ message: String, _ predicate: @escaping () -> Bool) {
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in predicate() }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [expectation], timeout: 12), .completed, message)
    }

    @MainActor private func metrics(_ app: XCUIApplication, launches: Int, requests: Int, stops: Int) {
        wait("Exact fixture counters", { app.buttons["parent-dot"].value as? String == "Launches \(launches) · requests \(requests) · stops \(stops)" })
    }

    @MainActor private func send(_ app: XCUIApplication, _ input: String) {
        let composer = app.descendants(matching: .any)["message-composer"].firstMatch
        wait("Composer must be ready", { composer.exists && composer.isEnabled })
        composer.tap(); composer.typeText(input)
        let send = app.buttons["send-message"]
        XCTAssertTrue(send.isEnabled); XCTAssertTrue(send.isHittable)
        send.tap()
        wait("Keyboard should dismiss after Send", { !app.keyboards.firstMatch.exists })
    }

    @MainActor private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
    }

    @MainActor private func details(_ app: XCUIApplication) {
        if !app.buttons["conversation-details"].exists { app.buttons["related-task-menu"].tap() }
        XCTAssertTrue(app.buttons["conversation-details"].waitForExistence(timeout: 5))
        app.buttons["conversation-details"].tap()
    }

    @MainActor private func reconnect(_ app: XCUIApplication) {
        details(app)
        XCTAssertTrue(app.buttons["Reconnect to your home"].waitForExistence(timeout: 5))
        app.buttons["Reconnect to your home"].tap()
        app.buttons["close-details"].tap()
    }

    @MainActor func testLostReplyReconnectExplicitReplayAndStopAtLargestText() throws {
        XCTAssertEqual(ProcessInfo.processInfo.environment["WT_IOS_QA_CONTENT_SIZE"], "accessibility-extra-extra-extra-large")
        let app = fixture("lost-reply")
        send(app, "First follow-up")
        XCTAssertTrue(app.staticTexts["follow-up-pending"].waitForExistence(timeout: 5))
        metrics(app, launches: 1, requests: 1, stops: 0)
        XCTAssertFalse(app.buttons["send-message"].isEnabled)
        capture("follow-up-unconfirmed-largest-text")

        reconnect(app)
        wait("Explicit replay must become available", { app.buttons["send-message"].isEnabled })
        metrics(app, launches: 1, requests: 1, stops: 0)
        XCTAssertTrue(app.staticTexts["saved-follow-up"].label.contains("First follow-up"))
        XCTAssertEqual(app.buttons["send-message"].label, "Retry follow-up")
        app.buttons["send-message"].tap()
        wait("Replay must resolve pending state", { !app.staticTexts["follow-up-pending"].exists })
        metrics(app, launches: 1, requests: 2, stops: 0)
        let composer = app.descendants(matching: .any)["message-composer"].firstMatch
        wait("New execution must finish before another Send", { composer.isEnabled })
        XCTAssertFalse((composer.value as? String ?? "").contains("First follow-up"))
        capture("follow-up-replay-one-execution")

        send(app, "Keep working")
        metrics(app, launches: 2, requests: 3, stops: 0)
        XCTAssertTrue(app.buttons["stop-task-composer"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["stop-task-composer"].isHittable)
        app.buttons["stop-task-composer"].tap()
        XCTAssertTrue(app.sheets["Stop this whole task?"].waitForExistence(timeout: 5))
        metrics(app, launches: 2, requests: 3, stops: 0)
        capture("whole-task-stop-confirmation")
        app.sheets["Stop this whole task?"].buttons["Stop task"].tap()
        metrics(app, launches: 2, requests: 3, stops: 1)
        XCTAssertTrue(app.staticTexts["Task ended"].waitForExistence(timeout: 5))
        capture("native-task-ended")
        reconnect(app)
        metrics(app, launches: 2, requests: 3, stops: 1)
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
    }

    @MainActor func testFailedFollowUpRestoresDraftAndNewSendStartsOnce() throws {
        let app = fixture("failed")
        send(app, "Try again")
        XCTAssertTrue(app.staticTexts["follow-up-notice"].waitForExistence(timeout: 5))
        metrics(app, launches: 0, requests: 1, stops: 0)
        let composer = app.descendants(matching: .any)["message-composer"].firstMatch
        wait("Failed message must be restored", { (composer.value as? String ?? "").contains("Try again") && composer.isEnabled })
        capture("failed-follow-up-preserved-draft")
        app.buttons["send-message"].tap()
        metrics(app, launches: 1, requests: 2, stops: 0)
        wait("Confirmed Send must clear draft", { !(composer.value as? String ?? "").contains("Try again") })
        capture("failed-follow-up-new-explicit-attempt")
        XCUIDevice.shared.orientation = .landscapeLeft
        XCTAssertTrue(app.buttons["related-task-menu"].isHittable)
        XCTAssertTrue(app.buttons["send-message"].exists || app.buttons["stop-task-composer"].exists)
        capture("interactive-landscape-largest-text")
        XCUIDevice.shared.orientation = .portrait
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
    }

    @MainActor func testUnconfirmedFollowUpAccessibilityAtLargestText() throws {
        let app = fixture("lost-reply")
        send(app, "First follow-up")
        XCTAssertTrue(app.staticTexts["follow-up-pending"].waitForExistence(timeout: 5))
        metrics(app, launches: 1, requests: 1, stops: 0)
        capture("unconfirmed-follow-up-accessibility")
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
    }
}

// These are genuine native renders of a synthetic conversation, not provider
// output or a claim that the authored storyboard's attachment/result features
// exist. The system controls text size, theme, safe areas and the keyboard.
final class NativeVisualTargetTests: XCTestCase {
    @MainActor private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = "astra-native-" + (ProcessInfo.processInfo.environment["WT_IOS_QA_APPEARANCE"] ?? "device") + "-" + name
        attachment.lifetime = .keepAlways; add(attachment)
    }

    @MainActor func testConversationComposerAndChildReturnAtNormalText() throws {
        continueAfterFailure = false
        XCTAssertEqual(ProcessInfo.processInfo.environment["WT_IOS_QA_CONTENT_SIZE"], "large")
        XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication()
        app.launchEnvironment["WT_IOS_FIXTURE_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_FIXTURE_SCENARIO"] = "design"
        app.launch()
        let child = app.buttons["child-link-fixture-child"]
        XCTAssertTrue(child.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "make the next step clearer")).firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["related-task-menu"].isHittable)
        XCTAssertFalse(app.tabBars.firstMatch.exists)
        XCTAssertTrue(app.buttons["send-message"].isHittable)
        capture("conversation")

        child.tap()
        let open = app.buttons["open-task-fixture-child"]
        XCTAssertTrue(open.waitForExistence(timeout: 5))
        capture("child-expanded")
        let composer = app.descendants(matching: .any)["message-composer"].firstMatch
        composer.tap(); composer.typeText("Can you show me the updated version?")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["related-task-menu"].isHittable, "Keyboard must preserve the toolbar")
        XCTAssertGreaterThanOrEqual(app.buttons["related-task-menu"].frame.minY, 40, "Toolbar stays below the native status area")
        XCTAssertTrue(app.buttons["send-message"].isHittable)
        capture("composer")
        open.tap()
        XCTAssertTrue(app.staticTexts["From Welcome screen conversation"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "shortened the setup text")).firstMatch.waitForExistence(timeout: 10))
        let visibleChild = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            let send = app.buttons["send-message"]
            return !app.keyboards.firstMatch.exists && send.exists && send.isHittable && send.frame.maxY < app.frame.maxY - 24
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [visibleChild], timeout: 10), .completed, "Child composer must be fully visible after keyboard dismissal")
        XCTAssertGreaterThanOrEqual(app.buttons["parent-dot"].frame.minY, 40)
        XCTAssertFalse(app.buttons["child-link-fixture-child"].exists)
        capture("child-detail")
        app.buttons["parent-dot"].tap()
        XCTAssertTrue(app.buttons["open-task-fixture-child"].waitForExistence(timeout: 10))
        XCTAssertTrue((composer.value as? String ?? "").contains("Can you show me the updated version?"))
        capture("parent-return-preserved-draft-and-expansion")
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
    }
}

final class NativeLocalStateTests: XCTestCase {
    @MainActor private func fixture(_ scenario: String) -> XCUIApplication {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication()
        app.launchEnvironment["WT_IOS_FIXTURE_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_FIXTURE_SCENARIO"] = scenario
        app.launchEnvironment["WT_IOS_FIXTURE_PERSISTENCE_ID"] = UUID().uuidString
        app.launch()
        XCTAssertTrue(app.buttons["send-message"].waitForExistence(timeout: 12))
        return app
    }
    @MainActor private func wait(_ reason: String, _ condition: @escaping () -> Bool) {
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in condition() }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [expectation], timeout: 12), .completed, reason)
    }
    @MainActor private func composer(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any)["message-composer"].firstMatch
    }
    @MainActor private func openChild(_ app: XCUIApplication) {
        app.buttons["related-task-menu"].tap()
        app.buttons["Conversations"].tap()
        let child = app.buttons["task-link-fixture-child"]
        XCTAssertTrue(child.waitForExistence(timeout: 8)); child.tap()
        XCTAssertTrue(app.staticTexts["From Welcome screen conversation"].waitForExistence(timeout: 10))
    }
    @MainActor private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
    }
    @MainActor func testExactUnsentParentAndChildDraftsSurviveProcessRelaunch() {
        let app = fixture("design")
        composer(app).tap(); composer(app).typeText("Keep this exact unfinished draft")
        app.terminate(); app.launch()
        wait("Relaunch restores the exact parent draft", { self.composer(app).value as? String == "Keep this exact unfinished draft" })
        XCTAssertEqual(app.buttons["parent-dot"].value as? String, "Launches 0 · requests 0 · stops 0")
        openChild(app)
        XCTAssertFalse((composer(app).value as? String ?? "").contains("unfinished"))
        composer(app).tap(); composer(app).typeText("Child draft stays separate")
        app.terminate(); app.launch()
        wait("Parent stays separate after terminating on the child", { self.composer(app).value as? String == "Keep this exact unfinished draft" })
        openChild(app)
        wait("Child's own draft is restored", { self.composer(app).value as? String == "Child draft stays separate" })
        app.buttons["parent-dot"].tap()
        wait("Returning restores parent draft", { self.composer(app).value as? String == "Keep this exact unfinished draft" })
        XCTAssertEqual(app.buttons["parent-dot"].value as? String, "Launches 0 · requests 0 · stops 0")
        capture("native-drafts-after-two-process-relaunches")
    }
    @MainActor func testWithinMessageReadingPositionSurvivesChildRelaunchAndRotation() {
        let app = fixture("reading")
        let history = app.scrollViews["conversation-history"]
        XCTAssertTrue(history.waitForExistence(timeout: 12))
        wait("All synthetic reading messages loaded", { app.staticTexts.matching(NSPredicate(format: "identifier BEGINSWITH %@", "native-message-")).count > 24 })
        history.swipeDown()
        XCTAssertTrue(app.buttons["jump-latest"].waitForExistence(timeout: 8))
        let elements = app.staticTexts.matching(NSPredicate(format: "identifier BEGINSWITH %@", "native-message-")).allElementsBoundByIndex
        let visible = elements.filter { $0.isHittable && $0.frame.minY > history.frame.minY + 8 && $0.frame.maxY < history.frame.maxY - 8 }
        XCTAssertFalse(visible.isEmpty)
        guard let message = visible.first else { return }
        let identifier = message.identifier, originalY = message.frame.minY
        capture("native-within-message-reading-before-child")
        openChild(app)
        app.buttons["parent-dot"].tap()
        wait("Parent returns to the same message and exact within-message offset", {
            let saved = app.staticTexts[identifier]
            return saved.exists && abs(saved.frame.minY - originalY) <= 3
        })
        app.terminate(); app.launch()
        wait("Process relaunch restores exact reading offset", {
            let saved = app.staticTexts[identifier]
            return saved.exists && abs(saved.frame.minY - originalY) <= 3
        })
        XCTAssertTrue(app.buttons["jump-latest"].exists)
        XCUIDevice.shared.orientation = .landscapeLeft
        XCTAssertTrue(app.buttons["related-task-menu"].isHittable)
        XCUIDevice.shared.orientation = .portrait
        wait("Rotation preserves the message anchor and within-message offset", {
            let saved = app.staticTexts[identifier]
            return saved.exists && abs(saved.frame.minY - originalY) <= 3
        })
        capture("native-within-message-reading-after-child-relaunch-rotation")
        app.buttons["jump-latest"].tap()
        wait("Latest follows the bottom again", { !app.buttons["jump-latest"].exists && app.buttons["child-link-fixture-child"].isHittable })
        XCTAssertEqual(app.buttons["parent-dot"].value as? String, "Launches 0 · requests 0 · stops 0")
    }
}

// A first verified home must expose its inventory without silently selecting or starting work.
final class NativeFreshHomeTests: XCTestCase {
    @MainActor func testFreshConfiguredHomeShowsInventoryAndExplicitSelection() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication()
        app.launchEnvironment["WT_IOS_FIXTURE_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_FIXTURE_SCENARIO"] = "fresh-home"
        app.launch()
        let root = app.buttons["conversation-root-fixture-root"]
        XCTAssertTrue(root.waitForExistence(timeout: 10))
        XCTAssertTrue(root.isHittable)
        XCTAssertFalse(app.buttons["send-message"].exists)
        XCTAssertTrue(app.buttons["Home connection and settings"].exists)
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
        root.tap()
        XCTAssertTrue(app.buttons["send-message"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["parent-dot"].exists)
        XCTAssertEqual(app.buttons["parent-dot"].value as? String, "Launches 0 · requests 0 · stops 0")
        let image = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        image.name = "first-home-explicit-selection-no-launch"; image.lifetime = .keepAlways; add(image)
    }
}

final class NativeConversationCreationTests: XCTestCase {
    @MainActor private func wait(_ message: String, _ predicate: @escaping () -> Bool) {
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in predicate() }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [expectation], timeout: 12), .completed, message)
    }
    @MainActor private func emptyHome(_ scenario: String) -> XCUIApplication {
        continueAfterFailure = false; XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication()
        app.launchEnvironment["WT_IOS_FIXTURE_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_FIXTURE_SCENARIO"] = scenario
        app.launch()
        XCTAssertTrue(app.buttons["new-conversation"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["send-message"].exists)
        app.buttons["new-conversation"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["new-conversation-project"].firstMatch.waitForExistence(timeout: 10))
        return app
    }
    @MainActor private func firstMessage(_ app: XCUIApplication) {
        let input = app.textViews["new-conversation-input"]
        for _ in 0..<5 { if input.exists && input.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(input.isHittable); input.tap(); input.typeText("Review this project")
        let button = app.buttons["create-conversation"]
        for _ in 0..<5 { if button.exists && button.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(button.isHittable); button.tap()
    }
    @MainActor func testCreateFirstConversationAtLargestText() throws {
        let app = emptyHome("empty-home")
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
        firstMessage(app)
        XCTAssertTrue(app.buttons["parent-dot"].waitForExistence(timeout: 12))
        wait("Exactly one new root", { app.buttons["parent-dot"].value as? String == "Launches 1 · requests 1 · stops 0" })
        XCTAssertTrue(app.buttons["send-message"].exists)
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
        let image = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        image.name = "created-first-native-conversation-largest-text"; image.lifetime = .keepAlways; add(image)
    }
    @MainActor func testLostCreationReplyReconnectAndExplicitRetryCreateOnlyOneRoot() throws {
        let app = emptyHome("creation-lost-reply")
        firstMessage(app)
        XCTAssertTrue(app.staticTexts["saved-conversation-launch"].waitForExistence(timeout: 8))
        let reconnect = app.buttons["Reconnect to your home"]
        for _ in 0..<5 { if reconnect.exists && reconnect.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(reconnect.isHittable); reconnect.tap()
        wait("Explicit retry available after read-only reconnect", { app.buttons["create-conversation"].isEnabled })
        XCTAssertEqual(app.buttons["create-conversation"].label, "Retry creation")
        XCTAssertFalse(app.buttons["parent-dot"].exists)
        let retry = app.buttons["create-conversation"]
        for _ in 0..<5 { if retry.exists && retry.isHittable { break }; app.swipeUp() }
        retry.tap()
        XCTAssertTrue(app.buttons["parent-dot"].waitForExistence(timeout: 12))
        wait("Explicit replay has two requests but one root", { app.buttons["parent-dot"].value as? String == "Launches 1 · requests 2 · stops 0" })
    }
}
