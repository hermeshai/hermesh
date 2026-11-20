import CoreGraphics
import XCTest

/// End-to-end UI regression tests for iPhone Remote Desktop pan/zoom, overscroll whitespace,
/// finger-anchored pinch zooming, blank-area rejection, one-tap fit recovery, safe areas in
/// portrait/landscape, and preserved trackpad semantics.
///
/// Uses `-HermeshTestScreen` (local synthetic 960×600 frame + in-app input recorder) and never
/// connects to or controls a real remote machine.
final class ScreenViewportUITests: XCTestCase {
    private let app = XCUIApplication()

    private struct ViewportState: Decodable {
        let x: Double
        let y: Double
        let w: Double
        let h: Double
        let s: Double
        let minScale: Double
        let fitted: Bool
        let safeX: Double
        let safeY: Double
        let safeW: Double
        let safeH: Double
        let boundsW: Double
        let boundsH: Double

        var maxX: Double { x + w }
        var maxY: Double { y + h }
        var midX: Double { x + w / 2 }
        var midY: Double { y + h / 2 }
        var safeMaxX: Double { safeX + safeW }
        var safeMaxY: Double { safeY + safeH }
        var safeMidX: Double { safeX + safeW / 2 }
        var safeMidY: Double { safeY + safeH / 2 }
    }

    private struct InputRecord: Decodable {
        let kind: String
        let x: Double
        let y: Double
        let button: String?
        let down: Bool?
        let clicks: Int?
    }

    private var surface: XCUIElement { app.descendants(matching: .any)["screen-surface"].firstMatch }
    private var stateElement: XCUIElement { app.descendants(matching: .any)["screen-test-state"].firstMatch }
    private var logElement: XCUIElement { app.descendants(matching: .any)["screen-test-log"].firstMatch }

    override func setUpWithError() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
        app.launchArguments = ["-HermeshTestScreen", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(surface.waitForExistence(timeout: 15), "remote desktop surface")
        XCTAssertTrue(stateElement.waitForExistence(timeout: 10), "synthetic viewport state")
        _ = try waitState("initial portrait fit") { $0.boundsW > 0 && $0.boundsH > $0.boundsW && $0.fitted }
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
    }

    // MARK: Tests

