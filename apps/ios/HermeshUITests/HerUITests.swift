import XCTest

/// Her on the iPhone (docs/her-server.md §9), as a person uses her, against
/// the owner Mac's her, whose model is her-mock (TestIPhone, iphoneHerEnv):
/// Her turned on for the Mac with Face ID, her onboarding connecting a model
/// with a Token Plan, a chat, Chinese typed at a line's end, her settings and her memory, a question, files she sends, an approval, and the Mac gone
/// offline, where a message waits on the iPhone until it is back; Her turned off with her data erased,
/// and on again, new; last, turned off with her data kept, which the machine's page erases. her-mock
/// answers a message by its directive (e2e/her_owner_test.go).
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHer go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testHerTeam() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herChat()
        try herSettings()
        herSend("[[mock:send:设置后仍能聊天]] hi")
        XCTAssertTrue(app.staticTexts["设置后仍能聊天"].waitForExistence(timeout: 60))
        try herTeam()
        herTeamAppearanceAndRestart()
    }

    /// Progress (docs/her-team.md §15): the work Her sent out as cards, and a sub-agent's record.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerProgress go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerProgress() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herProgress()
    }

    /// Her's connectors (docs/her-connectors-v2-demo.html): what their page offers, with GitHub's sheet; then one
    /// connected by its address each way her can find: needing no sign-in, a token (a wrong one first), and an
    /// account signed in to in the browser. Her uses the first in the conversation she has, with no card; turned off
    /// from the plus beside the composer, she no longer can; each is disconnected on its page. The addresses are
    /// her-mock's test tools (startMCPTools).
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerConnectors go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerConnectors() throws {
        let env = ProcessInfo.processInfo.environment
        guard env["HERMESH_E2E_HER"] != nil, let url = env["HERMESH_E2E_MCP_URL"] else { throw XCTSkip("run with Her built") }
        let tokenURL = try XCTUnwrap(env["HERMESH_E2E_MCP_TOKEN_URL"]), token = try XCTUnwrap(env["HERMESH_E2E_MCP_TOKEN"])
        let accountURL = try XCTUnwrap(env["HERMESH_E2E_MCP_ACCOUNT_URL"])
        try join()
        try herTurnOn()
        try herOnboarding()
        try herConnectors(url, tokenURL: tokenURL, token: token, accountURL: accountURL)
    }

    /// The files Her sent (docs/her-deliverables.md §3.3), from their card on Her's page: a report sent, changed and
    /// sent again is one row with two versions, each opening as it was sent; Show in Conversation and Reply from a
    /// file; search and the kinds; and the list while the Mac is offline, where a file opened before still opens.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerDeliveries go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerDeliveries() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herDeliveries()
    }

    /// Scheduled tasks (docs/kernel/routine-design.md §8), from their card on Her's page: the list, a
    /// task's page and a run's, and the ways in from her conversation.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerRoutines go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerRoutines() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herRoutines()
    }

    @objc func testHerAISharing() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()

        let draft = "[[mock:send:收到虚构样例]] fictional"
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        let disclosure = app.alerts["与第三方 AI 共享数据？"]
        herSend(draft, approveAI: false)
        XCTAssertTrue(disclosure.waitForExistence(timeout: 15))
        XCTAssertTrue(disclosure.staticTexts.containing(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@ AND label CONTAINS %@ AND label CONTAINS %@", "供应商：", "模型：", "数据：", "用途：")).firstMatch.exists)
        XCTAssertFalse(disclosure.staticTexts.containing(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS[c] %@", "撤回", "revoke")).firstMatch.exists)
        disclosure.buttons["不允许"].tap()
        XCTAssertEqual(composer.value as? String, draft)
        XCTAssertFalse(app.staticTexts["收到虚构样例"].exists)

        tap("her-send")
        XCTAssertTrue(disclosure.waitForExistence(timeout: 15))
        disclosure.buttons["允许与 AI 共享"].tap()
        XCTAssertTrue(app.staticTexts["收到虚构样例"].waitForExistence(timeout: 30))

        // Subsequent messages send directly without prompting again.
        herSend("[[mock:send:第二次直接发送]] second", approveAI: false)
        XCTAssertFalse(disclosure.waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["第二次直接发送"].waitForExistence(timeout: 30))

        herPage("settings")
        let clear = app.buttons["her-clear"]
        for _ in 0..<3 where !clear.isHittable { app.swipeUp() }
        XCTAssertTrue(clear.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["her-ai-sharing-revoke"].exists)
        XCTAssertFalse(app.buttons["撤回第三方 AI 共享授权"].exists)
        XCTAssertFalse(app.buttons["Revoke Third-Party AI Sharing"].exists)
        XCTAssertFalse(app.buttons["her-ai-sharing-review"].exists)
        herBackToConversation()
    }

    @objc func testHerNotifications() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()

        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let banner = springboard.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@ OR label CONTAINS %@", "Something needs you", "有事需要你处理", "synthetic ")).firstMatch
        func pushed() throws -> Int { try XCTUnwrap(control("pushed")["pushed"] as? Int) }
        func waitForPush(after before: Int) throws {
            try waitFor("the relay hands the Her notice to SpringBoard") { try pushed() > before ? true : nil }
        }
        func sendFromMac(_ message: String) throws {
            let text = try XCTUnwrap("[[mock:send:\(message)]] hi".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed))
            _ = try control("her-send?text=\(text)", method: "POST")
        }

        XCTAssertTrue(banner.waitForNonExistence(timeout: 10))
        var before = try pushed()
        herSend("[[mock:send:synthetic foreground reply]] hi")
        try waitForPush(after: before)
        XCTAssertFalse(banner.waitForExistence(timeout: 5), "the open Her conversation is quiet")
        XCTAssertTrue(app.staticTexts["synthetic foreground reply"].waitForExistence(timeout: 30))

        before = try pushed()
        try sendFromMac("synthetic synced reply")
        try waitForPush(after: before)
        XCTAssertFalse(banner.waitForExistence(timeout: 5), "a synced reply in the open conversation is quiet")
        XCTAssertTrue(app.staticTexts["synthetic synced reply"].waitForExistence(timeout: 30))

        openMenu()
        tap("overview-place")
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 10))
        before = try pushed()
        try sendFromMac("synthetic other page reply")
        try waitForPush(after: before)
        XCTAssertTrue(banner.waitForExistence(timeout: 10), "Her notifies when another page is open")
        banner.tap()
        XCTAssertTrue(app.staticTexts["synthetic other page reply"].waitForExistence(timeout: 30), "the other device's reply syncs")

        before = try pushed()
        XCUIDevice.shared.press(.home)
        try sendFromMac("synthetic background reply")
        try waitForPush(after: before)
        XCTAssertTrue(banner.waitForExistence(timeout: 10), "Her notifies on the Home Screen")
        banner.tap()
        XCTAssertTrue(app.staticTexts["synthetic background reply"].waitForExistence(timeout: 30))

        before = try pushed()
        herSend("[[mock:ask:synthetic question|yes|no]] hi")
        try waitForPush(after: before)
        XCTAssertFalse(banner.waitForExistence(timeout: 5), "a question in the open conversation stays quiet")
    }

    /// Sending (docs/her-sending.md) on its own: a message leaves the composer at once and shows once.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerSending go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerSending() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herChat()
        try herSendingOnce()
    }

    /// Replying (docs/her-reply.md §3.3) on its own: from a message's menu, to all of it and to words selected on a
    /// sheet; by swiping it left; and the quote that brings the message replied to into view.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerReplying go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerReplying() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herChat()
        try herReplying()
    }

    /// An approval in the composer's place, under a conversation longer than the page: its end stays above the card.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerApprovalEnd go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerApprovalEnd() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herApprovalKeepsTheEnd()
    }

    /// Her's page (docs/her-panel-demo.html), against her itself: under Abilities, her built-in skill turned off and on
    /// and its page with SKILL.md, web search switched off and on again, the browsers with a site set apart; under
    /// Settings, the Model page with the Token Plan onboarding connected and its models, then her environment's
    /// storage and its console, opened in the app.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerPageTabs go test ./e2e -run TestIPhone -v -timeout 40m
    @objc func testHerPageTabs() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()
        try herPageTabs()
    }

    @objc func testHer() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run by TestIPhone with her built (HERMESH_E2E_HER)") }
        try XCTContext.runActivity(named: "joins the owner by its connection address") { _ in try join() }
        try XCTContext.runActivity(named: "Her is first in the menu, and is turned on for the Mac with Face ID") { _ in try herTurnOn() }
        try XCTContext.runActivity(named: "with no model, Her speaks first, offers the ways, and connects a Token Plan") { _ in try herOnboarding() }
        try XCTContext.runActivity(named: "a chat: the message, then Her's reply") { _ in try herChat() }
        try XCTContext.runActivity(named: "typing Chinese at a line's end, the composer holds its height") { _ in try herTypingChinese() }
        try XCTContext.runActivity(named: "a message leaves the composer at once and shows once: typed, while Pinyin composes, and with the connection gone") { _ in try herSendingOnce() }
        try XCTContext.runActivity(named: "the plus beside the field: what it adds opens out of it, the field staying") { _ in try herAdd() }
        try XCTContext.runActivity(named: "her name opens her settings, a page of their own") { _ in try herSettings() }
        try XCTContext.runActivity(named: "what she notes shows in her settings as written, and is cleared there") { _ in try herMemory() }
        try XCTContext.runActivity(named: "a Mac older than the app says it needs an update, and the memory loads once it has one") { _ in try herOlder() }
        try XCTContext.runActivity(named: "a question: a tapped answer is the owner's message") { _ in try herQuestion() }
        try XCTContext.runActivity(named: "files Her sent: Markdown reads as the conversation renders it, code in its colours") { _ in try herFiles() }
        try XCTContext.runActivity(named: "an approval in the composer's place, allowed once") { _ in try herApproval() }
        try XCTContext.runActivity(named: "her name tag says what the agents do, its panel answers a question, and a session she starts is a card") { _ in try herTeam() }
        XCTContext.runActivity(named: "Her is the start page once enabled, and the team panel works in dark appearance") { _ in herTeamAppearanceAndRestart() }
        try XCTContext.runActivity(named: "the Mac offline: what was said stays, and a message waits here until it is back") { _ in try herOffline() }
        try XCTContext.runActivity(named: "opened on its last messages, earlier ones are read at the top") { _ in try herEarlier() }
        try XCTContext.runActivity(named: "turned off from her settings, her data erased: on again, she is new") { _ in try herTurnOff() }
        try XCTContext.runActivity(named: "turned off with her data kept: the card says so, removing the machine offers to erase it, and its page erases it") { _ in try herEraseKept() }
    }

    func herTurnOn() throws {
        openMenu()
        let drawer = app.descendants(matching: .any)["drawer"]
        let place = drawer.buttons["her-place"]
        XCTAssertTrue(place.waitForExistence(timeout: 5))
        XCTAssertLessThan(place.frame.minY, drawer.buttons["overview-place"].frame.minY, "first, above Work")
        place.tap()
        // Her runs nowhere yet: where should she?
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 10))
        tap("her-turn-on-owner-mac", timeout: 30)
        // What turning her on means, from the bytes to be signed; Face ID approves.
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        herShot("Her: turning her on")
        // Turning her on also lets her use the owner's computers, and the sheet says so; Approve stays in sight.
        tap("sign-approve")
    }

    func herOnboarding() throws {
        // her runs there with no model: Her speaks first, the app writing for her, and the composer waits.
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180), "Her's greeting")
        XCTAssertTrue(text("Key 和登录都只留在 owner-mac 上").exists)
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(composer.exists && !composer.isEnabled, "the composer waits for a model")
        // The ways, in a list, each saying what it takes.
        for way in ["chatgpt", "plan", "key"] { XCTAssertTrue(app.buttons["her-choice-\(way)"].exists, way) }
        XCTAssertTrue(text("已订阅的模型套餐，如百炼、火山方舟、智谱 GLM").exists)
        herShot("Her: no model yet")
        // An API key: which provider, then its key, the provider checked in its list.
        app.buttons["填写 API Key"].tap()
        XCTAssertTrue(text("用哪家的？").waitForExistence(timeout: 10))
        app.buttons["DeepSeek"].tap()
        XCTAssertTrue(text("把 DeepSeek 的 API Key 贴在下面").waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["her-choice-deepseek"].isSelected, "the provider picked is checked")
        let key = app.secureTextFields["her-key"]
        XCTAssertTrue(key.waitForExistence(timeout: 10))
        // Another way: Her asks which at once; a Token Plan instead, each plan with the line that tells it from
        // the others, then its key, the page to get one being the plan's own. her-mock stands in for Tencent
        // Cloud's plan (iphoneHerEnv).
        tap("her-other-way")
        XCTAssertTrue(text("好，换哪种方式？").waitForExistence(timeout: 10))
        XCTAssertFalse(key.exists, "the key is no longer asked for")
        app.buttons["接入 Token Plan"].tap()
        XCTAssertTrue(text("接入哪个套餐？").waitForExistence(timeout: 10))
        XCTAssertTrue(text("Coding Plan 专属密钥").exists)
        app.buttons["腾讯云 Token Plan"].tap()
        XCTAssertTrue(text("把 腾讯云 Token Plan 的 API Key 贴在下面").waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["去 腾讯云 Token Plan 获取 Key"].exists, "the plan's own page")
        XCTAssertTrue(key.waitForExistence(timeout: 10))
        // Picked, the conversation goes to its end: the key's field is there to tap.
        try waitFor("the key's field in view") { key.isHittable ? true : nil }
        herShot("Her: a Token Plan's key")
        key.tap()
        key.typeText("sk-e2e-plan-key")
        tap("her-key-save")
        // Connected: one quiet line above, Her's first words, and the composer opens. The key is in none of it.
        XCTAssertTrue(text("好了，我会用").waitForExistence(timeout: 120), "Her's first words")
        XCTAssertTrue(text("已接上 腾讯云 Token Plan").exists)
        XCTAssertFalse(text("sk-e2e-plan-key").exists)
        try waitFor("the composer opens") { composer.isEnabled ? true : nil }
        herShot("Her: a model connected")
    }

    func herChat() throws {
        herSend("[[mock:send:你好，主人]] hi")
        let words = app.staticTexts["你好，主人"]
        XCTAssertTrue(words.waitForExistence(timeout: 60), "Her's reply")
        // In her bubble on the left, as on the Mac: the words inset in it, and a short reply's bubble just as wide.
        let bubble = app.descendants(matching: .any).matching(identifier: "her-reply").containing(NSPredicate(format: "label == %@", "你好，主人")).firstMatch
        XCTAssertTrue(bubble.exists, "her bubble")
        XCTAssertTrue(bubble.frame.contains(words.frame) && words.frame.minX - bubble.frame.minX >= 10, "\(words.frame) in \(bubble.frame)")
        XCTAssertLessThan(bubble.frame.maxX, app.frame.midX, "on the left")
        // One of more than a line takes the whole of Her's side, ending where her first words (as long) do, whatever
        // each one's longest line.
        herSend("[[mock:send:这一段回复比较长，会折成好几行。多行的气泡都一样宽，右边停在同一条线上，不随每一行的字数变。]] hi")
        let replies = app.descendants(matching: .any).matching(identifier: "her-reply")
        let long = replies.containing(NSPredicate(format: "label BEGINSWITH %@", "这一段回复比较长")).firstMatch
        XCTAssertTrue(long.waitForExistence(timeout: 60), "the long reply")
        let first = replies.containing(NSPredicate(format: "label BEGINSWITH %@", "好了，我会用")).firstMatch
        XCTAssertEqual(long.frame.maxX, first.frame.maxX, accuracy: 1, "one edge for bubbles of more than a line")
        XCTAssertGreaterThan(long.frame.width, app.frame.width * 0.8, "most of the page")
        herShot("Her: a chat")
    }

    /// A reply (docs/her-reply.md): touch and hold a message for Reply, Copy and Select Text; what the next message
    /// replies to shows above the composer's words until it is sent or taken away; sent, the message quotes it at the
    /// top of its bubble and Her is told of it after the owner's words (her-mock sends back the prompt it got). Select
    /// Text opens the message's words on a sheet, where Reply leads the edit menu of what is selected. Swiping a
    /// message left replies to all of it. A quote brings the message it quotes into view.
    func herDeliveries() throws {
        let any = app.descendants(matching: .any)
        func write(_ text: String) throws {
            let q = text.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
            _ = try control("her-file?name=report.md&text=\(q)", method: "POST")
        }
        let button = any["her-files"].firstMatch
        // Sent: the Files card of her page counts one new file.
        try write("# 季度报告\n\n初稿：只有营收。\n")
        herSend("[[mock:attach:report.md]] 发我报告")
        XCTAssertTrue(app.buttons["her-file-report.md"].waitForExistence(timeout: 60), "the file in the conversation")
        herPage("work")
        XCTAssertTrue(waitUntil(timeout: 30) { button.label.contains("1 份新的") }, "one new file: \(button.label)")
        herBackToConversation()
        // Changed and sent again: still one file new.
        try write("# 季度报告\n\n第二版：营收和利润。\n")
        herSend("[[mock:attach:report.md]] 改好再发")
        XCTAssertTrue(waitUntil(timeout: 60) { self.app.buttons.matching(identifier: "her-file-report.md").count == 2 }, "sent twice")
        herPage("work")
        XCTAssertTrue(button.label.contains("1 份新的"), button.label)
        herShot("Her: the Files card counts a new file")

        // The page: one row, its two versions under it.
        tap("her-files")
        XCTAssertTrue(app.navigationBars["文件"].waitForExistence(timeout: 30), "the files page")
        let row = any["her-file-row-report.md"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 30), "the report's row")
        XCTAssertEqual(any.matching(identifier: "her-file-row-report.md").count, 1, "once")
        herShot("Her: the files she sent")
        // Two lines: the name and when, then Her's words and how many versions; the versions are on its page.
        XCTAssertTrue(row.label.contains("2 个版本"), row.label)
        row.tap()
        let page = any["her-delivery-page"].firstMatch
        XCTAssertTrue(page.waitForExistence(timeout: 30), "the latest on its page")
        XCTAssertTrue(text("第二版：营收和利润。").waitForExistence(timeout: 30), "the latest as it was sent")
        let versions = any["her-delivery-versions"].firstMatch
        XCTAssertTrue(versions.waitForExistence(timeout: 10), "the switch between versions")
        versions.buttons.element(boundBy: 1).tap()
        XCTAssertTrue(text("初稿：只有营收。").waitForExistence(timeout: 30), "the first version as it was sent")
        XCTAssertFalse(text("第二版").exists)
        herShot("Her: the first version")
        versions.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(text("第二版：营收和利润。").waitForExistence(timeout: 30), "the latest")

        // Show in Conversation: back to her page, with the message.
        tap("her-delivery-show")
        XCTAssertTrue(page.waitForNonExistence(timeout: 10))
        XCTAssertTrue(any["her-composer"].firstMatch.waitForExistence(timeout: 30), "her conversation")
        XCTAssertTrue(app.buttons["her-file-report.md"].firstMatch.waitForExistence(timeout: 10), "its message")
        herPage("work")
        XCTAssertTrue(waitUntil(timeout: 30) { !button.label.contains("新的") }, "the list was seen: nothing new (\(button.label))")

        // Reply from a file: the composer quotes it, and Her is told which file.
        tap("her-files")
        XCTAssertTrue(row.waitForExistence(timeout: 30))
        row.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 30))
        tap("her-delivery-reply")
        let bar = any["her-reply-bar"].firstMatch
        XCTAssertTrue(waitUntil(timeout: 15) { bar.exists && bar.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "report.md")).count > 0 }, "the file is quoted")
        herSend("[[mock:send]] 加一节现金流")
        XCTAssertTrue(waitUntil(timeout: 60) { any.matching(NSPredicate(format: "label CONTAINS %@", "quoted in part")).count > 0 }, "Her was told")

        // Search and the kinds.
        herPage("work")
        tap("her-files")
        XCTAssertTrue(row.waitForExistence(timeout: 30))
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10))
        search.tap()
        search.typeText("没有这个")
        XCTAssertTrue(any["her-files-empty"].firstMatch.waitForExistence(timeout: 15), "nothing found")
        // The field's own clear button: the words go, and the list is all again.
        search.buttons.firstMatch.tap()
        XCTAssertTrue(row.waitForExistence(timeout: 15), "all again")
        // The search ends with the bar's own button beside the field (iOS 26 shows Close there, not Cancel).
        let back = app.navigationBars.buttons["BackButton"]
        if let close = app.navigationBars.buttons.allElementsBoundByIndex.last(where: { $0.isHittable && $0.identifier != "BackButton" }) { close.tap() }
        XCTAssertTrue(back.waitForExistence(timeout: 10), "the search ended")
        // The tabs: the report is a document, and there is no media.
        let tabs = any["her-files-tabs"].firstMatch
        tabs.buttons["媒体"].tap()
        XCTAssertTrue(any["her-files-empty"].firstMatch.waitForExistence(timeout: 15), "no media")
        tabs.buttons["文档"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the report among the documents")
        tabs.buttons["全部"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 15))

        // The Mac offline: the list as last seen, and the latest version, opened before, opens from this iPhone.
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(back.waitForExistence(timeout: 10), "back to her page")
        back.tap()
        tap("her-files", timeout: 30)
        XCTAssertTrue(any["her-files-offline"].firstMatch.waitForExistence(timeout: 120), "it says the Mac is offline")
        XCTAssertTrue(row.waitForExistence(timeout: 30), "the list as last seen")
        herShot("Her: the files while the Mac is offline")
        row.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 30))
        XCTAssertTrue(text("第二版：营收和利润。").waitForExistence(timeout: 30), "opened from this iPhone")
        tap("close")
        _ = try control("machine?up=1", method: "POST")

        // Where a file came from is not on its row (docs/her-deliverables.md §3.5): a scheduled task makes a brief each
        // run, and Her sends it on when its report wakes her. The glass button at the top right opens the sources from
        // the bottom; choosing the task shows its files only, as the bar under the tabs says, until it is cleared.
        // The brief is there for her to send whether or not the run, which cannot ask, may write it.
        let brief = "# 每日早报".addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
        _ = try control("her-file?name=brief.md&text=\(brief)", method: "POST")
        XCTAssertTrue(back.waitForExistence(timeout: 10))
        herBackToConversation()
        herSend("[[mock:routine_file:每日早报|in 2s|brief.md]] 每天早上发我早报")
        approveIfAsked()
        XCTAssertTrue(app.buttons["her-file-brief.md"].waitForExistence(timeout: 120), "the brief, sent on")
        herPage("work")
        tap("her-files")
        let briefRow = any["her-file-row-brief.md"].firstMatch
        XCTAssertTrue(briefRow.waitForExistence(timeout: 30) && row.exists, "the brief among the files")
        let sources = any["her-files-sources"].firstMatch
        XCTAssertTrue(sources.waitForExistence(timeout: 30), "the filter at the top right")
        sources.tap()
        XCTAssertTrue(app.navigationBars["按来源看"].waitForExistence(timeout: 10), "the sources, from the bottom")
        let task = any["her-source-每日早报"].firstMatch
        XCTAssertTrue(task.waitForExistence(timeout: 10) && any["her-source-对话里要的"].firstMatch.exists, "the task, and the conversation")
        herShot("Her: the files by source")
        task.tap()
        XCTAssertTrue(app.navigationBars["按来源看"].waitForNonExistence(timeout: 10), "chosen: the sheet goes")
        let chosen = any["her-files-source"].firstMatch
        XCTAssertTrue(chosen.waitForExistence(timeout: 10), "the bar says which")
        XCTAssertTrue(waitUntil(timeout: 15) { !row.exists } && briefRow.exists, "only the task's files")
        XCTAssertTrue(chosen.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "每日早报")).count > 0, "named")
        herShot("Her: one source's files")
        tap("her-files-source-clear")
        XCTAssertTrue(row.waitForExistence(timeout: 15) && briefRow.exists, "all again")
        XCTAssertFalse(chosen.exists)
    }

    func herReplying() throws {
        let any = app.descendants(matching: .any)
        // Her long reply of herChat: in view above the keyboard, where her first words have scrolled away.
        let said = "这一段回复比较长"
        let bubble = any.matching(identifier: "her-reply").containing(NSPredicate(format: "label BEGINSWITH %@", said)).firstMatch
        XCTAssertTrue(bubble.waitForExistence(timeout: 30) && bubble.isHittable, "her bubble, in view")
        let bar = any["her-reply-bar"].firstMatch
        func quoting(_ words: String) -> Bool { bar.exists && bar.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", words)).count > 0 }
        func heard(_ words: String) -> Int { any.matching(NSPredicate(format: "label CONTAINS %@", words)).count }
        func menu(_ title: String) throws {
            bubble.press(forDuration: 1.0)
            XCTAssertTrue(app.buttons["回复"].waitForExistence(timeout: 10) && app.buttons["拷贝"].exists && app.buttons["选择文本"].exists, "Reply, Copy, Select Text")
            if title == "回复" { herShot("Her: a message's menu") }
            app.buttons[title].tap()
        }

        // Reply: above the composer, which has the keyboard; the × takes it away.
        try menu("回复")
        XCTAssertTrue(waitUntil { quoting("回复 Her") && quoting(said) }, "what the next message replies to")
        XCTAssertTrue(waitUntil(timeout: 5) { self.app.keyboards.count > 0 }, "the composer has the keyboard")
        herShot("Her: replying")
        tap("her-reply-cancel")
        XCTAssertTrue(bar.waitForNonExistence(timeout: 5), "taken away")

        // Sent: quoted at the top of the bubble. Her answers in a word, so the bubble stays clear of the bar at the top.
        try menu("回复")
        XCTAssertTrue(waitUntil { quoting(said) })
        herSend("[[mock:send:好的]] 这句是什么意思")
        XCTAssertTrue(bar.waitForNonExistence(timeout: 5), "gone with the message")
        let quotes = any.matching(identifier: "her-quote")
        XCTAssertTrue(waitUntil(timeout: 30) { quotes.count == 1 && quotes.firstMatch.label.contains(said) }, "the message quotes it")
        XCTAssertTrue(app.staticTexts["好的"].waitForExistence(timeout: 60), "Her's answer")
        herShot("Her: a reply")

        // A quote brings the message it quotes into view.
        XCTAssertTrue(waitUntil(timeout: 10) { quotes.firstMatch.isHittable }, "the quote can be tapped")
        let was = bubble.frame
        quotes.firstMatch.tap()
        XCTAssertTrue(waitUntil(timeout: 10) { bubble.isHittable }, "the message replied to is in view (it was at \(was))")

        // Select Text: the words on a sheet; touch and hold selects, and Reply leads the edit menu. Her is told what
        // the message replies to, after the owner's words.
        try menu("选择文本")
        let words = app.textViews["her-select-text"]
        XCTAssertTrue(words.waitForExistence(timeout: 10), "the message's words")
        XCTAssertTrue((words.value as? String)?.hasPrefix(said) == true, "\(String(describing: words.value))")
        words.coordinate(withNormalizedOffset: CGVector(dx: 0.3, dy: 0)).withOffset(CGVector(dx: 0, dy: 26)).press(forDuration: 1.0)
        let reply = try waitFor("Reply in the edit menu", timeout: 10) { () -> XCUIElement? in
            [self.app.menuItems["回复"], self.app.buttons["回复"]].first { $0.exists && $0.isHittable }
        }
        herShot("Her: selecting text")
        reply.tap()
        XCTAssertTrue(words.waitForNonExistence(timeout: 10), "the sheet goes")
        XCTAssertTrue(waitUntil { bar.exists }, "what was selected is what the next message replies to")
        herSend("[[mock:send]] 再说一遍")
        XCTAssertTrue(waitUntil(timeout: 30) { quotes.count == 2 }, "the second message quotes too")
        XCTAssertTrue(waitUntil(timeout: 60) { heard("Replying to this earlier message of yours") > 0 }, "Her was told, after the owner's words")

        // Swiping a message left replies to all of it.
        let last = any.matching(identifier: "her-reply").allElementsBoundByIndex.last { $0.isHittable }
        let from = try XCTUnwrap(last, "a reply of Her's in view").coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.5))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: -170, dy: 0)))
        XCTAssertTrue(waitUntil { quoting("回复 Her") }, "swiped left: replying")
        herShot("Her: swiped to reply")
        tap("her-reply-cancel")
        XCTAssertTrue(bar.waitForNonExistence(timeout: 5))
    }

    func herTypingChinese() throws {
        // On the Pinyin keyboard, the letters being composed are wider than the character they become: at a line's
        // end they take a new line, and the character comes back. The composer grows with the letters and keeps its
        // height while typing goes on, and sits back once the text has been still a moment. Its height is from the
        // field's top (held, the field keeps to the top of it) to Send's foot.
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        let send = app.buttons["her-send"]
        XCTAssertTrue(composer.waitForExistence(timeout: 30) && send.exists)
        func height() -> CGFloat { send.frame.maxY - composer.frame.minY }
        composer.tap()
        XCTAssertTrue(app.keys["w"].waitForExistence(timeout: 10), "the keyboard")
        let one = height()
        let space = app.keys["space"].exists ? app.keys["space"] : app.keys["空格"]
        var before = one, came = 0
        // A long syllable leaves room for its single character to come back from a new line. Short syllables in
        // a fixed sentence can all land away from that boundary, depending on the phone's width and font.
        for syllable in Array(repeating: "chuang", count: 36) {
            for c in syllable { app.keys[String(c)].tap() }
            let typed = height()
            space.tap()
            XCTAssertGreaterThanOrEqual(height(), typed - 1, "as tall right after “\(syllable)” became a character")
            if typed > before + 1 { // the letters took a new line
                sleep(2)
                if height() < typed - 1 { came += 1 }
            }
            before = height()
        }
        let text = composer.value as? String ?? ""
        XCTAssertTrue(text.unicodeScalars.contains { (0x4E00...0x9FFF).contains($0.value) }, "typed on the Pinyin keyboard: \(text)")
        XCTAssertGreaterThan(before, one + 30, "grown to more lines")
        XCTAssertGreaterThan(came, 0, "a character came back from a new line, and the composer sat back after it")
        herShot("Her: typing Chinese")
        // Cleared, it is one line again at once.
        composer.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: text.count + 10))
        try waitFor("one line again") { abs(height() - one) < 1 ? true : nil }
    }

    /// A message sent leaves the composer at once and shows once (docs/her-sending.md): typed; sent while the Pinyin
    /// keyboard still composes, what it composed going with it and not back into the field; and sent while the
    /// conversation's connection is gone (the relay down), delivered once it is back.
    func herSendingOnce() throws {
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 30))
        func typed() -> String {
            let v = composer.value as? String ?? ""
            return v == composer.placeholderValue ? "" : v
        }
        func shown(_ label: String) -> Int { app.staticTexts.matching(NSPredicate(format: "label == %@", label)).count }

        herSend("[[mock:send:只收到一次]] hi")
        XCTAssertTrue(waitUntil(timeout: 2) { typed().isEmpty }, "emptied at once: \(typed())")
        XCTAssertTrue(app.staticTexts["只收到一次"].waitForExistence(timeout: 60), "Her's reply")
        XCTAssertEqual(shown("[[mock:send:只收到一次]] hi"), 1, "the message once")
        XCTAssertTrue(typed().isEmpty, "nothing came back into the composer: \(typed())")

        // Pinyin being composed, no character chosen yet: Send. Before 2026-11-15 the message went, the keyboard wrote
        // what it composed back into the emptied field, and Send stayed greyed out under it.
        composer.tap()
        XCTAssertTrue(app.keys["n"].waitForExistence(timeout: 10), "the keyboard")
        composer.typeText("[[mock:send:拼音也送到了]] ")
        for c in "nihao" { app.keys[String(c)].tap() }
        XCTAssertTrue(app.buttons["her-send"].isEnabled, "Send takes what is being composed")
        app.buttons["her-send"].tap()
        XCTAssertTrue(app.staticTexts["拼音也送到了"].waitForExistence(timeout: 60), "Her's reply")
        let went = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "[[mock:send:拼音也送到了]]"))
        XCTAssertEqual(went.count, 1, "the message once")
        XCTAssertTrue(went.firstMatch.label.lowercased().contains("ni"), "with what was composed: \(went.firstMatch.label)")
        sleep(2)
        XCTAssertTrue(typed().isEmpty, "the keyboard wrote nothing back: \(typed())")
        XCTAssertTrue(waitUntil(timeout: 5) { app.keyboards.count == 0 }, "sent: the keyboard goes")
        herShot("Her: sent while Pinyin composed")

        // The connection gone: the message shows at once, waits, and goes once the relay is back.
        _ = try control("relay?up=0", method: "POST")
        herSend("[[mock:send:断线后送达]] hi")
        XCTAssertTrue(waitUntil(timeout: 2) { typed().isEmpty }, "emptied at once: \(typed())")
        let row = app.descendants(matching: .any).matching(NSPredicate(format: "identifier IN %@", ["her-sending", "her-waiting"])).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "shown as being sent, then waiting")
        herShot("Her: sent with the relay down")
        _ = try control("relay?up=1", method: "POST")
        XCTAssertTrue(app.staticTexts["断线后送达"].waitForExistence(timeout: 180), "delivered once the relay is back, and answered")
        XCTAssertEqual(shown("[[mock:send:断线后送达]] hi"), 1, "the message once")
        XCTAssertTrue(waitUntil(timeout: 30) { !app.descendants(matching: .any)["her-waiting"].exists && !app.descendants(matching: .any)["her-sending"].exists })

        // A command her takes without it joining the conversation (/context): it is not shown as being sent for long.
        // Before 2026-11-16 it said "Sending…" for good, with no way to remove it.
        herSend("/context")
        XCTAssertTrue(waitUntil(timeout: 2) { typed().isEmpty }, "emptied at once: \(typed())")
        XCTAssertTrue(waitUntil(timeout: 30) { !app.descendants(matching: .any)["her-sending"].exists }, "no longer shown as being sent")
    }

    func herAdd() throws {
        // The plus is in the composer, on the field's row with Send; a small popover opens out of it, and the
        // field stays in view.
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        let plus = app.descendants(matching: .any)["her-attach"].firstMatch
        let send = app.buttons["her-send"]
        XCTAssertTrue(plus.exists && composer.exists && send.exists)
        XCTAssertLessThan(plus.frame.maxX, composer.frame.minX, "before the field")
        XCTAssertEqual(plus.frame.midY, send.frame.midY, accuracy: 1, "on the row with Send, in the composer")
        tap("her-attach")
        XCTAssertTrue(app.buttons["照片"].waitForExistence(timeout: 10) && app.buttons["文件"].exists, "what can be added")
        XCTAssertTrue(composer.exists, "the field stays")
        sleep(1) // the menu settled, for the picture
        herShot("Her: adding photos or files")
        // Tapped away, the menu goes, and the plus is back.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)).tap()
        XCTAssertTrue(app.buttons["文件"].waitForNonExistence(timeout: 10), "the menu gone")
        XCTAssertTrue(plus.waitForExistence(timeout: 10) && composer.exists)
        // Files opens the Files browser once the popover has gone.
        tap("her-attach")
        XCTAssertTrue(app.buttons["文件"].waitForExistence(timeout: 10))
        app.buttons["文件"].tap()
        XCTAssertTrue(app.buttons["文件"].waitForNonExistence(timeout: 10), "the popover gone")
        let cancel = app.buttons["取消"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 15), "the Files browser")
        cancel.tap()
        XCTAssertTrue(cancel.waitForNonExistence(timeout: 10) && composer.exists)
    }

    func herSettings() throws {
        // Her's mark and name tag over the conversation, which scrolls under them; beside them Progress alone, looked at
        // often: the rest of her work is on her page, which her mark opens (the tag, the team panel: herTeam).
        let name = app.buttons["her-avatar"]
        XCTAssertTrue(name.exists && name.label.contains("Her") && app.buttons["her-tag"].exists, name.label)
        XCTAssertTrue(app.buttons["her-progress"].exists, "Progress beside them")
        for id in ["her-settings", "her-files", "her-routines"] { XCTAssertFalse(app.buttons[id].exists, "no \(id) beside them") }
        name.tap()
        // Her page: three tabs, Work first, each card a way in.
        XCTAssertTrue(app.buttons["her-tab-work"].waitForExistence(timeout: 10), "her page")
        for id in ["her-files", "her-routines"] { XCTAssertTrue(app.buttons[id].exists, "\(id) under Work") }
        XCTAssertFalse(app.buttons["her-progress"].exists, "no Progress card: it is over the conversation")
        herShot("Her: her page, Work")
        tap("her-tab-abilities")
        let memory = app.buttons["her-memory"]
        XCTAssertTrue(memory.waitForExistence(timeout: 10), "the Memory card")
        try waitFor("the Memory card says nothing yet (\(memory.label))") { memory.label.contains("还没有") ? true : nil }
        for id in ["her-skills", "her-connectors", "her-search", "her-browser"] {
            XCTAssertTrue(app.descendants(matching: .any)[id].firstMatch.exists, "\(id) under Abilities")
        }
        herShot("Her: her page, Abilities")
        tap("her-tab-settings")
        // Her model, how freely she acts, her notifications, where she runs, and clearing her conversation.
        let env = app.buttons["her-environment"]
        XCTAssertTrue(env.waitForExistence(timeout: 10) && env.label.contains("owner-mac"), env.label)
        XCTAssertTrue(app.buttons["先问我"].exists && app.buttons["完全访问"].exists && text("清空对话").exists)
        herShot("Her: her page, Settings")
        // Where she runs, on a page of its own: the environments, another machine, storage, the console, turning off.
        env.tap()
        XCTAssertTrue(text("运行在").waitForExistence(timeout: 10) && text("在另一个环境里开启 Her").exists, "the environment page")
        XCTAssertTrue(app.buttons["her-storage"].exists && app.buttons["her-console"].exists && app.buttons["her-turn-off"].exists)
        herShot("Her: environment")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        // Her model and how hard she thinks, on the Model page.
        tap("her-model-page")
        XCTAssertTrue(app.buttons["her-model"].waitForExistence(timeout: 10), "the Model page")
        try herThinking()
        herBackToConversation()
    }

    /// How hard she thinks, as on the Mac: a row under the model while the model offers levels, set in her conversation
    /// (what the row shows is what her says back); another model without the level says what it is now.
    func herThinking() throws {
        // What a picker's row shows, its title and the value chosen (iOS puts the value in either).
        let shown = { (e: XCUIElement) in "\(e.label) | \(e.value as? String ?? "")" }
        let modelRow = app.buttons["her-model"]
        XCTAssertFalse(app.buttons["her-thinking"].exists, "no thinking row: the first model offers no levels")
        let first = shown(modelRow)
        // Hy3 on the Token Plan offers no thinking and high (her's catalog).
        modelRow.tap()
        let hy3 = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND identifier != %@", "Hy3", "her-model")).firstMatch
        XCTAssertTrue(hy3.waitForExistence(timeout: 10), "Hy3 in the model menu")
        hy3.tap()
        let thinking = app.buttons["her-thinking"]
        XCTAssertTrue(thinking.waitForExistence(timeout: 30), "the thinking row, Hy3 offering levels")
        try waitFor("the model row says Hy3 (\(shown(modelRow)))") { shown(modelRow).contains("Hy3") ? true : nil }
        thinking.tap()
        XCTAssertTrue(app.buttons["默认"].waitForExistence(timeout: 10) && app.buttons["高"].exists && app.buttons["不思考"].exists, "Default and Hy3's own levels")
        herShot("Her: thinking levels")
        app.buttons["不思考"].tap()
        try waitFor("her says the level back (\(shown(thinking)))") { shown(thinking).contains("不思考") ? true : nil }
        herShot("Her: thinking set")
        // Back to the first model, which has no such level: the row goes with the model. (The toast saying the level it
        // is now is gone before the menu's dismissal lets a query run.)
        modelRow.tap()
        let back = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND identifier != %@", "Auto", "her-model")).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 10), "\(first): its model in the menu")
        back.tap()
        try waitFor("the thinking row gone with the model") { thinking.exists ? nil : true }
        XCTAssertTrue(shown(modelRow).contains("Auto"), shown(modelRow))
    }

    func herMemory() throws {
        // Her notes a rule (her-mock's remember) with no approval asked, and says so.
        herSend("[[mock:remember:- 回复一律用中文。(2026-11-11)]] 以后都用中文")
        XCTAssertTrue(text("记下了。").waitForExistence(timeout: 60), "her reply")
        // Her page: the Memory card opens the page, MEMORY.md as the conversation renders Markdown.
        herPage("abilities")
        let row = app.buttons["her-memory"]
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the Memory card")
        row.tap()
        XCTAssertTrue(text("回复一律用中文。(2026-11-11)").waitForExistence(timeout: 30), "the line, without its mark")
        XCTAssertFalse(text("- 回复一律用中文。(2026-11-11)").exists, "rendered, not as written")
        herShot("Her: memory")
        // Clearing it is at the page's foot, and asks first; then the page and the card say there is nothing.
        let clear = app.buttons["her-clear-memory"]
        for _ in 0 ..< 3 where !(clear.exists && clear.isHittable) { app.swipeUp() }
        XCTAssertTrue(clear.waitForExistence(timeout: 10), "Clear Memory")
        clear.tap()
        XCTAssertTrue(text("清空「Her」记住的事？").waitForExistence(timeout: 10), "asked first")
        app.buttons["清除"].firstMatch.tap()
        XCTAssertTrue(text("还没有记下什么").waitForExistence(timeout: 30), "nothing remembered")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        try waitFor("the card says nothing yet (\(row.label))", timeout: 30) { row.label.contains("还没有") ? true : nil }
        herBackToConversation()
    }

    /// A Mac older than the app (docs/version-skew.md §8): its her acts as if it lacked bots/memory, restarted as an
    /// update does (TestIPhone's her-without). First one from before her listed her methods: the page says Hermesh
    /// there needs an update, never her's "unknown method". Then one that lists them without it: the row says so before
    /// it is opened, and Clear Memory explains. Then her has it, and the open page loads the memory by itself.
    func herOlder() throws {
        _ = try control("her-without?methods=bots/memory,no-list", method: "POST")
        herPage("abilities")
        let row = app.buttons["her-memory"]
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the Memory card")
        row.tap()
        let needs = app.descendants(matching: .any)["needs-update"].firstMatch
        XCTAssertTrue(needs.waitForExistence(timeout: 60), "the page says the Mac needs an update")
        XCTAssertTrue(text("需要更新 Hermesh").exists, needs.label)
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'unknown method'")).firstMatch.exists, "never her's words")
        herShot("Her: a Mac older than the app")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        _ = try control("her-without?methods=bots/memory", method: "POST")
        try waitFor("the card says it needs an update (\(row.label))", timeout: 60) { row.label.contains("需更新") ? true : nil }

        row.tap()
        XCTAssertTrue(needs.waitForExistence(timeout: 10), "still older")
        _ = try control("her-without?methods=", method: "POST")
        XCTAssertTrue(text("还没有记下什么").waitForExistence(timeout: 90), "updated: the page loads by itself")
        XCTAssertFalse(needs.exists)
        herBackToConversation()
    }

    func herQuestion() throws {
        herSend("[[mock:ask:要哪种颜色？|蓝|红]] hi")
        let blue = app.buttons["蓝"]
        XCTAssertTrue(blue.waitForExistence(timeout: 60), "the answers to tap")
        XCTAssertTrue(app.buttons["红"].exists && text("要哪种颜色？").exists)
        // The answers are rows in the question's bubble. Half a point of slack: the frames come in fractions of a
        // point that do not add up exactly (a row ending at 442.00000000000007 in a bubble ending at 441.9999999).
        let question = app.descendants(matching: .any)["her-question"].firstMatch
        let bubble = question.frame.insetBy(dx: -0.5, dy: -0.5)
        XCTAssertTrue(bubble.contains(blue.frame) && bubble.contains(app.buttons["红"].frame), "\(blue.frame), \(app.buttons["红"].frame) in \(question.frame)")
        herShot("Her: a question")
        blue.tap()
        // The answer is the owner's message, and the answers fold away.
        XCTAssertTrue(app.staticTexts["蓝"].waitForExistence(timeout: 30), "the owner's answer")
        XCTAssertTrue(app.buttons["红"].waitForNonExistence(timeout: 30), "the answers fold away")
    }

    func herFiles() throws {
        // A note in Markdown and a Go file, made in her folder (as her model would), then sent.
        _ = try control("her-files", method: "POST")
        herSend("[[mock:attach:notes.md,main.go]] hi")
        let note = app.buttons["her-file-notes.md"]
        XCTAssertTrue(note.waitForExistence(timeout: 60), "the files Her sent")
        // The note on a page of its own, as the conversation renders Markdown: no marks, a table, code.
        note.tap()
        let page = app.descendants(matching: .any)["her-file-page"].firstMatch
        XCTAssertTrue(page.waitForExistence(timeout: 60), "the note's page")
        XCTAssertTrue(app.staticTexts["周报"].waitForExistence(timeout: 10), "its heading")
        XCTAssertFalse(text("# 周报").exists || text("**").exists, "none of its marks")
        XCTAssertTrue(app.staticTexts["已完成"].exists, "its table")
        XCTAssertTrue(page.descendants(matching: .any)["code-block"].exists, "its code")
        XCTAssertTrue(app.descendants(matching: .any)["her-file-share"].firstMatch.exists, "shared or saved from there")
        herShot("Her: a Markdown file")
        tap("close")
        XCTAssertTrue(page.waitForNonExistence(timeout: 10))
        // The Go file: its lines, not Quick Look's page with nothing on it.
        app.buttons["her-file-main.go"].tap()
        XCTAssertTrue(page.waitForExistence(timeout: 60), "the Go file's page")
        XCTAssertTrue(app.staticTexts["package main"].waitForExistence(timeout: 10), "its first line")
        XCTAssertTrue(text("fmt.Println(\"你好\")").exists, "its code")
        herShot("Her: a Go file")
        tap("close")
        XCTAssertTrue(page.waitForNonExistence(timeout: 10))
        // Files the owner sent from the Mac open from the owner's bubble the same way: the note on its page, the
        // PDF in Quick Look.
        _ = try control("her-owner-files", method: "POST")
        let plan = app.buttons["her-sent-plan.md"]
        XCTAssertTrue(plan.waitForExistence(timeout: 60), "the files the owner sent")
        plan.tap()
        XCTAssertTrue(page.waitForExistence(timeout: 60), "the note's page")
        XCTAssertTrue(app.staticTexts["计划"].waitForExistence(timeout: 10), "its heading")
        XCTAssertFalse(text("# 计划").exists, "none of its marks")
        herShot("Her: a Markdown file the owner sent")
        tap("close")
        XCTAssertTrue(page.waitForNonExistence(timeout: 10))
        app.buttons["her-sent-report.pdf"].tap()
        let quickLook = app.navigationBars.matching(NSPredicate(format: "identifier CONTAINS 'report'")).firstMatch
        XCTAssertTrue(quickLook.waitForExistence(timeout: 60), "the PDF in Quick Look")
        XCTAssertTrue(app.staticTexts["Quarterly report"].waitForExistence(timeout: 30), "its page")
        herShot("Her: a PDF the owner sent")
        app.buttons["QLOverlayDoneButtonAccessibilityIdentifier"].tap()
        XCTAssertTrue(quickLook.waitForNonExistence(timeout: 10))
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
        // Longer than the screen by now: it scrolls under her mark, with nothing drawn behind it.
        herShot("Her: under her mark")
        // Scrolled well up, a button brings the conversation back to its end, and goes.
        let toEnd = app.buttons["to-end"]
        XCTAssertFalse(toEnd.exists, "none at the end")
        app.swipeDown()
        app.swipeDown()
        XCTAssertTrue(toEnd.waitForExistence(timeout: 5), "the button back to the end")
        herShot("Her: scrolled up")
        toEnd.tap()
        let done = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "做完了")).firstMatch
        try waitFor("back at the end") { done.isHittable && !toEnd.exists ? true : nil }
    }

    /// Her and her team (docs/her-team.md §4.2 to §4.4, §4.8): her name tag says what the owner's agents are
    /// doing, the panel it opens answers a question where it is listed, a session Her starts is a card in her
    /// conversation that follows it, and the session's page says she sent the work.
    func herTeam() throws {
        // 选颜色 waits for an answer: the tag says so.
        let tag = app.buttons["her-tag"]
        try waitFor("the tag says a session waits") { tag.label.contains("在等你") ? true : nil }
        herShot("Her: the name tag, a session waits")
        tag.tap()
        let panel = app.descendants(matching: .any)["team-panel"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10), "the team panel")
        XCTAssertTrue(text("团队").exists && text("需要你处理").exists, "what needs the owner, listed")
        herShot("Her: the team panel")
        // A label's default swipe can be shorter than the dismissal threshold; drag out of the heading.
        let heading = text("团队").coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        heading.press(forDuration: 0.05, thenDragTo: heading.withOffset(CGVector(dx: 0, dy: -90)))
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "swiping the panel's heading up closes it")
        tag.tap()
        XCTAssertTrue(panel.waitForExistence(timeout: 5))
        // One tap answers it, there.
        tap("quick-answer-蓝")
        XCTAssertTrue(toast(containing: "已回答"))
        try waitFor("the question is answered") { try self.sessionStatus("选颜色") != "waiting_input" ? true : nil }
        // A tap beside it closes it: under the panel, where the dimmed conversation shows.
        app.descendants(matching: .any)["team-close"].firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.96)).tap()
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "the panel closes")
        try waitFor("the tag no longer says it waits") { !tag.label.contains("在等你") ? true : nil }
        tag.tap()
        tap("team-work")
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 10), "the panel's count opens Work")
        XCTAssertFalse(app.buttons["quick-answer-蓝"].exists, "Work no longer offers the answered question")
        openMenu()
        tap("her-place")
        // Her starts a coding session (asking first, in her default mode): a card where she started it.
        herSend(#"[[mock:mesh:session_start:{"project":"owner-mac/demo","agent":"gemini","prompt":"say 做完了","title":"Her 派的活"}]] 开工"#)
        XCTAssertTrue(app.descendants(matching: .any)["her-approval"].firstMatch.waitForExistence(timeout: 60), "Her asks before she starts it")
        tap("her-allow-once")
        let card = app.descendants(matching: .any)["her-session-Her 派的活"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 60), "the session's card")
        try waitFor("the card says the session is done", timeout: 90) { card.label.contains("完成") || card.label.contains("个文件") ? true : nil }
        // The scripted model echoes the tool result under the card, then the session's news, which she tells the
        // owner of: the card moves down under that, where the owner reads now (docs/her-team.md §4.4). One card.
        let words = app.descendants(matching: .any).matching(identifier: "her-reply")
        let news = words.containing(NSPredicate(format: "label CONTAINS %@", "turn_done")).firstMatch
        XCTAssertTrue(news.waitForExistence(timeout: 90), "what she says of the session's news")
        try waitFor("the card under what she said of its news") {
            let lowest = words.allElementsBoundByIndex.map(\.frame.maxY).max() ?? 0
            return card.frame.minY >= lowest ? true : nil
        }
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "her-session-Her 派的活").count, 1, "one card a session")
        if app.buttons["to-end"].exists { tap("to-end") }
        for _ in 0..<12 where !card.isHittable { app.swipeDown() }
        XCTAssertTrue(waitUntil { card.isHittable }, "the card is in view before opening it")
        herShot("Her: a session she started")
        // It opens the session, whose prompt has Her's face on its corner, and no name over it.
        press(card)
        let sender = app.descendants(matching: .any)["prompt-sender"].firstMatch
        XCTAssertTrue(sender.waitForExistence(timeout: 20), "who sent the prompt")
        XCTAssertEqual(sender.label, "Her 派的活")
        XCTAssertFalse(app.staticTexts["Her 派的活"].exists, "a face, not a line of text")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(tag.waitForExistence(timeout: 10), "back on Her's page")
        // What the team did today, as she counted it (docs/her-team.md §6.3): a card, which opens Work. Reading
        // asks nothing.
        herSend("[[mock:mesh:work_summary:{}]] 今天团队干了什么")
        let work = app.descendants(matching: .any)["her-work"].firstMatch
        XCTAssertTrue(work.waitForExistence(timeout: 60), "the card of the team's work")
        XCTAssertTrue(work.label.contains("个任务（今天）"), work.label)
        if app.buttons["to-end"].exists { tap("to-end") }
        for _ in 0..<12 where !work.isHittable { app.swipeDown() }
        XCTAssertTrue(waitUntil { work.isHittable }, "the work card is in view before opening it")
        herShot("Her: the team's work today")
        press(work)
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 10), "the work card opens Work")
        openMenu()
        tap("her-place")
        XCTAssertTrue(tag.waitForExistence(timeout: 10), "back on Her's page")
    }

    func herTeamAppearanceAndRestart() {
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["her-tag"].waitForExistence(timeout: 30), "Her is the start page once enabled")
        openMenu()
        tap("account"); tap("account-settings-open")
        reach("appearance-dark")
        tap("appearance-dark")
        tap("close")
        tap("her-place")
        tap("her-tag")
        let panel = app.descendants(matching: .any)["team-panel"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10))
        herShot("Her: the team panel in dark appearance")
        let heading = text("团队").coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        heading.press(forDuration: 0.05, thenDragTo: heading.withOffset(CGVector(dx: 0, dy: -90)))
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5))
        openMenu()
        tap("account"); tap("account-settings-open")
        tap("appearance-light")
        tap("close")
        tap("her-place")
    }

    func herOffline() throws {
        // Connected, no connection bubble at the foot; the Mac offline, the bubble at the foot says so, and what sending does.
        let line = app.descendants(matching: .any)["connection-line"].firstMatch
        XCTAssertFalse(line.exists, "nothing said while the Mac answers")
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(line.waitForExistence(timeout: 120), "the line that says the Mac is offline")
        // First it may say the Mac isn't responding (hermeshd can't reach it), until the relay has it offline.
        XCTAssertTrue(waitUntil(timeout: 120) { line.label.contains("owner-mac 离线 · 消息会排队到它上线") }, line.label)
        XCTAssertTrue(app.buttons["her-tag"].exists, "her name tag stays hers")
        XCTAssertTrue(app.staticTexts["你好，主人"].exists, "what was said stays")
        herSend("[[mock:send:回来了]] hi")
        let waiting = app.descendants(matching: .any)["her-waiting"].firstMatch
        XCTAssertTrue(waiting.waitForExistence(timeout: 30), "the message waits here")
        XCTAssertTrue(text("等 owner-mac 在线后送达").exists && app.buttons["her-discard"].exists)
        herShot("Her: the Mac offline")
        _ = try control("machine?up=1", method: "POST")
        XCTAssertTrue(app.staticTexts["回来了"].waitForExistence(timeout: 180), "delivered once the Mac is back, and answered")
        XCTAssertTrue(waiting.waitForNonExistence(timeout: 60))
        XCTAssertTrue(line.waitForNonExistence(timeout: 30), "the line goes once the Mac is back")
    }

    func herEarlier() throws {
        // Opened again on its last few messages (her sends 100; a test's window of 4, -HerHistoryLimit), the first
        // ones are no longer there, until it is scrolled up to its top, where they are read.
        app.terminate()
        app.launchArguments += ["-HerHistoryLimit", "4"]
        app.launch()
        openMenu()
        app.descendants(matching: .any)["drawer"].buttons["her-place"].tap()
        let first = app.staticTexts["你好，主人"]
        XCTAssertTrue(app.staticTexts["回来了"].waitForExistence(timeout: 60), "the last reply")
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 30))
        XCTAssertTrue(first.waitForNonExistence(timeout: 30), "only the last few messages")
        for _ in 0 ..< 15 where !first.waitForExistence(timeout: 2) { app.swipeDown() }
        XCTAssertTrue(first.waitForExistence(timeout: 30), "the first ones, read at the top")
        herShot("Her: earlier messages read")
    }

    func herTurnOff() throws {
        // At the end of her Environment page: turning her off asks whether her data goes too (docs/her-server.md §5.6).
        herPage("settings")
        tap("her-environment")
        XCTAssertTrue(text("运行在").waitForExistence(timeout: 10), "her environment")
        let off = app.buttons["her-turn-off"]
        for _ in 0 ..< 4 where !off.isHittable { app.swipeUp() }
        XCTAssertTrue(off.label.contains("在 owner-mac 上关闭 Her"), off.label)
        off.tap()
        XCTAssertTrue(text("在「owner-mac」上关闭 Her？").waitForExistence(timeout: 10), "asked first")
        XCTAssertTrue(text("保留数据的话，下次开启 Her 时接着用").exists, "what keeping means")
        XCTAssertTrue(app.buttons["关闭，保留数据"].exists, "keeping her data")
        herShot("Her: turning her off")
        app.buttons["关闭并清空数据"].tap()
        // Off, Her runs nowhere: the entry asks where she should run. On again there, she is new, with no model.
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 60), "the first run again")
        tap("her-turn-on-owner-mac", timeout: 30)
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        let approve = app.descendants(matching: .any)["sign-approve"].firstMatch
        for _ in 0 ..< 4 where !approve.exists { app.swipeUp() }
        tap("sign-approve")
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180), "a new Her, asking for a model")
        XCTAssertFalse(app.staticTexts["你好，主人"].exists, "nothing of what was said")
        herShot("Her: on again after erasing")
    }

    func herEraseKept() throws {
        // Turned off with her data kept, the first run's card says it is still there.
        herPage("settings")
        tap("her-environment")
        XCTAssertTrue(text("运行在").waitForExistence(timeout: 10), "her environment")
        let off = app.buttons["her-turn-off"]
        for _ in 0 ..< 4 where !off.isHittable { app.swipeUp() }
        off.tap()
        tap("关闭，保留数据", timeout: 10)
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 60), "the first run again")
        XCTAssertTrue(text("上次的数据还在").waitForExistence(timeout: 30), "her data kept, on the card")
        herShot("Her: off, her data kept")
        // Removing the machine could erase it too, while the machine answers; owner-mac stays here.
        openMachines()
        tap("machine-owner-mac", timeout: 20)
        tap("machine-menu")
        tap("移除…")
        XCTAssertTrue(app.buttons["移除，保留 Her 的数据"].waitForExistence(timeout: 10), "removing, her data kept")
        XCTAssertTrue(app.buttons["移除并清空 Her 的数据"].exists, "or erased")
        XCTAssertFalse(app.buttons["移除"].exists, "no removal that says nothing of Her")
        herShot("Her: removing her machine")
        if app.buttons["取消"].exists { app.buttons["取消"].tap() } else { app.otherElements["PopoverDismissRegion"].firstMatch.tap() }
        XCTAssertTrue(app.buttons["移除并清空 Her 的数据"].waitForNonExistence(timeout: 5), "cancelled")
        // The machine's page erases it: asked first, then Face ID.
        let erase = app.buttons["machine-erase-her"]
        for _ in 0 ..< 4 where !erase.exists || !erase.isHittable { app.swipeUp() }
        XCTAssertTrue(erase.waitForExistence(timeout: 10), "erasing her data, near the end of the machine's page")
        erase.tap()
        XCTAssertTrue(text("清空 Her 在「owner-mac」上的数据？").waitForExistence(timeout: 10), "asked first")
        XCTAssertTrue(text("无法恢复").exists, "what goes, for good")
        herShot("Her: erasing her data")
        app.buttons["清空"].tap()
        XCTAssertTrue(toast(containing: "已清空 Her 在 owner-mac 上的数据"))
        XCTAssertTrue(erase.waitForNonExistence(timeout: 60), "nothing left to erase")
        // The card says nothing of it now; on again, she is new.
        openMenu()
        app.descendants(matching: .any)["drawer"].buttons["her-place"].tap()
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 30), "the first run")
        XCTAssertFalse(text("上次的数据还在").exists, "no data kept")
        tap("her-turn-on-owner-mac", timeout: 30)
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        let approve = app.descendants(matching: .any)["sign-approve"].firstMatch
        for _ in 0 ..< 4 where !approve.exists { app.swipeUp() }
        tap("sign-approve")
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180), "a new Her, asking for a model")
    }

    /// Progress: a button over Her's conversation, empty until she sends work out, marked while her work waits; a sub-agent she starts is a post of its own,
    /// which says that it asks, and shows what set it off in a sheet from its ⓘ; its record shows what Her
    /// asked of it, answers its question there, then shows its call, its report, and how long and how many tokens it
    /// took; ended, its post is among the earlier ones.
    func herProgress() throws {
        // Progress is Her's: not in the menu, but a button over her conversation, at its top right.
        openMenu()
        let drawer = app.descendants(matching: .any)["drawer"]
        XCTAssertTrue(drawer.buttons["her-place"].waitForExistence(timeout: 10))
        XCTAssertFalse(drawer.buttons["progress-place"].exists, "not in the menu")
        tap("her-place")
        let entry = app.buttons["her-progress"]
        XCTAssertTrue(entry.waitForExistence(timeout: 10), "over her conversation")
        XCTAssertGreaterThan(entry.frame.minX, app.windows.firstMatch.frame.midX, "at its top right")
        entry.tap()
        XCTAssertTrue(app.navigationBars["进展"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["progress-empty"].firstMatch.waitForExistence(timeout: 30), "nothing sent out yet")
        herShot("Progress: nothing sent out yet")
        herBackToConversation()

        // Her hands a piece of work to a sub-agent (her-mock's relay_slow: "写长文", which runs a command); Progress is
        // marked while it waits for the owner.
        herSend("[[mock:relay_slow:3]] 写一篇长文")
        XCTAssertTrue(text("好，这个要一会儿，写好发你。").waitForExistence(timeout: 60), "Her's words")
        // She has answered, and her sub-agent waits for the owner: no dots, the question is Progress's (docs/her-team.md
        // §15.6).
        let typing = app.descendants(matching: .any)["her-typing"]
        XCTAssertTrue(waitUntil(timeout: 30) { !typing.exists }, "no busy bubble while what is at work waits for the owner")
        try waitFor("Progress is marked (\(entry.label))", timeout: 60) { entry.label.contains("在等你") ? true : nil }
        herShot("Her: Progress marked")
        entry.tap()
        let card = app.descendants(matching: .any)["progress-card-写长文"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 30), "the sub-agent's post")
        try waitFor("the post says it waits for the owner", timeout: 60) { card.label.contains("等你批准") ? true : nil }
        XCTAssertTrue(text("需要你").exists, "among what needs the owner")
        XCTAssertTrue(app.buttons["progress-review"].exists, "a way to its question")
        herShot("Progress: a sub-agent waits")

        // What set it off, in a sheet from the post's ⓘ: the owner's words and Her's.
        let why = app.buttons["progress-why"].firstMatch
        XCTAssertTrue(why.exists, "the post's ⓘ")
        why.tap()
        let sheet = app.descendants(matching: .any)["progress-source"].firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 10), "the sheet")
        XCTAssertTrue(text("来自你的一句话").exists)
        XCTAssertTrue(text("写一篇长文").exists, "what the owner said")
        XCTAssertTrue(text("好，这个要一会儿，写好发你。").exists, "what Her said back")
        herShot("Progress: what set it off")
        app.buttons.matching(NSPredicate(format: "label IN %@", ["关闭", "Close"])).firstMatch.tap()
        XCTAssertTrue(waitUntil { !sheet.exists }, "the sheet closes")

        // Its record, read as a session is: what Her asked of it as what was sent to it, and its question, answered
        // at the bottom.
        tap("progress-review")
        XCTAssertTrue(app.navigationBars["写长文"].waitForExistence(timeout: 10), "the record's page")
        let brief = app.descendants(matching: .any)["progress-brief"].firstMatch
        XCTAssertTrue(brief.waitForExistence(timeout: 10) && brief.label.contains("Write it and report."), "what Her asked: \(brief.label)")
        XCTAssertTrue(app.descendants(matching: .any)["her-approval"].firstMatch.waitForExistence(timeout: 30), "its question")
        XCTAssertTrue(text("运行这条命令？").exists)
        herShot("Progress: a record that asks")
        tap("her-allow-once")
        // It ends: its call as a grey line that opens to its output, a line that says its last words went back to Her.
        let state = app.descendants(matching: .any)["progress-state"].firstMatch
        try waitFor("the record says it is done", timeout: 90) { state.label.hasSuffix("完成") ? true : nil }
        XCTAssertTrue(app.descendants(matching: .any)["progress-done"].firstMatch.waitForExistence(timeout: 10), "its last words went back to Her")
        XCTAssertFalse(app.buttons["progress-stop"].exists, "nothing to stop")
        let call = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "sleep 3")).firstMatch
        XCTAssertTrue(call.exists, "its call, as a line")
        XCTAssertEqual(call.value as? String, "已收起")
        call.tap()
        XCTAssertTrue(app.descendants(matching: .any)["tool-output"].firstMatch.waitForExistence(timeout: 5), "it opens to its output")
        herShot("Progress: a record that ended")
        // Its info, as a session's: how long, how many tokens and steps, who sent it out.
        tap("progress-info")
        let info = app.descendants(matching: .any)["progress-info-sheet"].firstMatch
        XCTAssertTrue(info.waitForExistence(timeout: 10), "its info")
        for word in ["用时", "Tokens", "步骤", "派出"] { XCTAssertTrue(text(word).exists, word) }
        herShot("Progress: a record's info")
        app.buttons.matching(NSPredicate(format: "label IN %@", ["关闭", "Close"])).firstMatch.tap()
        XCTAssertTrue(waitUntil { !info.exists }, "the info closes")

        // Back in the feed it is among the earlier ones, and says how it ended.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.navigationBars["进展"].waitForExistence(timeout: 10))
        XCTAssertTrue(waitUntil(timeout: 60) { card.label.contains("完成") }, "the post says it is done: \(card.label)")
        XCTAssertTrue(text("早些时候").exists, "among the earlier ones")
        XCTAssertFalse(text("需要你").exists, "nothing needs the owner now")
        herShot("Progress: ended")

        // Another, allowed: it works on in the background. Her conversation's busy line stays, naming it, never what it
        // does; it opens its record, whose way back is her conversation. Progress says one is at work.
        herBackToConversation()
        herSend("[[mock:relay_slow:60]] 再写一篇")
        tap("her-allow-once", timeout: 60)
        try waitFor("the line names her sub-agent (\(typing.exists ? typing.label : "none"))", timeout: 30) { typing.exists && typing.label == "子智能体 · 写长文，正在工作" ? true : nil }
        herShot("Her: a sub-agent at work, in the busy line")
        typing.tap()
        XCTAssertTrue(app.navigationBars["写长文"].waitForExistence(timeout: 10), "its record")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(typing.waitForExistence(timeout: 10), "back in her conversation")
        try waitFor("Progress says one is at work (\(entry.label))", timeout: 60) { entry.label.contains("1 个进行中") ? true : nil }
        herShot("Her: Progress, one at work")
        entry.tap()
        XCTAssertTrue(app.descendants(matching: .any)["progress-now"].firstMatch.waitForExistence(timeout: 30), "its post, at work")
        XCTAssertTrue(text("现在").exists, "under Now")
        herShot("Progress: one at work")
        herBackToConversation()

        // A third while it works: the two take turns in the line, which ends with their number; it opens the system's
        // menu of them, each opening its record, and Progress.
        herSend("[[mock:relay_slow:60]] 第三篇")
        tap("her-allow-once", timeout: 60)
        try waitFor("two at work (\(typing.exists ? typing.label : "none"))", timeout: 30) { typing.exists && typing.label.hasPrefix("2 个子智能体正在工作") ? true : nil }
        herShot("Her: two sub-agents at work")
        typing.tap()
        let all = app.buttons["在「进展」中查看全部"]
        XCTAssertTrue(all.waitForExistence(timeout: 10), "the menu")
        let named = app.buttons.matching(NSPredicate(format: "label == %@", "写长文"))
        XCTAssertEqual(named.count, 2, "each at work")
        herShot("Her: the menu of who is at work")
        named.firstMatch.tap()
        XCTAssertTrue(app.navigationBars["写长文"].waitForExistence(timeout: 10), "its record")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(typing.waitForExistence(timeout: 10))
        typing.tap()
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.tap()
        XCTAssertTrue(app.navigationBars["进展"].waitForExistence(timeout: 10), "Progress")
        herBackToConversation()

        // While Her herself answers, her top capsule says "正在输入…" and the busy bubble stays clean (never mixing her status with sub-agents, nor naming her call).
        herSend("[[mock:bash:sleep 8]] 看看")
        tap("her-allow-once", timeout: 60)
        let tag = app.buttons["her-tag"]
        try waitFor("Her's capsule says typing (\(tag.exists ? tag.label : "none"))", timeout: 30) { tag.exists && tag.label.hasPrefix("正在输入…") ? true : nil }
        XCTAssertFalse(typing.firstMatch.label.hasPrefix("正在输入…"), "busy bubble no longer prefixes Her's typing status")
        XCTAssertFalse(typing.firstMatch.label.contains("sleep"), "never what she does")
        herShot("Her: at work herself")
    }

    /// A conversation longer than the page, then a sub-agent's question in the composer's place (the keyboard up as
    /// the message went): what Her said last stays in view above the card.
    func herApprovalKeepsTheEnd() throws {
        for i in 1 ... 6 {
            herSend("[[mock:send:第 \(i) 条，好让这段对话比一页长]] \(i)")
            XCTAssertTrue(text("第 \(i) 条，好让这段对话比一页长").waitForExistence(timeout: 60), "reply \(i)")
        }
        let card = app.descendants(matching: .any)["her-approval"].firstMatch
        let said = app.descendants(matching: .any).matching(identifier: "her-reply")
        func endAboveCard(_ words: String, _ name: String) throws {
            let last = said.containing(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
            XCTAssertTrue(last.waitForExistence(timeout: 60) && card.waitForExistence(timeout: 60), "her words, and the question")
            var was = CGRect.null
            XCTAssertTrue(waitUntil { defer { was = last.frame }; return last.frame == was && card.frame.minY > 0 }, "settled")
            herShot(name)
            XCTAssertLessThanOrEqual(last.frame.maxY, card.frame.minY, "the end above the question: \(last.frame) over \(card.frame)")
        }
        herSend("[[mock:relay_slow:60]] 写一篇长文")
        try endAboveCard("好，这个要一会儿", "Her: a question under a long conversation")

        // Allowed, then another sub-agent, whose question comes as what it told Her shows: the same.
        tap("her-allow-once", timeout: 30)
        herSend("[[mock:relay_note:60]] 汇总一下")
        try endAboveCard("agent_message", "Her: a second question under a long conversation")
    }

    func herConnectors(_ url: String, tokenURL: String, token: String, accountURL: String) throws {
        // Her page: the tray under Abilities, with nothing yet.
        herPage("abilities")
        let row = app.buttons["her-connectors"]
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the connectors tray")
        try waitFor("the tray says there is none yet (\(row.label))") { row.label.contains("还没有") ? true : nil }
        row.tap()
        XCTAssertTrue(text("还没有连接器").waitForExistence(timeout: 20), "the page, empty")
        herShot("Connectors: none yet")

        // The page offers GitHub: one button, an account's sign-in; a token is the owner's to choose instead.
        tap("connector-connect-github")
        let go = app.buttons["connect-go"]
        XCTAssertTrue(go.waitForExistence(timeout: 10), "GitHub's sheet")
        XCTAssertEqual(go.label, "登录并连接", "GitHub signs in with an account")
        XCTAssertTrue(text("Her 以你的身份使用它").exists, "what connecting means")
        herShot("Connectors: GitHub's sheet")
        tap("connect-token-instead")
        XCTAssertTrue(text("用令牌连接 GitHub").waitForExistence(timeout: 10), "a token, chosen")
        herShot("Connectors: GitHub with a token")
        XCTAssertEqual(app.buttons["connect-back"].label, "返回")
        tap("connect-back")
        XCTAssertTrue(go.waitForExistence(timeout: 10), "back on what connecting means")
        tap("connect-back")

        // The whole of a group, on a page of its own.
        tap("connectors-all-development")
        XCTAssertTrue(app.buttons["connector-connect-amplitude"].waitForExistence(timeout: 10), "the rest of the group")
        herShot("Connectors: a whole group")
        app.navigationBars.buttons["连接器"].firstMatch.tap()
        XCTAssertTrue(text("还没有连接器").waitForExistence(timeout: 10), "the page again")

        // An address of the owner's own that needs no sign-in: her asks it, says so, adds it, and lists its tools.
        try herConnectAddress(url, finds: "不需要登录")
        herShot("Connectors: an address, what her found")
        tap("connect-go")
        try herConnected("server")
        herShot("Connectors: connected")
        tap("connect-close")
        tap("connector-view-server")
        XCTAssertTrue(app.descendants(matching: .any)["connector-page"].firstMatch.waitForExistence(timeout: 10), "its page")
        herShot("Connectors: its page")
        tap("connector-tools")
        XCTAssertTrue(text("echo").waitForExistence(timeout: 20), "its tools")
        herShot("Connectors: its tools")
        app.navigationBars.buttons.firstMatch.tap()
        app.navigationBars.buttons["连接器"].firstMatch.tap()

        // One that wants a token: a token it refuses writes nothing; the right one connects it. Then it is
        // disconnected on its page.
        try herConnectAddress(tokenURL, finds: "Her 用你的令牌使用它")
        tap("connect-go")
        try herToken("not-the-token")
        XCTAssertTrue(app.descendants(matching: .any)["connect-failed"].firstMatch.waitForExistence(timeout: 30), "refused")
        herShot("Connectors: a token refused")
        tap("connect-retry")
        XCTAssertTrue(text("Her 用你的令牌使用它").waitForExistence(timeout: 30), "asked again")
        XCTAssertFalse(app.buttons["connector-view-server-2"].exists, "nothing was added")
        tap("connect-go")
        try herToken(token)
        try herConnected("server-2")
        XCTAssertEqual(app.buttons["connect-back"].label, "查看 server-2")
        tap("connect-back")
        XCTAssertTrue(text("保存在").waitForExistence(timeout: 10), "its page says where the token is")
        herShot("Connectors: one with a token")
        try herDisconnect("server-2")

        // One that signs in with an account: the browser, then it is connected; its page says it is signed in.
        try herConnectAddress(accountURL, finds: "Her 以你的身份使用它")
        XCTAssertEqual(app.buttons["connect-go"].label, "登录并连接")
        tap("connect-go")
        // iOS asks first whether the app may open the sign-in; the test server's sign-in page then approves at once.
        let proceed = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["继续"]
        XCTAssertTrue(proceed.waitForExistence(timeout: 30), "iOS asks before the sign-in opens")
        herShot("Connectors: the sign-in, asked")
        proceed.tap()
        try herConnected("server-2")
        XCTAssertEqual(app.buttons["connect-back"].label, "查看 server-2")
        tap("connect-back")
        XCTAssertTrue(text("mock-user").waitForExistence(timeout: 10), "its page names the account signed in to")
        XCTAssertTrue(text("已用你的 server-2 账号登录").exists, "and says it is signed in")
        herShot("Connectors: one with an account")
        try herDisconnect("server-2")
        herBackToConversation()

        // Her uses the first in the conversation she has, with no card.
        herSend("[[mock:mcp_env:server:HERMESH_UI_TEST]] 用一下")
        herAllowIfAsked()
        XCTAssertTrue(text("mcp done: (unset)").waitForExistence(timeout: 60), "what the connector said, in Her's answer")

        // Turned off from the plus beside the composer: Her no longer has it.
        tap("her-attach")
        tap("her-connectors-menu")
        let toggle = app.switches["connector-switch-server"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 20), "its switch in the sheet")
        XCTAssertEqual(toggle.value as? String, "1")
        herShot("Connectors: from the plus")
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.93, dy: 0.5)).tap()
        try waitFor("turned off (\(toggle.value ?? ""))") { toggle.value as? String == "0" ? true : nil }
        XCTAssertTrue(text("关掉的连接器，Her 从下一步起就不用了").exists, "the sheet says when it takes effect")
        app.navigationBars.buttons["完成"].tap()
        herSend("[[mock:mcp_env:server:HERMESH_UI_TEST]] 再用一下")
        herAllowIfAsked()
        let gone = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@ AND NOT label CONTAINS %@", "mcp done:", "(unset)")).firstMatch
        XCTAssertTrue(gone.waitForExistence(timeout: 60), "Her can't use it any more")

        // Disconnected on its page: the list has none again.
        herPage("abilities")
        tap("her-connectors")
        XCTAssertTrue(text("已停用").waitForExistence(timeout: 20), "off, in the list")
        tap("connector-view-server")
        try herDisconnect("server")
        XCTAssertTrue(text("还没有连接器").waitForExistence(timeout: 20), "none again")
        herShot("Connectors: none again")
        herBackToConversation()
    }

    /// Opens the sheet for an address of the owner's own and has her ask it: `finds` is what the sheet then says.
    func herConnectAddress(_ url: String, finds: String) throws {
        tap("connectors-add")
        let address = app.textFields["connect-address"]
        XCTAssertTrue(address.waitForExistence(timeout: 10), "the address")
        address.tap()
        address.typeText(url)
        tap("connect-continue")
        XCTAssertTrue(text(finds).waitForExistence(timeout: 30), "what her found: \(finds)")
    }

    /// Types a token into the sheet's token step and connects with it.
    func herToken(_ token: String) throws {
        let field = app.secureTextFields["connect-token"]
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the token")
        field.tap()
        field.typeText(token)
        tap("connect-token-go")
    }

    /// The sheet says the connector is added, under `name`, and that Her can use it.
    func herConnected(_ name: String) throws {
        XCTAssertTrue(app.descendants(matching: .any)["connect-done"].firstMatch.waitForExistence(timeout: 60), "connected")
        XCTAssertTrue(text("已添加 \(name)").exists && text("Her 现在就能用").exists, "added as \(name)")
    }

    /// Disconnects the connector whose page shows: asked first, then it is gone from the list.
    func herDisconnect(_ name: String) throws {
        let remove = app.descendants(matching: .any)["connector-remove"].firstMatch
        for _ in 0..<4 where !remove.isHittable { app.swipeUp() }
        remove.tap()
        let confirm = app.sheets.buttons["断开"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 10), "asked first")
        confirm.tap()
        try waitFor("\(name) is gone") { self.app.buttons["connector-view-\(name)"].exists ? nil : true }
    }

    /// Back down the pages to Her's conversation.
    func herPageTabs() throws {
        let any = app.descendants(matching: .any)
        let back = { self.app.navigationBars.buttons.element(boundBy: 0).tap() }
        func on(_ e: XCUIElement) -> Bool { (e.value as? String) == "1" }
        // What a switch here says shows for a moment only, often gone before a query after the tap: her-page.spec.ts
        // reads it on the Mac; here, what stays.
        herPage("abilities")

        // Skills: her built-in skill-creator, turned off and on here, and its page.
        tap("her-skills")
        XCTAssertTrue(app.navigationBars["技能"].waitForExistence(timeout: 10), "the skills page")
        let creator = app.buttons["her-skill-skill-creator"]
        XCTAssertTrue(creator.waitForExistence(timeout: 30), "her built-in skill")
        XCTAssertTrue(text("内置").exists && text("还没有自己的技能").exists, "built in; none of the owner's own")
        herShot("Her: skills")
        let toggle = app.switches["her-skill-switch-skill-creator"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10) && on(toggle), "on")
        toggle.switches.firstMatch.tap()
        try waitFor("off, as her says") { self.text("已停用").exists && !on(toggle) ? true : nil }
        creator.tap()
        XCTAssertTrue(app.navigationBars["skill-creator"].waitForExistence(timeout: 10), "its page")
        XCTAssertTrue(text("Her 内置").exists && text("/skill:skill-creator").exists && text("只能手动调用").exists, "where it came from, how it is called")
        let file = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "SKILL.md")).firstMatch
        XCTAssertTrue(file.waitForExistence(timeout: 30), "its files")
        herShot("Her: a skill's page")
        file.tap()
        XCTAssertTrue(text("name: skill-creator").waitForExistence(timeout: 30), "SKILL.md as text")
        app.buttons["完成"].tap()
        let skillOn = app.switches["her-skill-on"]
        XCTAssertTrue(skillOn.waitForExistence(timeout: 10) && !on(skillOn))
        skillOn.switches.firstMatch.tap()
        try waitFor("on again") { on(skillOn) ? true : nil }
        back()
        back()

        // Web search: the card says how it stands; the switch on its page writes her file.
        let searchCard = app.buttons["her-search"]
        try waitFor("the card says web search is on (\(searchCard.label))", timeout: 30) { searchCard.label.contains("已开启") ? true : nil }
        searchCard.tap()
        XCTAssertTrue(app.navigationBars["联网搜索"].waitForExistence(timeout: 10))
        let search = app.switches["her-search-on"]
        XCTAssertTrue(search.waitForExistence(timeout: 30) && on(search))
        XCTAssertTrue(text("搜索服务").exists && any.matching(NSPredicate(format: "identifier BEGINSWITH %@", "her-search-")).count > 2, "the services")
        herShot("Her: web search")
        search.switches.firstMatch.tap()
        try waitFor("off") { on(search) ? nil : true }
        back()
        try waitFor("the card says it is off (\(searchCard.label))", timeout: 30) { searchCard.label.contains("已关闭") ? true : nil }
        searchCard.tap()
        XCTAssertTrue(search.waitForExistence(timeout: 30) && !on(search), "off, as her says")
        search.switches.firstMatch.tap()
        try waitFor("on again") { on(search) ? true : nil }
        back()

        // The browsers: Her controls none here until she is let, as no app shows her browser's window; then the
        // owner's Chrome as it stands, and a site set apart from All Sites.
        let browserCard = app.buttons["her-browser"]
        try waitFor("the card says the browser is off (\(browserCard.label))", timeout: 30) { browserCard.label.contains("已关闭") ? true : nil }
        tap("her-browser")
        XCTAssertTrue(app.navigationBars["浏览器"].waitForExistence(timeout: 10))
        let control = app.switches["her-browser-on"]
        XCTAssertTrue(control.waitForExistence(timeout: 30) && !on(control), "off")
        control.switches.firstMatch.tap()
        try waitFor("Her controls browsers") { on(control) ? true : nil }
        XCTAssertTrue(text("你的 Chrome").exists && text("还没有例外").exists)
        let site = app.textFields["her-browser-site"]
        for _ in 0 ..< 3 where !site.isHittable { app.swipeUp() }
        site.tap()
        site.typeText("example.com")
        tap("her-browser-site-add")
        XCTAssertFalse(text("还没有例外").waitForExistence(timeout: 10), "the site set apart")
        herShot("Her: browsers, a site set apart")
        back()
        try waitFor("the card says the browser is on (\(browserCard.label))", timeout: 30) { browserCard.label.contains("已开启") ? true : nil }

        // Settings: the Model page, with the Token Plan onboarding connected, and its models.
        tap("her-tab-settings")
        tap("her-model-page")
        XCTAssertTrue(app.buttons["her-model"].waitForExistence(timeout: 30), "her model")
        let plan = app.buttons["her-provider-tencent-token-plan"]
        XCTAssertTrue(plan.waitForExistence(timeout: 30) && plan.label.contains("腾讯云 Token Plan"), "the plan connected")
        XCTAssertTrue(text("使用订阅").exists && app.buttons["her-chatgpt-sign-in"].exists && text("API 与自定义服务").exists)
        herShot("Her: model")
        plan.tap()
        XCTAssertTrue(app.switches.matching(NSPredicate(format: "identifier BEGINSWITH %@", "her-model-shown-")).firstMatch.waitForExistence(timeout: 30), "its models, each in the menu or not")
        herShot("Her: a provider's models")
        back()
        back()

        // Her environment: its storage, and its console in the app.
        tap("her-environment")
        tap("her-storage")
        XCTAssertTrue(text("存放内容").waitForExistence(timeout: 30), "what her data holds")
        let only = text("这个文件夹是 Her 的助手、对话、记忆和产出文件的唯一一份")
        for _ in 0 ..< 3 where !only.exists { app.swipeUp() }
        XCTAssertTrue(only.exists, "that it is the only copy")
        herShot("Her: storage")
        back()
        tap("her-console")
        let web = app.webViews.firstMatch
        XCTAssertTrue(web.waitForExistence(timeout: 30), "the console")
        let pages = web.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["连接器", "Connectors"])).firstMatch
        XCTAssertTrue(pages.waitForExistence(timeout: 60), "its pages")
        herShot("Her: the console")
        herBackToConversation()
    }

    /// Her's page from her conversation, by her mark, at `tab`: work, abilities or settings.
    func herPage(_ tab: String) {
        if app.keyboards.count > 0 { app.swipeDown() }
        tap("her-avatar")
        tap("her-tab-\(tab)")
    }

    func herBackToConversation() {
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        for _ in 0..<4 where !composer.exists {
            app.navigationBars.buttons.element(boundBy: 0).tap()
            _ = composer.waitForExistence(timeout: 3)
        }
        XCTAssertTrue(composer.waitForExistence(timeout: 10), "back to the conversation")
    }

    /// Allows what Her asks to run, when she asks.
    func herAllowIfAsked() {
        let allow = app.buttons["her-allow-once"]
        if allow.waitForExistence(timeout: 20) { allow.tap() }
    }

    // MARK: Helpers

    /// Scheduled tasks as the owner meets them: none yet, New writing a sentence for Her, one she makes (her-mock's
    /// routine) a line in her conversation that opens it, paused and resumed by its switch, run now, its run's message
    /// naming it and opening the run, which shows its message in the chat, and deleted.
    func herRoutines() throws {
        // A card of her page, under Work, beside Files.
        herPage("work")
        let entry = app.buttons["her-routines"]
        XCTAssertTrue(entry.waitForExistence(timeout: 10), "on her page")
        XCTAssertGreaterThan(entry.frame.minX, app.buttons["her-files"].frame.maxX, "beside Files, on its right")
        entry.tap()
        XCTAssertTrue(app.navigationBars["定时任务"].waitForExistence(timeout: 10))
        XCTAssertTrue(text("还没有定时任务").waitForExistence(timeout: 30), "none yet")
        herShot("Scheduled tasks: none yet")

        // New goes back to the conversation with a sentence to finish, the keyboard up.
        tap("routines-new")
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        try waitFor("the sentence in the composer", timeout: 10) { (composer.value as? String)?.hasPrefix("帮我建一个定时任务：") == true ? true : nil }
        XCTAssertTrue(waitUntil(timeout: 5) { self.app.keyboards.count > 0 }, "ready to type on")
        composer.typeText("[[mock:routine:拉伸|in 1m|say:该起来活动一下了。]] 一分钟后提醒我拉伸")
        tap("her-send")
        approveIfAsked()
        let lines = app.buttons.matching(identifier: "her-routine-line")
        let stretchLine = lines.containing(NSPredicate(format: "label CONTAINS %@", "已创建定时任务「拉伸」")).firstMatch
        XCTAssertTrue(stretchLine.waitForExistence(timeout: 60), "what Her did, as a line")

        // One that runs every night: its line opens it.
        herSend("[[mock:routine:夜间检查|0 3 * * *|say:夜间检查做完了。]] 每天凌晨三点检查一下")
        approveIfAsked()
        let nightLine = lines.containing(NSPredicate(format: "label CONTAINS %@", "已创建定时任务「夜间检查」")).firstMatch
        XCTAssertTrue(nightLine.waitForExistence(timeout: 60))
        herShot("Her: scheduled tasks she made")
        press(nightLine)
        XCTAssertTrue(app.navigationBars["夜间检查"].waitForExistence(timeout: 10), "the task's page")
        try waitFor("what it is told", timeout: 20) { text("[[mock:say:夜间检查做完了。]]").exists ? true : nil }
        XCTAssertTrue(text("说好的时间").exists, "when, as Her wrote it")
        XCTAssertTrue(text("接下来").waitForExistence(timeout: 10), "its next runs")
        herShot("Scheduled task: its page")

        // Paused on its page, it is under Paused in the list; its switch there resumes it.
        let enabled = app.switches["routine-enabled"].firstMatch
        XCTAssertTrue(enabled.waitForExistence(timeout: 10))
        XCTAssertEqual(enabled.value as? String, "1")
        enabled.switches.firstMatch.tap()
        try waitFor("paused", timeout: 20) { enabled.value as? String == "0" && !text("接下来").exists ? true : nil }
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(lines.firstMatch.waitForExistence(timeout: 10), "back in the conversation")
        herPage("work")
        entry.tap()
        XCTAssertTrue(text("已暂停").waitForExistence(timeout: 10), "under Paused")
        let night = app.descendants(matching: .any).matching(identifier: "routine-row").containing(NSPredicate(format: "label CONTAINS %@", "夜间检查")).firstMatch
        XCTAssertTrue(night.exists)
        herShot("Scheduled tasks: one paused")
        let rowSwitch = night.switches.firstMatch
        rowSwitch.switches.firstMatch.tap()
        try waitFor("resumed", timeout: 20) { rowSwitch.value as? String == "1" && !text("已暂停").exists ? true : nil }

        // Run now, once the owner says so: it runs, and its report reaches the conversation.
        press(night.buttons.firstMatch)
        XCTAssertTrue(app.navigationBars["夜间检查"].waitForExistence(timeout: 10))
        tap("routine-run-now")
        let confirm = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "routine-run-now-confirm", "立即运行")).allElementsBoundByIndex.last { $0.isHittable && $0.identifier != "routine-run-now" }
        XCTAssertNotNil(confirm, "asked first")
        confirm?.tap()
        let runs = app.descendants(matching: .any).matching(identifier: "routine-run")
        try waitFor("its run, told", timeout: 90) { runs.containing(NSPredicate(format: "label CONTAINS %@", "发给你了")).firstMatch.exists ? true : nil }
        herShot("Scheduled task: run now")
        herBackToConversation()
        XCTAssertTrue(app.staticTexts["夜间检查做完了。"].waitForExistence(timeout: 30), "its report, passed on")

        // The reminder, a minute after it was made: its message names the task, and opens the run.
        XCTAssertTrue(app.staticTexts["该起来活动一下了。"].waitForExistence(timeout: 120), "the reminder")
        let source = app.buttons.matching(identifier: "her-routine-source").containing(NSPredicate(format: "label CONTAINS %@", "拉伸")).firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 10), "which task sent it")
        herShot("Her: a scheduled task's message")
        press(source)
        XCTAssertTrue(app.navigationBars["一次运行"].waitForExistence(timeout: 10), "the run's page")
        try waitFor("how it went", timeout: 20) { text("发给你了").exists ? true : nil }
        XCTAssertTrue(app.buttons["routine-run-task"].exists, "its task a tap away")
        herShot("Scheduled task: a run")
        tap("routine-show-in-chat")
        XCTAssertTrue(waitUntil(timeout: 10) { self.app.staticTexts["该起来活动一下了。"].isHittable }, "its message in view")

        // A one-off that has run is over: under Finished, where it is deleted from its page.
        herPage("work")
        entry.tap()
        XCTAssertTrue(text("已结束").waitForExistence(timeout: 20), "under Finished")
        let stretch = app.descendants(matching: .any).matching(identifier: "routine-row").containing(NSPredicate(format: "label CONTAINS %@", "拉伸")).firstMatch
        press(stretch.buttons.firstMatch)
        XCTAssertTrue(app.navigationBars["拉伸"].waitForExistence(timeout: 10))
        tap("routine-delete")
        let delete = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "routine-delete-confirm", "删除")).allElementsBoundByIndex.last { $0.isHittable }
        XCTAssertNotNil(delete, "asked first")
        delete?.tap()
        XCTAssertTrue(app.navigationBars["定时任务"].waitForExistence(timeout: 10), "back at the list")
        try waitFor("gone from the list", timeout: 20) { !text("已结束").exists && !stretch.exists ? true : nil }
        XCTAssertTrue(night.exists, "the other one stays")
        herShot("Scheduled tasks: one deleted")
        herBackToConversation()

        // Deleted, its message still opens the run it came from, with the task named as deleted and nothing to open.
        XCTAssertTrue(source.waitForExistence(timeout: 10), "the message still names it")
        press(source)
        XCTAssertTrue(app.navigationBars["一次运行"].waitForExistence(timeout: 10), "the run's page")
        try waitFor("how it went", timeout: 20) { text("发给你了").exists ? true : nil }
        XCTAssertTrue(text("拉伸（已删除）").exists, "its task, named as deleted")
        XCTAssertFalse(app.buttons["routine-run-task"].exists, "a deleted task does not open")
        XCTAssertFalse(text("读不到这次运行").exists)
        herShot("Scheduled task: a run of a deleted task")
        app.navigationBars.buttons.element(boundBy: 0).tap()
    }

    /// Allows Her's next call when she asks (a routine she makes asks in the manual mode).
    func approveIfAsked() {
        if app.descendants(matching: .any)["her-approval"].firstMatch.waitForExistence(timeout: 30) { tap("her-allow-once") }
    }

    func herSend(_ text: String, approveAI: Bool = true) {
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 30), "the composer")
        composer.tap()
        composer.typeText(text)
        XCTAssertTrue(waitUntil { self.app.buttons["her-send"].isHittable }, "Send is reachable above the keyboard")
        tap("her-send")
        let allow = app.alerts["与第三方 AI 共享数据？"].buttons["允许与 AI 共享"]
        if approveAI, allow.waitForExistence(timeout: 2) { allow.tap() }
    }

    func herShot(_ name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }
}
