import CoreGraphics
import UIKit
import XCTest

/// Regression tests for the iPhone session sidebar's floating account button over a long list
/// in both dark and light mode (no horizontal mask covering rows beside the avatar, avatar tap
/// and row taps beside it work, and scrolling reaches the final row above the avatar).
/// Also checks the Overview row while tasks are running and the connection is down.
///
///     xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh \
///         -destination 'platform=iOS Simulator,name=…' -only-testing:HermeshUITests/SidebarUITests
final class SidebarUITests: XCTestCase {
    let app = XCUIApplication()

    private func launchDemo(_ arguments: [String]) {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"] + arguments
        app.launch()
        // Enter demo if onboarding shows (standalone run without a signed-in owner).
        let tryDemo = app.buttons["try-demo"]
        if tryDemo.waitForExistence(timeout: 10) {
            tryDemo.tap()
        }
    }

    func testLongSidebarNoBottomMaskDarkAndLight() throws {
        launchDemo(["-DemoSessions", "24"])
        openDrawer()
        for mode in ["dark", "light"] {
            setAppearance(mode)
            verifyLongSidebar(mode: mode)
        }
        // Verify tapping a row directly beside the floating account button opens that session.
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        let account = app.buttons["account"]
        scrollDrawerToTop(drawer)
        let rowBeside = try XCTUnwrap(
            drawer.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "session-"))
                .allElementsBoundByIndex
                .first { $0.exists && $0.frame.maxY > account.frame.minY && $0.frame.minY < account.frame.maxY },
            "a session row aligned with the floating account button"
        )
        let tapX = (account.frame.maxX + drawer.frame.maxX) / 2
        let tapY = max(rowBeside.frame.minY + 4, min(rowBeside.frame.maxY - 4, account.frame.midY))
        app.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: tapX, dy: tapY))
            .tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10),
                      "tapping a row beside the floating account button should open its session")
    }

    /// The avatar button opens a clean pull-up menu with pairing actions (as on the Mac) and Settings, without
    /// duplicating New Session / Her / Overview.
    func testAccountMenuPairingAndSettingsWithoutRedundantPlaces() {
        launchDemo(["-DemoSessions", "24"])
        openDrawer()
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        let account = app.buttons["account"]
        XCTAssertTrue(account.waitForExistence(timeout: 10))

        // Scroll to the very bottom of the 24-session list.
        let lastRow = app.buttons["session-Sidebar Task 24"]
        for _ in 0..<8 where !lastRow.exists || !lastRow.isHittable {
            let start = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.72))
            let end = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.2))
            start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0)
            usleep(300_000)
        }
        XCTAssertTrue(lastRow.isHittable, "scrolled to bottom of 24-session list")

        // Open the native pull-up menu from the avatar button: pairing items and Settings exist, redundant places do not.
        account.tap()
        XCTAssertTrue(app.buttons["account-settings-open"].waitForExistence(timeout: 5), "expected account-settings-open in account menu")
        for redundant in ["account-menu-new", "account-menu-her", "account-menu-overview"] {
            XCTAssertFalse(app.buttons[redundant].exists, "redundant \(redundant) should not appear in account menu")
        }
        for title in ["Connect an Agent…", "Add Environment…", "Scan a Code…", "Enter a Pairing Code…"] {
            XCTAssertTrue(app.buttons[title].exists, "expected '\(title)' in account menu")
        }
        attach(XCUIScreen.main.screenshot(), name: "sidebar-account-menu")

        // Open Add Environment directly from the account menu.
        let addEnv = app.buttons["Add Environment…"]
        addEnv.tap()
        XCTAssertTrue(app.buttons["close"].waitForExistence(timeout: 10), "Add Environment sheet opened from account menu")
        app.buttons["close"].tap()
    }

    /// Exercises Duo's decoupled two-state vertical bar when closed (`[ Conversations -> New Session | Sessions </> | Her | Overview ]`
    /// in `UITabBar` while a page is in view, locking `Sessions </>` while viewing any session so a single thumb tap returns
    /// to `SessionsHubView` and `Conversations 💬` jumps straight to New Session; hiding `UITabBar` and all page toolbar items
    /// when the sidebar slides out) and conventional split-view mode when unfolded beside the page.
    func testDuoNativeTabBarAndUnfoldedSidebarFooter() {
        launchDemo(["-HermeshTestDuoTabs", "YES", "-HermeshTestTurns", "YES"])
        let tabBar = app.tabBars.firstMatch
        XCTAssertTrue(tabBar.waitForExistence(timeout: 10), "native UITabBar shown on Duo closed page")
        for id in ["conversations-place", "sessions-place", "her-place", "overview-place"] {
            XCTAssertTrue(tabBar.buttons[id].exists, "expected \(id) in native 4-button UITabBar")
        }
        for forbiddenWhenClosed in ["account", "new-session-place", "new", "menu-close", "search", "view-options"] {
            XCTAssertFalse(app.buttons[forbiddenWhenClosed].exists, "\(forbiddenWhenClosed) must not appear while sidebar is closed on Her")
        }

        // Overview tab opens Overview without any + button on Duo closed; Conversations bubble opens New Session.
        tabBar.buttons["overview-place"].tap()
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 10))
        XCTAssertTrue(tabBar.buttons["overview-place"].isSelected)
        XCTAssertFalse(app.buttons["new"].exists, "no + button on Overview when Duo sidebar is closed")

        tabBar.buttons["conversations-place"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].firstMatch.waitForExistence(timeout: 10),
                      "Conversations bubble opens New Session")
        XCTAssertTrue(tabBar.buttons["conversations-place"].isSelected)
        XCTAssertFalse(tabBar.buttons["sessions-place"].isSelected)

        // Sessions </> tab opens the standalone SessionsHubView (with environment filter pills, Search + View Options,
        // and no redundant floating + button).
        tabBar.buttons["sessions-place"].tap()
        let hub = app.descendants(matching: .any)["sessions-hub"].firstMatch
        XCTAssertTrue(hub.waitForExistence(timeout: 10), "Sessions </> tab opens standalone SessionsHubView")
        XCTAssertTrue(tabBar.buttons["sessions-place"].isSelected)
        XCTAssertFalse(tabBar.buttons["conversations-place"].isSelected)
        XCTAssertTrue(app.buttons["hub-env-all"].exists, "environment filter strip shown at top of SessionsHubView")
        XCTAssertTrue(app.buttons["search"].exists && app.buttons["view-options"].exists,
                      "Search and ViewMenu shown on SessionsHubView")
        XCTAssertFalse(app.buttons["new-session-place"].exists && app.buttons["new"].exists,
                       "no redundant + button on SessionsHubView since Conversations bubble is adjacent")

        // Tapping a session row inside SessionsHubView pushes that session onto the navigation stack (keeping
        // `sessions-place` </> locked) so swiping back from the left edge returns to SessionsHubView rather than
        // sliding open the sidebar.
        app.buttons["session-Add a welcome page"].tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10))
        XCTAssertTrue(tabBar.buttons["sessions-place"].isSelected, "entering a session locks highlight onto Sessions </> tab")
        XCTAssertFalse(tabBar.buttons["conversations-place"].isSelected, "Conversations bubble stays unlocked while viewing an existing session")
        let edgeStart = app.coordinate(withNormalizedOffset: CGVector(dx: 0.02, dy: 0.5))
        let edgeEnd = app.coordinate(withNormalizedOffset: CGVector(dx: 0.78, dy: 0.5))
        edgeStart.press(forDuration: 0.05, thenDragTo: edgeEnd, withVelocity: .fast, thenHoldForDuration: 0)
        XCTAssertTrue(hub.waitForExistence(timeout: 10), "left-edge swipe back from a hub-opened session returns to SessionsHubView")
        XCTAssertFalse(app.buttons["menu-close"].exists, "swipe back from a hub-opened session must NOT open the sidebar")

        // Entering again and re-tapping `sessions-place` (</>) also returns directly to SessionsHubView!
        app.buttons["session-Add a welcome page"].tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10))
        tabBar.buttons["sessions-place"].tap()
        XCTAssertTrue(hub.waitForExistence(timeout: 10), "re-tapping Sessions </> from a session returns straight to SessionsHubView")

        // Sliding out the sidebar via top menu button switches to the sidebar's dedicated controls:
        // top `[ Collapse, Search, View Options ]`, bottom `[ Account | + New ]`, and hides the page's UITabBar completely.
        app.buttons["menu"].tap()
        XCTAssertTrue(app.buttons["menu-close"].waitForExistence(timeout: 5), "top Collapse button shown in sidebar")
        XCTAssertTrue(app.buttons["search"].exists && app.buttons["view-options"].exists,
                      "Search and View Options shown in the sidebar's top vertical bar")
        XCTAssertFalse(tabBar.isHittable, "page UITabBar hidden while sidebar is open")
        XCTAssertFalse(app.buttons["her-place"].isHittable && app.buttons["overview-place"].isHittable,
                       "no duplicate Her / Overview buttons in slid-out sidebar")
        for id in ["account", "new-session-place"] {
            XCTAssertTrue(app.buttons[id].waitForExistence(timeout: 5), "expected \(id) in slid-out sidebar bottom bar")
        }
        attach(XCUIScreen.main.screenshot(), name: "duo-closed-sidebar-clean")

        // Tapping a session opens that session (`session-more` ⋯ appears while sidebar is closed, and disappears completely
        // when the sidebar slides out again).
        app.buttons["session-Add a welcome page"].tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["session-more"].waitForExistence(timeout: 5), "session ⋯ shown on session page while sidebar is closed")
        XCTAssertTrue(tabBar.buttons["sessions-place"].isSelected, "Sessions </> tab stays locked when opening session from sidebar")
        XCTAssertFalse(app.buttons["account"].exists, "no avatar leaked on session page while sidebar is closed")
        XCTAssertFalse(app.buttons["new-session-place"].exists, "no + leaked on session page while sidebar is closed")
        XCTAssertTrue(tabBar.waitForExistence(timeout: 5) && tabBar.isHittable, "page UITabBar returns on session page")

        let wordmark = app.descendants(matching: .any)["drawer"].descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Hermesh")).firstMatch
        app.buttons["menu"].tap()
        XCTAssertTrue(app.buttons["menu-close"].waitForExistence(timeout: 5))
        let wordmarkOpenY = wordmark.frame.minY
        XCTAssertEqual(wordmarkOpenY, 35, accuracy: 2.5,
                       "DrawerView ignores NavigationStack top safe area so Wordmark sits at fixed 24pt+11pt=35pt even when navigationBar is visible")
        XCTAssertFalse(app.buttons["session-more"].exists, "session ⋯ must NOT bleed into vertical bar while sidebar is open")
        XCTAssertTrue(app.buttons["search"].exists && app.buttons["view-options"].exists,
                      "Search and View Options present at top of vertical bar while sidebar is open over a session")
        app.buttons["menu-close"].tap()
        XCTAssertTrue(app.buttons["session-more"].waitForExistence(timeout: 5), "session ⋯ returns once sidebar closes")
        XCTAssertFalse(app.buttons["account"].exists && app.buttons["new-session-place"].exists,
                       "sidebar bottom buttons disappear once sidebar closes")

        // Swiping open the sidebar keeps Wordmark at the exact same Y without navigation-bar safe-area jitter.
        swipeOpenDrawer()
        XCTAssertTrue(app.buttons["menu-close"].waitForExistence(timeout: 5))
        XCTAssertEqual(wordmark.frame.minY, wordmarkOpenY, accuracy: 0.5, "Wordmark Y stays stable across swipe and button open")
        app.buttons["menu-close"].tap()
        XCTAssertTrue(app.buttons["session-more"].waitForExistence(timeout: 5))

        tabBar.buttons["conversations-place"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].firstMatch.waitForExistence(timeout: 10),
                      "tapping Conversations bubble from a session goes straight to New Session")

        // When unfolded beside the page, Duo returns to conventional split-view mode: no thumb UITabBar, Her and
        // Overview rows in the left sidebar list, and Liquid Glass [ Account ··· + New ] over a soft bottom edge blur.
        XCUIDevice.shared.orientation = .landscapeLeft
        defer { XCUIDevice.shared.orientation = .portrait }
        sleep(2)
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        if app.frame.width > app.frame.height, drawer.exists, !app.buttons["menu"].exists {
            XCTAssertFalse(tabBar.isHittable, "no thumb UITabBar when unfolded beside the page")
            XCTAssertTrue(drawer.buttons["her-place"].exists && drawer.buttons["overview-place"].exists,
                          "Her and Overview return to the left sidebar list when unfolded")
            let account = drawer.buttons["account"]
            let plusNew = drawer.buttons["new-session-place"]
            XCTAssertTrue(account.exists && plusNew.exists, "avatar and Liquid Glass + New sit at the unfolded sidebar's foot")
            XCTAssertGreaterThan(plusNew.frame.minX, account.frame.maxX, "+ New sits to the right of the avatar")
            XCTAssertEqual(plusNew.frame.midY, account.frame.midY, accuracy: 4, "+ New aligned horizontally with avatar")
            attach(XCUIScreen.main.screenshot(), name: "duo-unfolded-liquid-glass-blur")
        }
    }

    /// Verifies that both SessionPage and HerConversation host their bottom composers in a soft-edge `.safeAreaBar(edge: .bottom)`
    /// (matching the top SessionBar and the unfolded Sidebar footer), while keeping the `to-end` button hittable above the bar
    /// on both slab portrait and unfolded wide split-screen layouts.
    func testComposerBottomSoftEdgeBlurSlabAndUnfolded() throws {
        XCUIDevice.shared.orientation = .portrait
        defer { XCUIDevice.shared.orientation = .portrait }
        launchDemo(["-DemoSessionDetailRegression", "-DemoSessions", "16", "-HerDemo", "24", "-HermeshTestTurns", "YES"])

        // 1. Slab portrait: open Long history session, scroll up so messages pass under both top SessionBar and bottom composer,
        //    and verify `to-end` sits above the composer and scrolls back to the bottom when tapped.
        openDrawer()
        let longRow = app.descendants(matching: .any)["session-Long history session"].firstMatch
        XCTAssertTrue(longRow.waitForExistence(timeout: 10), "Long history session in drawer")
        longRow.tap()

        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        let latest = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Long latest #24")).firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10), "session composer")
        XCTAssertTrue(latest.waitForExistence(timeout: 10), "opened at bottom message")

        let win = app.windows.firstMatch
        for _ in 0..<3 {
            let s = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.32))
            let e = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.78))
            s.press(forDuration: 0.05, thenDragTo: e, withVelocity: .default, thenHoldForDuration: 0.2)
            usleep(200_000)
        }
        let toEnd = app.buttons["to-end"]
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "to-end appears above bottom safeAreaBar when scrolled up")
        XCTAssertTrue(toEnd.isHittable, "to-end remains hittable above bottom safeAreaBar")
        XCTAssertLessThan(toEnd.frame.maxY, composer.frame.minY, "to-end floats above composer")
        usleep(300_000)
        let slabShot = XCUIScreen.main.screenshot()
        attach(slabShot, name: "slab-session-top-and-bottom-soft-blur")

        // Pixel check: text scrolled into the bottom safeAreaBar pocket (just below the composer glass) is softened/attenuated
        // compared to sharp full-contrast conversation rows in the middle of the viewport.
        let midViewport = CGRect(x: win.frame.minX + 24, y: win.frame.midY - 80, width: win.frame.width - 48, height: 140)
        let underPocket = CGRect(x: win.frame.minX + 24, y: composer.frame.maxY + 7, width: win.frame.width - 48, height: 3)
        let midContrast = contrastStats(in: slabShot.image, rectInPoints: midViewport)
        let pocketContrast = contrastStats(in: slabShot.image, rectInPoints: underPocket)
        XCTAssertGreaterThan(midContrast.range, 0.5, "expected sharp message text in middle viewport")
        XCTAssertLessThan(pocketContrast.range, midContrast.range * 0.65,
                          "bottom safeAreaBar soft edge attenuates content under composer (pocket=\(pocketContrast.range), mid=\(midContrast.range))")

        toEnd.tap()
        XCTAssertTrue(toEnd.waitForNonExistence(timeout: 6), "tapping to-end returns session to bottom")

        // 2. Unfolded wide split-screen: Sidebar footer on the left and Session / Her composers on the right share symmetrical
        //    bottom soft scroll-edge blurs, and `to-end` works on both SessionPage and HerConversation.
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(2)
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        guard app.frame.width > app.frame.height, drawer.waitForExistence(timeout: 5), !app.buttons["menu"].exists else {
            throw XCTSkip("no wide beside layout on this simulator")
        }

        let convX = (drawer.frame.maxX + app.frame.maxX) / 2
        for _ in 0..<3 {
            let s = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: convX, dy: app.frame.height * 0.32))
            let e = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: convX, dy: app.frame.height * 0.78))
            s.press(forDuration: 0.05, thenDragTo: e, withVelocity: .default, thenHoldForDuration: 0.2)
            usleep(200_000)
        }
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "to-end appears in unfolded session view")
        usleep(300_000)
        attach(XCUIScreen.main.screenshot(), name: "unfolded-symmetrical-session-bottom-blur")
        toEnd.tap()
        XCTAssertTrue(toEnd.waitForNonExistence(timeout: 6), "tapping to-end in unfolded session returns to bottom")

        // Switch to Her beside the sidebar, scroll up so messages pass under HerComposer, and verify Her's `to-end` + blur.
        drawer.buttons["her-place"].tap()
        let herComposer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(herComposer.waitForExistence(timeout: 10), "Her composer beside sidebar")
        usleep(500_000)
        for _ in 0..<3 {
            let s = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: convX, dy: app.frame.height * 0.36))
            let e = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: convX, dy: app.frame.height * 0.80))
            s.press(forDuration: 0.05, thenDragTo: e, withVelocity: .default, thenHoldForDuration: 0.2)
            usleep(200_000)
        }
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "to-end appears above HerComposer when scrolled up")
        XCTAssertTrue(toEnd.isHittable, "Her to-end is hittable above safeAreaBar")
        XCTAssertLessThan(toEnd.frame.maxY, herComposer.frame.minY, "Her to-end floats above HerComposer")
        usleep(300_000)
        let herShot = XCUIScreen.main.screenshot()
        attach(herShot, name: "unfolded-symmetrical-her-bottom-blur")

        let herMid = CGRect(x: drawer.frame.maxX + 24, y: app.frame.midY - 70, width: app.frame.maxX - drawer.frame.maxX - 48, height: 120)
        let herUnder = CGRect(x: drawer.frame.maxX + 24, y: herComposer.frame.maxY + 7, width: app.frame.maxX - drawer.frame.maxX - 48, height: 4)
        let herMidContrast = contrastStats(in: herShot.image, rectInPoints: herMid)
        let herPocketContrast = contrastStats(in: herShot.image, rectInPoints: herUnder)
        XCTAssertGreaterThan(herMidContrast.range, 0.4, "expected sharp Her message content in middle viewport")
        XCTAssertLessThan(herPocketContrast.range, herMidContrast.range * 0.65,
                          "HerComposer safeAreaBar soft edge blurs and attenuates content under composer (pocket=\(herPocketContrast.range), mid=\(herMidContrast.range))")

        toEnd.tap()
        XCTAssertTrue(toEnd.waitForNonExistence(timeout: 6), "tapping Her to-end returns HerConversation to bottom")
    }

    /// Verifies that standalone `SessionsHubView` runs live provider-orbit and shimmer animations (not frozen when
    /// `drawerOpen == false`), reflects real disconnected/connecting Relay states on machine pills instead of stale
    /// green dots, and opens `ConnectionDiagnosticsSheet` from its live sync banner.
    func testDuoSessionsHubAnimationsAndNetworkStates() {
        launchDemo(["-HermeshTestDuoTabs", "YES", "-DemoSidebarActivity", "YES"])
        let tabBar = app.tabBars.firstMatch
        XCTAssertTrue(tabBar.waitForExistence(timeout: 10))
        tabBar.buttons["sessions-place"].tap()

        let hub = app.descendants(matching: .any)["sessions-hub"].firstMatch
        XCTAssertTrue(hub.waitForExistence(timeout: 10), "SessionsHubView opened")

        // 1. Disconnected Relay (`DemoSidebarActivity` sets `relayConnected = false`):
        //    sync banner appears and machine pill reports Connecting/Reconnecting rather than stale Online.
        let syncLine = app.descendants(matching: .any)["sync-line"].firstMatch
        XCTAssertTrue(syncLine.waitForExistence(timeout: 10), "live sync banner shown on SessionsHubView when Relay is down")
        XCTAssertTrue(syncLine.label.contains("Connecting to Hermesh") || syncLine.label.contains("Can’t reach Hermesh"))

        let macPill = app.buttons["hub-env-Mac mini"]
        XCTAssertTrue(macPill.waitForExistence(timeout: 5))
        let pillVal = (macPill.value as? String) ?? ""
        XCTAssertTrue(pillVal.contains("Connecting") || pillVal.contains("Reconnecting"),
                      "machine pill must reflect Connecting/Reconnecting when Relay is disconnected, got '\(pillVal)'")

        // 2. Live running-session animation on SessionsHubView (`drawerOpen == false`):
        //    provider logo squircle orbit actively animates over time (not frozen!).
        let runningRow = app.buttons["session-Running sidebar task"]
        XCTAssertTrue(runningRow.waitForExistence(timeout: 10), "running session row in SessionsHubView")
        usleep(400_000)
        let shot1 = XCUIScreen.main.screenshot()
        usleep(380_000)
        let shot2 = XCUIScreen.main.screenshot()
        attach(shot1, name: "sessions-hub-live-orbit-and-connecting")

        let leading = CGRect(x: runningRow.frame.minX + 12, y: runningRow.frame.midY - 18, width: 44, height: 36)
        let orbit = blueOrbitStats(first: shot1.image, second: shot2.image, rectInPoints: leading)
        XCTAssertGreaterThan(orbit.bluePixels, 8, "expected brand-blue orbit pixels on SessionsHubView running row (got \(orbit.bluePixels))")
        XCTAssertGreaterThan(orbit.changedPixels, 6, "expected running row orbit animation to advance over time on SessionsHubView (got \(orbit.changedPixels))")

        // 3. Tapping the sync banner opens ConnectionDiagnosticsSheet with Reconnect Now.
        syncLine.tap()
        XCTAssertTrue(app.navigationBars["Connection Diagnostics"].waitForExistence(timeout: 5),
                      "tapping sync banner opens ConnectionDiagnosticsSheet")
        let reconnect = app.buttons["connection-diagnostics-reconnect"]
        for _ in 0..<3 where !reconnect.exists || !reconnect.isHittable { app.swipeUp() }
        XCTAssertTrue(reconnect.waitForExistence(timeout: 5), "Reconnect Now available in ConnectionDiagnosticsSheet")
    }

    func testOverviewHidesTaskSpinnerAndKeepsConnectionNotice() {
        launchDemo(["-DemoSidebarActivity", "YES"])
        openDrawer()
        let overview = app.buttons["overview-place"]
        XCTAssertTrue(overview.waitForExistence(timeout: 10))
        XCTAssertTrue(overview.label.contains("1 working"), "the demo task is running")

        let screenshot = XCUIScreen.main.screenshot()
        attach(screenshot, name: "sidebar-overview-running")
        // The trailing region has the orange attention dot. Any neutral contrast here is the task spinner.
        let trailing = CGRect(x: overview.frame.maxX - 48, y: overview.frame.midY - 10, width: 37, height: 20)
        let stats = contrastStats(in: screenshot.image, rectInPoints: trailing, ignoringOrange: true)
        XCTAssertLessThan(stats.range, 0.08, "task spinner next to Overview (neutral luminance range: \(stats.range))")

        overview.tap()
        let syncLine = app.descendants(matching: .any)["sync-line"].firstMatch
        XCTAssertTrue(syncLine.waitForExistence(timeout: 10))
        XCTAssertTrue(syncLine.label.contains("Connecting to Hermesh") || syncLine.label.contains("Can’t reach Hermesh"))
        attach(XCUIScreen.main.screenshot(), name: "overview-connection-notice")
    }

    /// A running session has no trailing spinner: a brand-blue light orbits its leading logo, and its title in the
    /// menu and its step at work in the session shimmer.
    func testRunningSessionShimmersWithoutSpinner() {
        launchDemo(["-DemoSidebarActivity", "YES"])
        openDrawer()
        let row = app.buttons["session-Running sidebar task"]
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the running demo session in the menu")
        usleep(400_000)
        let menuShot = XCUIScreen.main.screenshot()
        usleep(380_000)
        let menuShot2 = XCUIScreen.main.screenshot()
        attach(menuShot, name: "sidebar-running-row")
        // Leading logo: a brand-blue squircle orbit is visible and moves over time.
        let leading = CGRect(x: row.frame.minX + 14, y: row.frame.midY - 15, width: 44, height: 30)
        let orbit = blueOrbitStats(first: menuShot.image, second: menuShot2.image, rectInPoints: leading)
        XCTAssertGreaterThan(orbit.bluePixels, 8, "expected brand-blue orbit pixels around running provider icon (got \(orbit.bluePixels))")
        XCTAssertGreaterThan(orbit.changedPixels, 6, "expected running provider icon orbit to animate over time (got \(orbit.changedPixels))")
        // The title is short: the row's trailing end is empty, where the spinner was.
        let trailing = CGRect(x: row.frame.maxX - 44, y: row.frame.midY - 10, width: 32, height: 20)
        let stats = contrastStats(in: menuShot.image, rectInPoints: trailing)
        XCTAssertLessThan(stats.range, 0.08, "spinner at the running row's end (neutral luminance range: \(stats.range))")
        XCTAssertTrue(app.activityIndicators.allElementsBoundByIndex.filter { abs($0.frame.midY - row.frame.midY) < 22 }.isEmpty,
                      "an activity indicator on the running row")

        row.tap()
        let steps = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Ran 1 command")).firstMatch
        XCTAssertTrue(steps.waitForExistence(timeout: 10), "the running tool group")
        attach(XCUIScreen.main.screenshot(), name: "session-running-steps")
        XCTAssertTrue(app.activityIndicators.allElementsBoundByIndex.filter { abs($0.frame.midY - steps.frame.midY) < 22 }.isEmpty,
                      "an activity indicator on the running tool group")
        steps.tap()
        let step = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "npm test")).firstMatch
        XCTAssertTrue(step.waitForExistence(timeout: 10), "the running step in the opened group")
        attach(XCUIScreen.main.screenshot(), name: "session-running-step-open")
        XCTAssertTrue(app.activityIndicators.allElementsBoundByIndex.filter { abs($0.frame.midY - step.frame.midY) < 22 }.isEmpty,
                      "an activity indicator on the running step")
    }

    func testProjectGroupingTruncatesToFiveAndResetsOnCollapse() {
        launchDemo(["-DemoMultiProject", "YES"])
        openDrawer()

        let viewOptions = app.buttons["view-options"]
        XCTAssertTrue(viewOptions.waitForExistence(timeout: 10), "view-options button")
        viewOptions.tap()
        let groupMenu = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Group")).firstMatch
        XCTAssertTrue(groupMenu.waitForExistence(timeout: 5), "Group submenu")
        groupMenu.tap()
        let byProject = app.buttons["By Project"]
        XCTAssertTrue(byProject.waitForExistence(timeout: 5), "By Project option")
        byProject.tap()

        // Both projects stay visible at once because alpha folds to 5 rows + "Show more (3)".
        let alphaHeader = app.buttons["group-alpha"]
        let helloHeader = app.buttons["group-hello-world"]
        XCTAssertTrue(alphaHeader.waitForExistence(timeout: 5), "alpha group header")
        XCTAssertTrue(helloHeader.waitForExistence(timeout: 5), "hello-world group header")
        XCTAssertTrue(helloHeader.isHittable, "second project header stays visible on the first screen")

        XCTAssertTrue(app.buttons["session-Alpha Task 1"].exists)
        XCTAssertTrue(app.buttons["session-Alpha Task 5"].exists)
        XCTAssertFalse(app.buttons["session-Alpha Task 6"].exists, "6th idle task folded behind Show more")

        let showMore = app.buttons["show-more-alpha"]
        XCTAssertTrue(showMore.waitForExistence(timeout: 5), "Show more button for alpha")
        XCTAssertEqual(showMore.label, "Show more (3)")

        // Tapping Show more reveals the remaining 3 tasks and removes the button.
        showMore.tap()
        XCTAssertTrue(app.buttons["session-Alpha Task 6"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["session-Alpha Task 8"].exists)
        XCTAssertFalse(showMore.exists)

        // Collapsing and reopening the group resets it back to the compact 5-row view.
        alphaHeader.tap()
        XCTAssertTrue(app.buttons["session-Alpha Task 1"].waitForNonExistence(timeout: 5))
        alphaHeader.tap()
        XCTAssertTrue(app.buttons["session-Alpha Task 5"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["session-Alpha Task 6"].exists, "collapsing resets expanded state back to 5")
        XCTAssertTrue(showMore.waitForExistence(timeout: 5))

        // Restore default By Status grouping so persisted view preferences don't bleed into later tests.
        viewOptions.tap()
        XCTAssertTrue(groupMenu.waitForExistence(timeout: 5))
        groupMenu.tap()
        let byStatus = app.buttons["By Status"]
        XCTAssertTrue(byStatus.waitForExistence(timeout: 5))
        byStatus.tap()
    }

    func testGitStatusMarksAndLongPressPreviewCard() {
        launchDemo(["-DemoGitStatus", "YES"])
        openDrawer()

        let pushedRow = app.buttons["session-Git Pushed Task"]
        let mergedRow = app.buttons["session-Git Merged Task"]
        let unpushedRow = app.buttons["session-Git Unpushed Task"]
        let dirtyTitle = "Git Dirty Task — Refactor sidebar delivery perception and long-press status card"
        let dirtyRow = app.buttons["session-\(dirtyTitle)"]
        let cleanRow = app.buttons["session-Git Clean Task"]

        for row in [pushedRow, mergedRow, unpushedRow, dirtyRow, cleanRow] {
            XCTAssertTrue(row.waitForExistence(timeout: 10), "expected \(row.identifier) in sidebar")
        }

        // Delivered-only sidebar marks: only pushed & merged expose row status marks; unpushed/dirty/clean stay unmarked.
        XCTAssertEqual(pushedRow.value as? String, "Pushed")
        XCTAssertEqual(mergedRow.value as? String, "Merged")
        XCTAssertEqual((unpushedRow.value as? String) ?? "", "")
        XCTAssertEqual((dirtyRow.value as? String) ?? "", "")
        XCTAssertEqual((cleanRow.value as? String) ?? "", "")
        attach(XCUIScreen.main.screenshot(), name: "sidebar-git-marks")

        // Long-press a pushed session: read-only Git preview platter + native context menu actions appear, with zero buttons inside the preview card.
        pushedRow.press(forDuration: 0.85)
        let preview = app.otherElements["Preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 5), "long-press Git status preview card")
        for expected in [
            "Git Pushed Task",
            "Committed & pushed",
            "+42",
            "−8",
            "hermesh/pushed",
            "origin/hermesh/pushed",
            "7b2e91f",
            "feat(sidebar): sync pushed git mark",
            "Synced to remote branch origin/hermesh/pushed",
        ] {
            XCTAssertTrue(
                preview.descendants(matching: .any).matching(NSPredicate(format: "label == %@", expected)).firstMatch.exists,
                "expected '\(expected)' inside preview platter"
            )
        }
        XCTAssertEqual(preview.buttons.count, 0, "preview card must remain strictly read-only without action buttons")
        XCTAssertTrue(app.buttons["Rename"].waitForExistence(timeout: 5), "native context menu actions remain below preview")
        attach(XCUIScreen.main.screenshot(), name: "sidebar-git-preview-pushed")

        // Dismiss context menu by tapping outside near top-right.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.12)).tap()
        XCTAssertTrue(app.buttons["Rename"].waitForNonExistence(timeout: 5))

        // Long-press a dirty session with a long truncated row title: preview shows full multi-line title + uncommitted file status.
        dirtyRow.press(forDuration: 0.85)
        XCTAssertTrue(preview.waitForExistence(timeout: 5), "long-press preview for dirty session")
        XCTAssertTrue(
            preview.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "3 uncommitted files")).firstMatch.exists,
            "expected uncommitted files label in preview"
        )
        let fullTitle = preview.descendants(matching: .any).matching(NSPredicate(format: "label == %@", dirtyTitle)).firstMatch
        XCTAssertTrue(fullTitle.exists, "expected full untruncated title in preview")
        XCTAssertGreaterThan(fullTitle.frame.height, 26, "long title wraps across multiple lines in preview card")
        attach(XCUIScreen.main.screenshot(), name: "sidebar-git-preview-dirty")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.12)).tap()
        XCTAssertTrue(app.buttons["Rename"].waitForNonExistence(timeout: 5))

        // Also check light mode sidebar marks + merged preview card.
        setAppearance("light")
        attach(XCUIScreen.main.screenshot(), name: "sidebar-git-marks-light")
        mergedRow.press(forDuration: 0.85)
        XCTAssertTrue(preview.waitForExistence(timeout: 5), "long-press preview for merged session in light mode")
        XCTAssertTrue(
            preview.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Merged into main")).firstMatch.exists,
            "expected Merged into main in preview"
        )
        attach(XCUIScreen.main.screenshot(), name: "sidebar-git-preview-merged-light")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.12)).tap()
    }

    /// Keyboard dismissal across compact drawer (standard iPhone) and cover (iPhone Duo closed):
    /// opening the sidebar by button or swipe, opening Session Info, and sending a message all dismiss the keyboard.
    func testKeyboardDismissesOnSidebarAndSend() {
        launchDemo(["-DemoSessions", "6"])
        let keyboard = app.keyboards.firstMatch

        // 1. Her conversation: menu button, horizontal swipe to open sidebar, and Send all dismiss the keyboard.
        let herComposer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(herComposer.waitForExistence(timeout: 15), "Her composer")
        herComposer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up in Her")
        openDrawer()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "menu button dismisses Her keyboard")
        closeDrawer()

        herComposer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up in Her before swipe")
        swipeOpenDrawer()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "swiping sidebar open dismisses Her keyboard")

        // 2. Session page: menu button, swipe to open sidebar, Session Info sheet, and Send all dismiss the keyboard.
        let sessionRow = app.buttons["session-Sidebar Task 1"]
        XCTAssertTrue(sessionRow.waitForExistence(timeout: 10), "session row in drawer")
        sessionRow.tap()

        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10), "session composer")
        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up in session")
        openDrawer()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "menu button dismisses session keyboard")
        closeDrawer()

        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up before swipe")
        swipeOpenDrawer()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "swiping sidebar open dismisses session keyboard")
        closeDrawer()

        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up before Session Info")
        app.buttons["session-info"].tap()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "opening Session Info sheet dismisses keyboard")
        app.buttons["close"].tap()
        XCTAssertTrue(composer.waitForExistence(timeout: 5))
        XCTAssertFalse(keyboard.exists, "closing Session Info does not re-open keyboard")

        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "keyboard up before send")
        composer.typeText("Dismiss on send check")
        app.buttons["thread-send"].tap()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5), "sending message dismisses keyboard")
    }

    private func closeDrawer() {
        let close = app.buttons["menu-close"]
        let dim = app.descendants(matching: .any)["drawer-dim"].firstMatch
        if close.exists && close.isHittable {
            close.tap()
        } else if dim.waitForExistence(timeout: 5) {
            dim.tap()
        }
        XCTAssertTrue(app.buttons["menu"].waitForExistence(timeout: 5), "page back in front")
    }

    private func swipeOpenDrawer() {
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.28))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.88, dy: 0.28))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: 400, thenHoldForDuration: 0.05)
        XCTAssertTrue(app.descendants(matching: .any)["drawer"].firstMatch.waitForExistence(timeout: 5), "drawer swiped open")
    }

    private func verifyLongSidebar(mode: String) {
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        let account = app.buttons["account"]
        XCTAssertTrue(drawer.waitForExistence(timeout: 10), "drawer open (\(mode))")
        XCTAssertTrue(account.waitForExistence(timeout: 5), "account button (\(mode))")

        scrollDrawerToTop(drawer)
        usleep(400_000)

        let topShot = XCUIScreen.main.screenshot()
        attach(topShot, name: "sidebar-\(mode)-top")

        // The horizontal band directly beside the circular account button must show visible row text,
        // not a flat opaque mask matching the sidebar background.
        let sampleRect = CGRect(
            x: account.frame.maxX + 12,
            y: account.frame.minY + 10,
            width: max(20, drawer.frame.maxX - account.frame.maxX - 36),
            height: max(16, account.frame.height - 14)
        )
        let stats = contrastStats(in: topShot.image, rectInPoints: sampleRect)
        XCTAssertGreaterThan(stats.range, 0.35,
                             "row beside account button appears masked in \(mode) mode (luminance range=\(stats.range), contrastPixels=\(stats.contrastPixels))")
        XCTAssertGreaterThan(stats.contrastPixels, 200,
                             "expected visible text pixels beside account button in \(mode) mode (got \(stats.contrastPixels))")

        // Scroll to the very bottom and confirm the final session row is completely visible above the account button.
        let lastRow = app.buttons["session-Sidebar Task 24"]
        for _ in 0..<8 where !lastRow.exists || !lastRow.isHittable || lastRow.frame.maxY > account.frame.minY {
            let start = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.72))
            let end = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.2))
            start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0)
            usleep(400_000)
        }
        usleep(400_000)
        attach(XCUIScreen.main.screenshot(), name: "sidebar-\(mode)-bottom")

        XCTAssertTrue(lastRow.exists, "final session row exists (\(mode))")
        XCTAssertTrue(lastRow.isHittable, "final session row is hittable (\(mode))")
        XCTAssertLessThan(lastRow.frame.maxY, account.frame.minY + 4,
                          "final row (maxY=\(lastRow.frame.maxY)) should scroll above account button (minY=\(account.frame.minY)) in \(mode) mode")
    }

    private func openDrawer() {
        let menu = app.buttons["menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "menu button")
        menu.tap()
        XCTAssertTrue(app.descendants(matching: .any)["drawer"].firstMatch.waitForExistence(timeout: 10), "drawer")
    }

    private func setAppearance(_ tag: String) {
        let account = app.buttons["account"]
        XCTAssertTrue(account.waitForExistence(timeout: 10), "account button")
        XCTAssertTrue(account.isHittable, "account button hittable")
        account.tap()
        let openSettings = app.buttons["account-settings-open"]
        XCTAssertTrue(openSettings.waitForExistence(timeout: 5), "account-settings-open in menu")
        openSettings.tap()
        // Appearance is a page of the account sheet's own.
        let page = app.descendants(matching: .any)["account-appearance"].firstMatch
        XCTAssertTrue(page.waitForExistence(timeout: 10), "account-appearance")
        page.tap()
        let option = app.buttons["appearance-\(tag)"]
        for _ in 0..<4 where !option.exists || !option.isHittable { app.swipeUp() }
        XCTAssertTrue(option.waitForExistence(timeout: 10), "appearance-\(tag)")
        option.tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let close = app.buttons["close"]
        XCTAssertTrue(close.waitForExistence(timeout: 10), "close account sheet")
        close.tap()
        XCTAssertTrue(account.waitForExistence(timeout: 10), "back in drawer")
    }

    private func scrollDrawerToTop(_ drawer: XCUIElement) {
        let her = app.buttons["her-place"]
        for _ in 0..<8 where !her.exists || !her.isHittable {
            let start = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.28))
            let end = drawer.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.8))
            start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0)
            usleep(350_000)
        }
    }

    private func attach(_ screenshot: XCUIScreenshot, name: String) {
        let a = XCTAttachment(screenshot: screenshot)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["HERMESH_SIDEBAR_SHOTS"] {
            try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
        }
    }

    private func contrastStats(in image: UIImage, rectInPoints: CGRect, ignoringOrange: Bool = false) -> (range: Double, contrastPixels: Int) {
        let normalized: UIImage
        if image.imageOrientation == .up {
            normalized = image
        } else {
            let fmt = UIGraphicsImageRendererFormat()
            fmt.scale = image.scale
            normalized = UIGraphicsImageRenderer(size: image.size, format: fmt).image { _ in
                image.draw(in: CGRect(origin: .zero, size: image.size))
            }
        }
        guard let cg = normalized.cgImage else { return (0, 0) }
        let scale = CGFloat(cg.width) / max(1, normalized.size.width)
        let pixelRect = CGRect(
            x: floor(rectInPoints.minX * scale),
            y: floor(rectInPoints.minY * scale),
            width: floor(rectInPoints.width * scale),
            height: floor(rectInPoints.height * scale)
        ).intersection(CGRect(x: 0, y: 0, width: cg.width, height: cg.height))
        guard let cropped = cg.cropping(to: pixelRect), cropped.width > 0, cropped.height > 0 else { return (0, 0) }

        let w = cropped.width, h = cropped.height
        var rgba = [UInt8](repeating: 0, count: w * h * 4)
        let colorSpace = CGColorSpaceCreateDeviceRGB()
        guard let ctx = CGContext(
            data: &rgba, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
            space: colorSpace, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ) else { return (0, 0) }
        ctx.draw(cropped, in: CGRect(x: 0, y: 0, width: w, height: h))

        var luminances = [Double]()
        luminances.reserveCapacity(w * h)
        var minL = 1.0, maxL = 0.0
        for i in stride(from: 0, to: rgba.count, by: 4) {
            if ignoringOrange && Int(rgba[i]) - Int(rgba[i + 2]) > 10 { continue }
            let l = (0.299 * Double(rgba[i]) + 0.587 * Double(rgba[i + 1]) + 0.114 * Double(rgba[i + 2])) / 255.0
            luminances.append(l)
            if l < minL { minL = l }
            if l > maxL { maxL = l }
        }
        guard !luminances.isEmpty else { return (0, 0) }
        luminances.sort()
        let bgL = luminances[luminances.count / 2]
        let contrastPixels = luminances.reduce(0) { $0 + (abs($1 - bgL) > 0.25 ? 1 : 0) }
        return (maxL - minL, contrastPixels)
    }

    private func blueOrbitStats(first: UIImage, second: UIImage, rectInPoints: CGRect) -> (bluePixels: Int, changedPixels: Int) {
        func rgbaBytes(_ image: UIImage) -> [UInt8]? {
            guard let cg = image.cgImage else { return nil }
            let scale = CGFloat(cg.width) / max(1, image.size.width)
            let pixelRect = CGRect(
                x: floor(rectInPoints.minX * scale),
                y: floor(rectInPoints.minY * scale),
                width: floor(rectInPoints.width * scale),
                height: floor(rectInPoints.height * scale)
            ).intersection(CGRect(x: 0, y: 0, width: cg.width, height: cg.height))
            guard let cropped = cg.cropping(to: pixelRect), cropped.width > 0, cropped.height > 0 else { return nil }
            var buf = [UInt8](repeating: 0, count: cropped.width * cropped.height * 4)
            guard let ctx = CGContext(
                data: &buf, width: cropped.width, height: cropped.height, bitsPerComponent: 8,
                bytesPerRow: cropped.width * 4, space: CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
            ) else { return nil }
            ctx.draw(cropped, in: CGRect(x: 0, y: 0, width: cropped.width, height: cropped.height))
            return buf
        }
        guard let a = rgbaBytes(first), let b = rgbaBytes(second), a.count == b.count else { return (0, 0) }
        var blue = 0, changed = 0
        for i in stride(from: 0, to: a.count, by: 4) {
            let r1 = Int(a[i]), g1 = Int(a[i + 1]), b1 = Int(a[i + 2])
            let r2 = Int(b[i]), g2 = Int(b[i + 1]), b2 = Int(b[i + 2])
            if b1 > 140 && b1 - max(r1, g1) > 35 { blue += 1 }
            if abs(r1 - r2) + abs(g1 - g2) + abs(b1 - b2) > 45 { changed += 1 }
        }
        return (blue, changed)
    }
}
