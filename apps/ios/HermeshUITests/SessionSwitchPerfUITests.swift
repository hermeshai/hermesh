import XCTest

/// End-to-end session-switching benchmark reproducing rapid iPhone switching across heavy real-world session profiles:
/// - Switch A: 38 replies (~110K chars), 20 Markdown tables, 7 Mermaid diagrams, ~1,900 inline code spans, 474 items
/// - Switch B: 83 replies (~40K chars), ~680 items (~2.4 MB tool I/O & diffs)
/// - Switch C: 15 replies (~34K chars), 377 items collapsed to <= 100 steps (non-lazy VStack trap)
/// - Switch D: 8 replies (~10K chars), 229 items collapsed to <= 100 steps
final class SessionSwitchPerfUITests: XCTestCase {
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

    override func setUpWithError() throws {
        try XCTSkipUnless(
            ProcessInfo.processInfo.environment["HERMESH_SWITCH_PERF"] == "1",
            "Set TEST_RUNNER_HERMESH_SWITCH_PERF=1 to run the session-switching benchmark."
        )
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoPerfSuite", "-DemoSwitchPerf"]
        app.launch()
    }

    func testSessionSwitchingEndToEndPerf() throws {
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        XCTAssertTrue(element("her-reply").waitForExistence(timeout: 15), "initial demo ready")

        // 1. Cold open all 4 heavy sessions via Sidebar Drawer (D -> C -> B -> A)
        measure("01_ColdSwitch_4Sessions_D_C_B_A") {
            switchViaDrawer(to: "Switch D: Illustration Interrupt", landmark: "SWITCH_LANDMARK_D_READY")
            switchViaDrawer(to: "Switch C: CLI & Installer Verify", landmark: "SWITCH_LANDMARK_C_READY")
            switchViaDrawer(to: "Switch B: Sidebar & New Session", landmark: "SWITCH_LANDMARK_B_READY")
            switchViaDrawer(to: "Switch A: Context Compression", landmark: "SWITCH_LANDMARK_A_READY")
        }

        // 2. Warm back-and-forth switching across cached sessions (B -> C -> A -> D -> B -> D)
        //    Total distinct replies across A+B+C+D = 38 + 83 + 15 + 8 = 144 (+ Mega if opened),
        //    plus tests .onDisappear 2.4MB serialization, parsedReplies eviction, VStack layout, and FollowsEnd.
        //    Also open Mega once first so total cached replies exceed 150 (38 + 83 + 80 = 201 > 150!).
        switchViaDrawer(to: "Mega 80-Turn Architecture", landmark: "Comprehensive 80-Turn Stress Audit Complete")
        measure("02_WarmSwitch_BackAndForth_6x") {
            switchViaDrawer(to: "Switch B: Sidebar & New Session", landmark: "SWITCH_LANDMARK_B_READY")
            switchViaDrawer(to: "Switch A: Context Compression", landmark: "SWITCH_LANDMARK_A_READY")
            switchViaDrawer(to: "Switch C: CLI & Installer Verify", landmark: "SWITCH_LANDMARK_C_READY")
            switchViaDrawer(to: "Switch B: Sidebar & New Session", landmark: "SWITCH_LANDMARK_B_READY")
            switchViaDrawer(to: "Mega 80-Turn Architecture", landmark: "Comprehensive 80-Turn Stress Audit Complete")
            switchViaDrawer(to: "Switch D: Illustration Interrupt", landmark: "SWITCH_LANDMARK_D_READY")
        }

        // 3. Exact user-reported switch: from "修复插画中断逻辑" (Switch D) -> "优化 Her 上下文压缩" (Switch A)
        measure("03_ExactSwitch_Illustration_to_ContextCompression") {
            switchViaDrawer(to: "Switch A: Context Compression", landmark: "SWITCH_LANDMARK_A_READY")
        }

        // 4. Verify expanding a folded tool group and reading full tool input & output still works end-to-end
        measure("04_Verify_FoldedToolExpand_EndToEnd") {
            switchViaDrawer(to: "Switch B: Sidebar & New Session", landmark: "SWITCH_LANDMARK_B_READY")
            let summaries = app.descendants(matching: .any).matching(identifier: "tool-steps-summary")
            XCTAssertTrue(summaries.firstMatch.waitForExistence(timeout: 10), "folded tool summary")
            let target = summaries.allElementsBoundByIndex.last(where: \.isHittable) ?? summaries.firstMatch
            target.tap()
            let card = element("tool-steps")
            XCTAssertTrue(card.waitForExistence(timeout: 8), "tool steps card")
            let stepBtn = card.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Ran' OR label BEGINSWITH 'Read'"))
                .allElementsBoundByIndex.last { $0.isHittable && card.frame.contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY)) }
            if let stepBtn {
                stepBtn.tap()
                let out = card.descendants(matching: .any)["tool-output"].firstMatch
                XCTAssertTrue(out.waitForExistence(timeout: 8), "tool-output expanded")
                let deadline = Date().addingTimeInterval(5)
                var val = (out.value as? String) ?? ""
                while Date() < deadline && !val.contains("0123456789abcdef") {
                    usleep(50_000)
                    val = (out.value as? String) ?? ""
                }
                XCTAssertTrue(val.contains("0123456789abcdef"), "expected full tool body loaded, got prefix: \(val.prefix(120))")
            }
        }
    }

    private func element(_ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    private func switchViaDrawer(to title: String, landmark: String, file: StaticString = #filePath, line: UInt = #line) {
        let menu = app.buttons["menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10), "menu button", file: file, line: line)
        menu.tap()
        let drawer = element("drawer")
        XCTAssertTrue(drawer.waitForExistence(timeout: 8), "drawer opened", file: file, line: line)
        let row = drawer.descendants(matching: .any)["session-\(title)"].firstMatch
        for _ in 0 ..< 4 where !row.exists || !row.isHittable {
            drawer.swipeDown()
        }
        XCTAssertTrue(row.waitForExistence(timeout: 8), "drawer row \(title)", file: file, line: line)
        row.tap()
        let pred = NSPredicate(format: "label CONTAINS %@", landmark)
        let target = app.staticTexts.matching(pred).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 15), "session \(title) landmark '\(landmark)' rendered", file: file, line: line)
    }

    private func readSample() -> Sample {
        let m = element("frame-meter")
        XCTAssertTrue(m.waitForExistence(timeout: 10), "frame-meter")
        usleep(140_000)
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
        usleep(1_600_000)
        let a = readSample()
        let t0 = CFAbsoluteTimeGetCurrent()
        block()
        let actionMs = Int(((CFAbsoluteTimeGetCurrent() - t0) * 1000).rounded())
        let b = readSample()

        let wallMs = max(1, b.ms - a.ms)
        let df = max(0, b.frames - a.frames)
        let fps = Double(df) * 1000.0 / Double(wallMs)
        let dLate = max(0, b.late - a.late)
        let dSevere = max(0, b.severe - a.severe)
        let dHitchMs = max(0, b.hitchMs - a.hitchMs)
        let dCpuMs = max(0, b.cpuMs - a.cpuMs)
        let memMB = Double(b.memKB) / 1024.0

        let line = String(
            format: "SWITCH_BENCH %@ | actionMs=%d wallMs=%d fps=%.1f late=%d severe=%d hitchMs=%d maxGapMs=%d cpuMs=%d memMB=%.1f",
            name, actionMs, wallMs, fps, dLate, dSevere, dHitchMs, b.maxGap1500, dCpuMs, memMB
        )
        print(line)
        XCTContext.runActivity(named: line) { _ in }
    }
}
