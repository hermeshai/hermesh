import XCTest

/// Her's conversation under the finger, on a long one the demo keeps for a debug build (-HerDemo, DemoHer.swift): no
/// machine, her or core is needed, so this runs by itself.
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh -destination 'platform=iOS Simulator,name=…' \
///         -only-testing:HermeshUITests/HerScrollUITests
final class HerScrollUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        if name.contains("testLeaveAndReenterHerConversationIsNeverBlank") {
            return
        }
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-HerDemo", "40"]
        app.launch()
        tap("try-demo")
        tap("menu")
        tap("her-place")
        XCTAssertTrue(composer.waitForExistence(timeout: 20), "Her's conversation")
        XCTAssertTrue(waitUntil { self.lastMessage.isHittable }, "opened at its end")
    }

    func testKeyboardKeepsHeaderStill() {
        let avatar = app.buttons["her-avatar"], tag = app.buttons["her-tag"]
        let avatarFrame = avatar.frame, tagFrame = tag.frame
        XCTAssertGreaterThan(avatarFrame.height, 60)
        for _ in 0..<3 {
            composer.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
            XCTAssertEqual(avatar.frame.height, avatarFrame.height, accuracy: 1)
            XCTAssertEqual(avatar.frame.midY, avatarFrame.midY, accuracy: 1)
            XCTAssertEqual(tag.frame.midY, tagFrame.midY, accuracy: 1)
            XCTAssertLessThanOrEqual(composer.frame.maxY, app.keyboards.firstMatch.frame.minY + 1)
            tap("her-tag")
            XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5))
            tap("her-avatar")
            XCTAssertEqual(avatar.frame.height, avatarFrame.height, accuracy: 1)
            XCTAssertEqual(tag.frame.midY, tagFrame.midY, accuracy: 1)
        }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Her: stable header after typing"; shot.lifetime = .keepAlways; add(shot)
    }

    /// A quick flick moves the conversation, begun on a message as anywhere. (The swipe that replies used to take a
    /// flick fast enough to pass both its own distance and the scroll's in one touch, and the page stayed still.)
    func testQuickFlicksScroll() {
        // Down the screen from its end (to what was said earlier), twice, then back up: each as far as a flick goes.
        for (from, by) in [(0.35, 0.2), (0.3, 0.3), (0.6, -0.3)] as [(CGFloat, CGFloat)] {
            let before = lastMessage.frame.minY
            let start = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: from))
            start.press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: from + by)), withVelocity: .fast, thenHoldForDuration: 0)
            XCTAssertTrue(waitUntil(timeout: 5) { abs(self.lastMessage.frame.minY - before) > 150 }, "a flick from \(from) by \(by) moved it \(lastMessage.frame.minY - before)")
            XCTAssertEqual(lastMessage.frame.minY > before, by > 0, "the way the finger went")
            sleep(2) // comes to rest
        }
    }

    /// A message swiped left is replied to; one dragged up or down, or to the right, is not.
    func testSwipeLeftReplies() throws {
        let bar = app.descendants(matching: .any)["her-reply-bar"].firstMatch
        let replies = app.descendants(matching: .any).matching(identifier: "her-reply")
        let words = try XCTUnwrap(replies.allElementsBoundByIndex.last { $0.isHittable }, "words of Her's in view")
        let from = words.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.5))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 120)))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 150, dy: 0)))
        sleep(1)
        XCTAssertFalse(bar.exists, "dragged down, and to the right: no reply")
        if app.buttons["to-end"].exists { tap("to-end") }
        XCTAssertTrue(waitUntil { words.isHittable })
        let again = words.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.5))
        again.press(forDuration: 0.05, thenDragTo: again.withOffset(CGVector(dx: -170, dy: 0)))
        XCTAssertTrue(bar.waitForExistence(timeout: 5), "swiped left: replying")
    }

    /// The end is kept in view as the keyboard comes up, and a message sent from up the conversation shows at its end.
    func testTheEndIsFollowed() {
        composer.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(waitUntil { self.lastMessage.isHittable && self.lastMessage.frame.maxY < self.composer.frame.minY }, "the last message, over the keyboard")
        XCTAssertFalse(app.buttons["to-end"].exists, "still at the end")
        // Well up the conversation (the keyboard goes as it is dragged), then a message.
        for _ in 0 ..< 3 { app.swipeDown() }
        XCTAssertTrue(app.buttons["to-end"].waitForExistence(timeout: 5), "scrolled up")
        composer.tap()
        composer.typeText("from up here")
        XCTAssertTrue(app.buttons["to-end"].exists, "writing moves nothing")
        tap("her-send")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "sent: the keyboard goes")
        let sent = app.staticTexts["from up here"].firstMatch
        XCTAssertTrue(waitUntil { sent.exists && sent.isHittable && sent.frame.maxY < self.composer.frame.minY }, "the message, at the end")
        XCTAssertTrue(app.buttons["to-end"].waitForNonExistence(timeout: 5), "at the end")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Her: sent from up the conversation"; shot.lifetime = .keepAlways; add(shot)
    }

    /// A conversation with nothing said yet: Her's greeting across the page, under her mark, in Chinese as in lines of
    /// a few characters. (With no rows, the conversation was as wide as its margins, 40 points, and so was what shows
    /// over it.)
    func testGreetingTakesThePage() {
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-HerDemo", "0"]
        app.launch()
        tap("try-demo")
        tap("menu")
        tap("her-place")
        let greeting = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "你好，我是 Her")).firstMatch
        XCTAssertTrue(greeting.waitForExistence(timeout: 20), "Her's greeting")
        let page = window.frame.width
        XCTAssertGreaterThan(greeting.frame.width, page / 2, "across the page (\(greeting.frame.width) of \(page))")
        XCTAssertLessThan(greeting.frame.height, 120, "a few lines (\(greeting.frame.height))")
        XCTAssertEqual(greeting.frame.midX, page / 2, accuracy: 2, "in the middle")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Her: a new conversation"; shot.lifetime = .keepAlways; add(shot)
        if let dir = ProcessInfo.processInfo.environment["HER_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("greeting-\(Int(page)).png"))
        }
    }

    /// Leaving Her's conversation page (to Overview, another session, or a pushed page) and coming back must render
    /// the latest conversation messages immediately without an empty/blank viewport requiring a manual swipe.
    func testLeaveAndReenterHerConversationIsNeverBlank() {
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoHerReentryRepro"]
        app.launch()
        tap("try-demo")
        let lastRepro = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Her Reentry Landmark #50")).firstMatch
        XCTAssertTrue(waitUntil(timeout: 15) { lastRepro.exists && self.isVisibleInConversation(lastRepro) },
                      "initial Her open at bottom")
        sleep(1) // let initial nearTop `earlier()` load tall earlier turns (1...50, ~18,000 pt)
        XCTAssertTrue(hasConversationPixels(), "initial Her screenshot has rendered bubble pixels")

        // Phase A: Root switches (Her -> Coding Session -> Her, and Her -> Overview -> Her) when `bots/open` returns
        // `incremental: true, events: []` (0 row mutations after Frame-0 mount during DuoTabController slot switch!).
        // CRITICAL: Capture `XCUIScreen.main.screenshot()` BEFORE any XCUIApplication accessibility query (`exists` /
        // `waitForExistence`), because UIAccessibility tree walking forces UIHostingScrollView to materialize LazyVStack!
        for round in 1 ... 3 {
            tap("menu")
            tap(round == 2 ? "overview-place" : "session-Add a welcome page")
            sleep(round == 1 ? 0 : 2) // round 1: fast switch (<1s, convUp stays true); round 2-3: >1s letGo reconnect
            tap("menu")
            tap("her-place")
            sleep(1) // wait for 0.32s drawer close spring + 140ms bots/open WITHOUT touching app AX tree!
            let rawShot = XCUIScreen.main.screenshot().image
            let pixelsOK = hasConversationPixels(in: rawShot)
            if !pixelsOK {
                app.swipeUp()
                usleep(600_000)
                let afterPixels = hasConversationPixels(in: XCUIScreen.main.screenshot().image)
                XCTFail("Blank Her conversation (raw framebuffer before AX query) on root re-entry round \(round): pixels=\(pixelsOK)->\(afterPixels)")
                return
            }
            XCTAssertTrue(composer.waitForExistence(timeout: 5))
            XCTAssertTrue(isVisibleInConversation(lastRepro))
        }

        // Phase B: Push HerSettings onto NavigationStack (`HerPage.view.window == nil` while `convTask` stays open)
        // and pop back; then scroll up into mid-history (`atEnd == false`), tap `HerSessionCard` (#46 / #32) to push
        // `SessionPage` (`HerChat.letGo` tears down `convTask` after 1s!), and pop back when `bots/open` returns a
        // shorter reloaded snapshot (42...50)!
        tap("her-avatar")
        XCTAssertTrue(app.buttons["BackButton"].waitForExistence(timeout: 10), "pushed HerSettings over Her")
        tap("BackButton")
        sleep(1) // check raw framebuffer BEFORE AX tree walk!
        XCTAssertTrue(hasConversationPixels(in: XCUIScreen.main.screenshot().image), "Her pixels immediately after HerSettings pop")
        XCTAssertTrue(isVisibleInConversation(lastRepro), "still anchored at bottom after HerSettings pop")

        let midCard = app.descendants(matching: .any)["her-session-Add a welcome page"].firstMatch
        for _ in 0 ..< 4 where !(midCard.exists && isVisibleInConversation(midCard) && app.buttons["to-end"].exists) {
            let start = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.30))
            start.press(forDuration: 0.04, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.80)), withVelocity: .fast, thenHoldForDuration: 0)
            usleep(650_000)
        }
        XCTAssertTrue(app.buttons["to-end"].exists, "scrolled away from bottom (atEnd == false)")
        XCTAssertTrue(midCard.exists, "session card exists")
        midCard.tap()
        XCTAssertTrue(app.buttons["BackButton"].waitForExistence(timeout: 10), "pushed coding SessionPage over Her")
        sleep(2) // `SessionPage` holds NO `herConnections`, so `HerChat.letGo` tears down `convTask`!
        tap("BackButton")
        sleep(1) // wait for pop + 140ms `bots/open` (non-incremental 42...50 snapshot) BEFORE AX tree walk!
        let popRawShot = XCUIScreen.main.screenshot().image
        let popPixels = hasConversationPixels(in: popRawShot)
        if !popPixels {
            app.swipeUp()
            usleep(600_000)
            let afterPixels = hasConversationPixels(in: XCUIScreen.main.screenshot().image)
            XCTFail("Blank Her conversation (raw framebuffer) after scrolled-up session push -> pop! pixels=\(popPixels)->\(afterPixels)")
            return
        }
        XCTAssertTrue(composer.waitForExistence(timeout: 10), "popped back to HerConversation")
        XCTAssertTrue(hasAnyVisibleHerBubble(), "at least one Her bubble visible in viewport after pop")

        // Phase C: Rich Markdown/Mermaid/Image 80-turn Her (`-DemoPerfSuite`) + iPhone Duo (`-HermeshTestDuoTabs`)
        // switches across vertical UITabBar and `.cover` (`standUp`/`standDown`) sidebar!
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoPerfSuite", "-HermeshTestDuoTabs"]
        app.launch()
        tap("try-demo")
        let lastPerf = app.staticTexts["Her Turn #80 End"].firstMatch
        XCTAssertTrue(waitUntil(timeout: 15) { lastPerf.exists && self.isVisibleInConversation(lastPerf) }, "initial Duo Perf Her open")
        XCTAssertTrue(hasConversationPixels(), "initial Duo Perf Her pixels")

        let tabBar = app.tabBars.firstMatch
        for cycle in 1 ... 4 {
            if cycle % 2 == 1 {
                // Switch via Duo vertical UITabBar: Her -> Sessions -> Coding Session -> Her
                tabBar.buttons["sessions-place"].tap()
                XCTAssertTrue(app.descendants(matching: .any)["sessions-hub"].firstMatch.waitForExistence(timeout: 10))
                app.buttons["session-Mega 80-Turn Architecture"].firstMatch.tap()
                XCTAssertTrue(app.descendants(matching: .any)["thread-composer"].firstMatch.waitForExistence(timeout: 10))
                if cycle == 3 { sleep(2) }
                tabBar.buttons["her-place"].tap()
            } else {
                // Switch via Duo `.cover` sidebar (`standUp` hides `page.view`) -> Overview / Her
                tap("menu")
                XCTAssertTrue(app.buttons["menu-close"].waitForExistence(timeout: 5))
                app.buttons["session-Mega 80-Turn Architecture"].firstMatch.tap()
                XCTAssertTrue(app.descendants(matching: .any)["thread-composer"].firstMatch.waitForExistence(timeout: 10))
                if cycle == 4 { sleep(2) }
                tabBar.buttons["her-place"].tap()
            }
            sleep(1) // raw framebuffer check BEFORE any AX query!
            let pOK = hasConversationPixels(in: XCUIScreen.main.screenshot().image)
            if !pOK {
                app.swipeUp()
                usleep(600_000)
                let pAfter = hasConversationPixels(in: XCUIScreen.main.screenshot().image)
                XCTFail("Blank Duo Her conversation (raw framebuffer) on cycle \(cycle): pixels=\(pOK)->\(pAfter)")
                return
            }
            XCTAssertTrue(composer.waitForExistence(timeout: 10), "Duo re-entered Her cycle \(cycle)")
            XCTAssertTrue(lastPerf.exists && isVisibleInConversation(lastPerf))
        }
    }

    private func hasAnyVisibleHerBubble() -> Bool {
        let replies = app.descendants(matching: .any).matching(identifier: "her-reply").allElementsBoundByIndex
        return replies.contains { isVisibleInConversation($0) }
    }

    /// Samples rendered pixels in the middle conversation viewport (strictly below Her's header/tag and above the
    /// `to-end` button/composer) to catch CoreAnimation/LazyVStack/mask blanking even if accessibility frames exist.
    private func hasConversationPixels(in image: UIImage? = nil) -> Bool {
        guard let cg = (image ?? XCUIScreen.main.screenshot().image).cgImage else { return false }
        let w = cg.width, h = cg.height
        guard let data = cg.dataProvider?.data, let ptr = CFDataGetBytePtr(data) else { return false }
        let bpr = cg.bytesPerRow, bpp = max(4, cg.bitsPerPixel / 8)
        let x0 = Int(Double(w) * 0.14), x1 = Int(Double(w) * 0.86)
        let y0 = Int(Double(h) * 0.28), y1 = Int(Double(h) * 0.72)
        // Reference background pixel near left margin below header
        let refOff = y0 * bpr + Int(Double(w) * 0.03) * bpp
        let r0 = Int(ptr[refOff]), g0 = Int(ptr[refOff + 1]), b0 = Int(ptr[refOff + 2])
        var nonBg = 0, total = 0
        for y in stride(from: y0, to: y1, by: 4) {
            for x in stride(from: x0, to: x1, by: 4) {
                let off = y * bpr + x * bpp
                let dr = abs(Int(ptr[off]) - r0), dg = abs(Int(ptr[off + 1]) - g0), db = abs(Int(ptr[off + 2]) - b0)
                total += 1
                if dr + dg + db > 18 { nonBg += 1 }
            }
        }
        return total > 0 && Double(nonBg) / Double(total) > 0.015
    }

    private func isVisibleInConversation(_ el: XCUIElement) -> Bool {
        guard el.exists else { return false }
        let f = el.frame, w = window.frame
        return f.height > 4 && f.maxY > w.minY + 90 && f.minY < composer.frame.minY + 8
    }

    private var window: XCUIElement { app.windows.firstMatch }
    private var composer: XCUIElement { app.descendants(matching: .any)["her-composer"].firstMatch }
    /// The owner's last message of the demo's forty.
    private var lastMessage: XCUIElement { app.staticTexts.matching(NSPredicate(format: "label ENDSWITH %@", " #40")).firstMatch }

    private func waitUntil(timeout: TimeInterval = 10, _ check: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if check() { return true }
            usleep(300_000)
        }
        return false
    }

    private func openDrawerRow(_ identifier: String, scrollUp: Bool = false) {
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        XCTAssertTrue(drawer.waitForExistence(timeout: 10), "drawer")
        let item = drawer.descendants(matching: .any).matching(identifier: identifier).firstMatch
        for _ in 0 ..< 5 where !item.exists || !item.isHittable {
            if scrollUp { drawer.swipeDown() } else { drawer.swipeUp() }
        }
        XCTAssertTrue(item.waitForExistence(timeout: 10), identifier)
        item.tap()
    }

    private func tap(_ identifier: String) {
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 20), identifier)
        item.tap()
    }
}
