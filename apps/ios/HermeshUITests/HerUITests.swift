import XCTest

/// Her on the iPhone (docs/her-server.md §9), as a person uses her, against
/// the owner Mac's her, whose model is her-mock (TestIPhone, iphoneHerEnv):
/// Her turned on for the Mac with Face ID, her onboarding connecting a model
/// with a Token Plan, a chat, her settings and her memory, a question, files she sends, an approval, and the Mac gone
/// offline, where a message waits on the iPhone until it is back. her-mock
/// answers a message by its directive (e2e/her_owner_test.go).
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHer go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testHer() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run by TestIPhone with her built (HERMESH_E2E_HER)") }
        try XCTContext.runActivity(named: "joins the owner by its connection address") { _ in try join() }
        try XCTContext.runActivity(named: "Her is first in the menu, and is turned on for the Mac with Face ID") { _ in try herTurnOn() }
        try XCTContext.runActivity(named: "with no model, Her speaks first, offers the ways, and connects a Token Plan") { _ in try herOnboarding() }
        try XCTContext.runActivity(named: "a chat: the message, then Her's reply") { _ in try herChat() }
        try XCTContext.runActivity(named: "the plus beside the field: what it adds opens out of it, the field staying") { _ in try herAdd() }
        try XCTContext.runActivity(named: "her name opens her settings, a page of their own") { _ in try herSettings() }
        try XCTContext.runActivity(named: "what she notes shows in her settings as written, and is cleared there") { _ in try herMemory() }
        try XCTContext.runActivity(named: "a question: a tapped answer is the owner's message") { _ in try herQuestion() }
        try XCTContext.runActivity(named: "files Her sent: Markdown reads as the conversation renders it, code in its colours") { _ in try herFiles() }
        try XCTContext.runActivity(named: "an approval in the composer's place, allowed once") { _ in try herApproval() }
        try XCTContext.runActivity(named: "the Mac offline: what was said stays, and a message waits here until it is back") { _ in try herOffline() }
        try XCTContext.runActivity(named: "opened on its last messages, earlier ones are read at the top") { _ in try herEarlier() }
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
        // Turning her on also lets her use the owner's computers, and the sheet says so: Approve is below the fold.
        let approve = app.descendants(matching: .any)["sign-approve"].firstMatch
        for _ in 0..<4 where !approve.exists { app.swipeUp() }
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
        // Her's mark and name over the conversation, which scrolls under them; no button beside them.
        let name = app.buttons["her-name"]
        XCTAssertTrue(name.exists && name.label.contains("Her"), name.label)
        XCTAssertFalse(app.buttons["her-settings"].exists, "no button beside them")
        name.tap()
        // Where she runs, her model, and how freely she acts.
        XCTAssertTrue(text("运行在").waitForExistence(timeout: 10), "her settings")
        XCTAssertTrue(app.descendants(matching: .any)["her-environment"].firstMatch.label.contains("owner-mac"))
        XCTAssertTrue(text("模型").exists && app.buttons["先问我"].exists && app.buttons["完全访问"].exists)
        XCTAssertTrue(text("在另一台机器上开启 Her").exists)
        herShot("Her: settings")
        // Further down: her memory, a row of its own that says when there is nothing yet, then clearing.
        app.swipeUp()
        let memory = app.buttons["her-memory"]
        XCTAssertTrue(memory.waitForExistence(timeout: 10), "the memory row")
        XCTAssertTrue(text("还没有").waitForExistence(timeout: 10), memory.label)
        XCTAssertTrue(text("清空对话").exists && app.buttons["her-clear-memory"].exists, "clearing the chat or the memory")
        herShot("Her: settings, further down")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 10), "back to the conversation")
    }

    func herMemory() throws {
        // Her notes a rule (her-mock's remember) with no approval asked, and says so.
        herSend("[[mock:remember:- 回复一律用中文。(2026-11-11)]] 以后都用中文")
        XCTAssertTrue(text("记下了。").waitForExistence(timeout: 60), "her reply")
        // Her settings: the memory row opens the page, MEMORY.md as the conversation renders Markdown.
        app.buttons["her-name"].tap()
        XCTAssertTrue(text("运行在").waitForExistence(timeout: 10), "her settings")
        app.swipeUp()
        let row = app.buttons["her-memory"]
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the memory row")
        row.tap()
        XCTAssertTrue(text("回复一律用中文。(2026-11-11)").waitForExistence(timeout: 30), "the line, without its mark")
        XCTAssertFalse(text("- 回复一律用中文。(2026-11-11)").exists, "rendered, not as written")
        herShot("Her: memory")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        // Clearing it asks first, then the row says there is nothing.
        let clear = app.buttons["her-clear-memory"]
        if !clear.waitForExistence(timeout: 5) { app.swipeUp() }
        XCTAssertTrue(clear.waitForExistence(timeout: 10), "Clear Memory")
        clear.tap()
        XCTAssertTrue(text("清空「Her」记住的事？").waitForExistence(timeout: 10), "asked first")
        app.buttons["清除"].firstMatch.tap()
        XCTAssertTrue(text("还没有").waitForExistence(timeout: 30), "nothing remembered")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 10), "back to the conversation")
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
        for _ in 0 ..< 6 where !first.exists { app.swipeDown() }
        XCTAssertTrue(first.waitForExistence(timeout: 30), "the first ones, read at the top")
        herShot("Her: earlier messages read")
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
