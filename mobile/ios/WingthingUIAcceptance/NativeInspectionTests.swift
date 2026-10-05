import XCTest

// Only the app's simulator-only inspection route is exercised. No credentials,
// prompts, Stop requests, or mutations of existing conversations are submitted.
final class NativeInspectionTests: XCTestCase {
    @MainActor private func preview(largeText: Bool = false) throws -> (XCUIApplication, String) {
        continueAfterFailure = false
        let environment = ProcessInfo.processInfo.environment
        let app = XCUIApplication()
        for field in ["ORIGIN", "USER", "WING", "KEY", "ROOT", "CHILD"] {
            let value = try XCTUnwrap(environment["WT_IOS_QA_" + field], "Missing explicit QA identity: " + field)
            XCTAssertFalse(value.isEmpty)
            if field != "CHILD" { app.launchEnvironment["WT_IOS_PREVIEW_" + field] = value }
        }
        app.launchEnvironment["WT_IOS_PREVIEW_ENABLED"] = "1"
        app.launchEnvironment["WT_IOS_PREVIEW_SELECTED"] = environment["WT_IOS_QA_ROOT"]
        // Let the audit vary system preferences. A launch-argument override
        // pins the font category and prevents a genuine Dynamic Type audit.
        if largeText { XCTAssertEqual(environment["WT_IOS_QA_CONTENT_SIZE"], "accessibility-extra-extra-extra-large") }
        XCUIDevice.shared.orientation = .portrait
        app.launch()
        let child = try XCTUnwrap(environment["WT_IOS_QA_CHILD"])
        XCTAssertTrue(app.buttons["child-link-" + child].waitForExistence(timeout: 10), "Pinned parent must show its exact child")
        assertInspectionOnly(app)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "identifier BEGINSWITH %@", "native-message-")).firstMatch.waitForExistence(timeout: 10), "A real native message must load before layout inspection")
        return (app, child)
    }

    @MainActor private func capture(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways
        add(attachment)
    }

    @MainActor private func details(_ app: XCUIApplication) {
        if !app.buttons["conversation-details"].exists { app.buttons["related-task-menu"].tap() }
        XCTAssertTrue(app.buttons["conversation-details"].waitForExistence(timeout: 5))
        app.buttons["conversation-details"].tap()
    }

    @MainActor private func assertInspectionOnly(_ app: XCUIApplication) {
        XCTAssertTrue(app.otherElements["read-only-preview"].exists || app.staticTexts["read-only-preview"].exists)
        XCTAssertFalse(app.buttons["send-message"].exists)
        XCTAssertFalse(app.textFields["message-composer"].exists)
    }

    @MainActor func testChildHistoryDetailsAndParentReturnRemainReadOnly() throws {
        let (app, child) = try preview()
        capture(app, "parent-portrait")
        app.buttons["child-link-" + child].tap()
        XCTAssertTrue(app.buttons["open-task-" + child].waitForExistence(timeout: 5))
        app.buttons["open-task-" + child].tap()
        XCTAssertFalse(app.buttons["child-link-" + child].exists, "Selected child is not its own related-task shortcut")
        let history = app.scrollViews["conversation-history"]
        XCTAssertTrue(history.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "identifier BEGINSWITH %@", "native-message-")).firstMatch.waitForExistence(timeout: 10))
        assertInspectionOnly(app)
        capture(app, "child-history")
        history.swipeDown()
        XCTAssertTrue(app.buttons["jump-latest"].waitForExistence(timeout: 5))
        app.buttons["jump-latest"].tap()
        XCTAssertFalse(app.buttons["jump-latest"].exists)
        details(app)
        XCTAssertTrue(app.buttons["close-details"].waitForExistence(timeout: 5))
        for _ in 0..<8 { if app.staticTexts["Activity"].exists { break }; app.swipeUp() }
        XCTAssertTrue(app.staticTexts["Activity"].exists)
        XCTAssertFalse(app.buttons["Stop task"].exists)
        capture(app, "child-details")
        app.buttons["close-details"].tap()
        app.buttons["parent-dot"].tap()
        XCTAssertTrue(app.buttons["related-task-menu"].waitForExistence(timeout: 10))
        assertInspectionOnly(app)
        details(app)
        XCTAssertTrue(app.buttons["Reconnect to your home"].waitForExistence(timeout: 5))
        app.buttons["Reconnect to your home"].tap()
        app.buttons["close-details"].tap()
        XCTAssertTrue(app.buttons["related-task-menu"].waitForExistence(timeout: 10))
        capture(app, "parent-return-reconnected")
    }

    @MainActor func testLargeTextRotationAndAccessibility() throws {
        let (app, _) = try preview(largeText: true)
        capture(app, "parent-large-text")
        if #available(iOS 17.0, *) { try app.performAccessibilityAudit() }
        XCUIDevice.shared.orientation = .landscapeLeft
        XCTAssertTrue(app.buttons["related-task-menu"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["related-task-menu"].isHittable)
        XCTAssertTrue(app.buttons["parent-dot"].isHittable)
        XCTAssertGreaterThan(app.scrollViews["conversation-history"].frame.height, 80, "Landscape must retain a usable reading viewport")
        assertInspectionOnly(app)
        capture(app, "parent-landscape-large-text")
        XCUIDevice.shared.orientation = .portrait
    }

    @MainActor func testDefaultLaunchExplainsHomeConnectionWithoutNetworkConfiguration() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launch()
        XCTAssertTrue(app.buttons["Connect a home"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["send-message"].exists)
        app.buttons["Connect a home"].tap()
        for _ in 0..<8 {
            if app.textFields["HTTPS home address"].exists { break }
            app.swipeUp()
        }
        XCTAssertTrue(app.textFields["HTTPS home address"].exists)
        for _ in 0..<8 {
            if app.buttons["Connect"].exists { break }
            app.swipeUp()
        }
        XCTAssertFalse(app.buttons["Connect"].isEnabled)
        capture(app, "default-home-connection")
    }
}
