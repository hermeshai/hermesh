import XCTest

/// A wide screen (regular width, wider than tall: an iPhone Pro Max or iPhone Duo open, on its side), in the
/// demo world: the menu beside the page expanded or as its rail, the session list as a column with the chosen
/// session beside it, the menu over the page where there is no room for it, and one column again upright
/// (docs/iphone-app.md). An iPhone app is upright but on iPhone Duo's inner display, so the test asks the app to
/// turn on any (-HermeshTestTurns). HERMESH_WIDE_SHOTS names a folder for the screenshots.
final class WideUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-HermeshTestTurns", "YES", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
    }

    func testThreeColumns() throws {
        XCUIDevice.shared.orientation = .portrait
        app.launch()
        tap("try-demo")
        XCTAssertTrue(app.buttons["run-demo-task"].waitForExistence(timeout: 20))
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(2)
        let drawer = element("drawer"), rail = element("rail"), list = element("list-column"), scrim = element("menu-scrim")
        shot("0-side")
        guard drawer.waitForExistence(timeout: 5) else { throw XCTSkip("no regular width on its side here") }
        let width = app.frame.width, menuWidth = drawer.frame.width

        // The menu beside the page: expanded, or its rail; never hidden, and no menu button.
        XCTAssertFalse(app.buttons["menu"].exists, "no menu button beside the menu")
        XCTAssertFalse(rail.exists)
        shot("1-work-expanded")
        tap("menu-hide")
        XCTAssertTrue(rail.waitForExistence(timeout: 5), "the rail")
        XCTAssertTrue(drawer.waitForNonExistence(timeout: 5), "in the menu's place")
        XCTAssertLessThan(rail.frame.width, 160)
        XCTAssertFalse(app.buttons["menu"].exists, "the rail expands it, not a menu button")
        shot("2-work-rail")
        tap("rail-expand")
        XCTAssertTrue(drawer.waitForExistence(timeout: 5), "expanded again")
        XCTAssertTrue(rail.waitForNonExistence(timeout: 5))

        // All Sessions: the list as a column, nothing chosen yet.
        tap("sessions-place")
        XCTAssertTrue(list.waitForExistence(timeout: 5), "the list's column")
        XCTAssertTrue(element("no-session").exists, "nothing chosen yet")
        let narrow = width < menuWidth + 320 + 400
        if narrow {
            // No room for the expanded menu beside the list: its rail, though the owner left it expanded.
            XCTAssertTrue(rail.waitForExistence(timeout: 5), "the menu gives way to its rail")
            XCTAssertEqual(element("rail-sessions").isSelected, true, "the rail marks where you are")
        }
        shot("3-list")

        // A session shows beside the list, which stays; another takes its place.
        let rows = list.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "session-"))
        XCTAssertGreaterThan(rows.count, 1, "the demo's sessions")
        rows.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "the session")
        XCTAssertTrue(list.exists, "beside its list")
        XCTAssertGreaterThan(app.buttons["session-info"].frame.minX, list.frame.maxX, "to the list's right")
        shot("4-list-session")
        rows.element(boundBy: 1).tap()
        sleep(1)
        XCTAssertTrue(list.exists && app.buttons["session-info"].exists)
        XCTAssertEqual(app.buttons.matching(identifier: "session-info").count, 1, "in the first's place, not over it")
        shot("5-list-other-session")

        if narrow {
            // Asked for from the rail, the menu shows over the page; the page's shade or a place puts it away.
            tap("rail-expand")
            XCTAssertTrue(drawer.waitForExistence(timeout: 5), "the menu over the page")
            XCTAssertTrue(scrim.isHittable, "the page shaded under it")
            XCTAssertTrue(list.exists, "the columns keep their place")
            shot("6-menu-over")
            scrim.tap()
            XCTAssertTrue(drawer.waitForNonExistence(timeout: 5), "put away")
            XCTAssertTrue(rail.exists && app.buttons["session-info"].exists)
            tap("rail-expand")
            XCTAssertTrue(drawer.waitForExistence(timeout: 5))
            tap("work-place")
            XCTAssertTrue(list.waitForNonExistence(timeout: 5), "Work has no list")
            XCTAssertTrue(drawer.waitForExistence(timeout: 5) && !scrim.exists, "and the menu is beside it again, as the owner left it")
            shot("7-work-again")
            tap("sessions-place")
            rows.element(boundBy: 0).tap()
            XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10))
        }

        // Upright: one column, the same pages; Back leads to the list.
        XCUIDevice.shared.orientation = .portrait
        sleep(2)
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "the session still")
        XCTAssertFalse(list.exists || rail.exists || drawer.exists, "alone")
        shot("8-upright-session")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.navigationBars["All Sessions"].waitForExistence(timeout: 5), "back to the list, a page")
        shot("9-upright-list")
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
