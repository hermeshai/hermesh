import XCTest

/// First use with only the iPhone (docs/onboarding.md §4, §5), run by
/// TestIPhoneFirstRun (e2e/iphone_firstrun_test.go): the phone claims a new
/// server and makes its management key; the guide, put off once and taken
/// up again from Get Started, leads it to a machine, its coding agent and a
/// project, an agent and that agent's first session.
extension HermeshUITests {
    func testFirstRun() throws {
        let world = try control("first-run")
        try XCTContext.runActivity(named: "sets up its own server with the claim code and makes its key") { _ in
            try claimServer(world["relay"] as! String, claim: world["claim"] as! String)
        }
        try XCTContext.runActivity(named: "puts the guide off, and takes it up again from Get Started") { _ in try putOff() }
        try XCTContext.runActivity(named: "adds a Linux machine from its pairing link, with nothing to compare") { _ in try addMachineByLink() }
        try XCTContext.runActivity(named: "rescans the machine, tests its coding agent and adds a project there") { _ in try codingAgent() }
        try XCTContext.runActivity(named: "connects an agent, and its first session shows its answer") { _ in try firstTask() }
        try XCTContext.runActivity(named: "says this iPhone alone can approve, until hidden") { _ in try onlyDevice() }
        try XCTContext.runActivity(named: "keeps Get Started away while the machine reconnects") { _ in try reconnect() }
        try XCTContext.runActivity(named: "erases everything, and opens on its first run saying what was done") { _ in
            try eraseEverything(URL(string: world["relay"] as! String)!)
        }
    }

