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

    /// Unsent composer drafts stay isolated across New Session and each existing session, survive leaving for Overview,
    /// and clear only on the session that sends.
    func testComposerDraftMemoryAcrossNewSessionExistingSessionsAndOverview() {
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        let composerText = { (composer.value as? String) ?? "" }

        openSessionFromDrawer("Long history session")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        composer.tap()
        composer.typeText("long session draft")
        XCTAssertEqual(composerText(), "long session draft")

        openSessionFromDrawer("Delayed history session")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        XCTAssertNotEqual(composerText(), "long session draft", "Delayed session must not inherit Long session draft")
        composer.tap()
        composer.typeText("delayed session draft")
        XCTAssertEqual(composerText(), "delayed session draft")

        // New Session has its own draft, isolated from both existing sessions.
        tap("menu")
        tap("new-session-place")
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].waitForExistence(timeout: 10))
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        XCTAssertFalse(["long session draft", "delayed session draft"].contains(composerText()))
        composer.tap()
        composer.typeText("new session draft")
        XCTAssertEqual(composerText(), "new session draft")

        // Leave SessionPage completely for Overview, then come back to each target.
        openOverview()

        openSessionFromDrawer("Long history session")
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() == "long session draft" }, "Long session draft kept: \(composerText())")

        tap("menu")
        tap("new-session-place")
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() == "new session draft" }, "New session draft kept: \(composerText())")

        openSessionFromDrawer("Delayed history session")
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() == "delayed session draft" }, "Delayed session draft kept: \(composerText())")

        // Sending in Delayed clears only Delayed's draft; Long and New Session keep theirs.
        let sendBtn = app.buttons["thread-send"]
        XCTAssertTrue(waitUntil(timeout: 5) { sendBtn.isEnabled })
        sendBtn.tap()
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() != "delayed session draft" })

        openSessionFromDrawer("Long history session")
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() == "long session draft" }, "Long session draft untouched after Delayed sent")

        tap("menu")
        tap("new-session-place")
        XCTAssertTrue(waitUntil(timeout: 5) { composerText() == "new session draft" }, "New session draft untouched after Delayed sent")
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

    /// Sending a message keeps the conversation at the very bottom over the keyboard and composer,
    /// and sending while scrolled up brings the new message and reply back to the bottom.
    func testSendingMessageScrollsToVeryBottom() {
        openSessionFromOverview("Long history session")
        let longLatest = text(beginningWith: "Long latest #24")
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10), "session composer")
        XCTAssertTrue(waitUntil(timeout: 10) { longLatest.exists && self.isAboveComposer(longLatest) },
                      "long history should open at latest message #24 above composer")

        // 1. Focus composer so keyboard rises and box expands: latest message stays above the composer.
        composer.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "keyboard shown")
        XCTAssertTrue(waitUntil(timeout: 5) { self.isAboveComposer(longLatest) },
                      "latest message should stay above composer when keyboard opens (msg=\(longLatest.frame), composer=\(composer.frame))")
        XCTAssertFalse(app.buttons["to-end"].exists, "still at the bottom with keyboard up")

        // 2. Send a multi-line message from the bottom: keyboard dismisses and both sent prompt and reply land above the composer.
        composer.typeText("Bottom follow-up line 1\nLine 2\nLine 3")
        tap("thread-send")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "sending dismisses keyboard")
        XCTAssertFalse(app.buttons["thread-model"].exists, "composer folds back to single line after send")
        let sentAtBottom = text(beginningWith: "Bottom follow-up line 1")
        XCTAssertTrue(waitUntil(timeout: 8) {
            guard let reply = self.latestDemoFollowUpReply() else { return false }
            return sentAtBottom.exists && self.isAboveComposer(reply)
        }, "sent message and reply should auto-scroll to the very bottom above composer")
        XCTAssertFalse(app.buttons["to-end"].exists, "to-end hidden after sending at bottom")

        // 3. Scroll well up in the conversation, type in composer, and send: auto-scrolls all the way to the bottom.
        scrollUpConversation(times: 4)
        let toEnd = app.buttons["to-end"]
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "to-end appears after scrolling up")
        composer.tap()
        composer.typeText("Sent after scrolling up")
        XCTAssertTrue(toEnd.exists, "typing in composer while scrolled up does not steal scroll position")
        tap("thread-send")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "sending from scrolled-up position dismisses keyboard")
        let sentFromUp = text(beginningWith: "Sent after scrolling up")
        XCTAssertTrue(waitUntil(timeout: 8) {
            guard let reply = self.latestDemoFollowUpReply() else { return false }
            return !toEnd.exists && sentFromUp.exists && self.isAboveComposer(sentFromUp) && self.isAboveComposer(reply)
        }, "sending from scrolled-up position should scroll to the very bottom")
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
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        let bottomLimit: CGFloat = if app.buttons["interrupt"].exists {
            app.buttons["interrupt"].frame.minY
        } else if composer.exists {
            composer.frame.minY
        } else {
            win.maxY - 20
        }
        return (win.minY + 60, bottomLimit + 10)
    }

    private func isAboveComposer(_ element: XCUIElement) -> Bool {
        guard element.exists else { return false }
        let win = app.windows.firstMatch.frame
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        let bottomLimit = composer.exists ? composer.frame.minY : win.maxY - 20
        let frame = element.frame
        return frame.height > 0 && frame.minY >= win.minY + 60 && frame.maxY <= bottomLimit + 14
    }

    private func latestDemoFollowUpReply() -> XCUIElement? {
        let replies = app.staticTexts.matching(NSPredicate(format: "label == %@", "Simulated follow-up reply in Demo Mode."))
        return replies.allElementsBoundByIndex.last
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

final class LiveProgressUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = [
            "-AppleLanguages", "(en)",
            "-AppleLocale", "en_US",
            "-DemoLiveProgress",
        ]
        app.launch()
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        let menu = app.descendants(matching: .any)["menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 15), "menu")
        menu.tap()
        let overview = app.descendants(matching: .any)["overview-place"].firstMatch
        XCTAssertTrue(overview.waitForExistence(timeout: 15), "overview-place")
        overview.tap()
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 15), "Overview page")
    }

    func testFloatingChecklistFocusWakeLineAndAutoCollapse() {
        let row = app.descendants(matching: .any)["session-Live progress session"].firstMatch
        for _ in 0..<4 where !row.exists || !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "overview row Live progress session")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page")

        // 1. Inline B2 checklist spine flows at the foot of the conversation above the composer:
        let todoCard = app.descendants(matching: .any)["thread-todo-card"].firstMatch
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(todoCard.waitForExistence(timeout: 8), "inline checklist spine")
        XCTAssertTrue(composer.waitForExistence(timeout: 8), "composer")
        XCTAssertLessThanOrEqual(todoCard.frame.maxY, composer.frame.minY + 4, "checklist sits above composer")

        // All 5 steps render directly inline in order with no fold toggle, highlighting active step 3/5:
        XCTAssertFalse(app.buttons["thread-todo-toggle"].exists, "no fold toggle")
        XCTAssertTrue(app.descendants(matching: .any)["thread-todo-step-0"].exists, "completed step 0 visible")
        XCTAssertTrue(app.descendants(matching: .any)["thread-todo-step-4"].exists, "pending step 4 visible")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Rewrite CALayer halo")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Verify GPU usage")).firstMatch.exists)

        // 2. Long live focus phrase wraps up to two lines on iPhone without single-line truncation, and legacy Run update_steps never leaks:
        let working = app.descendants(matching: .any)["thread-working"].firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 5), "live working sub-focus")
        XCTAssertTrue(working.label.contains("Run HermeshKit tests and iOS Simulator UI tests"), "untruncated focus label: \(working.label)")
        XCTAssertFalse(app.staticTexts["Run update_steps"].exists, "Run update_steps must not leak into chat log")

        // 3. Background continuation wake line "<task> finished" is visible and tappable:
        let wakeLine = app.buttons["thread-wake-line"].firstMatch
        XCTAssertTrue(wakeLine.waitForExistence(timeout: 5), "wake line")
        XCTAssertTrue(wakeLine.label.contains("Benchmark CALayer sprite orbit finished"), "wake label: \(wakeLine.label)")

        // 4. Auto-collapses once all steps finish and the turn settles:
        let finalReply = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "GPU usage reduced to 2.7%.")).firstMatch
        XCTAssertTrue(finalReply.waitForExistence(timeout: 15), "final reply streamed")
        XCTAssertTrue(todoCard.waitForNonExistence(timeout: 6), "inline checklist auto-collapsed")

        // Tapping the wake line opens Background Tasks:
        wakeLine.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Benchmark CALayer sprite orbit")).firstMatch.waitForExistence(timeout: 8))
    }
}

