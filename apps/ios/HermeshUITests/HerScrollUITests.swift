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

    private func tap(_ identifier: String) {
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 20), identifier)
        item.tap()
    }
}