    func claimServer(_ relay: String, claim: String) throws {
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 30), "the first page")
        XCTAssertTrue(app.buttons["start-join"].exists)
        tap("own-server")
        tap("claim-toggle")
        let address = app.textFields["connection-address"]
        XCTAssertTrue(address.waitForExistence(timeout: 10))
        address.tap()
        address.typeText(relay)
        let code = app.textFields["claim-code"]
        code.tap()
        code.typeText(claim)
        tap("continue")
        // The management key: what it does, then made with Face ID.
        XCTAssertTrue(text("创建管理密钥").waitForExistence(timeout: 30), "the key")
        XCTAssertTrue(text("只在这台 iPhone 里").exists && text("服务器无权放行").exists)
        tap("create-key")
        let on = app.buttons["notifications-on"]
        XCTAssertTrue(on.waitForExistence(timeout: 60), "set up")
        XCTAssertTrue(text("通知内容端到端加密，服务器看不到").exists)
        XCTAssertTrue(text("ChatGPT 请求连接").exists, "what notifications bring")
        allowNotifications(after: on)
        XCTAssertTrue(text("你的代码在哪台电脑上？").waitForExistence(timeout: 30), "the guide")
    }

    func putOff() throws {
        tap("set-up-later")
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 30), "the overview")
        XCTAssertTrue(step(0).waitForExistence(timeout: 30), "Get Started, with what the guide left")
        XCTAssertTrue(text("连上你的电脑").exists)
        // No machine yet, so no second-device row.
        XCTAssertFalse(app.descendants(matching: .any)["second-device"].exists)
        press(step(0).buttons["get-started-continue"])
        XCTAssertTrue(text("你的代码在哪台电脑上？").waitForExistence(timeout: 30), "back in the guide")
    }

    func addMachineByLink() throws {
        tap("computer-linux")
        // An own server: the command with this account's address.
        let command = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "install.sh | sh -s -- ")).firstMatch
        XCTAssertTrue(command.waitForExistence(timeout: 10), "the install command")
        let address = String(command.label.split(separator: " ").last!)
        let code = try control("machine?address=\(address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")["code"] as! String
        // Opened as a link: the code matches the key, so nothing to compare.
        app.open(URL(string: "hermesh://pair?code=\(code)")!)
        let name = app.textFields["machine-name"]
        XCTAssertTrue(name.waitForExistence(timeout: 30), "its request")
        try waitFor("found by its code") { app.descendants(matching: .any)["verification-code"].exists ? nil : true }
        XCTAssertTrue(text("vps-1 请求加入").exists)
        XCTAssertEqual(name.value as? String, "vps-1")
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 60), "it joins")
        XCTAssertTrue(text("vps-1 已加入").exists)
    }

    func codingAgent() throws {
        // The sheet closes by itself, and the guide goes on to that machine's coding agents.
        let agent = app.buttons["agent-claude"]
        XCTAssertTrue(agent.waitForExistence(timeout: 30), "its coding agent")
        XCTAssertTrue(text("vps-1 上找到 1 个").exists || text("vps-1 上找到 2 个").exists)
        XCTAssertTrue(text("还支持").exists && text("Gemini CLI").exists, "the others it works with")
        let rescan = app.buttons["rescan"]
        press(rescan)
        try waitFor("rescanned") { rescan.label.contains("重新扫描") ? true : nil }
        press(agent)
        tap("test-agent")
        let result = app.descendants(matching: .any)["test-result"].firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: 120), "the test's answer")
        XCTAssertTrue(result.label.contains("ready"), result.label)
        tap("add-project")
        tap("repo-~/app")
        tap("add-project-confirm")
        XCTAssertTrue(toast(containing: "已添加项目「app」"))
        back(to: "agents-continue")
        XCTAssertTrue(text("已登录 · 1 个项目").waitForExistence(timeout: 10))
        tap("agents-continue")
    }

    func firstTask() throws {
        let command = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "auth login ")).firstMatch
        XCTAssertTrue(command.waitForExistence(timeout: 10), "what to send the agent")
        let address = command.label.components(separatedBy: "auth login ")[1].split(separator: " ")[0]
        _ = try control("agent?address=\(address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        // Its request opens by itself, for all projects.
        XCTAssertTrue(app.buttons["approve"].waitForExistence(timeout: 30), "the request")
        XCTAssertTrue(text("Cursor 请求连接").exists)
        tap("approve")
        XCTAssertTrue(text("试一下").waitForExistence(timeout: 30), "try it")
        XCTAssertTrue(text("跟我打个招呼").exists, "the first task")
        _ = try control("task", method: "POST")
        let session = app.descendants(matching: .any)["first-session"].firstMatch
        XCTAssertTrue(session.waitForExistence(timeout: 60), "its session")
        try waitFor("its answer") { session.label.contains("你好") ? true : nil }
        tap("guide-done", timeout: 60)
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 30), "the overview")
        XCTAssertFalse(app.descendants(matching: .any)["get-started"].firstMatch.exists, "Get Started is done")
    }

    func onlyDevice() throws {
        let row = app.descendants(matching: .any)["second-device"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the second-device row")
        XCTAssertTrue(text("只有这台 iPhone 能批准").exists)
        press(row.buttons["隐藏"])
        XCTAssertTrue(row.waitForNonExistence(timeout: 10))
    }

    /// While vps-1 is away its coding agent is not ready, and when it is
    /// back it reports it a moment later: Get Started, done, stays away.
    func reconnect() throws {
        let row = app.descendants(matching: .any)["machine-row-vps-1"].firstMatch
        let card = app.descendants(matching: .any)["get-started"].firstMatch
        _ = try control("vps?state=down", method: "POST")
        try waitFor("vps-1 offline") { row.label.contains("离线") ? true : nil }
        XCTAssertFalse(card.waitForExistence(timeout: 3), "Get Started stays away while vps-1 is offline")
        _ = try control("vps?state=up", method: "POST")
        try waitFor("vps-1 back") { row.label.contains("在线") ? true : nil }
        XCTAssertFalse(card.waitForExistence(timeout: 3), "Get Started stays away once vps-1 is back")
    }

    func eraseEverything(_ relay: URL) throws {
        openMenu()
        tap("account")
        // At the bottom of the account: the list makes its rows as they scroll in.
        XCTAssertTrue(app.buttons["appearance-system"].waitForExistence(timeout: 20), "the account")
        let erase = app.descendants(matching: .any)["erase-everything"].firstMatch
        for _ in 0 ..< 6 where !(erase.exists && erase.isHittable) { app.swipeUp() }
        XCTAssertTrue(erase.waitForExistence(timeout: 5), "Erase Everything, at the bottom of the account")
        press(erase)
        // What it erases, by name.
        XCTAssertTrue(text("vps-1").waitForExistence(timeout: 20), "the machine")
        XCTAssertTrue(text("Cursor").exists && text("这台 iPhone").exists, "the agent and this iPhone")
        // The address to type and the button, further down.
        let field = app.textFields["erase-confirm"], button = app.buttons["erase-button"]
        for _ in 0 ..< 6 where !(button.exists && button.isHittable) { app.swipeUp() }
        XCTAssertTrue(button.waitForExistence(timeout: 5), "the button")
        XCTAssertFalse(button.isEnabled, "waits for the server's address")
        field.tap()
        // Typed, and the keyboard put away (it covers the button).
        field.typeText("\(relay.host!):\(relay.port!)\n")
        try waitFor("the button enabled and in reach") { button.isEnabled && button.isHittable ? true : nil }
        button.tap()
        // vps-1 erases itself now; the agent signs in again when next used; this iPhone last.
        let note = app.descendants(matching: .any)["erased-note"].firstMatch
        XCTAssertTrue(note.waitForExistence(timeout: 60), "the first run says what was done")
        XCTAssertTrue(text("Hermesh 已全部清空").exists)
        XCTAssertTrue(text("Cursor当时离线，之后各自重置或重新配对即可").exists, "the agent, to sign in again")
        XCTAssertEqual(try control("erased")["erased"] as? Bool, true, "vps-1 erased itself")
        XCTAssertTrue(app.buttons["start-new"].exists, "the first run")
        press(note.buttons["关闭"])
        XCTAssertTrue(note.waitForNonExistence(timeout: 10))
    }

    /// Back one page, until the one with this element shows.
    func back(to id: String) {
        let b = app.navigationBars.buttons["BackButton"]
        XCTAssertTrue(b.waitForExistence(timeout: 10), "no way back")
        press(b)
        XCTAssertTrue(app.descendants(matching: .any)[id].firstMatch.waitForExistence(timeout: 15), "back to \(id)")
    }

    /// A row of Get Started.
    func step(_ i: Int) -> XCUIElement {
        app.descendants(matching: .any)["get-started-\(i)"].firstMatch
    }
}
