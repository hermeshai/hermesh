import XCTest

/// Haptics (Apple's HIG, Playing haptics), as the log the app keeps under -HermeshHapticLog has them, against the
/// owner Mac: on a session's page, its agent asking while the page shows; then on Her's, her model her-mock: a
/// message sent; Her's first words since, once a turn (what she says after them in it plays none); an approval she
/// waits on; a question of hers; an answer tapped, which is a message sent like any other.
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
