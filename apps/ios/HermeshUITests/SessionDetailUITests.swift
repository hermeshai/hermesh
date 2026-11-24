import XCTest

/// Regression tests for iPhone session detail (Overview / Progress -> Session Detail & Drawer -> Session Detail):
/// 1. Session Info omits the unreliable "Turns" row while keeping underlying turnCount data intact.
/// 2. Long history opens positioned at the bottom latest content.
/// 3. Delayed multi-batch history waits for loading & layout completion, then positions at the bottom.
/// 4. Re-entering and switching between sessions always positions at the bottom of the target session.
/// 5. Streaming updates follow the bottom when at the bottom, and never steal scroll position once the user scrolls up.
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh \
///         -destination 'platform=iOS Simulator,name=…' -only-testing:HermeshUITests/SessionDetailUITests
final class SessionDetailUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = [
            "-AppleLanguages", "(en)",
            "-AppleLocale", "en_US",
            "-DemoSessionDetailRegression",
        ]
        app.launch()
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        openOverview()
    }

    /// Task Info (SessionInfoSheet) must not show the unreliable "Turns" row even when underlying turnCount > 0.
    func testSessionInfoHidesTurnsRow() {
        openSessionFromOverview("Long history session")
        tap("session-info")
        XCTAssertTrue(app.buttons["close"].waitForExistence(timeout: 10), "Task Info sheet")
        XCTAssertTrue(app.navigationBars["Task Info"].waitForExistence(timeout: 5), "Task Info navigation bar")
        XCTAssertTrue(element(containing: "Started by").waitForExistence(timeout: 10), "Started by row")
        XCTAssertTrue(element(containing: "Coding agent").exists, "Coding agent row")
        XCTAssertFalse(element(containing: "Turns").exists, "Task Info should not display unreliable Turns row")
        XCTAssertFalse(element(containing: "轮数").exists, "Task Info should not display Chinese Turns row")
        app.swipeUp()
        XCTAssertTrue(element(containing: "Started").exists, "Started row")
        XCTAssertFalse(element(containing: "Turns").exists, "Task Info should not display Turns row after scrolling")
        XCTAssertFalse(element(containing: "轮数").exists, "Task Info should not display Chinese Turns row after scrolling")
        tap("close")
    }

    /// Long history, delayed multi-batch loading, re-entry, and switching all land at the latest bottom content.
    func testLongDelayedReenterAndSwitchPositionAtBottom() {
        // 1. Delayed history opened cold from Overview ("查看进展 -> 会话详情").
        openSessionFromOverview("Delayed history session")
        let delayedLatest = text(beginningWith: "Delayed latest #24")
        let delayedStart = text(beginningWith: "Delayed start #1")
        XCTAssertTrue(waitUntil(timeout: 15) { delayedLatest.exists && self.isVisibleInViewport(delayedLatest) },
                      "delayed history should land at latest message #24 once loaded and laid out (frame=\(delayedLatest.frame))")
        XCTAssertFalse(isVisibleInViewport(delayedStart), "first delayed message should be scrolled above the viewport")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end should not show while positioned at the bottom")

        // 2. Scroll up inside Delayed history session, go back to Overview, and re-enter it.
        scrollUpConversation(times: 3)
        XCTAssertTrue(app.buttons["to-end"].waitForExistence(timeout: 5), "to-end appears after scrolling up")
        XCTAssertFalse(isVisibleInViewport(delayedLatest), "latest message scrolled below viewport")
        navigateBack()
        openSessionFromOverview("Delayed history session")
        XCTAssertTrue(waitUntil(timeout: 10) { delayedLatest.exists && self.isVisibleInViewport(delayedLatest) },
                      "re-entering session should position at the latest bottom content (frame=\(delayedLatest.frame))")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end should not show after re-entering at bottom")

        // 3. Open Long history session from Drawer, scroll up, switch to Delayed, and switch back to Long.
        navigateBack()
        openSessionFromDrawer("Long history session")
        let longLatest = text(beginningWith: "Long latest #24")
        let longStart = text(beginningWith: "Long start #1")
        XCTAssertTrue(waitUntil(timeout: 10) { longLatest.exists && self.isVisibleInViewport(longLatest) },
                      "long history should open at latest message #24 (frame=\(longLatest.frame))")
        XCTAssertFalse(isVisibleInViewport(longStart), "first long message should be above viewport")

        // Scroll up so the current session is away from the bottom, then switch via Drawer.
        scrollUpConversation(times: 3)
        XCTAssertTrue(app.buttons["to-end"].waitForExistence(timeout: 5), "scrolled up in long history session")
        openSessionFromDrawer("Delayed history session")
        XCTAssertTrue(waitUntil(timeout: 10) { delayedLatest.exists && self.isVisibleInViewport(delayedLatest) },
                      "switching to Delayed history session should land at bottom (frame=\(delayedLatest.frame))")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end hidden after switching")

        // Switch back to Long history session (cached) and confirm it also lands at the bottom.
        openSessionFromDrawer("Long history session")
        XCTAssertTrue(waitUntil(timeout: 10) { longLatest.exists && self.isVisibleInViewport(longLatest) },
                      "switching back to Long history session should land at bottom (frame=\(longLatest.frame))")
        XCTAssertFalse(isVisibleInViewport(longStart), "first long message still above viewport after switching back")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end hidden after switching back")
    }

    /// Streaming updates follow the bottom while at the bottom, and never steal position after the user scrolls up.
    func testStreamingFollowsBottomAndRespectsUserScrollUp() throws {
        openSessionFromOverview("Streaming session")

        // Initial load should position at the bottom and follow incoming live updates while at the end.
        XCTAssertTrue(waitUntil(timeout: 12) { self.hasVisibleLiveUpdate(atLeast: 1) },
                      "staying at bottom should follow incoming Live update into view")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end hidden while following at bottom")

        // User actively scrolls up to read earlier history while streaming continues.
        scrollUpConversation(times: 4)
        let toEnd = app.buttons["to-end"]
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "to-end appears after user scrolls up")
        usleep(400_000) // let deceleration settle completely

        // Pick an earlier message currently visible in the viewport as our position anchor.
        let anchor = try XCTUnwrap(visibleHistoryAnchor(), "expected an earlier Stream history message in view")
        let anchorBeforeY = anchor.frame.minY
        let targetUpdateNumber = max(try liveUpdates().map(\.number).max() ?? 0, 1) + 4

        // Wait until at least 4 newer committed updates have streamed in while staying scrolled up.
        let laterUpdate = text(beginningWith: "Live update #\(targetUpdateNumber):")
        XCTAssertTrue(laterUpdate.waitForExistence(timeout: 15), "Live update #\(targetUpdateNumber) should arrive in the background")
        usleep(400_000)

        // The user's reading position must not have been stolen by the streaming updates.
        XCTAssertTrue(toEnd.exists, "to-end should remain visible while scrolled up during streaming")
        XCTAssertTrue(anchor.exists, "anchored earlier message should still exist")
        XCTAssertEqual(anchor.frame.minY, anchorBeforeY, accuracy: 4,
                       "streaming updates stole scroll position (anchor moved from \(anchorBeforeY) to \(anchor.frame.minY))")
        XCTAssertFalse(isVisibleInViewport(laterUpdate), "new streaming message should stay below viewport while user is reading earlier history")

        // Tapping Back to the Bottom returns to the latest streamed messages.
        toEnd.tap()
        XCTAssertTrue(waitUntil(timeout: 8) { !toEnd.exists && self.hasVisibleLiveUpdate(atLeast: targetUpdateNumber) },
                      "tapping to-end should bring Live update #\(targetUpdateNumber)+ into view")
    }

    // MARK: Helpers

    private func openOverview() {
        tap("menu")
        tap("overview-place")
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 15), "Overview page")
    }

    private func openSessionFromOverview(_ title: String) {
        let row = app.descendants(matching: .any)["session-\(title)"].firstMatch
        for _ in 0..<6 where !row.exists || !row.isHittable { app.swipeUp() }
        for _ in 0..<6 where !row.exists || !row.isHittable { app.swipeDown() }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "overview row \(title)")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page for \(title)")
    }

    private func openSessionFromDrawer(_ title: String) {
        tap("menu")
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        XCTAssertTrue(drawer.waitForExistence(timeout: 10), "drawer")
        let row = drawer.descendants(matching: .any)["session-\(title)"].firstMatch
        for _ in 0..<5 where !row.exists || !row.isHittable {
            let start = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
            let end = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
            start.press(forDuration: 0.05, thenDragTo: end)
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "drawer row \(title)")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page for \(title)")
        XCTAssertTrue(app.descendants(matching: .any)["drawer-dim"].waitForNonExistence(timeout: 5), "drawer closed")
    }

    private func navigateBack() {
        app.buttons["BackButton"].tap()
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 10), "back to Overview")
    }

    private func scrollUpConversation(times: Int) {
        let win = app.windows.firstMatch
        for _ in 0..<times {
            let start = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
            let end = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
            start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0.25)
            usleep(200_000)
        }
    }

    private func visibleHistoryAnchor() -> XCUIElement? {
        let win = app.windows.firstMatch.frame
        let items = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Stream item #"))
        return items.allElementsBoundByIndex.first { el in
            el.exists && el.frame.height > 0 && el.frame.minY > win.minY + 110 && el.frame.maxY < win.maxY - 140
        }
    }

    // A single accessibility snapshot keeps the labels and frames from the same live update.
    private func liveUpdates() throws -> [(number: Int, frame: CGRect)] {
        let prefix = "Live update #"
        var nodes = [try app.snapshot()]
        var updates: [(Int, CGRect)] = []
        while let node = nodes.popLast() {
            nodes.append(contentsOf: node.children)
            if node.elementType == .staticText, node.label.hasPrefix(prefix),
               let number = Int(node.label.dropFirst(prefix.count).prefix { $0.isNumber }) {
                updates.append((number, node.frame))
            }
        }
        return updates
    }

    private func hasVisibleLiveUpdate(atLeast minimum: Int) -> Bool {
        guard let updates = try? liveUpdates() else { return false }
        let bounds = visibleVerticalBounds
        return updates.contains { $0.number >= minimum && $0.frame.height > 0 &&
            $0.frame.minY >= bounds.top && $0.frame.maxY <= bounds.bottom }
    }

    private func element(containing substring: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", substring)).firstMatch
    }

    private var visibleVerticalBounds: (top: CGFloat, bottom: CGFloat) {
        let win = app.windows.firstMatch.frame
        let bottomLimit = app.buttons["interrupt"].exists ? app.buttons["interrupt"].frame.minY : win.maxY - 20
        return (win.minY + 60, bottomLimit + 10)
    }

    private func isVisibleInViewport(_ element: XCUIElement) -> Bool {
        guard element.exists else { return false }
        let frame = element.frame, bounds = visibleVerticalBounds
        return frame.height > 0 && frame.minY >= bounds.top && frame.maxY <= bounds.bottom
    }

    private func text(beginningWith prefix: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
    }

    private func tap(_ identifier: String) {
        let item = app.descendants(matching: .any)[identifier].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 15), identifier)
        item.tap()
    }

    private func waitUntil(timeout: TimeInterval, _ check: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if check() { return true }
            usleep(200_000)
        }
        return false
    }
}
