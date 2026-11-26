import XCTest

/// A coding session's files on the iPhone (docs/session-files.md §4), as the Mac shows them (session-files.spec.ts),
/// against files-mac, another machine over the relay (otherMacFiles in e2e/desktop_test.go): the files a reply links
/// are tags; a page opens on a page of its own and runs, as the reply's turn left it, the current one a tap away;
/// code opens at the line the reply named; a link to nothing is no link; a file's page pulls the folder's tree up from its
/// foot, and a file picked there opens in its place; the session's folder, from ⋯, a level at a time; on a wide screen the
/// page takes the menu's room, as a session's does, and the session has the side panel's tabs (docs/session-files.md
/// §15). Run it on an iPhone Pro and a Pro Max.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testSessionFiles go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testSessionFiles() throws {
        try join()
        openSession("文件预览")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "原型")).firstMatch.waitForExistence(timeout: 60), "the reply")

        // Explicit links are clickable tags on the first frame, before ResolveFiles answers; inline code joins once it
        // does, and missing or outside links drop theirs.
        let page = tag("docs/demo.html")
        let fileURLTag = app.links.containing(NSPredicate(format: "label CONTAINS %@", "新版")).firstMatch
        XCTAssertTrue(page.exists, "demo.html is a clickable tag at once")
        XCTAssertTrue(fileURLTag.exists, "a file:// link is a clickable tag at once")
        let lines = tag("src/lines.txt", line: 30)
        XCTAssertTrue(lines.waitForExistence(timeout: 20), "inline code that is a file is a tag")
        XCTAssertFalse(tag("gone.md").exists, "a link to nothing is no link")
        XCTAssertFalse(tag("/etc/hosts").exists, "a file outside the session's folder is no link")
        shot("files-tags")
        page.tap()
        XCTAssertTrue(app.descendants(matching: .any)["session-file-page"].firstMatch.waitForExistence(timeout: 20), "the file's page")
        let web = app.webViews.firstMatch
        XCTAssertTrue(web.staticTexts["第一版"].waitForExistence(timeout: 30), "the page as turn 1 left it")
        web.buttons["点我"].tap()
        XCTAssertTrue(web.staticTexts["点过了"].waitForExistence(timeout: 10), "its script runs")
        let bar = app.descendants(matching: .any)["file-version"].firstMatch
        XCTAssertTrue(bar.waitForExistence(timeout: 10))
        XCTAssertTrue(bar.staticTexts["这是第 1 轮结束时的版本，之后改过"].exists, "the version bar")
        shot("files-page-turn")
        // No source to switch to; an iPhone's page is the screen's already: no button to widen it. A page has no tree.
        XCTAssertFalse(app.buttons["file-source"].exists, "a page has no source button")
        XCTAssertFalse(app.buttons["file-drawer"].exists, "a page has no tree under it")
        XCTAssertFalse(app.descendants(matching: .any)["menu-hide"].exists, "nothing to widen upright")
        bar.buttons["查看当前版本"].tap()
        XCTAssertTrue(app.webViews.firstMatch.staticTexts["第二版"].waitForExistence(timeout: 30), "the current version")
        XCTAssertTrue(app.descendants(matching: .any)["file-version"].staticTexts["这是当前版本"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()

        // Turn 2's file:// link opens the page too; code opens at the line the reply named, in view.
        fileURLTag.tap()
        XCTAssertTrue(app.webViews.firstMatch.staticTexts["第二版"].waitForExistence(timeout: 30), "the file:// page")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        XCTAssertTrue(lines.waitForExistence(timeout: 20), "inline code that is a file is a tag")
        lines.tap()
        let line = app.staticTexts["line 30"].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 20), "the file's code")
        XCTAssertTrue(line.isHittable, "line 30 in view")
        shot("files-code-line")
        // ⋯: Share… and Copy Path.
        tap("file-more")
        XCTAssertTrue(app.buttons["file-share"].waitForExistence(timeout: 5), "Share…")
        tap("file-copy-path")
        // The tree at the page's foot, pulled up: the file marked; a file picked there opens in this page.
        let treeBar = app.buttons["file-drawer"]
        XCTAssertTrue(treeBar.waitForExistence(timeout: 5), "the tree's bar at the foot")
        XCTAssertGreaterThan(treeBar.frame.minY, app.frame.height * 0.8, "at the page's foot")
        treeBar.tap()
        let sheet = app.descendants(matching: .any)["file-tree-sheet"].firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 10), "the tree, pulled up")
        XCTAssertTrue(app.buttons["tree-src/lines.txt"].waitForExistence(timeout: 20), "opened to the file")
        XCTAssertTrue(app.buttons["tree-src/lines.txt"].isSelected, "the file marked")
        shot("files-drawer")
        tap("tree-README.md", timeout: 20)
        XCTAssertTrue(sheet.waitForNonExistence(timeout: 10), "the tree goes back down")
        XCTAssertTrue(app.navigationBars["README.md"].waitForExistence(timeout: 20), "README.md in the same page")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "site")).firstMatch.waitForExistence(timeout: 20), "its words")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(tag("docs/demo.html").waitForExistence(timeout: 10), "one page back: the conversation")

        // Turn 3's external simulator screenshot renders inline once while its file:// link stays a clickable tag,
        // before/after PNGs sit side-by-side in one row, and tapping opens the full-screen blurred Lightbox.
        let shotCard = app.buttons["session-image-ios_simulator_screenshot.png"]
        let beforeCard = app.buttons["session-image-before.png"]
        let afterCard = app.buttons["session-image-after.png"]
        let shotTag = app.links.containing(NSPredicate(format: "label CONTAINS %@", "ios_simulator_screenshot.png")).firstMatch
        XCTAssertTrue(shotCard.waitForExistence(timeout: 20), "inline simulator screenshot card")
        XCTAssertEqual(app.buttons.matching(identifier: "session-image-ios_simulator_screenshot.png").count, 1, "deduplicated inline screenshot")
        XCTAssertTrue(shotTag.exists, "deduplicated file:// tag remains clickable")
        XCTAssertTrue(beforeCard.waitForExistence(timeout: 20) && afterCard.waitForExistence(timeout: 20), "comparison strip images")
        XCTAssertEqual(beforeCard.frame.minY, afterCard.frame.minY, accuracy: 8, "2-column side-by-side layout on iPhone")
        shot("files-inline-images")

        shotCard.tap()
        let lb = app.descendants(matching: .any)["session-image-lightbox"].firstMatch
        XCTAssertTrue(lb.waitForExistence(timeout: 10), "blurred Lightbox overlay")
        XCTAssertEqual(app.staticTexts["lightbox-title"].label, "ios_simulator_screenshot.png")
        XCTAssertTrue(app.staticTexts["lightbox-meta"].label.contains("1 / 3 · 120 × 260"), "Lightbox metadata")
        XCTAssertTrue(app.buttons["lightbox-more"].exists && app.buttons["lightbox-close"].exists, "only More and Close in Lightbox top bar")
        shot("files-image-lightbox")
        app.descendants(matching: .any)["lightbox-image"].firstMatch.swipeLeft()
        XCTAssertTrue(app.staticTexts["lightbox-title"].waitForExistence(timeout: 5))
        XCTAssertEqual(app.staticTexts["lightbox-title"].label, "before.png")
        app.buttons["lightbox-close"].tap()
        XCTAssertTrue(lb.waitForNonExistence(timeout: 5), "Lightbox closes")

        // The session's folder from ⋯, a level at a time: folders first; a file in one opens its page, as it is now.
        tap("session-more")
        tap("session-files")
        XCTAssertTrue(app.descendants(matching: .any)["session-folder-page"].firstMatch.waitForExistence(timeout: 20), "Project Files")
        let docs = app.buttons["folder-entry-docs"]
        XCTAssertTrue(docs.waitForExistence(timeout: 20), "the folder's entries")
        XCTAssertLessThan(docs.frame.minY, app.buttons["folder-entry-README.md"].frame.minY, "folders first")
        shot("files-folder")
        docs.tap()
        tap("folder-entry-demo.js", timeout: 20)
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "document.title")).firstMatch.waitForExistence(timeout: 20), "demo.js as it is now")
        XCTAssertFalse(app.descendants(matching: .any)["file-version"].exists, "no version bar for a file as it is now")

        // On its side, where the screen is wide (a Pro Max, iPhone Duo open), the menu stands beside the page: the
        // page's button widens it over the menu, which shrinks to its rail, as a session's page does. Elsewhere, none.
        app.terminate()
        app.launchArguments += ["-HermeshTestTurns", "YES"]
        app.launch()
        openSession("文件预览")
        XCTAssertTrue(tag("docs/demo.html").waitForExistence(timeout: 30))
        tag("docs/demo.html").tap()
        XCTAssertTrue(app.webViews.firstMatch.staticTexts["第一版"].waitForExistence(timeout: 30))
        XCUIDevice.shared.orientation = .landscapeLeft
        defer { XCUIDevice.shared.orientation = .portrait }
        sleep(2)
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        let widen = app.descendants(matching: .any)["menu-hide"].firstMatch
        if drawer.waitForExistence(timeout: 5) && drawer.frame.width > 200 {
            XCTAssertTrue(widen.waitForExistence(timeout: 5), "the page's button beside the menu")
            XCTAssertGreaterThan(widen.frame.minX, drawer.frame.maxX, "in the page's bar")
            let before = app.webViews.firstMatch.frame.width
            shot("files-page-wide")
            widen.tap()
            XCTAssertTrue(app.descendants(matching: .any)["rail"].firstMatch.waitForExistence(timeout: 5), "the menu shrinks to its rail")
            XCTAssertGreaterThan(app.webViews.firstMatch.frame.width, before + 100, "the page takes the menu's room")
            XCTAssertFalse(widen.exists, "the rail expands it again, not the page")
            shot("files-page-widened")

            // The session's side panel: its button beside ⋯; opened empty, 项目文件 with sizes.
            app.navigationBars.buttons.element(boundBy: 0).tap()
            tap("session-side-panel", timeout: 10)
            let panel = app.descendants(matching: .any)["side-panel"].firstMatch
            XCTAssertTrue(panel.waitForExistence(timeout: 10), "the side panel beside the conversation")
            XCTAssertTrue(app.descendants(matching: .any)["rail"].firstMatch.exists, "the menu as its rail")
            let tabs = app.descendants(matching: .any).matching(identifier: "side-panel-tab")
            XCTAssertEqual(tabs.count, 1)
            XCTAssertTrue(app.buttons["tree-README.md"].waitForExistence(timeout: 20), "项目文件: the tree alone")
            // A reply's page: a tab of its own, running.
            tag("docs/demo.html").tap()
            XCTAssertTrue(panel.webViews.firstMatch.staticTexts["第一版"].waitForExistence(timeout: 30), "the page, in its tab")
            XCTAssertEqual(tabs.count, 2)
            XCTAssertTrue(tag("docs/demo.html").isHittable, "the conversation beside it")
            shot("files-panel-page")
            // Code at its line, in a tab of its own, the tree hidden; shown, a file picked there opens in this tab.
            tag("src/lines.txt", line: 30).tap()
            XCTAssertTrue(app.staticTexts["line 30"].firstMatch.waitForExistence(timeout: 20), "code at its line")
            XCTAssertEqual(tabs.count, 3)
            XCTAssertFalse(app.buttons["tree-src/lines.txt"].exists, "the tree hidden at first")
            tap("file-tree-toggle")
            XCTAssertTrue(app.buttons["tree-src/lines.txt"].waitForExistence(timeout: 10), "the tree beside the file")
            shot("files-panel-code-tree")
            tap("tree-README.md")
            XCTAssertTrue(app.descendants(matching: .any)["side-panel-path"].firstMatch.waitForExistence(timeout: 10))
            XCTAssertTrue(waitUntil(timeout: 10) { app.descendants(matching: .any)["side-panel-path"].firstMatch.label == "README.md" }, "README.md in the same tab")
            XCTAssertEqual(tabs.count, 3)
            // Enlarged, it takes the page; shrunk, the conversation is back.
            tap("side-panel-max")
            XCTAssertTrue(waitUntil(timeout: 5) { !tag("docs/demo.html").isHittable }, "the panel takes the page")
            shot("files-panel-max")
            tap("side-panel-max")
            XCTAssertTrue(waitUntil(timeout: 5) { tag("docs/demo.html").isHittable }, "beside the conversation again")
            tap("session-side-panel")
            XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "put away")
        } else {
            XCTAssertFalse(widen.exists, "no wide screen here: nothing to widen")
        }
    }

    /// A reply's tag, by the file it opens (its words are the reply's).
    private func tag(_ path: String, line: Int? = nil) -> XCUIElement {
        app.links.matching(NSPredicate(format: "identifier ENDSWITH %@", "p=\(path)" + (line.map { "&l=\($0)" } ?? ""))).firstMatch
    }

    private func shot(_ name: String) {
        // The whole screen: on its side, the app's own screenshot comes out turned and cut.
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
