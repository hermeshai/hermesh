import XCTest

/// First use with only the iPhone (docs/onboarding.md §4, §5), run by
/// TestIPhoneFirstRun (e2e/iphone_firstrun_test.go): the phone claims a
/// new server, and Get Started leads it to a machine, its coding agent, a
/// project, an agent and that agent's first session.
extension HermeshUITests {
    func testFirstRun() throws {
        let world = try control("first-run")
        try XCTContext.runActivity(named: "sets up its own server with the claim code") { _ in
            try claimServer(world["relay"] as! String, claim: world["claim"] as! String)
        }
        try XCTContext.runActivity(named: "adds a machine from its pairing link, with nothing to compare") { _ in try addMachineByLink() }
        try XCTContext.runActivity(named: "checks the machine's coding agent again and tests it") { _ in try checkAndTest() }
        try XCTContext.runActivity(named: "adds a project the machine found") { _ in try addFoundProject() }
        try XCTContext.runActivity(named: "says this iPhone alone can approve, until hidden") { _ in try onlyDevice() }
        try XCTContext.runActivity(named: "connects an agent, whose first session opens by itself") { _ in try firstTask() }
    }

    func claimServer(_ relay: String, claim: String) throws {
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
        let finish = app.buttons["finish"]
        XCTAssertTrue(finish.waitForExistence(timeout: 60), "set up")
        XCTAssertTrue(text("这台 iPhone 已经是你的可信设备").exists)
        XCTAssertTrue(text("不开通知的话，只有打开 Hermesh 时才能看到").exists)
        allowNotifications(after: finish)
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 30), "the overview")
        // Get Started, at its first row; no machine yet, so no second-device row.
        XCTAssertTrue(step(0).waitForExistence(timeout: 30), "Get Started")
        XCTAssertTrue(step(0).buttons["添加…"].exists, "the first row carries the button")
        XCTAssertFalse(app.descendants(matching: .any)["second-device"].exists)
    }

    func addMachineByLink() throws {
        press(step(0).buttons["添加…"])
        // An own server: the command with this account's address comes first.
        let command = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "install.sh | sh -s -- ")).firstMatch
        XCTAssertTrue(command.waitForExistence(timeout: 10), "the install command")
        let address = String(command.label.split(separator: " ").last!)
        tap("close")
        XCTAssertTrue(command.waitForNonExistence(timeout: 10), "the sheet closes")
        let code = try control("machine?address=\(address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")["code"] as! String
        // Opened as a link: the code matches the key, so nothing to compare.
        app.open(URL(string: "hermesh://pair?code=\(code)")!)
        XCTAssertTrue(app.descendants(matching: .any)["code-matches"].waitForExistence(timeout: 30), "the code matches")
        XCTAssertFalse(text("和对方显示的是否一致").exists, "nothing to compare")
        XCTAssertEqual(app.textFields["machine-name"].value as? String, "vps-1")
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 60), "it joins")
        XCTAssertTrue(text("「vps-1」已加入").exists)
        tap("open-machine")
    }

    func checkAndTest() throws {
        let agent = app.buttons["agent-claude"]
        XCTAssertTrue(agent.waitForExistence(timeout: 30), "its coding agent")
        tap("check-again")
        XCTAssertTrue(app.buttons["check-again"].waitForExistence(timeout: 30))
        try waitFor("Check Again done") { app.buttons["check-again"].isEnabled ? true : nil }
        agent.tap()
        tap("test-agent")
        let result = app.descendants(matching: .any)["test-result"].firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: 120), "the test's answer")
        XCTAssertTrue(result.label.contains("ready"), result.label)
        back(to: "check-again")
        back(to: "get-started")
    }

    func addFoundProject() throws {
        // The coding agent is ready: the project is next.
        XCTAssertTrue(step(2).buttons["添加…"].waitForExistence(timeout: 30), "Add a project is next")
        press(step(2).buttons["添加…"])
        tap("repo-~/app")
        tap("add-project-confirm")
        XCTAssertTrue(toast(containing: "已添加项目「app」"))
        XCTAssertTrue(step(3).buttons["连接…"].waitForExistence(timeout: 30), "Connect your agent is next")
    }

    func onlyDevice() throws {
        let row = app.descendants(matching: .any)["second-device"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the second-device row")
        XCTAssertTrue(text("只有这台 iPhone 能批准").exists)
        press(row.buttons["隐藏"])
        XCTAssertTrue(row.waitForNonExistence(timeout: 10))
    }

    func firstTask() throws {
        press(step(3).buttons["连接…"])
        let command = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "auth login ")).firstMatch
        XCTAssertTrue(command.waitForExistence(timeout: 10), "what to send the agent")
        let address = command.label.components(separatedBy: "auth login ")[1].split(separator: " ")[0]
        tap("close")
        XCTAssertTrue(command.waitForNonExistence(timeout: 10), "the sheet closes")
        _ = try control("agent?address=\(address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        let verify = app.buttons["verify-pairing"]
        XCTAssertTrue(verify.waitForExistence(timeout: 30), "the request")
        verify.tap()
        app.buttons["scope-projects"].tap()
        let all = app.switches["所有项目（包括以后添加的）"]
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.switches.firstMatch.exists ? all.switches.firstMatch.tap() : all.tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("approve")
        // The first task: what to send it, then its session as soon as it starts.
        XCTAssertTrue(app.descendants(matching: .any)["client-connected"].waitForExistence(timeout: 30), "connected")
        XCTAssertTrue(text("跟我打个招呼").exists, "the first task")
        _ = try control("task", method: "POST")
        XCTAssertTrue(text("你好").waitForExistence(timeout: 60), "its session opens")
        XCTAssertFalse(app.descendants(matching: .any)["client-connected"].exists)
        back(to: "working-today")
        XCTAssertTrue(app.descendants(matching: .any)["get-started"].firstMatch.waitForNonExistence(timeout: 30), "Get Started is done")
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