    func testPortraitOverscrollBottomBarZoomAccurateClickAndFit() throws {
        let initial = try readState()
        XCTAssertTrue(initial.fitted)
        XCTAssertFalse(app.buttons["screen-fit"].exists, "fit button hidden when fitted")
        XCTAssertGreaterThanOrEqual(initial.y, initial.safeY - 1)
        XCTAssertLessThanOrEqual(initial.maxY, initial.safeMaxY + 1)

        // 1x direct touch: tap inside picture hits exact normalized coordinates.
        let before1x = readLog().count
        let insidePt = CGPoint(x: initial.x + initial.w * 0.25, y: initial.y + initial.h * 0.50)
        tapPoint(insidePt)
        let click1x = try waitButtonClick(after: before1x)
        XCTAssertEqual(click1x.x, 0.25, accuracy: 0.02)
        XCTAssertEqual(click1x.y, 0.50, accuracy: 0.02)

        // Drag remote bottom bar up to the center of the safe area at 1x.
        drag(from: CGPoint(x: initial.safeMidX, y: initial.midY),
             to: CGPoint(x: initial.safeMidX, y: initial.midY - 260))
        let dragged1x = try waitState("bottom bar dragged to screen center at 1x") { !$0.fitted && abs($0.maxY - $0.safeMidY) <= 4 }
        XCTAssertTrue(app.buttons["screen-fit"].waitForExistence(timeout: 5), "fit button appears after 1x pan")

        // Release and wait: must not snap back.
        usleep(600_000)
        let settled1x = try readState()
        XCTAssertEqual(settled1x.maxY, dragged1x.maxY, accuracy: 2.0, "no snap-back after release at 1x")

        // Blank area below bottom bar must reject tap, double-tap, long-press, and long-press drag.
        let blankPt = CGPoint(x: settled1x.safeX + 60, y: min(settled1x.safeMaxY - 50, settled1x.maxY + 80))
        let beforeBlank = readLog().count
        tapPoint(blankPt)
        tapPoint(blankPt)
        coord(blankPt).press(forDuration: 0.65)
        coord(blankPt).press(forDuration: 0.6, thenDragTo: coord(CGPoint(x: blankPt.x + 50, y: blankPt.y)))
        usleep(250_000)
        XCTAssertEqual(readLog().count, beforeBlank, "blank area must emit zero remote inputs in direct touch")

        // Single tap on the bottom bar right after blank gestures must click accurately as clicks == 1 (no double-click pollution).
        let afterBlank = try readState()
        let bottomTap1x = CGPoint(x: afterBlank.safeMidX, y: afterBlank.maxY - 8)
        let exp1xX = (bottomTap1x.x - afterBlank.x) / afterBlank.w
        let exp1xY = (bottomTap1x.y - afterBlank.y) / afterBlank.h
        tapPoint(bottomTap1x)
        let bottomClick1x = try waitButtonClick(after: beforeBlank)
        XCTAssertEqual(bottomClick1x.clicks ?? 1, 1, "blank tap must not pollute double-tap detection")
        XCTAssertEqual(bottomClick1x.x, exp1xX, accuracy: 0.02)
        XCTAssertEqual(bottomClick1x.y, exp1xY, accuracy: 0.02)

        // Zoom in centered on the remote bottom bar (bounds center sits right on the bottom bar).
        let pinchCenter = CGPoint(x: afterBlank.boundsW / 2, y: afterBlank.boundsH / 2)
        let anchorRemoteY = (pinchCenter.y - afterBlank.y) / afterBlank.s
        surface.pinch(withScale: 2.4, velocity: 1.0)
        let zoomed = try waitState("zoomed in on bottom bar") { $0.s > afterBlank.s * 1.35 }

        // Wait after pinch release: must not snap back and must remain anchored near center.
        usleep(600_000)
        let settledZoom = try readState()
        XCTAssertEqual(settledZoom.s, zoomed.s, accuracy: 0.02, "scale does not snap back")
        XCTAssertEqual(settledZoom.maxY, zoomed.maxY, accuracy: 2.0, "position does not snap back")
        XCTAssertLessThan(settledZoom.maxY, settledZoom.safeMaxY - 40, "bottom bar stayed near center, not clamped to screen bottom")
        let projectedAnchorY = settledZoom.y + anchorRemoteY * settledZoom.s
        XCTAssertEqual(projectedAnchorY, pinchCenter.y, accuracy: 30, "bottom bar stayed anchored near pinch center")
        XCTAssertLessThanOrEqual(settledZoom.x, settledZoom.safeMidX + 1)
        XCTAssertGreaterThanOrEqual(settledZoom.maxX, settledZoom.safeMidX - 1)
        XCTAssertLessThanOrEqual(settledZoom.y, settledZoom.safeMidY + 1)
        XCTAssertGreaterThanOrEqual(settledZoom.maxY, settledZoom.safeMidY - 1)

        // Blank area below zoomed bottom bar still rejects clicks.
        let zoomBlankPt = CGPoint(x: settledZoom.safeX + 60, y: min(settledZoom.safeMaxY - 50, settledZoom.maxY + 70))
        let beforeZoomBlank = readLog().count
        tapPoint(zoomBlankPt)
        usleep(200_000)
        XCTAssertEqual(readLog().count, beforeZoomBlank, "zoomed blank area must emit zero remote inputs")

        // Tap inside the zoomed bottom bar hits exact normalized coordinates.
        let zoomBottomPt = CGPoint(x: settledZoom.safeMidX, y: settledZoom.maxY - 12)
        let expZoomX = (zoomBottomPt.x - settledZoom.x) / settledZoom.w
        let expZoomY = (zoomBottomPt.y - settledZoom.y) / settledZoom.h
        tapPoint(zoomBottomPt)
        let bottomClickZoom = try waitButtonClick(after: beforeZoomBlank)
        XCTAssertEqual(bottomClickZoom.x, expZoomX, accuracy: 0.02)
        XCTAssertEqual(bottomClickZoom.y, expZoomY, accuracy: 0.02)

        // One-tap Fit restores fitted placement and hides screen-fit.
        app.buttons["screen-fit"].tap()
        let reset = try waitState("restored to portrait fit") { $0.fitted && abs($0.s - $0.minScale) < 0.002 }
        XCTAssertEqual(reset.y, initial.y, accuracy: 2.0)
        XCTAssertTrue(app.buttons["screen-fit"].waitForNonExistence(timeout: 5), "fit button hides once fitted")
    }

