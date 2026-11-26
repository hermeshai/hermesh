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
        let leading = CGRect(x: row.frame.minX + 10, y: row.frame.midY - 15, width: 36, height: 30)
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
        guard let cg = image.cgImage else { return (0, 0) }
        let scale = CGFloat(cg.width) / max(1, image.size.width)
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
