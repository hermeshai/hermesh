import XCTest

/// A turn of hundreds of tool calls in a row (the demo's -DemoManyTools, DemoService.swift): its steps open into a card
/// that stops growing and scrolls inside, lays out only the rows near what is in sight, and follows the newest step
/// while the turn runs (ToolStepsUITests, which CI runs); what opening and scrolling them cost (ToolStepsPerfUITests,
/// run by hand: its measures print as "PERF …" lines).
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh \
///         -destination 'platform=iOS Simulator,name=…' -only-testing:HermeshUITests/ToolStepsUITests
class ToolStepsCase: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoManyTools"]
        app.launch()
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        tap("menu")
        tap("overview-place")
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 15), "Overview page")
    }

    func open(_ title: String) {
        let row = app.descendants(matching: .any)["session-\(title)"].firstMatch
        for _ in 0..<6 where !row.exists || !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "overview row \(title)")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page for \(title)")
    }

    func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    func tap(_ id: String) {
        let e = element(id)
        XCTAssertTrue(e.waitForExistence(timeout: 15), id)
        e.tap()
    }
}

final class ToolStepsUITests: ToolStepsCase {
    func testHundredsOfStepsOpenInACardThatScrolls() throws {
        open("Many steps")
        let summary = element("tool-steps-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 10), "the steps, folded")
        XCTAssertTrue(summary.label.hasPrefix("Read "), summary.label)
        let card = element("tool-steps")

        summary.tap()
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        // It stops growing at seven and a half rows, and lays out only the rows near what is in sight.
        XCTAssertLessThanOrEqual(card.frame.height, 240, "capped")
        XCTAssertGreaterThan(card.frame.height, 190, "not shrunk")
        let rowHeight = try XCTUnwrap(card.buttons.allElementsBoundByIndex.map(\.frame.height).filter { $0 > 20 }.min(), "a row")
        XCTAssertEqual(card.frame.height / rowHeight, 7.5, accuracy: 0.5, "seven and a half rows: \(card.frame.height) / \(rowHeight)")
        let drawn = card.buttons.count
        XCTAssertLessThan(drawn, 80, "rows laid out: \(drawn) of 600")
        XCTAssertGreaterThan(drawn, 6, "the rows in sight")
        // It opens at the newest step: the last command's row (call 597; the two after it are an edit and a search) is in the card.
        let last = card.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Test597 ")).firstMatch
        XCTAssertTrue(last.exists && card.frame.contains(CGPoint(x: last.frame.midX, y: last.frame.midY)), "at the newest step")
        // A finished turn: no arrow back.
        XCTAssertFalse(element("steps-to-end").exists)

        // A step opened in the card shows its output there, in view.
        XCTAssertTrue(card.waitForExistence(timeout: 5))
        let step = card.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Ran")).allElementsBoundByIndex.last { card.frame.contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY)) }
        let ran = try XCTUnwrap(step, "a command in sight")
        ran.tap()
        let output = card.descendants(matching: .any)["tool-output"].firstMatch
        XCTAssertTrue(output.waitForExistence(timeout: 5), "its output")
        sleep(1)
        XCTAssertLessThanOrEqual(output.frame.height, 162, "an output a size down in the card")
        XCTAssertTrue(card.frame.intersects(output.frame), "in view in the card")
    }

    func testARunningTurnsStepsFollowTheNewest() throws {
        open("Many steps, running")
        let summary = element("tool-steps-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 10))
        summary.tap()
        let card = element("tool-steps")
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        XCTAssertLessThanOrEqual(card.frame.height, 240, "capped")
        // The whole card in sight, clear of the box (the page is longer than the screen).
        let page = app.coordinate(withNormalizedOffset: CGVector(dx: 0.15, dy: 0.4))
        page.press(forDuration: 0.05, thenDragTo: page.withOffset(CGVector(dx: 0, dy: -300)), withVelocity: .fast, thenHoldForDuration: 0.1)
        sleep(1)
        // Steps come every second; the card stays at the newest, with no arrow back.
        sleep(3)
        XCTAssertFalse(element("steps-to-end").exists, "following")
        XCTAssertTrue(newestInSight(card), "the newest step in sight")

        // Scrolled up, it stays where the owner left it as steps come, and offers the way back.
        card.swipeDown(velocity: .slow)
        let back = element("steps-to-end")
        XCTAssertTrue(back.waitForExistence(timeout: 5), "the arrow back")
        sleep(3)
        XCTAssertFalse(newestInSight(card), "not dragged to the newest")
        back.tap()
        XCTAssertTrue(back.waitForNonExistence(timeout: 5), "back at the newest")
        sleep(3)
        XCTAssertFalse(element("steps-to-end").exists, "following again")
        XCTAssertTrue(newestInSight(card), "the newest step in sight again")
    }

    /// A card scrolled to its top lets go: a drag down that starts on it scrolls the page, not the card's bounce.
    func testADragPastACardsTopScrollsThePage() throws {
        open(smallTitle)
        // The eleventh turn's 26 steps (from call 500: 7 reads, 7 commands, 6 edits, 6 searches).
        let summary = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'tool-steps-summary' AND label BEGINSWITH 'Read 7 files'")).firstMatch
        for _ in 0..<6 where !summary.exists || !summary.isHittable { app.swipeDown() }
        XCTAssertTrue(summary.isHittable, "the big group")
        summary.tap()
        let card = element("tool-steps")
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        XCTAssertGreaterThan(card.frame.height, 200, "a card that scrolls: \(card.frame.height)")
        sleep(1)
        func drag(_ dy: CGFloat) {
            let from = card.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: dy > 0 ? 0.1 : 0.9))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: dy)), withVelocity: .slow, thenHoldForDuration: 0.3)
            sleep(1)
        }
        // It opens at its newest step; dragged down, it scrolls to its first, and the page stays.
        let at = card.frame.minY
        for _ in 0..<4 { drag(200) }
        XCTAssertEqual(card.frame.minY, at, accuracy: 2, "the card scrolled, not the page")
        let first = card.buttons.matching(NSPredicate(format: "label CONTAINS 'pkg20/file50.go'")).firstMatch
        XCTAssertTrue(first.exists && card.frame.contains(CGPoint(x: first.frame.midX, y: first.frame.midY)), "at its first step")
        // There, a drag down on it scrolls the page.
        let before = card.frame.minY
        drag(200)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "past-the-card's-top"; shot.lifetime = .keepAlways; add(shot)
        XCTAssertGreaterThan(card.frame.minY, before + 100, "the page scrolled with the drag: from \(before) to \(card.frame.minY)")
    }

    /// While steps come into an open card, a drag on the page outside it scrolls the page, every time.
    func testARunningTurnsPageScrollsAroundItsCard() throws {
        open("Many steps, running")
        let summary = element("tool-steps-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 10))
        summary.tap()
        let card = element("tool-steps")
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        sleep(2)
        for n in 1 ... 3 {
            // Above the card, on the turn's text (on the left: the arrow back to the bottom is in the middle).
            let before = card.frame.minY
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.15, dy: 0)).withOffset(CGVector(dx: 0, dy: max(140, before - 260)))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 100)), withVelocity: .slow, thenHoldForDuration: 0.3)
            sleep(1)
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "page-dragged-\(n)"; shot.lifetime = .keepAlways; add(shot)
            XCTAssertGreaterThan(card.frame.minY, before + 50, "drag \(n): the page scrolled, from \(before) to \(card.frame.minY)")
        }
    }

    private let smallTitle = "Small steps: keep every log call structured across the whole repository and its tests"

    /// A small group's card has nothing to scroll: a drag that starts on it scrolls the page, as one anywhere else does.
    func testADragOnASmallCardScrollsThePage() throws {
        open(smallTitle)
        let summaries = app.descendants(matching: .any).matching(identifier: "tool-steps-summary")
        XCTAssertTrue(summaries.firstMatch.waitForExistence(timeout: 10), "the steps, folded")
        let summary = try XCTUnwrap(summaries.allElementsBoundByIndex.last { $0.isHittable }, "a group in sight")
        summary.tap()
        let card = element("tool-steps")
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        XCTAssertLessThan(card.frame.height, 200, "a small card: \(card.frame.height)")
        sleep(1)
        let before = summary.frame.minY
        let from = card.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 220)), withVelocity: .slow, thenHoldForDuration: 0.3)
        sleep(1)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "small-card-dragged"; shot.lifetime = .keepAlways; add(shot)
        XCTAssertGreaterThan(summary.frame.minY, before + 120, "the page scrolled with the drag: from \(before) to \(summary.frame.minY)")
    }

    /// A long title keeps to its capsule, between the buttons beside it, with where it runs under it.
    func testALongTitleKeepsToItsCapsule() throws {
        open(smallTitle)
        let capsule = app.buttons["session-info"]
        let more = app.buttons["session-more"]
        XCTAssertTrue(more.waitForExistence(timeout: 10))
        let others = app.navigationBars.buttons.allElementsBoundByIndex.filter { $0.identifier != "session-info" && $0.identifier != "session-more" }
        let back = try XCTUnwrap(others.min { $0.frame.minX < $1.frame.minX }, "the button back")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "long-title"; shot.lifetime = .keepAlways; add(shot)
        XCTAssertTrue(capsule.label.contains("Mac mini/hello-world"), capsule.label)
        XCTAssertGreaterThanOrEqual(capsule.frame.minX, back.frame.maxX, "clear of the button back: \(capsule.frame) \(back.frame)")
        XCTAssertLessThanOrEqual(capsule.frame.maxX, more.frame.minX, "clear of ⋯: \(capsule.frame) \(more.frame)")
        XCTAssertGreaterThan(capsule.frame.width, 120, "still a capsule to tap")
        // One width whatever the title, the long one ending in …; Session Info says it whole.
        XCTAssertEqual(capsule.frame.width, 220, accuracy: 1, "the capsule's width: \(capsule.frame)")
        capsule.tap()
        let whole = element("session-info-title")
        XCTAssertTrue(whole.waitForExistence(timeout: 10), "the whole title in Session Info")
        XCTAssertEqual(whole.label, smallTitle)
    }

    /// Whether the step under way (its verb, "…ing") is in the card's sight.
    private func newestInSight(_ card: XCUIElement) -> Bool {
        let doing = card.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Reading' OR label BEGINSWITH 'Searching' OR label BEGINSWITH 'Editing' OR label BEGINSWITH 'Running'"))
        return doing.allElementsBoundByIndex.contains { card.frame.contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY)) }
    }
}

