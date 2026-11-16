import XCTest

/// Her's page (docs/her-panel-demo.html) in the demo of a debug build (-HerDemo, DemoHer.swift), no Mac needed: her
/// mark opens it; three tabs, each card leading to its page and back; Remote Desktop at the top right. The demo answers
/// none of her environment's calls, so the pages show what they show before her answers: what is checked here is
/// where everything is and where it leads. testHerPageTabs (HerUITests) checks what they hold, against her.
final class HerPageTabsUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-HerDemo", "3"]
        app.launch()
        tap("try-demo")
        tap("menu")
        tap("her-place")
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 20), "Her's conversation")
    }

    func testTabsAndTheirPages() {
        // The conversation's top: her, and Progress, looked at often, at the top right; the rest of her work is on her
        // page.
        let screen = app.windows.firstMatch.frame
        let progress = app.buttons["her-progress"]
        XCTAssertTrue(progress.waitForExistence(timeout: 10) && progress.frame.minX > screen.midX, "Progress over the conversation, on the right")
        for id in ["her-files", "her-routines"] { XCTAssertFalse(app.buttons[id].exists, "no \(id) over the conversation") }
        progress.tap()
        XCTAssertTrue(app.navigationBars["进展"].waitForExistence(timeout: 10), "Progress opens")
        shot("进展")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("her-avatar")
        let work = app.buttons["her-tab-work"]
        XCTAssertTrue(work.waitForExistence(timeout: 10), "her page")
        // The tab chosen last is kept: Work, for this test.
        tap("her-tab-work")
        XCTAssertTrue(work.isSelected, "Work")
        shot("her page, work")
        XCTAssertFalse(app.buttons["her-progress"].exists, "no Progress card: it is over the conversation")
        let remote = app.buttons["her-computer-control"]
        if remote.exists { XCTAssertGreaterThan(remote.frame.minX, screen.midX, "Remote Desktop at the top right") }

        // Each tab's cards, each opening its page and coming back to the same tab.
        let tabs: [(tab: String, cards: [(id: String, title: String)])] = [
            ("work", [("her-files", "文件"), ("her-routines", "定时任务")]),
            ("abilities", [("her-memory", "记忆"), ("her-skills", "技能"), ("her-connectors", "连接器"), ("her-search", "联网搜索"), ("her-browser", "浏览器")]),
        ]
        for (tab, cards) in tabs {
            tap("her-tab-\(tab)")
            XCTAssertTrue(app.buttons["her-tab-\(tab)"].isSelected, tab)
            shot("her page, \(tab)")
            for card in cards {
                tap(card.id)
                XCTAssertTrue(app.navigationBars[card.title].waitForExistence(timeout: 10), "\(card.id) opens \(card.title)")
                shot(card.title)
                app.navigationBars.buttons.element(boundBy: 0).tap()
                XCTAssertTrue(app.buttons["her-tab-\(tab)"].waitForExistence(timeout: 10) && app.buttons["her-tab-\(tab)"].isSelected, "back on \(tab)")
            }
        }

        // Settings: the Model and Environment pages, and Storage under the second.
        tap("her-tab-settings")
        XCTAssertTrue(app.buttons["her-environment"].waitForExistence(timeout: 10) && text("清空对话").exists)
        shot("her page, settings")
        tap("her-model-page")
        XCTAssertTrue(app.navigationBars["模型"].waitForExistence(timeout: 10))
        shot("model")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("her-environment")
        XCTAssertTrue(app.navigationBars["环境"].waitForExistence(timeout: 10) && text("运行在").exists)
        XCTAssertTrue(app.buttons["her-turn-off"].exists && app.buttons["her-console"].exists && app.buttons["her-storage"].exists)
        shot("environment")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        // The tab chosen last is the one her page opens on.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 10), "back to the conversation")
        tap("her-avatar")
        XCTAssertTrue(app.buttons["her-tab-settings"].waitForExistence(timeout: 10) && app.buttons["her-tab-settings"].isSelected, "settings again")
    }

    func testLargeText() {
        // At a large text size: the cards keep their words, and the tabs fit.
        app.terminate()
        app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryXXXL"]
        app.launch()
        let demo = app.descendants(matching: .any)["try-demo"].firstMatch
        if demo.waitForExistence(timeout: 5) { demo.tap() }
        tap("menu")
        tap("her-place")
        tap("her-avatar")
        for tab in ["work", "abilities", "settings"] {
            tap("her-tab-\(tab)")
            let t = app.buttons["her-tab-\(tab)"]
            XCTAssertTrue(t.isHittable && t.frame.width > 80, "\(tab): \(t.frame)")
            sleep(1)
            shot("large text, \(tab)")
        }
    }

    private func text(_ s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    private func tap(_ id: String) {
        let e = app.descendants(matching: .any)[id].firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: 15), "no \(id)")
        if e.isHittable { e.tap() } else { e.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap() }
    }

    private func shot(_ name: String) {
        let s = XCTAttachment(screenshot: app.screenshot())
        s.name = name
        s.lifetime = .keepAlways
        add(s)
    }
}
