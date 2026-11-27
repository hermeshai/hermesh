import XCTest

/// Files the owner sent with a prompt (docs/session-files.md §15), opened from its bubble: a page runs on its own page,
/// saying the files it refers to were not sent; Markdown reads as it looks; a picture opens in the session's Lightbox,
/// with no menu (it is the owner's own). On a wide screen (a Pro Max on its side) a page opens in a tab of the side
/// panel, its ⋯ sharing it. Run it on an iPhone Pro and a Pro Max.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testSessionAttachments go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testSessionAttachments() throws {
        try join()
        _ = try control("start?title=\(q("附件"))&file=page.html&file=notes.md&file=shot.png", method: "POST")
        openSession("附件")
        let page = app.buttons["prompt-attachment-page.html"]
        XCTAssertTrue(page.waitForExistence(timeout: 60), "the page, on the bubble")

        // A page: its own page, running; the script it refers to was not sent, and the page says so.
        page.tap()
        XCTAssertTrue(app.descendants(matching: .any)["session-attachment-page"].firstMatch.waitForExistence(timeout: 20), "the attachment's page")
        XCTAssertTrue(app.webViews.firstMatch.staticTexts["附件页"].waitForExistence(timeout: 30), "it runs")
        XCTAssertTrue(app.descendants(matching: .any)["attachment-alone"].firstMatch.exists, "only this file was sent")
        tap("file-more")
        XCTAssertTrue(app.buttons["file-share"].waitForExistence(timeout: 5), "Share…")
        app.buttons["file-share"].tap()
        dismissShareSheet()
        shotAttachments("attachment-page")
        back()

        // Markdown, as it reads.
        tap("prompt-attachment-notes.md", timeout: 20)
        XCTAssertTrue(app.staticTexts["Notes"].firstMatch.waitForExistence(timeout: 20), "the heading")
        back()

        // A picture, large, with no menu.
        tap("prompt-attachment-shot.png", timeout: 20)
        XCTAssertTrue(app.descendants(matching: .any)["session-image-lightbox"].firstMatch.waitForExistence(timeout: 10), "the Lightbox")
        XCTAssertTrue(app.descendants(matching: .any)["lightbox-image"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["lightbox-more"].exists, "the owner's own picture: no menu")
        app.buttons["lightbox-close"].tap()

        // On its side, where the screen is wide: the page in a tab of the side panel.
        XCUIDevice.shared.orientation = .landscapeLeft
        defer { XCUIDevice.shared.orientation = .portrait }
        sleep(2)
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        guard drawer.waitForExistence(timeout: 5), drawer.frame.width > 200 || app.descendants(matching: .any)["rail"].firstMatch.exists else { return }
        page.tap()
        let panel = app.descendants(matching: .any)["side-panel"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10), "the side panel")
        XCTAssertTrue(panel.webViews.firstMatch.staticTexts["附件页"].waitForExistence(timeout: 30), "the page, in its tab")
        XCTAssertTrue(panel.staticTexts["page"].waitForExistence(timeout: 5), "attached page tab drops .html suffix when untitled")
        tap("file-more")
        XCTAssertTrue(app.buttons["file-share"].waitForExistence(timeout: 5), "Share… on the tab")
        shotAttachments("attachment-panel")
    }

    /// One page back, to the conversation.
    private func back() {
        let page = app.descendants(matching: .any)["session-attachment-page"].firstMatch
        tap("BackButton")
        XCTAssertTrue(page.waitForNonExistence(timeout: 10), "back to the conversation")
    }

    private func q(_ s: String) -> String { s.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)! }

    /// The system's share sheet, put away.
    private func dismissShareSheet() {
        let sheet = app.otherElements["ActivityListView"].firstMatch
        if sheet.waitForExistence(timeout: 10) {
            sheet.swipeDown(velocity: .fast)
            _ = sheet.waitForNonExistence(timeout: 5)
        }
    }

    private func shotAttachments(_ name: String) {
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
