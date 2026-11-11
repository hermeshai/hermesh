import XCTest

/// The app on iPhone Duo, in the demo world: every root page, a session, the
/// menu and the sheets, in the pose the Simulator is in when it runs (Device
/// Hub sets the pose; XCUITest cannot), upright and on its side.
/// HERMESH_DUO_SHOTS names a folder for the screenshots, each named after the
/// pose (HERMESH_DUO_POSE) and the page.
final class DuoUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
    }

    func testWalk() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_DUO_SHOTS"] != nil else { throw XCTSkip("set HERMESH_DUO_SHOTS") }
        XCUIDevice.shared.orientation = .portrait
        app.launch()
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 20))
        shot("onboarding")
        tap("try-demo")
        XCTAssertTrue(app.buttons["run-demo-task"].waitForExistence(timeout: 20))
        sleep(2)
        walk("")
        // On its side: where the app turns with the device (the inner display), the same walk again.
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(2)
        let window = app.windows.firstMatch.frame
        if window.width > window.height { walk("-side") } else { shot("overview-side") }
    }

    /// The menu dragged open and shut by a finger, slowly, HERMESH_DUO_SWIPES times in each of three runs, its
    /// hitches measured (on a real device, where the frames are what the owner sees).
    func testMenuSwipe() throws {
        guard let count = Int(ProcessInfo.processInfo.environment["HERMESH_DUO_SWIPES"] ?? "") else { throw XCTSkip("set HERMESH_DUO_SWIPES") }
        app.activate() // the app already running stays so, for a trace attached to it
        let demo = app.buttons["try-demo"]
        if demo.waitForExistence(timeout: 5) { demo.tap() }
        XCTAssertTrue(app.buttons["menu"].waitForExistence(timeout: 20), "a root page")
        sleep(2)
        let dim = app.descendants(matching: .any)["drawer-dim"]
        // The menu's moves are signposted animation intervals: their hitches, as the frames the owner sees.
        let options = XCTMeasureOptions()
        options.iterationCount = 3
        measure(metrics: [XCTOSSignpostMetric(subsystem: "io.hermesh.ios", category: "menu", name: "move")], options: options) {
            for _ in 0..<count {
                drag(from: 0.15, to: 0.95)
                XCTAssertTrue(app.buttons["menu-close"].exists || (dim.exists && dim.isHittable), "the menu open")
                drag(from: 0.9, to: 0.1)
                XCTAssertTrue(app.buttons["menu"].isHittable, "the page back")
            }
        }
    }

    /// iPhone Duo open (unfolded by hand: Device Hub on the Simulator, or the device itself). On its side the
    /// menu stands beside the page, drags narrower, hides and comes back; upright it slides over the page as on
    /// an iPhone. Runs against the app as it is (the demo, or signed in), from Her's page, a root page.
    func testOpen() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_DUO_OPEN"] != nil else { throw XCTSkip("set HERMESH_DUO_OPEN") }
        app.activate()
        let drawer = app.descendants(matching: .any)["drawer"], dim = app.descendants(matching: .any)["drawer-dim"]
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(2)
        if app.buttons["menu"].exists { app.buttons["menu"].tap(); sleep(1) } // hidden when the test began
        tap("her-place")
        sleep(1)
        XCTAssertTrue(drawer.exists && !app.buttons["menu"].exists, "on its side, the menu beside the page")
        shot("open-side")
        hideAndResize("-open")
        XCUIDevice.shared.orientation = .portrait
        sleep(2)
        shot("open-upright")
        XCTAssertTrue(app.buttons["menu"].waitForExistence(timeout: 5), "upright, the page has the menu button")
        XCTAssertFalse(drawer.exists, "and the menu waits under it")
        app.buttons["menu"].tap()
        sleep(1)
        XCTAssertTrue(dim.isHittable, "the page a card beside the menu")
        shot("open-upright-menu")
        dim.tap()
        sleep(1)
        XCTAssertTrue(app.buttons["menu"].isHittable, "the page back")
    }

    private func drag(from: CGFloat, to: CGFloat) {
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: from, dy: 0.5))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: to, dy: 0.5))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: 300, thenHoldForDuration: 0.1)
        sleep(1)
    }

    private func walk(_ suffix: String) {
        shot("overview" + suffix)
        hideAndResize(suffix)
        openMenu()
        shot("menu" + suffix)
        closeMenu()
        openMenu()
        app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "drawer-session-")).firstMatch.tap()
        sleep(2)
        shot("session" + suffix)
        tap("session-info")
        sleep(1)
        shot("session-info" + suffix)
        tap("close")
        openMenu()
        app.buttons["机器"].firstMatch.tap()
        sleep(1)
        shot("machines" + suffix)
        app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "machine-")).firstMatch.tap()
        sleep(1)
        shot("machine" + suffix)
        back()
        openMenu()
        app.buttons["授权"].firstMatch.tap()
        sleep(1)
        shot("grants" + suffix)
        openMenu()
        tap("account")
        sleep(1)
        shot("account" + suffix)
        tap("close")
        openMenu()
        tap("search")
        sleep(1)
        shot("search" + suffix)
        closeSearch()
        openMenu()
        tap("her-place")
        sleep(1)
        shot("her" + suffix)
        openMenu()
        app.buttons["概览"].firstMatch.tap()
        tap("new")
        sleep(1)
        shot("new" + suffix)
        app.buttons["添加机器…"].firstMatch.tap()
        sleep(2)
        shot("add-machine" + suffix)
        tap("approve") // the demo's machine joins, so the next walk is asked about another
        tap("close")
    }

    /// Beside the page, the menu's hairline drags it narrower, its sidebar button hides it, and the page's
    /// button shows it again. Elsewhere there is nothing to do.
    private func hideAndResize(_ suffix: String) {
        let drawer = app.descendants(matching: .any)["drawer"]
        guard !app.buttons["menu"].exists, drawer.exists else { return }
        let width = drawer.frame.width
        let hairline = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: width - 4, dy: app.frame.height / 2))
        hairline.press(forDuration: 0.05, thenDragTo: hairline.withOffset(CGVector(dx: -60, dy: 0)))
        sleep(1)
        XCTAssertEqual(drawer.frame.width, width - 60, accuracy: 4, "the menu dragged narrower")
        shot("menu-narrow" + suffix)
        tap("menu-hide")
        sleep(1)
        XCTAssertFalse(drawer.exists, "the menu hidden")
        XCTAssertTrue(app.buttons["menu"].isHittable, "the page's button to show it")
        shot("menu-hidden" + suffix)
        tap("menu")
        sleep(1)
        XCTAssertTrue(drawer.exists && !app.buttons["menu"].exists, "the menu beside the page again")
        XCTAssertEqual(drawer.frame.width, width - 60, accuracy: 4, "as wide as it was left")
        let back = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: width - 64, dy: app.frame.height / 2))
        back.press(forDuration: 0.05, thenDragTo: back.withOffset(CGVector(dx: 60, dy: 0)))
        sleep(1)
        XCTAssertEqual(drawer.frame.width, width, accuracy: 4, "the menu dragged back")
    }

    /// Search closes by its sheet's Close, or where there is none (bars on the side) by the field's own.
    private func closeSearch() {
        let field = app.searchFields.firstMatch, close = app.buttons["close"]
        if close.exists {
            close.tap()
        } else {
            let frame = field.frame
            let cancel = app.buttons.allElementsBoundByIndex.first { $0.frame.minX >= frame.maxX - 2 && abs($0.frame.midY - frame.midY) < 12 }
            XCTAssertNotNil(cancel, "the search field's close")
            cancel?.tap()
        }
        XCTAssertTrue(field.waitForNonExistence(timeout: 5), "search closed")
    }

    /// Back, in the bar at the top or in the one on the side.
    private func back() {
        let bar = app.navigationBars.buttons.element(boundBy: 0)
        if bar.exists { bar.tap() } else { app.buttons["BackButton"].firstMatch.tap() }
    }

    /// Opens the menu from its button, and checks that it shows as the screen asks: beside the page with no
    /// button (regular width, on its side), in the page's place with its own Close in the bar on the side (iPhone Duo closed),
    /// or with the page a card beside it that closes it (an iPhone, iPhone Duo open and upright).
    private func openMenu() {
        let menu = app.buttons["menu"], drawer = app.descendants(matching: .any)["drawer"]
        let frame = app.frame
        let dim = app.descendants(matching: .any)["drawer-dim"]
        // Open already: a sheet from it was closed. (Covering, the page under the menu is hidden, its button too.)
        if app.buttons["menu-close"].exists || (dim.exists && dim.isHittable) { return }
        guard menu.waitForExistence(timeout: 5) else {
            XCTAssertTrue(drawer.exists && drawer.frame.width < frame.width * 0.6, "no menu button: the menu stands beside the page")
            return
        }
        XCTAssertTrue(frame.width < 600 || frame.height > frame.width, "a menu button at compact width, or upright")
        let button = menu.frame
        let onTheSide = button.midX > frame.midX // in the bar on the trailing side, not at the top's leading end
        menu.tap()
        sleep(1)
        let close = app.buttons["menu-close"]
        if onTheSide {
            XCTAssertTrue(close.isHittable, "the menu's own Close, in the bar on the side")
            XCTAssertEqual(close.frame.midX, button.midX, accuracy: 2, "where the menu button was")
            XCTAssertEqual(close.frame.midY, button.midY, accuracy: 2, "where the menu button was")
            XCTAssertFalse(menu.isHittable, "the page gone from view")
        } else {
            XCTAssertFalse(close.exists)
            XCTAssertTrue(app.descendants(matching: .any)["drawer-dim"].isHittable, "the page a card beside the menu")
        }
    }

    /// Closes the menu by what closes it there; beside the page nothing does.
    private func closeMenu() {
        let close = app.buttons["menu-close"], dim = app.descendants(matching: .any)["drawer-dim"]
        if close.exists { close.tap() } else if dim.exists && dim.isHittable { dim.tap() } else { return }
        sleep(1)
        XCTAssertTrue(app.buttons["menu"].isHittable, "the page back")
    }

    private func tap(_ identifier: String) {
        let element = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: 20), identifier)
        element.tap()
    }

    private func shot(_ name: String) { duoShot(self, name) }
}

