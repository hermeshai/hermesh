import XCTest

/// Haptics (Apple's HIG, Playing haptics), as the log the app keeps under -HermeshHapticLog has them, against the
/// owner Mac: on a session's page, its agent asking while the page shows, the answer taken, and the turn it lets end
/// (done, or failed); then on Her's, her model her-mock: a message sent; Her's first words since, once a turn (what
/// she says after them in it plays none); an approval she waits on; a question of hers; an answer tapped, which is a
/// message sent like any other; last, the Mac's screen: its session ending, and Reconnect bringing it back.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerHaptics go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testHerHaptics() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        app.terminate()
        app.launchArguments += ["-HermeshHapticLog"]
        app.launch()
        try join()
        // A session's agent asks while its page shows.
        _ = try control("ask?title=\("震一下吗".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)&wait=30s", method: "POST")
        openSession("震一下吗", timeout: 30)
        XCTAssertTrue(app.descendants(matching: .any)["question"].firstMatch.waitForExistence(timeout: 60), "the question")
        var heard = ["asks"]
        played(heard)
        // Answered, the turn goes on and is done: the answer taken, then the turn.
        try answerHere("震一下吗")
        heard += ["success", "success"]
        played(heard)
        // One whose turn fails once answered: the answer taken, then the failure.
        let fails = "fail 出错了".addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        _ = try control("ask?title=\("错一下吗".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)&wait=30s&then=sleep%202s%0A\(fails)", method: "POST")
        openSession("错一下吗", timeout: 30)
        XCTAssertTrue(app.descendants(matching: .any)["question"].firstMatch.waitForExistence(timeout: 60), "the question")
        heard += ["asks"]
        played(heard)
        try answerHere("错一下吗")
        heard += ["success", "error"]
        played(heard)

        try herTurnOn()
        try herOnboarding()
        heard = haptics()

        herSend("[[mock:send:你好，主人]] hi")
        XCTAssertTrue(app.staticTexts["你好，主人"].waitForExistence(timeout: 60), "Her's reply")
        heard += ["sent", "reply"]
        played(heard)

        herSend("[[mock:confirm]] hi")
        XCTAssertTrue(app.descendants(matching: .any)["her-approval"].firstMatch.waitForExistence(timeout: 60), "the approval")
        heard += ["sent", "reply", "asks"]
        played(heard)
        tap("her-allow-once")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "做完了")).firstMatch.waitForExistence(timeout: 60), "Her's word once it ran")
        sleep(2)
        played(heard)

        herSend("[[mock:ask:要哪种颜色？|蓝|红]] hi")
        XCTAssertTrue(app.buttons["蓝"].waitForExistence(timeout: 60), "the answers to tap")
        heard += ["sent", "asks"]
        played(heard)
        app.buttons["蓝"].tap()
        XCTAssertTrue(app.buttons["红"].waitForNonExistence(timeout: 30), "the answers fold away")
        XCTAssertEqual(Array(haptics().prefix(heard.count + 1)), heard + ["sent"])

        // The Mac's screen: its picture comes (none plays), its session ends (error), Reconnect brings it back (success).
        heard = haptics()
        try controlOpen()
        played(heard)
        _ = try control("screen-end", method: "POST")
        let reconnect = app.buttons["重新连接"]
        XCTAssertTrue(reconnect.waitForExistence(timeout: 30), "the session ended")
        heard += ["error"]
        played(heard)
        reconnect.tap()
        let surface = app.descendants(matching: .any)["screen-surface"].firstMatch
        try waitFor("the Mac's picture again", timeout: 90) { (surface.value as? String) == "live" ? true : nil }
        heard += ["success"]
        played(heard)
        tap("screen-close")
    }

    /// Answers the question on the session's page that shows, and waits for it to be taken.
    private func answerHere(_ title: String) throws {
        let panel = app.descendants(matching: .any)["question"]
        panel.buttons["发布"].firstMatch.tap()
        tap("answer")
        try waitFor("answered") { try self.sessionStatus(title) != "waiting_input" ? true : nil }
    }

    /// The haptics played so far, by name, in order.
    private func haptics() -> [String] {
        let log = app.descendants(matching: .any).matching(identifier: "haptics").firstMatch
        return ((log.value as? String) ?? "").split(separator: " ").map(String.init)
    }

    private func played(_ names: [String], file: StaticString = #filePath, line: UInt = #line) {
        if !waitUntil(timeout: 5, { self.haptics() == names }) {
            XCTFail("haptics: \(haptics()), not \(names)", file: file, line: line)
        }
    }
}
