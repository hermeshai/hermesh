import XCTest

/// A walk through every page, from the first launch, for looking at the layout on each iPhone model: no test of
/// behaviour, it keeps a picture of each page in the result bundle, named in order, and goes on past a page it cannot
/// reach. Run by TestIPhone, with Her built, on the Simulator HERMESH_IPHONE_SIMULATOR names;
/// HERMESH_WALK_TEXT=<content size category> (TEST_RUNNER_-prefixed) sets the text size.
///
///     TEST_RUNNER_HERMESH_WALK_TEXT=UICTContentSizeCategoryAccessibilityL HERMESH_IPHONE_E2E=1 \
///       HERMESH_IPHONE_SIMULATOR=<id> HERMESH_IPHONE_RESULTS=<dir>.xcresult HERMESH_IPHONE_GREP=testWalkthrough \
///       go test ./e2e -run 'TestIPhone$' -timeout 40m
extension HermeshUITests {
    @objc func testWalkthrough() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        continueAfterFailure = true
        if let size = ProcessInfo.processInfo.environment["HERMESH_WALK_TEXT"] {
            app.terminate()
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", size]
            app.launch()
        }
        var n = 0
        func shot(_ name: String) {
            n += 1
            sleep(1)
            herShot(String(format: "%02d %@", n, name))
        }

