import XCTest

/// End-to-end iPhone UI tests for inline code-block execution, secret masking,
/// destructive confirmation, live Stop, attached bottom terminal drawer, and Send to Chat (`↑ 发入对话`).
final class CodeBlockRunnerUITests: XCTestCase {
    let app = XCUIApplication()

    private func launchSession(chinese: Bool = false) {
        continueAfterFailure = false
        app.launchArguments = chinese
            ? ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-DemoCodeBlockRunner"]
            : ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoCodeBlockRunner"]
        app.launch()
        let tryDemo = app.buttons["try-demo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 15), "try-demo")
        tryDemo.tap()
        tap("menu")
        tap("overview-place")
        let row = app.descendants(matching: .any)["session-Code block runner session"].firstMatch
        for _ in 0 ..< 4 where !row.exists || !row.isHittable { scrollPage(up: false) }
        for _ in 0 ..< 4 where !row.exists || !row.isHittable { scrollPage(up: true) }
        XCTAssertTrue(row.waitForExistence(timeout: 10), "Code block runner session row")
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "session detail opened")
    }

    func testInlineRunnerStopConfirmSecretsAndSendToChat() throws {
        launchSession(chinese: false)

        // 1. Secret masking hides raw API key and hides Run on that block's header until revealed.
        let maskedText = scrollToCodeText(containing: "sk-p••••••••")
        XCTAssertTrue(maskedText.exists, "masked API key placeholder shown")
        XCTAssertFalse(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "1234567890abcdefghij")).firstMatch.exists,
            "raw API key should be masked initially"
        )
        let secretToggle = app.buttons["code-secret-toggle"].firstMatch
        XCTAssertTrue(secretToggle.waitForExistence(timeout: 5), "secret toggle button shown")
        XCTAssertNil(
            buttonOnRow("code-run", midY: secretToggle.frame.midY),
            "Run must be hidden on the secret block header while secrets are masked"
        )
        secretToggle.tap()
        XCTAssertTrue(
            waitUntil(timeout: 3) {
                self.app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "1234567890abcdefghij")).firstMatch.exists
            },
            "revealing secrets shows full token"
        )
        XCTAssertTrue(
            waitUntil(timeout: 3) { self.buttonOnRow("code-run", midY: secretToggle.frame.midY) != nil },
            "Run appears on the secret block header once secrets are revealed"
        )

        // 2. Destructive command requires inline confirmation before running.
        let destructiveText = scrollToCodeText(containing: "rm -rf ./dist")
        let destructiveRun = try XCTUnwrap(headerButton("code-run", above: destructiveText), "destructive Run… button")
        destructiveRun.tap()
        let confirmRun = app.buttons["code-confirm-run"].firstMatch
        XCTAssertTrue(confirmRun.waitForExistence(timeout: 5), "destructive confirmation Run Anyway button appears")
        confirmRun.tap()
        XCTAssertTrue(
            waitUntil(timeout: 5) { self.hasTerminalOutput(containing: "cleaned ./dist") },
            "destructive command output streamed into drawer"
        )

        // 3. Long-running command exposes touch Stop button and stops immediately.
        let sleepText = scrollToCodeText(containing: "sleep 5")
        let sleepRun = try XCTUnwrap(headerButton("code-run", above: sleepText), "sleep Run button")
        sleepRun.tap()
        let stopBtn = app.buttons["code-stop"].firstMatch
        XCTAssertTrue(stopBtn.waitForExistence(timeout: 5), "red Stop button appears while running")
        stopBtn.tap()
        XCTAssertTrue(
            waitUntil(timeout: 5) {
                self.app.buttons.matching(identifier: "code-term-toggle").allElementsBoundByIndex.contains {
                    $0.label.contains("Stopped ·") || (($0.value as? String)?.contains("Stopped ·") == true)
                }
            },
            "stopping transitions drawer status to Stopped"
        )

        // 4. Multi-line script runs silently in place (preserving script on top), folds/unfolds, and sends output to chat on demand.
        let scriptText = scrollToCodeText(containing: "for entry in README.md")
        let scriptRun = try XCTUnwrap(headerButton("code-run", above: scriptText), "multi-line script Run button")
        scriptRun.tap()
        XCTAssertTrue(
            waitUntil(timeout: 5) {
                self.hasTerminalOutput(containing: "README.md") && self.hasTerminalOutput(containing: "package.json")
            },
            "multi-line script streams directory listing into drawer"
        )
        XCTAssertTrue(
            scriptText.exists && scriptText.label.contains("#!/usr/bin/env bash"),
            "original multi-line script preserved on top"
        )
        XCTAssertFalse(
            app.staticTexts["Simulated follow-up reply in Demo Mode."].exists,
            "running in place must not automatically send output to the conversation"
        )

        // Fold and unfold the terminal output body via the left status toggle right below `scriptText`.
        let toggle = try XCTUnwrap(drawerButton("code-term-toggle", below: scriptText), "script drawer status toggle")
        toggle.tap()
        XCTAssertTrue(
            waitUntil(timeout: 3) { !self.hasTerminalOutput(containing: "package.json") },
            "tapping status toggle folds terminal output body"
        )
        let toggleAgain = try XCTUnwrap(drawerButton("code-term-toggle", below: scriptText), "script drawer status toggle when folded")
        toggleAgain.tap()
        XCTAssertTrue(
            waitUntil(timeout: 3) { self.hasTerminalOutput(containing: "package.json") },
            "tapping status toggle again unfolds terminal output body"
        )

        // Tap `↑ Send to Chat`: dispatches formatted command result into the active conversation.
        let sendBtn = try XCTUnwrap(drawerButton("code-send-to-chat", below: scriptText), "script Send to Chat button")
        sendBtn.tap()
        XCTAssertTrue(
            app.staticTexts["Simulated follow-up reply in Demo Mode."].waitForExistence(timeout: 8),
            "Send to Chat dispatches formatted command result into the session conversation"
        )
        XCTAssertTrue(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Command result (exit code 0):")).firstMatch.exists,
            "dispatched user bubble contains exit code header"
        )
    }

    func testChineseInlineRunnerAndSendToChatLabels() throws {
        launchSession(chinese: true)

        let scriptText = scrollToCodeText(containing: "for entry in README.md")
        let runBtn = try XCTUnwrap(headerButton("code-run", above: scriptText), "Chinese Run button")
        XCTAssertTrue(runBtn.label.contains("运行"), "expected 运行 label, got \(runBtn.label)")
        runBtn.tap()

        XCTAssertTrue(
            waitUntil(timeout: 5) {
                self.app.buttons.matching(identifier: "code-term-toggle").allElementsBoundByIndex.contains {
                    $0.label.contains("退出码 0 ·") || (($0.value as? String)?.contains("退出码 0 ·") == true)
                }
            },
            "Chinese exit status shown"
        )

        let sendBtn = try XCTUnwrap(drawerButton("code-send-to-chat", below: scriptText), "发入对话 button")
        XCTAssertTrue(sendBtn.label.contains("发入对话"), "expected 发入对话 label, got \(sendBtn.label)")
        sendBtn.tap()

        XCTAssertTrue(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "运行结果（退出码 0）：")).firstMatch.waitForExistence(timeout: 8),
            "Chinese formatted result dispatched to conversation"
        )
    }

    // MARK: - Helpers

    /// Safe vertical drag inside the middle of the window so iOS Notification Center / Home Bar are never triggered.
    private func scrollPage(up: Bool) {
        let win = app.windows.firstMatch
        let start = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.34 : 0.72))
        let end = win.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.76 : 0.30))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0.2)
        usleep(200_000)
    }

    private func scrollToCodeText(containing snippet: String) -> XCUIElement {
        let query = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", snippet))
        func visible() -> XCUIElement? {
            let win = app.windows.firstMatch.frame
            let el = query.firstMatch
            guard el.exists else { return nil }
            let f = el.frame
            // Leave room above for the 30pt code-block header and below for the composer/drawer.
            return (f.height > 0 && f.minY >= win.minY + 125 && f.maxY <= win.maxY - 140) ? el : nil
        }
        if let v = visible() { return v }
        for _ in 0 ..< 6 {
            scrollPage(up: true)
            if let v = visible() { return v }
        }
        for _ in 0 ..< 6 {
            scrollPage(up: false)
            if let v = visible() { return v }
        }
        return query.firstMatch
    }

    private func buttonOnRow(_ identifier: String, midY: CGFloat) -> XCUIElement? {
        app.buttons.matching(identifier: identifier).allElementsBoundByIndex.first {
            $0.exists && abs($0.frame.midY - midY) < 18
        }
    }

    private func headerButton(_ identifier: String, above codeText: XCUIElement) -> XCUIElement? {
        let targetTop = codeText.frame.minY
        return app.buttons.matching(identifier: identifier).allElementsBoundByIndex
            .filter { $0.exists && $0.frame.maxY <= targetTop + 12 && $0.frame.minY >= targetTop - 55 }
            .min(by: { abs($0.frame.maxY - targetTop) < abs($1.frame.maxY - targetTop) })
    }

    private func drawerButton(_ identifier: String, below codeText: XCUIElement) -> XCUIElement? {
        let targetBottom = codeText.frame.maxY
        return app.buttons.matching(identifier: identifier).allElementsBoundByIndex
            .filter { $0.exists && $0.frame.minY >= targetBottom - 8 && $0.frame.minY <= targetBottom + 80 }
            .min(by: { abs($0.frame.minY - targetBottom) < abs($1.frame.minY - targetBottom) })
    }

    private func hasTerminalOutput(containing substring: String) -> Bool {
        app.textViews.allElementsBoundByIndex.contains { tv in
            tv.exists && ((tv.value as? String)?.contains(substring) == true)
        }
    }

    private func tap(_ identifier: String) {
        let item = app.descendants(matching: .any)[identifier].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 15), identifier)
        item.tap()
    }

    private func waitUntil(timeout: TimeInterval, _ check: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if check() { return true }
            usleep(150_000)
        }
        return false
    }
}