final class ToolStepsPerfUITests: ToolStepsCase {
    // What opening and scrolling 600 steps cost, as the app's frame meter counts it (FrameMeter.swift, this fixture
    // only): frames drawn, the late ones, the longest gap. XCTest's hitch metrics need a device; these work in the
    // Simulator. They assert nothing of the card, so they measure the page before the card was capped too.

    /// Opening the steps: the longest frame from the tap to them shown, five times (folded again between).
    func testOpeningHundredsOfSteps() throws {
        let (summary, rows) = manySteps()
        let page = app.windows.firstMatch
        var longest: [Int] = []
        for _ in 0..<5 {
            sleep(4) // past the meter's three seconds: the longest frame is the opening's
            summary.tap()
            XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 30), "the steps")
            longest.append(meter().longest3s)
            for _ in 0..<20 where !summary.isHittable { page.swipeDown() }
            summary.tap()
            XCTAssertTrue(rows.firstMatch.waitForNonExistence(timeout: 30), "folded")
        }
        report("open.longestFrame.ms", longest)
    }

    /// Scrolling the page with the steps open, fast both ways, from above them.
    func testScrollingThePageOverHundredsOfSteps() throws {
        let (summary, rows) = manySteps()
        summary.tap()
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 30), "the steps")
        let page = app.windows.firstMatch
        let top = page.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2))
        let bottom = page.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85))
        scrolling("page") {
            top.press(forDuration: 0.01, thenDragTo: bottom, withVelocity: .fast, thenHoldForDuration: 0)
            bottom.press(forDuration: 0.01, thenDragTo: top, withVelocity: .fast, thenHoldForDuration: 0)
        }
    }

    /// Scrolling inside the card, where it scrolls on its own, fast both ways.
    func testScrollingInsideHundredsOfSteps() throws {
        let (summary, rows) = manySteps()
        summary.tap()
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 30), "the steps")
        let card = element("tool-steps")
        try XCTSkipUnless(card.exists, "no card that scrolls on its own")
        scrolling("card") {
            card.swipeDown(velocity: .fast)
            card.swipeUp(velocity: .fast)
        }
    }

    /// Five rounds of `scroll`: the frames per second, the late frames and the longest gap of each, from the meter's
    /// counts before and after it.
    private func scrolling(_ name: String, _ scroll: () -> Void) {
        var late: [Int] = [], fps: [Int] = [], longest: [Int] = []
        for _ in 0..<5 {
            sleep(4)
            let a = meter()
            scroll()
            let b = meter()
            late.append(b.late - a.late)
            fps.append(Int((Double(b.frames - a.frames) * 1000 / Double(max(b.ms - a.ms, 1))).rounded()))
            longest.append(b.longest3s)
        }
        report("scroll.\(name).fps", fps)
        report("scroll.\(name).lateFrames", late)
        report("scroll.\(name).longestFrame.ms", longest)
    }

    /// What the meter has counted (since launch) and the longest frame of the last three seconds.
    private func meter() -> (frames: Int, late: Int, ms: Int, longest3s: Int) {
        let m = element("frame-meter")
        XCTAssertTrue(m.waitForExistence(timeout: 5), "the frame meter (-DemoManyTools)")
        usleep(150_000) // its label follows a tenth of a second behind
        let words = m.label.split(separator: " ")
        func n(_ key: String) -> Int { words.firstIndex(of: Substring(key)).flatMap { Int(words[$0 + 1]) } ?? -1 }
        return (n("frames"), n("late"), n("ms"), n("longest3s"))
    }

    /// One line in the log per measure: its median and every value, for comparing runs.
    private func report(_ name: String, _ values: [Int]) {
        let median = values.sorted()[values.count / 2]
        print("PERF \(name) median \(median) values \(values)")
        XCTContext.runActivity(named: "\(name): median \(median), values \(values)") { _ in }
    }

    /// The many-steps session open, its steps folded: their line, and their command rows (the line starts with "Read").
    private func manySteps() -> (XCUIElement, XCUIElementQuery) {
        open("Many steps")
        let summary = element("tool-steps-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 10), "the steps, folded")
        return (summary, app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Ran'")))
    }
}
