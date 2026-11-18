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
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("menu"); tap("overview-place")
        XCTAssertTrue(app.buttons["Today"].isSelected, "demo does not inherit the real workspace's seeded time range")
    }

    func testChineseHerEntry() {
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
        tap("try-demo")
        tap("委派示例任务")
        tap("her-deny")
        XCTAssertTrue(words("已拒绝权限").waitForExistence(timeout: 10))
    }

    func testDelegateResultsAndProgress() {
        tap("try-demo")
        tap("Delegate a sample task")
        tap("her-allow-once")
        XCTAssertTrue(words("Sample task complete.").waitForExistence(timeout: 20))
        tap("her-session-Her sample task")
        XCTAssertTrue(words("Welcome from Her!").waitForExistence(timeout: 15))
        tap("session-info")
        let file = app.staticTexts["src/welcome.ts"]
        for _ in 0..<4 where !file.isHittable { app.swipeUp() }
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
        XCTAssertTrue(words("intentional simulated error").waitForExistence(timeout: 20))
        tap("Retry sample task")
        tap("her-allow-once")
        XCUIDevice.shared.press(.home)
        app.activate()
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
        XCTAssertFalse(words("Welcome from Her!").waitForExistence(timeout: 9), "past the demo completion deadline, a stopped task stays stopped")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(words("The simulated task was stopped.").exists)
        XCTAssertFalse(words("Sample task complete.").exists)
    }

    private func words(_ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func tap(_ id: String) {
        let element = app.descendants(matching: .any)[id].firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: 15), id)
        element.tap()
    }
}
