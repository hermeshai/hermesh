import XCTest

/// A wide screen (regular width, wider than tall: an iPhone Pro Max or iPhone Duo open, on its side), in the
/// demo world: the menu, with the sessions in it, beside the page expanded or as its rail (docs/her-team.md §16),
/// the rail's logos of the agents at work, the menu over the page where there is no room for it, and one column
/// again upright (docs/iphone-app.md). An iPhone app is upright but on iPhone Duo's inner display, so the test
/// asks the app to turn on any (-HermeshTestTurns). HERMESH_WIDE_SHOTS names a folder for the screenshots.
final class WideUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-HermeshTestTurns", "YES", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
    }

    func testTheMenuBesideThePage() throws {
        XCUIDevice.shared.orientation = .portrait
        app.launch()
        tap("try-demo")
        tap("menu"); tap("overview-place")
        XCTAssertTrue(app.buttons["run-demo-task"].waitForExistence(timeout: 20))
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(2)
        let drawer = element("drawer"), rail = element("rail"), scrim = element("menu-scrim")
        shot("0-side")
        guard drawer.waitForExistence(timeout: 5) else { throw XCTSkip("no regular width on its side here") }

        // The menu beside the page, the sessions in it: expanded, or its rail; never hidden, and no menu button.
        XCTAssertFalse(app.buttons["menu"].exists, "no menu button beside the menu")
        XCTAssertFalse(rail.exists)
        XCTAssertFalse(element("list-column").exists, "no list column: the menu is the list")
        let rows = drawer.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "session-"))
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10), "the demo's sessions in the menu")
        shot("1-overview-expanded")

        // A session opens beside the menu, which stays, its row the current one; another takes its place.
        rows.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "the session")
        XCTAssertTrue(drawer.exists, "beside the menu")
        XCTAssertGreaterThan(app.buttons["session-info"].frame.minX, drawer.frame.maxX, "to the menu's right")
        XCTAssertTrue(rows.element(boundBy: 0).isSelected, "its row the current one")
        shot("2-session")
        rows.element(boundBy: 1).tap()
        sleep(1)
        XCTAssertEqual(app.buttons.matching(identifier: "session-info").count, 1, "in the first's place, not over it")

        // Its rail: Her, the overview, search, and the agents at work.
        tap("menu-hide")
        XCTAssertTrue(rail.waitForExistence(timeout: 5), "the rail")
        XCTAssertTrue(drawer.waitForNonExistence(timeout: 5), "in the menu's place")
        XCTAssertLessThan(rail.frame.width, 160)
        XCTAssertFalse(app.buttons["menu"].exists, "the rail expands it, not a menu button")
        for id in ["rail-her", "rail-overview", "rail-search"] { XCTAssertTrue(element(id).exists, id) }
        XCTAssertFalse(element("rail-sessions").exists || element("rail-work").exists, "no places of the old map")
        shot("3-rail")
        tap("rail-overview")
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 5))
        XCTAssertTrue(rail.exists, "the rail stays from page to page")
        tap("rail-expand")
        XCTAssertTrue(drawer.waitForExistence(timeout: 5), "expanded again")
        XCTAssertTrue(rail.waitForNonExistence(timeout: 5))
        sleep(1) // until it has slid in

        if app.frame.width < drawer.frame.width + 400 {
            // No room for the expanded menu: asked for from the rail, it shows over the page; the shade or a row puts it away.
            XCTAssertTrue(scrim.isHittable, "the page shaded under it")
            shot("4-menu-over")
            scrim.tap()
            XCTAssertTrue(drawer.waitForNonExistence(timeout: 5), "put away")
            tap("rail-expand")
            XCTAssertTrue(drawer.waitForExistence(timeout: 5))
            visibleRow(rows).tap()
            XCTAssertTrue(drawer.waitForNonExistence(timeout: 5), "a row chosen puts it away")
        } else {
            visibleRow(rows).tap()
        }
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10))

        // Upright: one column, the session in front, the menu behind its button.
        XCUIDevice.shared.orientation = .portrait
        sleep(2)
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "the session still")
        XCTAssertFalse(rail.exists || drawer.exists, "alone")
        XCTAssertTrue(app.buttons["menu"].exists, "the menu behind its button")
        shot("5-upright-session")
    }

    /// A session row the list shows whole (it scrolls under the menu's top band, and keeps where it was left).
    private func visibleRow(_ rows: XCUIElementQuery) -> XCUIElement {
        let band = element("menu-hide").frame.maxY + 16
        let row = rows.allElementsBoundByIndex.first { $0.isHittable && $0.frame.minY >= band }
        XCTAssertNotNil(row, "a session row in view")
        return row ?? rows.firstMatch
    }

    private func element(_ identifier: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: identifier).firstMatch
    }

    private func tap(_ identifier: String) {
        let e = element(identifier)
        XCTAssertTrue(e.waitForExistence(timeout: 20), identifier)
        e.tap()
    }

    private func shot(_ name: String) {
        let png = XCUIScreen.main.screenshot().pngRepresentation
        let a = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        a.name = name; a.lifetime = .keepAlways; add(a)
        guard let dir = ProcessInfo.processInfo.environment["HERMESH_WIDE_SHOTS"] else { return }
        try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
    }
}
