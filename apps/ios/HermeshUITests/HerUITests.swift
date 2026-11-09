import XCTest

/// Her on the iPhone (docs/her-server.md §9), as a person uses her, against
/// the owner Mac's her, whose model is her-mock (TestIPhone, iphoneHerEnv):
/// Her turned on for the Mac with Face ID, her onboarding connecting a model
/// with an API key, a chat, a question, an approval, and the Mac gone
/// offline, where a message waits on the iPhone until it is back. her-mock
/// answers a message by its directive (e2e/her_owner_test.go).
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHer go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testHer() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run by TestIPhone with her built (HERMESH_E2E_HER)") }
        try XCTContext.runActivity(named: "joins the owner by its connection address") { _ in try join() }
        try XCTContext.runActivity(named: "Her is first in the menu, and is turned on for the Mac with Face ID") { _ in try herTurnOn() }
        try XCTContext.runActivity(named: "with no model, Her speaks first and connects one with an API key") { _ in try herOnboarding() }
        try XCTContext.runActivity(named: "a chat: the message, then Her's reply") { _ in try herChat() }
        try XCTContext.runActivity(named: "a question: a tapped answer is the owner's message") { _ in try herQuestion() }
        try XCTContext.runActivity(named: "an approval in the composer's place, allowed once") { _ in try herApproval() }
        try XCTContext.runActivity(named: "the Mac offline: what was said stays, and a message waits here until it is back") { _ in try herOffline() }
    }

    func herTurnOn() throws {
        openMenu()
        let drawer = app.descendants(matching: .any)["drawer"]
        let place = drawer.buttons["her-place"]
        XCTAssertTrue(place.waitForExistence(timeout: 5))
        XCTAssertLessThan(place.frame.minY, drawer.buttons["概览"].frame.minY, "first, above the overview")
        place.tap()
        // Her runs nowhere yet: where should she?
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 10))
        tap("her-turn-on-owner-mac", timeout: 30)
        // What turning her on means, from the bytes to be signed; Face ID approves.
        XCTAssertTrue(text("在机器「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        herShot("Her: turning her on")
        tap("sign-approve")
    }

    func herOnboarding() throws {
        // her runs there with no model: Her speaks first, the app writing for her, and the composer waits.
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180), "Her's greeting")
        XCTAssertTrue(text("Key 和登录都只留在 owner-mac 上").exists)
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(composer.exists && !composer.isEnabled, "the composer waits for a model")
        herShot("Her: no model yet")
        app.buttons["填写 API Key"].tap()
        XCTAssertTrue(text("用哪家的？").waitForExistence(timeout: 10))
        app.buttons["DeepSeek"].tap()
        XCTAssertTrue(text("把 DeepSeek 的 API Key 贴在下面").waitForExistence(timeout: 10))
        let key = app.secureTextFields["her-key"]
        XCTAssertTrue(key.waitForExistence(timeout: 10))
        key.tap()
        key.typeText("sk-e2e-good-key")
        tap("her-key-save")
        // Connected: one quiet line above, Her's first words, and the composer opens. The key is in none of it.
        XCTAssertTrue(text("好了，我会用").waitForExistence(timeout: 120), "Her's first words")
        XCTAssertTrue(text("已接上 DeepSeek").exists)
        XCTAssertFalse(text("sk-e2e-good-key").exists)
        try waitFor("the composer opens") { composer.isEnabled ? true : nil }
        herShot("Her: a model connected")
    }

    func herChat() throws {
        herSend("[[mock:send:你好，主人]] hi")
        XCTAssertTrue(app.staticTexts["你好，主人"].waitForExistence(timeout: 60), "Her's reply")
        herShot("Her: a chat")
    }

    func herQuestion() throws {
        herSend("[[mock:ask:要哪种颜色？|蓝|红]] hi")
        let blue = app.buttons["蓝"]
        XCTAssertTrue(blue.waitForExistence(timeout: 60), "the answers to tap")
        XCTAssertTrue(app.buttons["红"].exists && text("要哪种颜色？").exists)
        blue.tap()
        // The answer is the owner's message, and the answers fold away.
        XCTAssertTrue(app.staticTexts["蓝"].waitForExistence(timeout: 30), "the owner's answer")
        XCTAssertTrue(app.buttons["红"].waitForNonExistence(timeout: 30), "the answers fold away")
    }

    func herApproval() throws {
        herSend("[[mock:confirm]] hi")
        let panel = app.descendants(matching: .any)["her-approval"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 60), "the approval")
        XCTAssertTrue(text("运行这条命令？").exists && text("touch confirmed.txt").exists, "what Her wants to run")
        XCTAssertTrue(text("要执行命令，确认一下。").exists, "Her's words before it")
        XCTAssertTrue(app.buttons["her-deny"].exists && app.buttons["her-allow-once"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["her-composer"].exists, "in the composer's place")
        herShot("Her: an approval")
        tap("her-allow-once")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "做完了")).firstMatch.waitForExistence(timeout: 60), "Her's word once it ran")
        XCTAssertTrue(panel.waitForNonExistence(timeout: 10))
    }

    func herOffline() throws {
        _ = try control("machine?up=0", method: "POST")
        let line = app.descendants(matching: .any)["her-offline"].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 120), "the line that says the Mac is offline")
        XCTAssertTrue(line.label.contains("owner-mac 离线"), line.label)
        XCTAssertTrue(app.staticTexts["你好，主人"].exists, "what was said stays")
        herSend("[[mock:send:回来了]] hi")
        let waiting = app.descendants(matching: .any)["her-waiting"].firstMatch
        XCTAssertTrue(waiting.waitForExistence(timeout: 30), "the message waits here")
        XCTAssertTrue(text("等 owner-mac 在线后送达").exists && app.buttons["her-discard"].exists)
        herShot("Her: the Mac offline")
        _ = try control("machine?up=1", method: "POST")
        XCTAssertTrue(app.staticTexts["回来了"].waitForExistence(timeout: 180), "delivered once the Mac is back, and answered")
        XCTAssertTrue(waiting.waitForNonExistence(timeout: 60))
        XCTAssertFalse(line.exists)
    }

    // MARK: Helpers

    func herSend(_ text: String) {
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 30), "the composer")
        composer.tap()
        composer.typeText(text)
        tap("her-send")
    }

    func herShot(_ name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }
}