        // The first launch, and the demo from it.
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 30))
        shot("welcome")
        if go("try-demo") {
            sleep(3)
            shot("demo work")
            if go("menu") { shot("demo menu"); _ = go("drawer-dim") }
            if go("run-demo-task") { sleep(2); shot("demo question") }
            _ = go("exit-demo")
        }

        // Joining by the connection address.
        XCTAssertTrue(go("own-server"))
        shot("own server")
        let field = app.textFields["connection-address"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText(try control("address")["address"] as! String)
        tap("continue")
        XCTAssertTrue(text("加入你的 Hermesh").waitForExistence(timeout: 30))
        shot("join")
        tap("send-join")
        let digits = app.textFields["digits"]
        let finish = app.buttons["notifications-on"]
        let deadline = Date().addingTimeInterval(60)
        while !digits.exists && !finish.exists && Date() < deadline { sleep(1) }
        if digits.exists {
            shot("digits")
            let d = try waitFor("the Mac's digits") { (try? self.control("digits")["digits"] as? String).flatMap { $0?.isEmpty == false ? $0 : nil } }
            digits.tap()
            digits.typeText(d)
        }
        XCTAssertTrue(finish.waitForExistence(timeout: 30))
        shot("notifications")
        allowNotifications(after: finish)
        XCTAssertTrue(app.buttons["her-not-now"].waitForExistence(timeout: 30))
        shot("her first run")
        tap("her-not-now")

        // The overview and the menu with the sessions, before Her.
        sleep(5)
        shot("overview")
        app.swipeUp()
        shot("overview, further down")
        openMenu()
        shot("menu")
        if go("view-options") { shot("menu, view menu"); dismiss() }
        if go("search") {
            shot("search")
            cancelSearch()
        }
        _ = try control("rich?title=\("排版".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        openSession("排版", timeout: 30)
        sleep(3)
        shot("session, rich")
        app.swipeDown(); app.swipeDown()
        shot("session, rich, top")
        openSession("写 hello")
        sleep(3)
        shot("session, files written")
        if go("session-info") { sleep(2); shot("session info"); app.swipeUp(); shot("session info, further down"); dismiss() }
        openSession("选颜色")
        sleep(3)
        shot("session, question")
        openMenu()
        if go("account"), go("account-settings-open"), go("account-machines") {
            shot("machines")
            if go("machine-owner-mac") {
                sleep(2)
                shot("machine")
                app.swipeUp()
                shot("machine, further down")
                if go("agent-gemini") { sleep(2); shot("coding agent"); app.swipeUp(); shot("coding agent, further down"); back() }
            }
        }
        openMenu()
        if go("account"), go("account-settings-open"), go("account-grants") {
            shot("grants")
            let grant = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'grant-'")).firstMatch
            if grant.waitForExistence(timeout: 10) { press(grant); sleep(2); shot("grant"); app.swipeUp(); shot("grant, further down") }
        }
        showOverview()
        if go("new") {
            shot("new menu")
            if go(app.buttons["添加环境…"]) { sleep(1); shot("add machine"); dismiss() } else { dismiss() }
        }
        openMenu()
        if go("account"), go("account-settings-open") {
            sleep(2)
            shot("account")
            app.swipeUp()
            shot("account, further down")
            dismiss()
        }

        // Her, turned on: where she runs, the signing sheet, her starting, her onboarding.
        openMenu()
        tap("her-place")
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 10))
        shot("her where")
        _ = try control("her-slow", method: "POST")
        tap("her-turn-on-owner-mac", timeout: 30)
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30))
        shot("her signing sheet")
        let approve = app.descendants(matching: .any)["sign-approve"].firstMatch
        for _ in 0..<4 where !approve.isHittable { app.swipeUp() }
        shot("her signing sheet, approve")
        tap("sign-approve")
        _ = text("Her 正在 owner-mac 上启动").waitForExistence(timeout: 20)
        shot("her starting")
        if go("her-tag") { shot("her starting, team panel"); closeTeam() }
        if app.descendants(matching: .any)["her-starting-long"].firstMatch.waitForExistence(timeout: 20) { shot("her starting, longer than usual") }
        _ = try control("her-slow", method: "DELETE")
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180))
        shot("her no model")
        app.buttons["填写 API Key"].tap()
        XCTAssertTrue(text("用哪家的？").waitForExistence(timeout: 10))
        shot("her which provider")
        app.buttons["DeepSeek"].tap()
        let key = app.secureTextFields["her-key"]
        XCTAssertTrue(key.waitForExistence(timeout: 10))
        shot("her key")
        key.tap()
        shot("her key, keyboard")
        key.typeText("sk-e2e-key")
        tap("her-key-save")
        XCTAssertTrue(text("好了，我会用").waitForExistence(timeout: 120))
        shot("her connected")

        // Her's conversation.
        herSend("[[mock:send:你好，主人。这一段回复比较长，会折成好几行，看看气泡在这台手机上排得怎么样。]] 你好")
        XCTAssertTrue(text("你好，主人").waitForExistence(timeout: 60))
        dismissKeyboard()
        shot("her chat")
        let composer = app.descendants(matching: .any)["her-composer"].firstMatch
        composer.tap()
        composer.typeText("一条很长的消息，打到好几行，看看输入框在小屏上会不会顶到顶上去。一条很长的消息，打到好几行，看看输入框。")
        shot("her composer, long")
        composer.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 60))
        dismissKeyboard()
        if go("her-attach") { shot("her add menu"); app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)).tap(); sleep(1) }
        herSend("[[mock:ask:要哪种颜色？|蓝|红]] 问我")
        sleep(5)
        dismissKeyboard()
        shot("her question")
        if go(app.buttons["蓝"]) { sleep(3) }
        herSend(#"[[mock:mesh:session_start:{"project":"owner-mac/demo","agent":"gemini","prompt":"say 做完了","title":"Her 派的活"}]] 开工"#)
        if app.descendants(matching: .any)["her-approval"].firstMatch.waitForExistence(timeout: 60) {
            dismissKeyboard()
            shot("her approval")
            tap("her-allow-once")
        }
        sleep(10)
        if app.buttons["to-end"].exists { tap("to-end") }
        shot("her session card")
        herSend("[[mock:mesh:work_summary:{}]] 今天团队干了什么")
        sleep(6)
        dismissKeyboard()
        if app.buttons["to-end"].exists { tap("to-end") }
        shot("her work card")
        if go("her-tag") {
            sleep(1)
            shot("her team panel")
            closeTeam()
        }
        // Progress, over her conversation.
        if go("her-progress") { sleep(2); shot("progress"); back() }
        // Her's page: each tab, and the pages its cards open.
        if go("her-avatar") {
            sleep(2)
            shot("her page, work")
            if go("her-files") { sleep(2); shot("files"); back() }
            if go("her-routines") { sleep(2); shot("routines"); back() }
            if go("her-tab-abilities") {
                sleep(1)
                shot("her page, abilities")
                if go("her-memory") { sleep(2); shot("her memory"); back() }
                if go("her-skills") { sleep(2); shot("skills"); back() }
                if go("her-connectors") {
                    sleep(2)
                    shot("connectors")
                    if go("connectors-add") { sleep(1); shot("add connector"); dismiss() }
                    back()
                }
                if go("her-search") { sleep(2); shot("web search"); back() }
                if go("her-browser") { sleep(2); shot("browser"); back() }
            }
            if go("her-tab-settings") {
                sleep(1)
                shot("her page, settings")
                if go("her-model-page") { sleep(2); shot("her model"); back() }
                if go("her-environment") {
                    sleep(2)
                    shot("her environment")
                    if go("her-storage") { sleep(2); shot("storage"); back() }
                    back()
                }
            }
            herBackToConversation()
        }

        // Dark appearance.
        openMenu()
        if go("account"), go("account-settings-open") {
            _ = go("appearance-dark")
            dismiss()
            if go("her-place") || go("her-tag") {
                sleep(2)
                shot("her, dark")
                if go("her-tag") { shot("her team panel, dark"); closeTeam() }
            }
            openMenu()
            shot("menu, dark")
            tap("overview-place")
            sleep(2)
            shot("work, dark")
        }
        // Back to her page, as the app opens on it: cold start once Her is on.
        app.terminate()
        app.launch()
        sleep(1)
        herShot(String(format: "%02d relaunch, at once", n + 1)); n += 1
        sleep(6)
        shot("relaunch")
    }

    /// Taps what id names if it shows within a few seconds; whether it did.
    private func go(_ id: String) -> Bool { go(app.descendants(matching: .any)[id].firstMatch) }

    private func go(_ e: XCUIElement) -> Bool {
        guard e.waitForExistence(timeout: 8) else { return false }
        press(e)
        return true
    }

    private func back() {
        let own = app.buttons["BackButton"] // a session's page draws its bar
        let b = own.exists && own.isHittable ? own : app.navigationBars.buttons.element(boundBy: 0)
        if b.exists { b.tap(); sleep(1) }
    }

    /// Closes a sheet, a menu or a popover.
    private func dismiss() {
        for id in ["close", "完成", "取消"] where app.buttons[id].exists {
            app.buttons[id].firstMatch.tap()
            sleep(1)
            return
        }
        let region = app.otherElements["PopoverDismissRegion"].firstMatch
        if region.exists { region.tap(); sleep(1); return }
        app.swipeDown(velocity: .fast)
        sleep(1)
    }

    /// Closes the team panel by a tap on the dim just under it (on a small iPhone, a strip at the foot: the panel
    /// scrolls, so swiping its heading up scrolls it).
    private func closeTeam() {
        let panel = app.descendants(matching: .any)["team-panel"].firstMatch
        guard panel.exists else { return }
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: panel.frame.midX, dy: panel.frame.maxY + 10)).tap()
        sleep(1)
    }

    private func dismissKeyboard() {
        guard app.keyboards.count > 0 else { return }
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)).tap()
        sleep(1)
    }
}