    func testLandscapeSafeAreaOverscrollZoomAndReset() throws {
        XCUIDevice.shared.orientation = .landscapeLeft
        let land = try waitState("landscape safe fit") { $0.boundsW > $0.boundsH && $0.fitted }
        XCTAssertEqual(surface.frame.width, app.frame.width, accuracy: 1)
        XCTAssertEqual(surface.frame.height, app.frame.height, accuracy: 1)

        // Fit strictly respects landscape safe area (Dynamic Island on sides, Home Indicator at bottom).
        XCTAssertGreaterThanOrEqual(land.x, land.safeX - 1, "clear of leading safe area")
        XCTAssertLessThanOrEqual(land.maxX, land.safeMaxX + 1, "clear of trailing safe area")
        XCTAssertGreaterThanOrEqual(land.y, land.safeY - 1, "clear of top safe area")
        XCTAssertLessThanOrEqual(land.maxY, land.safeMaxY + 1, "clear of bottom Home Indicator safe area")
        XCTAssertFalse(app.buttons["screen-fit"].exists)

        // Drag bottom bar up to landscape safe center and confirm no snap-back.
        drag(from: CGPoint(x: land.safeMidX, y: land.midY),
             to: CGPoint(x: land.safeMidX, y: land.midY - 240))
        let dragged = try waitState("landscape bottom bar at center") { !$0.fitted && abs($0.maxY - $0.safeMidY) <= 4 }
        usleep(600_000)
        let settled = try readState()
        XCTAssertEqual(settled.maxY, dragged.maxY, accuracy: 2.0, "landscape bottom bar stays at center without snap-back")

        // Blank area below bottom bar in landscape must emit zero remote inputs.
        let blankPt = CGPoint(x: settled.safeX + 60, y: min(settled.safeMaxY - 30, settled.maxY + 60))
        let beforeBlank = readLog().count
        tapPoint(blankPt)
        usleep(200_000)
        XCTAssertEqual(readLog().count, beforeBlank, "landscape blank area must emit zero remote inputs")

        // Shift slightly down so bounds center lies inside the bottom bar, then pinch to zoom on it.
        drag(from: CGPoint(x: settled.safeMidX, y: settled.safeMidY),
             to: CGPoint(x: settled.safeMidX, y: settled.safeMidY + 40))
        let prePinch = try waitState("landscape bottom bar positioned for centered pinch") { $0.maxY > $0.boundsH / 2 + 4 }
        surface.pinch(withScale: 2.0, velocity: 1.0)
        let zoomed = try waitState("landscape zoomed on bottom bar") { $0.s > prePinch.s * 1.25 }
        usleep(500_000)
        let settledZoom = try readState()
        XCTAssertEqual(settledZoom.maxY, zoomed.maxY, accuracy: 2.0)
        XCTAssertLessThan(settledZoom.maxY, settledZoom.safeMaxY - 30)

        // Accurate click on zoomed landscape bottom bar.
        let tapPt = CGPoint(x: settledZoom.safeMidX, y: settledZoom.maxY - 10)
        let expX = (tapPt.x - settledZoom.x) / settledZoom.w
        let expY = (tapPt.y - settledZoom.y) / settledZoom.h
        let beforeClick = readLog().count
        tapPoint(tapPt)
        let click = try waitButtonClick(after: beforeClick)
        XCTAssertEqual(click.x, expX, accuracy: 0.02)
        XCTAssertEqual(click.y, expY, accuracy: 0.02)

        // One-tap Fit restores landscape fit inside safe area.
        XCTAssertTrue(app.buttons["screen-fit"].waitForExistence(timeout: 5))
        app.buttons["screen-fit"].tap()
        let reset = try waitState("landscape fit restored") { $0.fitted && abs($0.s - $0.minScale) < 0.002 }
        XCTAssertLessThanOrEqual(reset.maxY, reset.safeMaxY + 1)

        // Rotating back to portrait keeps fitted placement.
        XCUIDevice.shared.orientation = .portrait
        _ = try waitState("portrait restored") { $0.boundsH > $0.boundsW && $0.fitted }
    }

