import XCTest

/// Markdown as Her's conversation draws it, on the demo's replies that put it to the test (-HerDemoMarkdown,
/// DemoMarkdown.swift): tables of every shape, nested lists and quotes, code, math. No machine, her or core is needed.
/// Each page of it is kept as a picture (in HER_SHOTS too, when set) to look at.
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh -destination 'platform=iOS Simulator,name=…' \
///         -only-testing:HermeshUITests/MarkdownUITests
final class MarkdownUITests: XCTestCase {
    let app = XCUIApplication()
    static let cases = 8

    override func setUp() {
        continueAfterFailure = true
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-HerDemo", "\(Self.cases)", "-HerDemoMarkdown", "YES"]
        app.launch()
        tap("try-demo")
        tap("menu")
        tap("her-place")
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 20), "Her's conversation")
    }

    /// From the conversation's start to its end, a page at a time: no two pieces of text over each other.
    func testGallery() {
        let first = message(1)
        for _ in 0 ..< 40 where !(first.exists && first.isHittable) { drag(by: 0.6) }
        XCTAssertTrue(first.isHittable, "the conversation's start")
        let last = message(Self.cases)
        for page in 1 ... 40 {
            keep("page-\(page)")
            checkNothingOver(page)
            if last.exists && last.isHittable && page > 1 && atEnd() { break }
            drag(by: -0.55)
        }
        // The last reply's footnotes: its references by their numbers, its notes after it.
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "A second note², and the first again¹.")).firstMatch.exists, "references by number")
        let notes = app.descendants(matching: .any)["footnotes"].firstMatch
        XCTAssertTrue(notes.exists, "the notes")
        XCTAssertTrue(notes.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "A note with bold")).firstMatch.exists, "the second note, its Markdown read")
    }

    /// Text drawn over other text: two words in one of Her's replies whose frames cross, or that run out of it.
    private func checkNothingOver(_ page: Int) {
        let screen = app.windows.firstMatch.frame
        let replies = app.descendants(matching: .any).matching(identifier: "her-reply").allElementsBoundByIndex.filter { screen.intersects($0.frame) }
        XCTAssertFalse(replies.isEmpty, "page \(page): a reply of Her's in view")
        for reply in replies {
            let box = reply.frame
            let texts = reply.staticTexts.allElementsBoundByIndex.map { ($0.label, $0.frame) }.filter { !$0.1.isEmpty }
            XCTAssertFalse(texts.isEmpty, "page \(page): words in a reply")
            for (i, a) in texts.enumerated() {
                for b in texts[(i + 1)...] {
                    let both = a.1.intersection(b.1)
                    if both.width > 3 && both.height > 3 && !a.1.contains(b.1) && !b.1.contains(a.1) {
                        XCTFail("page \(page): “\(a.0.prefix(30))” \(a.1) is over “\(b.0.prefix(30))” \(b.1)")
                    }
                }
                // Below or past the bubble's end; a table or code wider than the page scrolls sideways, so its right is not checked.
                if a.1.maxY > box.maxY + 1 || a.1.minX < box.minX - 1 {
                    XCTFail("page \(page): “\(a.0.prefix(30))” \(a.1) runs out of its reply \(box)")
                }
            }
        }
    }

    private var lastFrame: CGRect = .zero
    /// The last message stays where it was after a drag: the conversation is at its end.
    private func atEnd() -> Bool {
        let now = message(Self.cases).frame
        defer { lastFrame = now }
        return abs(now.minY - lastFrame.minY) < 2
    }

    private func message(_ n: Int) -> XCUIElement { app.staticTexts.matching(NSPredicate(format: "label ENDSWITH %@", " #\(n)")).firstMatch }

    /// Moves the conversation by part of the screen: down (to what was said earlier) for a positive part.
    private func drag(by part: CGFloat) {
        let window = app.windows.firstMatch
        let from = part > 0 ? 0.2 : 0.8
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: from))
            .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: from + part)), withVelocity: .slow, thenHoldForDuration: 0.3)
        sleep(1)
    }

    private func keep(_ name: String) {
        let shot = app.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let dir = ProcessInfo.processInfo.environment["HER_SHOTS"] {
            try? shot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
        }
    }

    private func tap(_ identifier: String) {
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 20), identifier)
        item.tap()
    }
}