/// A screenshot kept with the test, and in HERMESH_DUO_SHOTS, named after the pose and the page.
private func duoShot(_ test: XCTestCase, _ name: String) {
    let env = ProcessInfo.processInfo.environment
    let png = XCUIScreen.main.screenshot().pngRepresentation
    let a = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
    a.name = name; a.lifetime = .keepAlways; test.add(a)
    guard let dir = env["HERMESH_DUO_SHOTS"] else { return }
    let pose = env["HERMESH_DUO_POSE"] ?? "pose"
    try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(pose)-\(name).png"))
}

extension HermeshUITests {
    /// iPhone Duo against an owner Mac (TestIPhone's world, HERMESH_E2E_CONTROL, with her built): the app joined,
    /// its pages as they are outside the demo, and Her's page with a conversation, in the pose the Simulator is in.
    @objc func testDuo() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_DUO_SHOTS"] != nil else { throw XCTSkip("set HERMESH_DUO_SHOTS") }
        duoShot(self, "real-onboarding")
        try join()
        sleep(3)
        duoShot(self, "real-overview")
        openMenu()
        duoShot(self, "real-menu")
        tap("drawer-session-选颜色")
        XCTAssertTrue(app.descendants(matching: .any)["question"].waitForExistence(timeout: 20), "the question")
        duoShot(self, "real-question")
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { return }
        try herTurnOn()
        try herOnboarding()
        duoShot(self, "real-her-onboarding")
        herSend("[[mock:send:你好，主人]] hi")
        XCTAssertTrue(app.staticTexts["你好，主人"].waitForExistence(timeout: 60), "Her's reply")
        duoShot(self, "real-her")
    }
}