    func testTrackpadKeepsSemanticsAndSupports1xPanMode() throws {
        let initial = try readState()

        // Switch to trackpad mode: Pan Screen button is immediately available at 1x.
        app.buttons["screen-mode"].tap()
        let panBtn = app.buttons["screen-pan"]
        XCTAssertTrue(panBtn.waitForExistence(timeout: 5), "trackpad pan button available at 1x")

        // Enable Pan Screen mode: one-finger drag moves canvas to center, taps emit zero remote input.
        panBtn.tap()
        let beforePan = readLog().count
        drag(from: CGPoint(x: initial.safeMidX, y: initial.midY),
             to: CGPoint(x: initial.safeMidX, y: initial.midY - 260))
        let panned = try waitState("trackpad 1x pan moved bottom bar to center") { !$0.fitted && abs($0.maxY - $0.safeMidY) <= 4 }
        tapPoint(CGPoint(x: panned.safeMidX, y: panned.midY))
        usleep(200_000)
        XCTAssertEqual(readLog().count, beforePan, "panning mode emits zero remote inputs")

        // Turn off Pan Screen: existing trackpad semantics (drag & tap in blank area operate virtual pointer) remain intact.
        panBtn.tap()
        let beforeTrackpad = readLog().count
        drag(from: CGPoint(x: panned.safeX + 60, y: panned.safeMaxY - 90),
             to: CGPoint(x: panned.safeX + 160, y: panned.safeMaxY - 90))
        let moved = try waitMove(after: beforeTrackpad)
        XCTAssertGreaterThan(moved.x, 0.52, "dragging in blank area moves virtual pointer right")

        // Canvas did not snap back while moving the trackpad pointer.
        let afterMoveState = try readState()
        XCTAssertEqual(afterMoveState.maxY, panned.maxY, accuracy: 2.0, "trackpad pointer move does not snap back overscrolled canvas")

        // Tapping in blank area still clicks at the virtual pointer (existing trackpad semantics).
        let beforeTap = readLog().count
        tapPoint(CGPoint(x: panned.safeX + 60, y: panned.safeMaxY - 90))
        let click = try waitButtonClick(after: beforeTap)
        XCTAssertEqual(click.x, moved.x, accuracy: 0.02, "trackpad tap clicks at pointer x")
        XCTAssertEqual(click.y, moved.y, accuracy: 0.02, "trackpad tap clicks at pointer y")

        // One-tap Fit restores fit and exits panning.
        app.buttons["screen-fit"].tap()
        _ = try waitState("trackpad fit restored") { $0.fitted }
        XCTAssertTrue(app.buttons["screen-fit"].waitForNonExistence(timeout: 5))
    }

    // MARK: Helpers

    private func coord(_ pt: CGPoint) -> XCUICoordinate {
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: pt.x, dy: pt.y))
    }

    private func tapPoint(_ pt: CGPoint) {
        coord(pt).tap()
    }

    private func drag(from: CGPoint, to: CGPoint) {
        coord(from).press(forDuration: 0.05, thenDragTo: coord(to))
    }

    private func readState() throws -> ViewportState {
        let raw = try XCTUnwrap(stateElement.value as? String, "screen-test-state value")
        return try JSONDecoder().decode(ViewportState.self, from: Data(raw.utf8))
    }

    private func readLog() -> [InputRecord] {
        guard let raw = logElement.value as? String,
              let list = try? JSONDecoder().decode([InputRecord].self, from: Data(raw.utf8))
        else { return [] }
        return list
    }

    private func waitState(_ label: String, timeout: TimeInterval = 8, _ check: (ViewportState) -> Bool) throws -> ViewportState {
        let deadline = Date().addingTimeInterval(timeout)
        var last: ViewportState?
        while Date() < deadline {
            if let s = try? readState() {
                last = s
                if check(s) { return s }
            }
            usleep(100_000)
        }
        XCTFail("timed out waiting for \(label); last state: \(String(describing: last))")
        return try readState()
    }

    private func waitButtonClick(after count: Int, timeout: TimeInterval = 5) throws -> InputRecord {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let log = readLog()
            if log.count > count,
               let hit = log[count...].first(where: { $0.kind == "button" && $0.down == true }) {
                return hit
            }
            usleep(100_000)
        }
        XCTFail("timed out waiting for button click after index \(count)")
        throw XCTSkip("missing button click")
    }

    private func waitMove(after count: Int, timeout: TimeInterval = 5) throws -> InputRecord {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let log = readLog()
            if log.count > count,
               let hit = log[count...].last(where: { $0.kind == "move" }) {
                return hit
            }
            usleep(100_000)
        }
        XCTFail("timed out waiting for move event after index \(count)")
        throw XCTSkip("missing move")
    }
}
