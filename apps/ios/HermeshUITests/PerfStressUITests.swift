import XCTest

/// Systematic end-to-end stress & performance benchmark suite across all core iOS capabilities (`-DemoPerfSuite`):
/// - Sessions: 80-turn Her chat, 260-session Sidebar & Global Search, 80-turn Mega Session cold open, scrolling,
///   composer typing, and 25Hz live token streaming
/// - Action Panels: SideMenu drawer, 240-step ToolGroup & step output, BackgroundTasksPanel, SessionInfo 1,500-line
///   DiffView, and Landscape WideSidePanelHost
/// - Demo & File Previews: Live Overview (LinkMap comets + 8 concurrent shimmers/orbits), 1,200-file directory tree,
///   interactive HTML Demo (`WKWebView`), 600-line Markdown + Mermaid doc, and 3,000-line highlighted TypeScript code
///
/// Invoke with:
///     TEST_RUNNER_HERMESH_PERF_SUITE=1 xcodebuild test -project apps/ios/Hermesh.xcodeproj -scheme Hermesh \
///         -destination 'platform=iOS Simulator,id=…' -only-testing:HermeshUITests/PerfStressUITests
final class PerfStressUITests: XCTestCase {
    private let app = XCUIApplication()

    private struct Sample {
        let frames: Int
        let late: Int
        let severe: Int
        let ms: Int
        let hitchMs: Int
        let maxGap1500: Int
        let cpuMs: Int
        let memKB: Int
    }

    private struct ScenarioResult {
        let name: String
        let wallMs: Int
        let fps: Double
        let lateFrames: Int
        let severeHitches: Int
        let hitchMs: Int
        let hitchRateMsPerSec: Double
        let maxGapMs: Int
        let cpuMs: Int
        let cpuPercent: Double
        let endMemMB: Double
        let deltaMemMB: Double
    }

    private var results: [ScenarioResult] = []

    override func setUpWithError() throws {
        try XCTSkipUnless(
            ProcessInfo.processInfo.environment["HERMESH_PERF_SUITE"] == "1",
            "Set TEST_RUNNER_HERMESH_PERF_SUITE=1 to run the full stress benchmark suite."
        )
        continueAfterFailure = true
        XCUIDevice.shared.orientation = .portrait
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoPerfSuite"]
        app.launch()
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    func testComprehensivePerfSuite() throws {
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo button")

        // 1. Cold enter Demo -> 80-turn Her Conversation + fast scroll
        measure("01_Her_80Turns_OpenAndScroll") {
            tryDemo.tap()
            XCTAssertTrue(element("her-reply").waitForExistence(timeout: 15), "80-turn Her conversation loaded")
            fastScrollVertical(rounds: 3)
        }

        // 2. Left Sidebar Drawer open/close + 260 nested sessions scroll
        measure("02_Sidebar_260Sessions_OpenCloseAndScroll") {
            for _ in 0 ..< 2 {
                tap("menu")
                let dim = element("drawer-dim")
                if dim.waitForExistence(timeout: 5) { dim.tap() }
            }
            tap("menu")
            let overviewBtn = element("overview-place")
            XCTAssertTrue(overviewBtn.waitForExistence(timeout: 5))
            let drawerArea = app.coordinate(withNormalizedOffset: CGVector(dx: 0.35, dy: 0.72))
            let drawerTop = app.coordinate(withNormalizedOffset: CGVector(dx: 0.35, dy: 0.28))
            for _ in 0 ..< 3 {
                drawerArea.press(forDuration: 0.01, thenDragTo: drawerTop, withVelocity: .fast, thenHoldForDuration: 0)
            }
            for _ in 0 ..< 3 {
                drawerTop.press(forDuration: 0.01, thenDragTo: drawerArea, withVelocity: .fast, thenHoldForDuration: 0)
            }
            overviewBtn.tap()
            XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 10), "Overview loaded")
        }

