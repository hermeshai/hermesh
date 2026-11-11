import XCTest

/// Computer Control on the iPhone (docs/computer-control.md), as a person
/// uses it, against the owner Mac of TestIPhone. That Mac's screen helper
/// shows its test picture (640 × 400) and writes the input it is sent to a
/// file the control server reads back (`screen`), so the test sees on the Mac
/// what each touch did: Her's settings have the row, Face ID opens the page,
/// the picture comes, a tap clicks where it lands, the phone's keyboard types
/// text and keys, Paste gives the Mac the phone's clipboard and what the Mac
/// copies comes back, trackpad mode clicks where its pointer is, the page turns
/// with the phone, and Close ends it.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testComputerControl go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testComputerControl() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run by TestIPhone with her built (HERMESH_E2E_HER)") }
        try XCTContext.runActivity(named: "joins the owner by its connection address") { _ in try join() }
        try XCTContext.runActivity(named: "Her is turned on for the Mac") { _ in try herTurnOn() }
        try XCTContext.runActivity(named: "Her's settings open the Mac's screen") { _ in try controlOpen() }
        try XCTContext.runActivity(named: "a tap clicks where it lands; a long press is the other button") { _ in try controlTouch() }
        try XCTContext.runActivity(named: "the phone's keyboard types text, and the keys a phone lacks") { _ in try controlKeyboard() }
        try XCTContext.runActivity(named: "Paste gives the Mac the phone's clipboard; what the Mac copies comes to the phone") { _ in try controlClipboard() }
        try XCTContext.runActivity(named: "trackpad mode: a finger moves the pointer, a tap clicks where it is") { _ in try controlTrackpad() }
        try XCTContext.runActivity(named: "the bar folds into a round button, moves to another edge, and opens there") { _ in try controlBar() }
        try XCTContext.runActivity(named: "the page turns with the phone, and Close ends the session") { _ in try controlTurnAndClose() }
    }

    /// The input the Mac's helper was sent so far, each a JSON object with one member (`button`, `text`, …).
    private func screenInput() throws -> [[String: Any]] {
        let log = try control("screen")["input"] as? String ?? ""
        return log.split(separator: "\n").compactMap { try? JSONSerialization.jsonObject(with: Data($0.utf8)) as? [String: Any] }
    }

    private func screenEvents(_ kind: String) throws -> [[String: Any]] {
        try screenInput().compactMap { $0[kind] as? [String: Any] }
    }

    private var surface: XCUIElement { app.descendants(matching: .any)["screen-surface"].firstMatch }

    func controlOpen() throws {
        let name = app.buttons["her-name"]
        XCTAssertTrue(name.waitForExistence(timeout: 180), "Her's page")
        name.tap()
        let row = app.descendants(matching: .any)["her-computer-control"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 60), "the row in Her's settings")
        XCTAssertTrue(text("远程桌面").exists && text("在手机上查看并操作 owner-mac 的屏幕。").exists)
        herShot("Computer Control: the row in Her's settings")
        row.tap()
        // Face ID stands aside in the tests (-HermeshTestApprove); then the Mac's picture.
        XCTAssertTrue(surface.waitForExistence(timeout: 30), "the page")
        try waitFor("the Mac's picture", timeout: 90) { (surface.value as? String) == "live" ? true : nil }
        let quality = app.descendants(matching: .any)["screen-quality"].firstMatch
        try waitFor("connected, straight to the Mac") { quality.label.hasSuffix("ms") && !quality.label.contains("中转") ? true : nil }
        XCTAssertFalse(app.descendants(matching: .any)["screen-watch-only"].exists, "the Mac takes input")
        herShot("Computer Control: the Mac's screen")
    }

    func controlTouch() throws {
        // The picture fits the page's width and sits in its middle: a touch a quarter in, half down, is there on the Mac.
        surface.coordinate(withNormalizedOffset: CGVector(dx: 0.25, dy: 0.5)).tap()
        let click = try waitFor("the click on the Mac") { try screenEvents("button").first { ($0["button"] as? String) == "MOUSE_BUTTON_LEFT" } }
        XCTAssertEqual(click["x"] as? Double ?? -1, 0.25, accuracy: 0.02, "where it landed")
        XCTAssertEqual(click["y"] as? Double ?? -1, 0.5, accuracy: 0.04)
        try waitFor("the button let go") { try screenEvents("button").contains { ($0["button"] as? String) == "MOUSE_BUTTON_LEFT" && ($0["down"] as? Bool) != true } ? true : nil }
        // Held, it is the right button.
        surface.coordinate(withNormalizedOffset: CGVector(dx: 0.75, dy: 0.5)).press(forDuration: 0.8)
        let right = try waitFor("the right click on the Mac") { try screenEvents("button").first { ($0["button"] as? String) == "MOUSE_BUTTON_RIGHT" } }
        XCTAssertEqual(right["x"] as? Double ?? -1, 0.75, accuracy: 0.02)
    }

    func controlKeyboard() throws {
        tap("screen-keyboard")
        XCTAssertTrue(app.buttons["screen-key-esc"].waitForExistence(timeout: 10), "the row of keys over the keyboard")
        let keys = app.textFields["screen-keys"]
        keys.typeText("hi 你好")
        try waitFor("the text on the Mac") { try screenEvents("text").compactMap { $0["text"] as? String }.joined().contains("hi 你好") ? true : nil }
        XCTAssertTrue(try screenEvents("text").allSatisfy { ($0["paste"] as? String) == "PASTE_APP" }, "what it can't type, a Mac pastes as an app does")
        app.buttons["screen-key-esc"].tap()
        try waitFor("esc on the Mac") { try screenEvents("key").contains { ($0["key"] as? String) == "NAMED_KEY_ESCAPE" } ? true : nil }
        // A modifier holds until the next key: Command, then C.
        // Typing Chinese goes through the Simulator's hardware keyboard, which puts the on-screen keyboard
        // away and brings it back: a tap while the row moves misses. Wait until it is still.
        let command = app.buttons["screen-key-command"]
        try waitAtRest(command)
        command.tap()
        keys.typeText("c")
        try waitFor("Command-C on the Mac") {
            try screenEvents("key").contains { ($0["character"] as? String) == "c" && ($0["command"] as? Bool) == true } ? true : nil
        }
        herShot("Computer Control: the keyboard")
        app.buttons["screen-key-hide"].tap()
        XCTAssertTrue(app.buttons["screen-key-esc"].waitForNonExistence(timeout: 10), "the keyboard put away")
    }

    /// Waits until a key in the row stops moving (the keyboard coming up, or going away and back).
    private func waitAtRest(_ key: XCUIElement) throws {
        var last = CGRect.null
        try waitFor("the row of keys at rest") {
            let f = key.frame
            defer { last = f }
            return f == last ? true : nil
        }
    }

    func controlClipboard() throws {
        // Text the phone holds, put there by another app (this test's); the system's Paste key reads it without asking.
        let phone = "从手机粘贴 pasted from the phone"
        UIPasteboard.general.string = phone
        tap("screen-keyboard")
        let paste = app.descendants(matching: .any)["screen-key-paste"].firstMatch
        XCTAssertTrue(paste.waitForExistence(timeout: 10), "Paste in the row of keys")
        try waitAtRest(paste)
        paste.tap()
        let sent = try waitFor("the phone's clipboard on the Mac") { try screenEvents("clipboard").last }
        XCTAssertEqual(sent["text"] as? String, phone, "all of it, in one go")
        XCTAssertEqual(sent["paste"] as? String, "PASTE_APP", "pasted as an app pastes (Command-V on a Mac)")
        XCTAssertFalse(try screenEvents("text").contains { ($0["text"] as? String)?.contains("pasted from the phone") == true }, "not typed key by key")
        herShot("Computer Control: Paste")

        // Copied on the Mac: the phone's clipboard has it, and the page says so for a moment.
        let mac = "在 Mac 上复制 copied on the Mac"
        let changes = UIPasteboard.general.changeCount
        _ = try control("screen-clipboard?text=\(mac.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!)", method: "POST")
        let copied = app.descendants(matching: .any)["screen-clipboard-copied"].firstMatch
        XCTAssertTrue(copied.waitForExistence(timeout: 20), "the word that the Mac's text came")
        XCTAssertEqual(copied.label, "已复制 owner-mac 上的文字")
        herShot("Computer Control: copied from the Mac")
        // Reading the clipboard from this test would ask; its count says it changed, and Paste shows what it holds.
        XCTAssertNotEqual(UIPasteboard.general.changeCount, changes, "the phone's clipboard was written")
        paste.tap()
        try waitFor("the Mac's text, back from the phone's clipboard") { try (screenEvents("clipboard").last?["text"] as? String) == mac ? true : nil }
        XCTAssertTrue(copied.waitForNonExistence(timeout: 5), "the word goes by itself")
        app.buttons["screen-key-hide"].tap()
        XCTAssertTrue(paste.waitForNonExistence(timeout: 10), "the keyboard put away")
    }

    func controlTrackpad() throws {
        tap("screen-mode")
        let before = try screenEvents("button").count
        // The pointer starts in the middle; a finger dragged to the right takes it there, wherever the finger is.
        let from = surface.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.7))
        from.press(forDuration: 0.05, thenDragTo: surface.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7)))
        let moved = try waitFor("the pointer moved on the Mac") { try screenEvents("move").last }
        XCTAssertGreaterThan(moved["x"] as? Double ?? 0, 0.55, "to the right of the middle")
        // A tap anywhere clicks where the pointer is, not under the finger.
        surface.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.3)).tap()
        let click = try waitFor("the click at the pointer") {
            let all = try screenEvents("button")
            return all.count > before ? all[before] : nil
        }
        XCTAssertEqual(click["x"] as? Double ?? -1, moved["x"] as? Double ?? -2, accuracy: 0.02, "at the pointer")
        herShot("Computer Control: trackpad mode")
        tap("screen-mode") // back to direct touch, which the app remembers
    }

    func controlBar() throws {
        let quality = app.descendants(matching: .any)["screen-quality"].firstMatch
        let ball = app.buttons["screen-unfold"]
        let before = try screenInput().count
        herShot("Computer Control: the bar")
        tap("screen-fold")
        XCTAssertTrue(ball.waitForExistence(timeout: 5), "the round button")
        XCTAssertFalse(quality.exists, "the bar folded away")
        XCTAssertFalse(app.buttons["screen-close"].exists)
        // Dragged towards the left side, it keeps to that edge.
        ball.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.25)))
        try waitFor("the button on the left") { ball.frame.minX < 30 && ball.frame.midY < app.frame.height / 2 ? true : nil }
        herShot("Computer Control: the bar folded, moved")
        XCTAssertEqual(try screenInput().count, before, "moving the bar did nothing on the Mac")
        // Open there, it runs down the side.
        ball.tap()
        let close = app.buttons["screen-close"]
        XCTAssertTrue(close.waitForExistence(timeout: 5), "the bar again")
        let fold = app.buttons["screen-fold"]
        XCTAssertGreaterThan(close.frame.minY, fold.frame.maxY, "its buttons one under the other")
        XCTAssertLessThan(abs(close.frame.midX - fold.frame.midX), 2)
        XCTAssertLessThan(fold.frame.minX, 30, "against the side it was left at")
        herShot("Computer Control: the bar down the side")
    }

    func controlTurnAndClose() throws {
        XCUIDevice.shared.orientation = .landscapeLeft
        try waitFor("the page on its side") { surface.frame.width > surface.frame.height ? true : nil }
        XCTAssertEqual(surface.value as? String, "live", "the picture stays")
        XCTAssertEqual(surface.frame.size, app.frame.size, "the picture's page takes the whole screen")
        // On its side the bar has its own place, down the right side, beside the picture.
        let close = app.buttons["screen-close"], fold = app.buttons["screen-fold"]
        try waitFor("the bar down the right side") { close.frame.minY > fold.frame.maxY && fold.frame.minX > app.frame.width * 0.75 ? true : nil }
        XCUIDevice.shared.orientation = .portrait
        try waitFor("upright again") { surface.frame.width < surface.frame.height ? true : nil }
        tap("screen-close")
        XCTAssertTrue(surface.waitForNonExistence(timeout: 10), "the page closed")
        XCTAssertTrue(app.descendants(matching: .any)["her-computer-control"].firstMatch.waitForExistence(timeout: 10), "back in Her's settings")
    }
}
