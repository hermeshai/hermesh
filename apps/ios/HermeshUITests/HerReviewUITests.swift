import XCTest

/// The public reviewer path, with the app built in Release and no account or external service.
final class HerReviewUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
    }

    func testPublicHerEntry() {
        tap("try-demo")
        XCTAssertTrue(app.staticTexts["Demo Mode"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 15), "Release demo opens Her without credentials or a computer")
        XCTAssertFalse(app.buttons["her-attach"].isEnabled, "demo never reads personal photos or files")
        tap("her-avatar")
        XCTAssertTrue(app.staticTexts["Her uses preset sample tasks in this demo. No model, account, computer or service is connected."].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["Connect a Model"].exists)
        XCTAssertFalse(app.buttons["her-ai-sharing-revoke"].exists)
        XCTAssertFalse(app.buttons["Revoke Third-Party AI Sharing"].exists)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("menu"); tap("overview-place")
        XCTAssertTrue(app.buttons["Today"].isSelected, "demo does not inherit the real workspace's seeded time range")
    }

    func testChineseHerEntry() {
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
        tap("try-demo")
        tap("her-avatar")
        XCTAssertTrue(app.staticTexts["Her 在演示中使用预设示例任务，未连接模型、账户、电脑或服务。"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["her-ai-sharing-revoke"].exists)
        XCTAssertFalse(app.buttons["撤回第三方 AI 共享授权"].exists)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("委派示例任务")
        tap("her-deny")
        XCTAssertTrue(words("已拒绝权限").waitForExistence(timeout: 10))
        tap("exit-demo"); tap("try-demo")
        tap("委派示例任务"); tap("her-allow-once"); tap("显示示例结果")
        XCTAssertTrue(words("示例任务完成").waitForExistence(timeout: 10))
    }

    func testDelegateResultsAndProgress() {
        tap("try-demo")
        tap("Delegate a sample task")
        tap("her-allow-once")
        tap("Show sample result")
        XCTAssertTrue(words("Sample task complete.").waitForExistence(timeout: 20))
        tap("her-session-Her sample task")
        XCTAssertTrue(words("Welcome from Her!").waitForExistence(timeout: 15))
        tap("session-info")
        XCTAssertTrue(words("Linux Studio").waitForExistence(timeout: 10), "Her's Mac delegated to the sample Linux computer")
        let file = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "src/welcome.ts")).firstMatch
        for _ in 0..<5 where !file.exists || !file.isHittable || file.frame.maxY > app.frame.height - 120 { app.swipeUp() }
        XCTAssertTrue(file.exists, "src/welcome.ts")
        file.tap()
        XCTAssertTrue(words("+export const greeting").waitForExistence(timeout: 10))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("close")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("her-progress")
        tap("progress-card-Check sample change")
        XCTAssertTrue(app.descendants(matching: .any)["progress-record"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(words("Inspect the sample diff").exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Release Her demo: sample review record"; shot.lifetime = .keepAlways; add(shot)
    }

    func testDenyFailureRetryAndReset() {
        tap("try-demo")
        tap("Delegate a sample task")
        tap("her-deny")
        XCTAssertTrue(words("Permission denied.").waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["her-session-Her sample task"].exists)
        tap("exit-demo")
        tap("try-demo")
        tap("Simulate a failure")
        tap("her-allow-once")
        tap("Show sample result")
        XCTAssertTrue(words("intentional simulated error").waitForExistence(timeout: 20))
        tap("Retry sample task")
        tap("her-allow-once")
        XCUIDevice.shared.press(.home)
        app.activate()
        XCTAssertFalse(words("Sample task complete.").exists, "the reviewer controls when the preset result appears")
        tap("Show sample result")
        XCTAssertTrue(words("Sample task complete.").waitForExistence(timeout: 20))
        tap("exit-demo")
        tap("try-demo")
        XCTAssertFalse(words("Sample task complete.").exists)
        tap("Delegate a sample task")
        tap("her-allow-once")
        tap("exit-demo")
        tap("try-demo")
        XCTAssertTrue(app.buttons["Delegate a sample task"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["her-session-Her sample task"].exists)
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 15), "relaunch restores onboarding, never a demo account")
    }

    func testInterruptedCodingTaskStaysStopped() {
        tap("try-demo")
        tap("Delegate a sample task")
        tap("her-allow-once")
        tap("her-session-Her sample task")
        tap("interrupt"); tap("interrupt-confirm")
        XCTAssertTrue(words("The simulated task was stopped.").waitForExistence(timeout: 10))
        XCTAssertFalse(words("Welcome from Her!").exists, "stopping never emits the preset completion")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["Retry sample task"].waitForExistence(timeout: 10), "Her shows the stopped task after returning")
        XCTAssertTrue(words("The simulated task was stopped.").waitForExistence(timeout: 5))
        XCTAssertFalse(words("Sample task complete.").exists)
    }

    private func words(_ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func tap(_ id: String) {
        let element = app.buttons[id].firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: 15), id)
        element.tap()
    }
}
