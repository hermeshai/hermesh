import XCTest

/// A coding session's files on the iPhone (docs/session-files.md §4), as the Mac shows them (session-files.spec.ts),
/// against files-mac, another machine over the relay (otherMacFiles in e2e/desktop_test.go): the files a reply links
/// are tags; a page opens on a page of its own and runs, as the reply's turn left it, the current one a tap away;
/// code opens at the line the reply named; a link to nothing is no link; the session's folder, from ⋯, a level at a time;
/// on a wide screen the page takes the menu's room, as a session's does. Run it on an iPhone Pro and a Pro Max.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testSessionFiles go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testSessionFiles() throws {
        try join()
        openSession("文件预览")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "原型")).firstMatch.waitForExistence(timeout: 60), "the reply")

        // The page, as turn 1 left it: it runs, its script beside it read too.
        let page = tag("docs/demo.html")
        XCTAssertTrue(page.waitForExistence(timeout: 30), "demo.html is a tag")
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
        // No source to switch to; an iPhone's page is the screen's already: no button to widen it.
        XCTAssertFalse(app.buttons["file-source"].exists, "a page has no source button")
        XCTAssertFalse(app.descendants(matching: .any)["menu-hide"].exists, "nothing to widen upright")
        bar.buttons["查看当前版本"].tap()
        XCTAssertTrue(app.webViews.firstMatch.staticTexts["第二版"].waitForExistence(timeout: 30), "the current version")
        XCTAssertTrue(app.descendants(matching: .any)["file-version"].staticTexts["这是当前版本"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()

        // Code, at the line the reply named, in view.
        let lines = tag("src/lines.txt", line: 30)
        XCTAssertTrue(lines.waitForExistence(timeout: 20), "inline code that is a file is a tag")
        lines.tap()
        let line = app.staticTexts["line 30"].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 20), "the file's code")
        XCTAssertTrue(line.isHittable, "line 30 in view")
        shot("files-code-line")
        app.navigationBars.buttons.element(boundBy: 0).tap()

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
        } else {
            XCTAssertFalse(widen.exists, "no wide screen here: nothing to widen")
        }
    }

    /// A reply's tag, by the file it opens (its words are the reply's).
    private func tag(_ path: String, line: Int? = nil) -> XCUIElement {
        app.links.matching(NSPredicate(format: "identifier ENDSWITH %@", "p=\(path)" + (line.map { "&l=\($0)" } ?? ""))).firstMatch
    }

    private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