final class QuestionFlowUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = [
            "-AppleLanguages", "(en)",
            "-AppleLocale", "en_US",
            "-DemoQuestionFlow",
        ]
        app.launch()
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        let menu = app.descendants(matching: .any)["menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 15), "menu")
        menu.tap()
        let sessionRow = app.descendants(matching: .any)["session-Question flow session"].firstMatch
        XCTAssertTrue(sessionRow.waitForExistence(timeout: 15), "drawer row Question flow session")
        sessionRow.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page")
    }

    func testInlineQuestionScrollsWithContentAndSettlesCleanly() {
        let panel = app.descendants(matching: .any)["question"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10), "inline question panel")

        // Redundant "Run ask_question" tool call wrapper must not show.
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "ask_question")).firstMatch.exists)

        // All 3 options render in full inside the conversation scroll stream.
        let opt1 = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Keep sage green & muted purple")).firstMatch
        let opt3 = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Merge pushed and merged states")).firstMatch
        XCTAssertTrue(opt1.exists && opt3.exists, "all options exist without nested clipping")

        // Scrolling up moves the question panel down with the content so earlier messages become readable,
        // and surfaces the "Back to question" pill ("to-end").
        let conv = app.scrollViews.firstMatch
        for _ in 0..<4 {
            let start = conv.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.30))
            let finish = conv.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.82))
            start.press(forDuration: 0.02, thenDragTo: finish)
        }
        let backToQuestion = app.buttons["to-end"]
        XCTAssertTrue(backToQuestion.waitForExistence(timeout: 5), "Back to question pill appears when scrolled up")
        XCTAssertTrue(backToQuestion.label.contains("Back to question"), "pill label: \(backToQuestion.label)")

        // Tapping Back to question smoothly returns to the question panel at the bottom.
        backToQuestion.tap()
        XCTAssertTrue(backToQuestion.waitForNonExistence(timeout: 5), "pill hides once back at question")
        XCTAssertTrue(opt1.isHittable, "first option hittable again at bottom")

        // Tapping custom answer field focuses cleanly (without DismissKeyboardTapGesture stealing focus) and hides bottom composer;
        // tapping outside dismisses keyboard, and choosing an option overrides custom text and settles into Style A card.
        let customField = app.textFields["answer-text"]
        XCTAssertTrue(customField.exists && customField.isHittable, "custom answer field hittable")
        customField.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "keyboard opens on custom answer field")
        XCTAssertFalse(app.textFields["thread-composer"].exists, "bottom composer hides while typing custom question answer")
        customField.typeText("Custom draft")

        let answerBtn = app.buttons["answer"]
        XCTAssertTrue(answerBtn.wait(for: \.isEnabled, toEqual: true, timeout: 5), "custom draft enables answer")

        // Tapping visible option 3 while keyboard is open selects it, clears custom text, dismisses keyboard, and restores composer;
        // then switching to Recommended option 1 and answering settles into Style A card.
        opt3.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "picking option dismisses keyboard")
        XCTAssertTrue(app.textFields["thread-composer"].waitForExistence(timeout: 5), "bottom composer returns when keyboard closes")

        opt1.tap()
        XCTAssertTrue(answerBtn.wait(for: \.isEnabled, toEqual: true, timeout: 5))
        answerBtn.tap()

        let settled = app.descendants(matching: .any)["settled-question"].firstMatch
        XCTAssertTrue(settled.waitForExistence(timeout: 10), "settled Style A card")
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "pending panel gone")
        XCTAssertTrue(app.textFields["thread-composer"].waitForExistence(timeout: 5), "bottom composer returns once settled")
        let rawLeak = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "(Recommended)")).firstMatch
        XCTAssertFalse(rawLeak.exists, "raw (Recommended) prefix must not leak into settled card")
        let cleanAnswer = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Keep sage green & muted purple")).firstMatch
        XCTAssertTrue(cleanAnswer.exists, "settled card shows chosen answer with inline Recommended badge")
    }
}
