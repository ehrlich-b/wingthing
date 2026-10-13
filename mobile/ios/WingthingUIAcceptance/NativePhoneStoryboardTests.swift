import XCTest

final class NativePhoneStoryboardTests: XCTestCase {
    private var controlURL: URL { URL(string: ProcessInfo.processInfo.environment["WT_IOS_PHONE_TEST_CONTROL"]!)! }
    @MainActor private func control(_ op: String, wing: String = "main") async throws -> [String: Any] {
        XCTAssertEqual(controlURL.host, "127.0.0.1")
        var request = URLRequest(url: controlURL); request.httpMethod = "POST"
        request.setValue("Bearer " + ProcessInfo.processInfo.environment["WT_IOS_PHONE_TEST_TOKEN"]!, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["op": op, "wing": wing])
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }
    @MainActor private func wait(_ reason: String, _ condition: @escaping () -> Bool) {
        let exp = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in condition() }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [exp], timeout: 15), .completed, reason)
    }
    @MainActor private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
    }
    @MainActor private func app() -> XCUIApplication {
        continueAfterFailure = false; XCUIDevice.shared.orientation = .portrait
        let app = XCUIApplication(); app.launchEnvironment["WT_IOS_PHONE_FIXTURE_ID"] = UUID().uuidString
        app.launchEnvironment["WT_IOS_PAID_FIXTURE_ENABLED"] = "1"
        return app
    }
    @MainActor private func pair(_ app: XCUIApplication, wing: String, captureSteps: Bool = false, dropReply: Bool = false) async throws {
        let prior = try await control("stats", wing: wing)["devices"] as? Int
        if prior != 0 { _ = try await control("revoke", wing: wing) }
        let value = try await control("new", wing: wing)
        app.terminate(); app.launchEnvironment["WT_IOS_PHONE_TEST_LINK"] = value["link"] as? String; app.launch()
        XCTAssertTrue(app.buttons["phone-confirm-pairing"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["phone-pairing-emoji"].exists)
        if captureSteps { capture("05-3-check-both-screens") }
        app.buttons["phone-confirm-pairing"].tap()
        XCTAssertTrue(app.staticTexts["phone-waiting-terminal"].waitForExistence(timeout: 8))
        if captureSteps { capture("05-4-make-the-pairing-private") }
        if dropReply { _ = try await control("drop", wing: wing) }
        _ = try await control("allow", wing: wing)
        if dropReply {
            XCTAssertTrue(app.buttons["phone-check-pairing"].waitForExistence(timeout: 15))
            capture("05-18-pairing-succeeds-but-the-final-reply-drops")
            app.buttons["phone-check-pairing"].tap()
        }
        XCTAssertTrue(app.buttons["phone-refresh"].waitForExistence(timeout: 15))
        wait("Inventories finish refreshing", { app.buttons["phone-refresh"].isEnabled })
        app.launchEnvironment.removeValue(forKey: "WT_IOS_PHONE_TEST_LINK")
    }
    @MainActor private func menu(_ app: XCUIApplication, _ label: String) {
        app.buttons["phone-machines-menu"].tap(); XCTAssertTrue(app.buttons[label].waitForExistence(timeout: 5)); app.buttons[label].tap()
    }
    @MainActor private func send(_ app: XCUIApplication, text: String) {
        let field = app.textFields["phone-input"].exists ? app.textFields["phone-input"] : app.textViews["phone-input"]
        XCTAssertTrue(field.waitForExistence(timeout: 10)); field.tap(); field.typeText(text)
        XCTAssertTrue(app.buttons["phone-send"].isEnabled); app.buttons["phone-send"].tap()
    }
    @MainActor private func waitForConnected(_ app: XCUIApplication) throws {
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in app.buttons["phone-send"].isEnabled }, object: nil)
        guard XCTWaiter.wait(for: [expectation], timeout: 15) == .completed else {
            throw NSError(domain: "NativePhoneAcceptance", code: 1, userInfo: [NSLocalizedDescriptionKey: "Session reconnect did not finish"])
        }
    }
    @MainActor func testPairFleetCoordinatorCollisionFallbackAndSessionControl() async throws {
        let app = app(); app.launch()
        XCTAssertTrue(app.buttons["phone-manual-pairing"].waitForExistence(timeout: 10))
        capture("05-1-start-on-ehrlichmac"); capture("05-2-scan-with-bryans-iphone")
        capture("05-17-camera-or-local-network-permission-denied")
        try await pair(app, wing: "main", captureSteps: true)
        capture("05-5-your-sessions-on-ehrlichmac"); capture("05-16-same-wifi-and-tailnet-both-stay-free")
        try await pair(app, wing: "remote")
        capture("05-7-manage-each-wing-separately"); capture("13-1-open-the-app-anywhere")
        let rows = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'phone-session-'"))
        XCTAssertEqual(app.buttons["phone-refresh"].value as? String, "5 sessions", "Two exact origins with same raw ID, one merged coordinator, one unproven duplicate")
        let coordinator = rows.matching(NSPredicate(format: "label CONTAINS 'Message your coordinator'")).firstMatch
        XCTAssertTrue(coordinator.exists)
        XCTAssertTrue(coordinator.label.contains("direct")); XCTAssertTrue(coordinator.label.contains("via EhrlichMac"))
        coordinator.tap()
        XCTAssertTrue(app.scrollViews["phone-wing-thread"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'DNS helper is ready'")).firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.scrollViews["phone-terminal"].exists)
        capture("13-6-the-coordinator-is-always-on-top")
        menu(app, "Run in cloud")
        XCTAssertTrue(app.buttons["phone-cloud-request"].waitForExistence(timeout: 5))
        capture("10-3-start-from-bryans-iphone")
        app.buttons["Cancel"].tap()
        send(app, text: "Run the pairing tests first.")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'I received your direction'")).firstMatch.waitForExistence(timeout: 12))
        capture("13-5-open-steer-take-over")
        app.buttons["phone-thread-terminal"].tap()
        XCTAssertTrue(app.scrollViews["phone-terminal"].waitForExistence(timeout: 8))
        capture("13-8-coordinator-terminal")
        app.buttons["phone-sessions-back"].tap()
        XCTAssertTrue(app.buttons["phone-refresh"].waitForExistence(timeout: 8))
        wait("All machine refreshes finish", { app.buttons["phone-refresh"].isEnabled })
        let search = app.textFields["phone-search"]; search.tap(); search.typeText("resolver retries")
        wait("Native body search returns live and archived results", { app.staticTexts["phone-search-status"].label == "2 matching sessions" })
        capture("13-4-search-every-session-everywhere")
        let archive = rows.matching(NSPredicate(format: "label CONTAINS 'Resolver follow-up'")).firstMatch
        for _ in 0..<5 { if archive.exists && archive.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(archive.isHittable); archive.tap()
        XCTAssertTrue(app.staticTexts["Archived session · transcript only"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["phone-send"].isEnabled)
        XCTAssertFalse(app.buttons["phone-stop"].isEnabled)
        capture("13-10-archived-native-transcript")
        app.buttons["phone-sessions-back"].tap()
        XCTAssertTrue(search.waitForExistence(timeout: 10))
        search.tap(); search.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 16))
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["phone-refresh"].waitForExistence(timeout: 15))
        _ = try await control("down", wing: "remote")
        app.buttons["phone-refresh"].tap()
        wait("Direct route goes offline", { coordinator.label.contains("offline") })
        capture("13-9-direct-down-via-mac")
        capture("08-5-wing-asleep-relay-interrupted-or-pro-expired")
        coordinator.tap()
        XCTAssertTrue(app.staticTexts["forge · via EhrlichMac"].waitForExistence(timeout: 12))
        send(app, text: "Use the Mac route.")
        let stats = try await control("stats")
        XCTAssertTrue((stats["inputs"] as? [String])?.contains("fixture-remote-wing|coordinator|Use the Mac route.\r") == true)
        capture("13-3-the-mac-wing-is-the-remote-control")
        app.buttons["phone-sessions-back"].tap()
        XCTAssertTrue(app.buttons["phone-refresh"].waitForExistence(timeout: 10))
        wait("Local row is ready after refreshing an offline remote", { app.buttons["phone-refresh"].isEnabled })
        let local = rows.matching(NSPredicate(format: "label CONTAINS 'QR pairing'")).firstMatch
        local.tap(); XCTAssertTrue(app.scrollViews["phone-terminal"].waitForExistence(timeout: 10))
        send(app, text: "Run the pairing tests first.")
        capture("05-13-attach-and-type-from-the-phone")
        _ = try await control("drop-input")
        send(app, text: "Send this only once.")
        XCTAssertTrue(app.staticTexts["phone-error"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["phone-error"].label.contains("unconfirmed"))
        capture("08-6-network-changes-while-sending")
        let before = try await control("stats")["inputs"] as! [String]
        XCTAssertEqual(before.filter { $0.contains("Send this only once.") }.count, 1)
        app.buttons["phone-reconnect"].tap()
        try waitForConnected(app)
        let after = try await control("stats")["inputs"] as! [String]
        XCTAssertEqual(before, after, "Reconnect cannot resend the uncertain input")
        app.buttons["phone-stop"].tap()
        XCTAssertTrue(app.buttons["Stop this session"].waitForExistence(timeout: 5))
        capture("05-14-stop-means-stop-this-session")
        app.buttons["Stop this session"].tap()
        XCTAssertTrue(app.staticTexts["phone-error"].waitForExistence(timeout: 8))
        XCTAssertTrue(app.staticTexts["phone-error"].label.contains("Stop requested"))
        app.buttons["phone-sessions-back"].tap()
        menu(app, "Connection and privacy")
        capture("08-3-what-the-relay-can-see"); capture("08-4-latency-is-visible-not-promised")
        app.buttons["Done"].tap()
        menu(app, "Notifications")
        XCTAssertTrue(app.buttons["phone-enable-notifications"].waitForExistence(timeout: 5))
        app.buttons["phone-enable-notifications"].tap()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        if springboard.buttons["Allow"].waitForExistence(timeout: 3) { springboard.buttons["Allow"].tap() }
        XCTAssertTrue(app.buttons["Disable notifications"].waitForExistence(timeout: 8))
        capture("05-15-attention-notification-with-quiet-content")
        app.buttons["Disable notifications"].tap()
        app.buttons["Done"].tap()
        _ = try await control("up", wing: "remote")
        _ = try await control("revoke")
        app.buttons["phone-refresh"].tap()
        wait("Revoked wing reports lost access", { app.staticTexts["phone-error"].label.contains("revoked") })
        capture("05-12-bryans-iphone-is-lost")
        _ = try await control("up", wing: "remote")
    }
    @MainActor func testCloudRequestIsDormantOffByDefaultAndUsesOnlyCoordinatorInput() async throws {
        let app = app()
        app.launchEnvironment.removeValue(forKey: "WT_IOS_PAID_FIXTURE_ENABLED")
        try await pair(app, wing: "main")
        let coordinator = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'phone-session-' AND label CONTAINS 'Message your coordinator'")).firstMatch
        coordinator.tap()
        XCTAssertTrue(app.scrollViews["phone-wing-thread"].waitForExistence(timeout: 10))
        app.buttons["phone-machines-menu"].tap()
        XCTAssertFalse(app.buttons["Run in cloud"].exists)
        app.tap()
        app.terminate(); app.launchEnvironment["WT_IOS_PAID_FIXTURE_ENABLED"] = "1"; app.launch()
        XCTAssertTrue(app.buttons["phone-refresh"].waitForExistence(timeout: 15))
        wait("Restored fleet finishes reading", { app.buttons["phone-refresh"].isEnabled })
        coordinator.tap()
        XCTAssertTrue(app.scrollViews["phone-wing-thread"].waitForExistence(timeout: 10))
        menu(app, "Run in cloud")
        XCTAssertTrue(app.buttons["phone-cloud-request"].waitForExistence(timeout: 5))
        capture("10-3-start-from-bryans-iphone")
        app.buttons["phone-cloud-request"].tap()
        wait("Cloud fixture sends one ordinary phone input", { app.scrollViews["phone-wing-thread"].exists })
        let inputs = try await control("stats")["inputs"] as! [String]
        XCTAssertEqual(inputs.filter { $0.hasPrefix("fixture-remote-wing|coordinator|Request cloud work using the owner-preauthorized") }.count, 1)
    }
    @MainActor func testPairingCancellationExpiryUnreachableAndDroppedReceipt() async throws {
        let app = app()
        let baseline = try await control("stats")["devices"] as? Int
        if baseline != 0 { _ = try await control("revoke") }
        _ = try await control("up", wing: "remote")
        var value = try await control("new")
        app.launchEnvironment["WT_IOS_PHONE_TEST_LINK"] = value["link"] as? String; app.launch()
        XCTAssertTrue(app.buttons["phone-confirm-pairing"].waitForExistence(timeout: 12))
        app.buttons["phone-cancel-pairing"].tap()
        XCTAssertTrue(app.buttons["phone-manual-pairing"].waitForExistence(timeout: 8))
        capture("05-11-the-emoji-differ")
        value = try await control("stats"); XCTAssertEqual(value["devices"] as? Int, 0)
        // A consumed invitation reaches the real Go manager, which refuses it.
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["phone-cancel-pairing"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["phone-error"].label.contains("already used") || app.staticTexts["phone-error"].label.contains("cancelled"))
        capture("05-8-qr-expired-or-already-used")
        let wrong = try await control("wrongpin")["link"] as! String
        app.terminate(); app.launchEnvironment["WT_IOS_PHONE_TEST_LINK"] = wrong; app.launch()
        XCTAssertTrue(app.staticTexts["Identity check failed"].waitForExistence(timeout: 15))
        capture("05-10-the-wing-fingerprint-does-not-match")
        _ = try await control("down")
        let link = try await control("new")["link"] as! String
        app.terminate(); app.launchEnvironment["WT_IOS_PHONE_TEST_LINK"] = link; app.launch()
        XCTAssertTrue(app.staticTexts["phone-error"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["phone-error"].label.contains("reachable"))
        capture("05-9-ehrlichmac-is-unreachable")
        _ = try await control("up")
        try await pair(app, wing: "main", dropReply: true)
        let stats = try await control("stats"); XCTAssertEqual(stats["devices"] as? Int, 1)
    }
}
