import XCTest

/// A turn of hundreds of tool calls in a row (the demo's -DemoManyTools, DemoService.swift): its steps open into a card
/// that stops growing and scrolls inside, lays out only the rows near what is in sight, follows the newest step while
/// the turn runs, and scrolls without hitches. The measures print as the test runs (XCTest's own report).
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh \
///         -destination 'platform=iOS Simulator,name=…' -only-testing:HermeshUITests/ToolStepsUITests
final class ToolStepsUITests: XCTestCase {
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

    func testARunningTurnsStepsFollowTheNewest() throws {
        open("Many steps, running")
        let summary = element("tool-steps-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 10))
        summary.tap()
        let card = element("tool-steps")
        XCTAssertTrue(card.waitForExistence(timeout: 5), "the card")
        XCTAssertLessThanOrEqual(card.frame.height, 240, "capped")
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
    }

    // MARK: Helpers

    /// Whether the step under way (its verb, "…ing") is in the card's sight.
    private func newestInSight(_ card: XCUIElement) -> Bool {
        let doing = card.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Reading' OR label BEGINSWITH 'Searching' OR label BEGINSWITH 'Editing' OR label BEGINSWITH 'Running'"))
        return doing.allElementsBoundByIndex.contains { card.frame.contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY)) }
    }

    private func open(_ title: String) {
        let row = app.descendants(matching: .any)["session-\(title)"].firstMatch
        for _ in 0..<6 where !row.exists || !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "overview row \(title)")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session page for \(title)")
    }

    private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    private func tap(_ id: String) {
        let e = element(id)
        XCTAssertTrue(e.waitForExistence(timeout: 15), id)
        e.tap()
    }
}