        // 3. Overview Live Dashboard (LinkMap comets + 8 RunningTiles shimmers/orbits + scroll + tabs)
        measure("03_Overview_LiveDashboard_IdleAndScroll") {
            // Observe 2.0s of active Overview animations (LinkMap + 8 concurrent shimmers & orbits)
            Thread.sleep(forTimeInterval: 2.0)
            fastScrollVertical(rounds: 2)
            // Scroll back to top of Overview
            let top = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
            let bottom = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85))
            for _ in 0 ..< 3 {
                top.press(forDuration: 0.01, thenDragTo: bottom, withVelocity: .fast, thenHoldForDuration: 0)
            }
        }

        // 4. Global SearchSheet live keystroke filtering across 260 sessions
        measure("04_Search_260Sessions_LiveTyping") {
            tap("menu")
            tap("search")
            let field = app.searchFields.firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 10), "search field")
            if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
            field.typeText("Architecture")
            Thread.sleep(forTimeInterval: 0.2)
            if let clearBtn = field.buttons.firstMatch.exists ? field.buttons.firstMatch : nil {
                clearBtn.tap()
            }
            field.typeText("Pipeline")
            Thread.sleep(forTimeInterval: 0.2)
            let frame = field.frame
            if let cancel = app.buttons.allElementsBoundByIndex.first(where: { $0.frame.minX >= frame.maxX - 2 && abs($0.frame.midY - frame.midY) < 16 }) {
                cancel.tap()
            } else {
                app.swipeDown(velocity: .fast)
            }
            _ = field.waitForNonExistence(timeout: 5)
        }

        // Ensure drawer is closed if still open
        if element("drawer-dim").exists && element("drawer-dim").isHittable {
            element("drawer-dim").tap()
        }

        // 5. Cold Open 80-Turn Mega Session (~1,800 items, Markdown, Tables, Code, Diagrams, Images, 240-step ToolGroup)
        measure("05_MegaSession_80Turns_ColdOpen") {
            openSessionFromOverview("Mega 80-Turn Architecture")
            XCTAssertTrue(element("tool-steps-summary").waitForExistence(timeout: 15), "240-step summary visible")
        }

        // 6. Fast Scroll Up & Down 80-Turn Mega Session
        measure("06_MegaSession_80Turns_FastScroll") {
            fastScrollVertical(rounds: 4)
            let toEnd = element("to-end")
            if toEnd.exists && toEnd.isHittable {
                toEnd.tap()
                Thread.sleep(forTimeInterval: 0.4)
            }
        }

        // 7. Composer Typing inside 80-Turn Mega Session (measures keystroke invalidation cost)
        measure("07_MegaSession_ComposerTyping") {
            let composer = element("thread-composer")
            XCTAssertTrue(composer.waitForExistence(timeout: 10), "composer")
            composer.tap()
            if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
            composer.typeText("Verify 120Hz scroll and zero full-transcript diff")
            // Dismiss keyboard by tapping middle-left of conversation
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.35)).tap()
            Thread.sleep(forTimeInterval: 0.3)
        }

        // 8. Action Panels: 240-Step ToolGroup Card + Output Expand, BackgroundTasksPanel, SessionInfo 1,500-Line Diff
        measure("08_ActionPanels_240Tools_BgTasks_1500LineDiff") {
            let toEnd = element("to-end")
            if toEnd.exists && toEnd.isHittable { toEnd.tap(); Thread.sleep(forTimeInterval: 0.3) }

            let summary = app.descendants(matching: .any)
                .matching(NSPredicate(format: "identifier == 'tool-steps-summary' AND label BEGINSWITH 'Read 60 files'"))
                .firstMatch
            for _ in 0 ..< 5 where !summary.exists || !summary.isHittable {
                app.swipeDown()
            }
            if summary.exists && summary.isHittable {
                summary.tap()
                let card = element("tool-steps")
                if card.waitForExistence(timeout: 8) {
                    // Ensure card is well inside viewport before inner scroll gestures
                    if card.frame.maxY > app.frame.height - 140 {
                        app.swipeUp()
                        Thread.sleep(forTimeInterval: 0.2)
                    }
                    if card.isHittable {
                        let cTop = card.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2))
                        let cBot = card.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
                        cTop.press(forDuration: 0.02, thenDragTo: cBot, withVelocity: .fast, thenHoldForDuration: 0)
                        cBot.press(forDuration: 0.02, thenDragTo: cTop, withVelocity: .fast, thenHoldForDuration: 0)
                    }
                    if let ran = card.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Ran'")).allElementsBoundByIndex.last(where: { $0.isHittable && card.frame.contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY)) }) {
                        ran.tap()
                        _ = card.descendants(matching: .any)["tool-output"].firstMatch.waitForExistence(timeout: 5)
                    }
                    if summary.isHittable { summary.tap() }
                }
            }

            let bgBar = element("thread-background-bar")
            if bgBar.exists && bgBar.isHittable {
                bgBar.tap()
                Thread.sleep(forTimeInterval: 0.4)
                let closeBtn = app.buttons["close"]
                if closeBtn.exists && closeBtn.isHittable {
                    closeBtn.tap()
                } else {
                    app.swipeDown(velocity: .fast)
                }
                Thread.sleep(forTimeInterval: 0.3)
            }

            tap("session-info")
            let engineDiff = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'src/core/Engine.ts'")).firstMatch
            if engineDiff.waitForExistence(timeout: 10) {
                engineDiff.tap()
                Thread.sleep(forTimeInterval: 0.3)
                fastScrollVertical(rounds: 2)
                app.navigationBars.buttons.element(boundBy: 0).tap()
            }
            tap("close")
        }

        // 9. Demo & Project File Previews: 1,200-file tree, Interactive HTML Demo (`WKWebView`), Markdown + Mermaid, 3,000-line Code
        measure("09_Previews_1200Files_HTMLDemo_Markdown_3000LineCode") {
            tap("session-more")
            tap("session-files")
            XCTAssertTrue(element("session-folder-page").waitForExistence(timeout: 10), "Project Files root")

            // 9a. 1,200-file folder open & scroll
            tap("folder-entry-packages")
            tap("folder-entry-generated")
            XCTAssertTrue(element("folder-entry-module_0001.ts").waitForExistence(timeout: 10), "1,200 files listed")
            fastScrollVertical(rounds: 2)
            navBack()
            navBack()

            // 9b. Interactive HTML Demo (`WKWebView` canvas dashboard) + 600-line Markdown + Mermaid
            tap("folder-entry-docs")
            tap("folder-entry-interactive-demo.html")
            XCTAssertTrue(element("file-page").waitForExistence(timeout: 10), "Interactive HTML Demo webview")
            Thread.sleep(forTimeInterval: 1.0)
            navBack()

            tap("folder-entry-Architecture.md")
            XCTAssertTrue(element("file-markdown").waitForExistence(timeout: 10), "600-line Markdown preview")
            Thread.sleep(forTimeInterval: 0.5)
            fastScrollVertical(rounds: 2)
            navBack()
            navBack()

            // 9c. 3,000-line TypeScript CodePage + Shiki Highlighter
            tap("folder-entry-src")
            tap("folder-entry-core")
            tap("folder-entry-Engine.ts")
            XCTAssertTrue(element("file-code").waitForExistence(timeout: 10), "3,000-line CodePage")
            Thread.sleep(forTimeInterval: 0.5)
            fastScrollVertical(rounds: 2)
            navBack()
            navBack()
            navBack()
            navBack()
        }

        // 10. Landscape Wide Split Workbench: SidePanel open/close/enlarge over 80-turn session
        XCUIDevice.shared.orientation = .landscapeLeft
        Thread.sleep(forTimeInterval: 0.8)
        measure("10_WideSplit_SidePanel_OpenCloseMax") {
            let sideBtn = element("session-side-panel")
            if sideBtn.waitForExistence(timeout: 8) {
                for _ in 0 ..< 2 {
                    sideBtn.tap()
                    Thread.sleep(forTimeInterval: 0.45)
                    let maxBtn = element("side-panel-max")
                    if maxBtn.exists && maxBtn.isHittable {
                        maxBtn.tap()
                        Thread.sleep(forTimeInterval: 0.35)
                        maxBtn.tap()
                        Thread.sleep(forTimeInterval: 0.35)
                    }
                    sideBtn.tap()
                    Thread.sleep(forTimeInterval: 0.45)
                }
            }
        }
        XCUIDevice.shared.orientation = .portrait
        Thread.sleep(forTimeInterval: 0.8)

        // 11. Live 25Hz Token & Reasoning Streaming Session
        if app.buttons["BackButton"].exists { app.buttons["BackButton"].tap() }
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 10), "Back on Overview")
        openSessionFromOverview("Live Streaming Stress")
        measure("11_LiveStreaming_25Hz_FollowAndScroll") {
            // 2.5s of active 25Hz token + reasoning streaming while pinned to bottom, plus user scroll up & back
            Thread.sleep(forTimeInterval: 2.5)
            fastScrollVertical(rounds: 1)
            let toEnd = element("to-end")
            if toEnd.exists && toEnd.isHittable { toEnd.tap() }
            Thread.sleep(forTimeInterval: 1.0)
        }

        printSummaryTable()
    }

    // MARK: - Helpers

    private func element(_ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    private func tap(_ id: String, file: StaticString = #filePath, line: UInt = #line) {
        let e = element(id)
        XCTAssertTrue(e.waitForExistence(timeout: 12), "Missing element \(id)", file: file, line: line)
        e.tap()
    }

    private func navBack() {
        let back = app.navigationBars.buttons.element(boundBy: 0)
        if back.waitForExistence(timeout: 5) { back.tap() }
    }

    private func openSessionFromOverview(_ title: String) {
        let row = element("session-\(title)")
        for _ in 0 ..< 5 where !row.exists || !row.isHittable {
            app.swipeDown()
        }
        for _ in 0 ..< 5 where !row.exists || !row.isHittable {
            app.swipeUp()
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "Overview session row \(title)")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 12), "Opened session \(title)")
    }

    private func fastScrollVertical(rounds: Int) {
        let win = app.windows.firstMatch
        let top = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.24))
        let bottom = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.80))
        for _ in 0 ..< rounds {
            bottom.press(forDuration: 0.01, thenDragTo: top, withVelocity: .fast, thenHoldForDuration: 0)
            top.press(forDuration: 0.01, thenDragTo: bottom, withVelocity: .fast, thenHoldForDuration: 0)
        }
    }

    private func readSample() -> Sample {
        let m = element("frame-meter")
        XCTAssertTrue(m.waitForExistence(timeout: 10), "frame-meter element")
        usleep(160_000) // Allow 100ms FrameMeterView label refresh to settle
        let words = m.label.split(separator: " ")
        func n(_ key: String) -> Int {
            guard let idx = words.firstIndex(of: Substring(key)), idx + 1 < words.count else { return 0 }
            return Int(words[idx + 1]) ?? 0
        }
        return Sample(
            frames: n("frames"),
            late: n("late"),
            severe: n("severe"),
            ms: n("ms"),
            hitchMs: n("hitchMs"),
            maxGap1500: n("maxGap1500"),
            cpuMs: n("cpuMs"),
            memKB: n("memKB")
        )
    }

    private func measure(_ name: String, block: () -> Void) {
        usleep(1_600_000) // Clear 1.5s rolling maxGap window before scenario starts
        let a = readSample()
        block()
        let b = readSample()

        let wallMs = max(1, b.ms - a.ms)
        let df = max(0, b.frames - a.frames)
        let fps = Double(df) * 1000.0 / Double(wallMs)
        let dLate = max(0, b.late - a.late)
        let dSevere = max(0, b.severe - a.severe)
        let dHitchMs = max(0, b.hitchMs - a.hitchMs)
        let hitchRate = Double(dHitchMs) * 1000.0 / Double(wallMs)
        let dCpuMs = max(0, b.cpuMs - a.cpuMs)
        let cpuPct = Double(dCpuMs) * 100.0 / Double(wallMs)
        let endMemMB = Double(b.memKB) / 1024.0
        let deltaMemMB = Double(b.memKB - a.memKB) / 1024.0

        let res = ScenarioResult(
            name: name,
            wallMs: wallMs,
            fps: fps,
            lateFrames: dLate,
            severeHitches: dSevere,
            hitchMs: dHitchMs,
            hitchRateMsPerSec: hitchRate,
            maxGapMs: b.maxGap1500,
            cpuMs: dCpuMs,
            cpuPercent: cpuPct,
            endMemMB: endMemMB,
            deltaMemMB: deltaMemMB
        )
        results.append(res)
        let line = String(
            format: "PERF_SCENARIO %@ | wall=%dms fps=%.1f late=%d severe=%d hitch=%dms (%.1fms/s) maxGap=%dms cpu=%dms (%.1f%%) mem=%.1fMB (%+.1fMB)",
            res.name, res.wallMs, res.fps, res.lateFrames, res.severeHitches, res.hitchMs,
            res.hitchRateMsPerSec, res.maxGapMs, res.cpuMs, res.cpuPercent, res.endMemMB, res.deltaMemMB
        )
        print(line)
        XCTContext.runActivity(named: line) { _ in }
    }

    private func printSummaryTable() {
        print("=== PERF_SUMMARY_BEGIN ===")
        let totalWall = results.reduce(0) { $0 + $1.wallMs }
        let totalCpu = results.reduce(0) { $0 + $1.cpuMs }
        let totalHitch = results.reduce(0) { $0 + $1.hitchMs }
        let totalSevere = results.reduce(0) { $0 + $1.severeHitches }
        let peakMem = results.map(\.endMemMB).max() ?? 0
        for r in results {
            let row = String(
                format: "PERF_ROW\t%@\t%d\t%.1f\t%d\t%d\t%d\t%.1f\t%d\t%d\t%.1f\t%.1f\t%+.1f",
                r.name, r.wallMs, r.fps, r.lateFrames, r.severeHitches, r.hitchMs,
                r.hitchRateMsPerSec, r.maxGapMs, r.cpuMs, r.cpuPercent, r.endMemMB, r.deltaMemMB
            )
            print(row)
            XCTContext.runActivity(named: row) { _ in }
        }
        let total = String(
            format: "PERF_TOTAL\twallMs=%d\ttotalCpuMs=%d\tavgCpuPct=%.1f\ttotalHitchMs=%d\thitchRateMsPerS=%.1f\ttotalSevere=%d\tpeakMemMB=%.1f",
            totalWall, totalCpu, Double(totalCpu) * 100.0 / Double(max(1, totalWall)),
            totalHitch, Double(totalHitch) * 1000.0 / Double(max(1, totalWall)), totalSevere, peakMem
        )
        print(total)
        XCTContext.runActivity(named: total) { _ in }
        print("=== PERF_SUMMARY_END ===")
    }
}
