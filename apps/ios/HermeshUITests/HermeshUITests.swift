import XCTest

/// The iPhone app end to end, as a person uses it, in Chinese. Run by
/// TestIPhone (e2e/iphone_test.go), which sets up an owner Mac with a
/// session in every state and serves HERMESH_E2E_CONTROL: the world around
/// the phone (the Mac approving the join, an agent asking to connect, what
/// the machines say a session's state is).
///
/// One test, in steps: the iPhone joins once, and each step builds on it.
final class HermeshUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-HermeshTestApprove", "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
    }

    func testTheIPhoneApp() throws {
        try XCTContext.runActivity(named: "joins the owner by its connection address") { _ in try join() }
        try XCTContext.runActivity(named: "counts the sessions waiting on the overview, and answers one from the menu") { _ in try answerFromOverview() }
        try XCTContext.runActivity(named: "interrupts a running session") { _ in try interrupt() }
        try XCTContext.runActivity(named: "shows a reply as the agent writes it") { _ in try streamedReply() }
        try XCTContext.runActivity(named: "archives a session and undoes it") { _ in try archiveAndUndo() }
        try XCTContext.runActivity(named: "lays the session list out from the view menu") { _ in try viewMenu() }
        try XCTContext.runActivity(named: "renames a session from its row, and deletes another once confirmed") { _ in try renameAndDelete() }
        try XCTContext.runActivity(named: "shows what a session changed") { _ in try diff() }
        try XCTContext.runActivity(named: "approves an agent asking to connect") { _ in try approveAgent() }
        try XCTContext.runActivity(named: "renews a grant") { _ in try renew() }
        try XCTContext.runActivity(named: "sets up waking one of an agent's bots, after a check that catches a wrong key") { _ in try wake() }
        try XCTContext.runActivity(named: "shows a machine and its projects") { _ in try machine() }
        try XCTContext.runActivity(named: "marks the trusted devices among the machines") { _ in try devices() }
        try XCTContext.runActivity(named: "chooses light or dark in the account") { _ in try appearance() }
        try XCTContext.runActivity(named: "shows the work on the overview, and usage in the account") { _ in try usage() }
        try XCTContext.runActivity(named: "a swipe that starts on a row opens the menu, not the row") { _ in try swipeOnRow() }
        try XCTContext.runActivity(named: "offers New from the overview's top right") { _ in try newMenu() }
        try XCTContext.runActivity(named: "searches sessions from the menu") { _ in try search() }
        try XCTContext.runActivity(named: "opened without the relay, shows what it kept and catches up") { _ in try offline() }
        try XCTContext.runActivity(named: "back and forth to the home screen, never shows its Mac unreachable") { _ in try backAndForth() }
        try XCTContext.runActivity(named: "a question arrives as a notification and opens its session") { _ in try notification() }
    }

    // MARK: Steps

    func join() throws {
        let address = try control("address")["address"] as! String
        tap("own-server")
        let field = app.textFields["connection-address"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText(address)
        tap("continue")
        tap("join")
        // The Mac approves (the test approves at once, so the waiting screen
        // may be gone already); then the two digits it shows, unless none
        // are asked for.
        let digits = app.textFields["digits"]
        let finish = app.buttons["finish"]
        let deadline = Date().addingTimeInterval(60)
        while !digits.exists && !finish.exists && Date() < deadline { sleep(1) }
        if digits.exists {
            let d = try waitFor("the Mac's digits") { (try? self.control("digits")["digits"] as? String).flatMap { $0?.isEmpty == false ? $0 : nil } }
            digits.tap()
            digits.typeText(d)
        }
        XCTAssertTrue(finish.waitForExistence(timeout: 30), "joined")
        XCTAssertTrue(text("这台 iPhone 已经是你的可信设备").exists)
        allowNotifications(after: finish)
        XCTAssertTrue(app.navigationBars["概览"].waitForExistence(timeout: 30), "the overview")
        try waitFor("this iPhone registers for notifications") { (try self.control("registered")["devices"] as? Int ?? 0) > 0 ? true : nil }
    }

    func answerFromOverview() throws {
        // The overview counts them (one asks, two failed); the menu lists them.
        let waiting = app.descendants(matching: .any)["sessions-waiting"].firstMatch
        XCTAssertTrue(waiting.waitForExistence(timeout: 30), "the sessions waiting")
        XCTAssertTrue(waiting.label.contains("3 个会话等你处理"), waiting.label)
        press(waiting)
        XCTAssertTrue(app.descendants(matching: .any)["drawer-dim"].waitForExistence(timeout: 5), "the menu opens")
        tap("drawer-session-选颜色")
        XCTAssertTrue(app.descendants(matching: .any)["question"].waitForExistence(timeout: 20), "the question")
        app.buttons["蓝"].tap()
        tap("answer")
        XCTAssertTrue(toast(containing: "已回答"))
        try waitFor("the question is answered") { try self.sessionStatus("选颜色") != "waiting_input" ? true : nil }
    }

    func interrupt() throws {
        openFromMenu(session: "跑很久")
        XCTAssertTrue(text("跑很久").waitForExistence(timeout: 20))
        tap("interrupt")
        tap("interrupt-confirm")
        try waitFor("the turn stops") { try self.sessionStatus("跑很久") == "idle" ? true : nil }
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "这一轮被打断了")).firstMatch.waitForExistence(timeout: 20))
    }

    func streamedReply() throws {
        _ = try control("stream?title=\("慢慢写".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        openFromMenu(session: "慢慢写")
        // The reply shows while the turn still runs...
        let draft = app.descendants(matching: .any)["session-draft"].firstMatch
        XCTAssertTrue(draft.waitForExistence(timeout: 30))
        XCTAssertEqual(try sessionStatus("慢慢写"), "running")
        try waitFor("the whole reply is written") { draft.exists && draft.label.contains("写完以后还要等一会儿") ? true : nil }
        // ...and stays, as the stored message, once it is done (the prompt above says it too).
        try waitFor("the turn ends") { try self.sessionStatus("慢慢写") == "idle" ? true : nil }
        XCTAssertTrue(draft.waitForNonExistence(timeout: 10))
        XCTAssertGreaterThanOrEqual(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "写完以后还要等一会儿")).count, 2)
    }

    func archiveAndUndo() throws {
        openMenu()
        app.buttons["全部会话"].tap()
        let row = app.descendants(matching: .any)["session-写 hello"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20))
        row.swipeLeft()
        app.buttons["归档"].firstMatch.tap()
        try waitFor("archived") { try self.sessionStatus("写 hello") == "archived" ? true : nil }
        let undo = app.buttons["撤销"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        try waitFor("unarchived") { try self.sessionStatus("写 hello") == "idle" ? true : nil }
    }

    func viewMenu() throws {
        // Still on 全部会话. By status (the default), then by date.
        XCTAssertTrue(app.descendants(matching: .any)["group-已结束"].waitForExistence(timeout: 10))
        tap("view-options")
        menuItem("分组").tap()
        menuItem("按日期").tap()
        XCTAssertTrue(app.descendants(matching: .any)["group-今天"].waitForExistence(timeout: 10), "grouped by date")
        tap("view-options")
        menuItem("分组").tap()
        menuItem("按状态").tap()
        // A group's heading folds it.
        let row = app.descendants(matching: .any)["session-写 hello"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        tap("group-已结束")
        XCTAssertTrue(row.waitForNonExistence(timeout: 5), "folded")
        tap("group-已结束")
        XCTAssertTrue(row.waitForExistence(timeout: 5), "open again")
        // Without the metadata, a row is its title alone.
        XCTAssertTrue(row.label.contains("bot"), row.label)
        tap("view-options")
        menuItem("显示").tap()
        menuItem("元数据").tap()
        XCTAssertTrue(waitUntil { !row.label.contains("bot") }, row.label)
        tap("view-options")
        menuItem("显示").tap()
        menuItem("元数据").tap()
        XCTAssertTrue(waitUntil { row.label.contains("bot") }, row.label)
    }

    func renameAndDelete() throws {
        _ = try control("start?title=\("改个名字".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        _ = try control("start?title=\("要删的".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        // Long-press: Rename, Archive, Delete…; Rename asks for the new title.
        let row = app.descendants(matching: .any)["session-改个名字"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 30))
        row.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["删除…"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["归档"].exists)
        app.buttons["重命名"].tap()
        let field = app.alerts.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        XCTAssertEqual(field.value as? String, "改个名字")
        field.tap()
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 8) + "新的名字")
        app.alerts.buttons["重命名"].firstMatch.tap()
        try waitFor("renamed on the Mac") { (try self.control("session?title=\("改个名字".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)")["title"] as? String) == "新的名字" ? true : nil }
        XCTAssertTrue(app.descendants(matching: .any)["session-新的名字"].waitForExistence(timeout: 20))
        // Swipe: Delete asks first, saying what stays.
        let gone = app.descendants(matching: .any)["session-要删的"].firstMatch
        XCTAssertTrue(gone.waitForExistence(timeout: 20))
        gone.swipeLeft()
        app.buttons["删除"].firstMatch.tap()
        XCTAssertTrue(text("删除「要删的」？").waitForExistence(timeout: 5))
        XCTAssertTrue(text("仍保留在分支").exists)
        let confirm = app.descendants(matching: .any)["delete-confirm"].firstMatch
        if confirm.waitForExistence(timeout: 3) { press(confirm) } else { app.sheets.buttons["删除"].firstMatch.tap() }
        XCTAssertTrue(toast(containing: "已删除"))
        try waitFor("deleted on the Mac") { try self.sessionStatus("要删的") == "" ? true : nil }
        XCTAssertTrue(gone.waitForNonExistence(timeout: 10))
    }

    func diff() throws {
        openFromMenu(session: "写 hello")
        tap("session-info")
        let file = text("hello.txt")
        XCTAssertTrue(file.waitForExistence(timeout: 20), "the changed file is listed")
        file.tap()
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "hello again")).firstMatch.waitForExistence(timeout: 10), "its diff")
        app.navigationBars.buttons.element(boundBy: 0).tap() // back to the file list
        tap("close")
    }

    func approveAgent() throws {
        openMenu()
        app.buttons["概览"].tap()
        let code = try control("agent", method: "POST")["code"] as! String
        XCTAssertFalse(code.isEmpty)
        let verify = app.buttons["verify-pairing"]
        XCTAssertTrue(verify.waitForExistence(timeout: 30), "the request shows on the overview")
        verify.tap()
        XCTAssertTrue(text("「Cursor」想连接你的电脑").waitForExistence(timeout: 10))
        app.buttons["scope-projects"].tap()
        let all = app.switches["所有项目（包括以后添加的）"]
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.switches.firstMatch.exists ? all.switches.firstMatch.tap() : all.tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("approve")
        XCTAssertTrue(text("「Cursor」已连接").waitForExistence(timeout: 30), "approved")
        app.buttons["完成"].tap()
        try waitFor("the agent is connected") { (try self.control("agent")["connected"] as? Bool) == true ? true : nil }
    }

    func renew() throws {
        let before = try control("grant?label=bot")["version"] as? Int ?? 0
        openMenu()
        app.buttons["授权"].tap()
        let grant = app.buttons["grant-bot"]
        XCTAssertTrue(grant.waitForExistence(timeout: 20))
        grant.tap()
        tap("renew")
        app.buttons["90 天"].tap()
        tap("renew-confirm")
        XCTAssertTrue(toast(containing: "续期 90 天"))
        try waitFor("the grant is renewed") {
            let g = try self.control("grant?label=bot")
            return (g["version"] as? Int ?? 0) > before && (g["days"] as? Int ?? 0) >= 89 ? true : nil
        }
    }

    func wake() throws {
        let url = try control("webhook")["url"] as! String
        openMenu()
        app.buttons["授权"].tap()
        let grant = app.buttons["grant-bot"]
        XCTAssertTrue(grant.waitForExistence(timeout: 20))
        grant.tap()
        let add = app.buttons["wake-add"]
        for _ in 0..<5 where !add.exists { app.swipeUp() }
        tap("wake-add", timeout: 30)
        let name = app.textFields["wake-name"]
        XCTAssertTrue(name.waitForExistence(timeout: 10))
        name.tap()
        name.typeText("Tech Demos")
        tap("wake-next")
        tap("wake-copy")
        XCTAssertTrue(text("在「Tech Demos」里建一个 routine").exists)
        tap("wake-made")
        let field = app.textFields["wake-url"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText(url)
        let key = app.textFields["wake-key"]
        key.tap()
        key.typeText("wrong")
        tap("wake-check")
        // A wrong key: the check says so, and nothing is kept.
        let reason = app.staticTexts["wake-reason"]
        XCTAssertTrue(reason.waitForExistence(timeout: 30))
        XCTAssertTrue(reason.label.contains("密钥不对"), reason.label)
        tap("wake-back")
        key.tap()
        key.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 5) + "crsr_ok")
        tap("wake-check")
        XCTAssertTrue(app.staticTexts["wake-ok"].waitForExistence(timeout: 30))
        tap("wake-done")
        let row = app.buttons["wake-tech-demos"]
        for _ in 0..<5 where !row.exists { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 20))
        XCTAssertTrue(row.label.contains("已开"), row.label)
        let calls = try control("webhook")["calls"] as? [String] ?? []
        XCTAssertEqual(calls, ["hermesh.check tech-demos Bearer wrong", "hermesh.check tech-demos Bearer crsr_ok"])
    }

    func machine() throws {
        openMenu()
        app.buttons["机器"].tap()
        let m = app.buttons["machine-owner-mac"]
        XCTAssertTrue(m.waitForExistence(timeout: 20))
        m.tap()
        // Its page: the coding agents, each opening its own; projects are there.
        XCTAssertTrue(text("暂停接受新会话").waitForExistence(timeout: 20))
        let agent = app.buttons["agent-gemini"]
        XCTAssertTrue(agent.waitForExistence(timeout: 20), "the coding agent")
        XCTAssertTrue(agent.label.contains("1 个项目"), agent.label)
        XCTAssertFalse(text("demo").exists, "projects are on the agent's page")
        agent.tap()
        // The agent's page: its projects, how agents work in each set from a menu.
        XCTAssertTrue(text("demo").waitForExistence(timeout: 20), "its project")
        tap("project-menu-demo")
        tap("原地：直接改这个目录")
        try waitFor("the project works in place") { self.text("原地").exists && !self.text("独立 worktree").exists ? true : nil }
        tap("project-menu-demo")
        tap("独立 worktree：每个会话在自己的分支里")
        try waitFor("a separate worktree again") { self.text("独立 worktree").exists ? true : nil }
        // Another directory by the same name doesn't replace the project.
        tap("add-project")
        let dir = app.textFields.element(boundBy: 0)
        XCTAssertTrue(dir.waitForExistence(timeout: 10))
        dir.tap()
        dir.typeText("/tmp/demo")
        app.buttons["添加"].tap()
        XCTAssertTrue(text("已经有叫「demo」的项目").waitForExistence(timeout: 20), "the name is taken")
        app.alerts.buttons["好"].exists ? app.alerts.buttons["好"].tap() : app.alerts.buttons.firstMatch.tap()
        tap("close")
        XCTAssertTrue(text("demo").waitForExistence(timeout: 10), "the project as it was")
        app.navigationBars.buttons.element(boundBy: 0).tap()
    }

    func devices() throws {
        // The owner's Mac is a machine and a trusted device; this iPhone a
        // trusted device only.
        openMenu()
        app.buttons["机器"].tap()
        let mac = app.buttons["machine-owner-mac"]
        XCTAssertTrue(mac.waitForExistence(timeout: 20))
        XCTAssertTrue(mac.label.contains("可信设备"), mac.label)
        let phone = app.descendants(matching: .any)["device-this"].firstMatch
        XCTAssertTrue(phone.waitForExistence(timeout: 20), "this iPhone")
        XCTAssertTrue(phone.label.contains("这台 iPhone") && phone.label.contains("可信设备"), phone.label)
        XCTAssertTrue(text("其他可信设备").exists)
    }

    func appearance() throws {
        openMenu()
        tap("account")
        // Three small phones, as Display & Brightness has them.
        let system = app.buttons["appearance-system"], dark = app.buttons["appearance-dark"]
        XCTAssertTrue(system.waitForExistence(timeout: 20))
        XCTAssertTrue(system.isSelected && system.label == "跟随系统", system.label)
        press(dark)
        try waitFor("dark chosen") { dark.isSelected && !system.isSelected ? true : nil }
        press(system)
        try waitFor("the system's again") { system.isSelected && !dark.isSelected ? true : nil }
        // Notifications are a page of their own, further down.
        app.swipeUp()
        tap("notifications")
        XCTAssertTrue(app.switches["notify-questions"].waitForExistence(timeout: 10), "the notification settings")
        XCTAssertTrue(app.switches["notify-turns"].exists && text("连接请求").exists)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("close")
    }

    func swipeOnRow() throws {
        // The page follows the finger, so the finger ends where it started on
        // the row: the row must not open as well.
        for id in ["machine-row-owner-mac", "agent-bot"] {
            let row = app.descendants(matching: .any)[id].firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 20), id)
            row.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: row.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)))
            XCTAssertTrue(app.descendants(matching: .any)["drawer-dim"].waitForExistence(timeout: 5), "the menu opens from \(id)")
            app.descendants(matching: .any)["drawer-dim"].tap()
            sleep(1)
            XCTAssertTrue(app.navigationBars["概览"].exists && app.buttons["menu"].exists, "\(id) opened too")
        }
    }

    func newMenu() throws {
        tap("new")
        let code = app.buttons["输入配对码…"]
        XCTAssertTrue(code.waitForExistence(timeout: 5), "the New menu")
        XCTAssertTrue(app.buttons["连接 agent…"].exists)
        code.tap()
        XCTAssertTrue(app.navigationBars["输入配对码"].waitForExistence(timeout: 10), "the pairing code sheet")
        tap("close")
    }

    func usage() throws {
        openMenu()
        app.buttons["概览"].tap()
        // Nothing runs since the interrupt; today's sessions as the machine
        // counts them; the agent that worked, the one approved folded; the machine.
        let today = app.descendants(matching: .any)["working-today"]
        XCTAssertTrue(today.waitForExistence(timeout: 20))
        try waitFor("nothing runs, today's work counted") { today.label.hasPrefix("0") && today.label.contains("个会话在跑") && today.label.contains("今天有活动") ? true : nil }
        let bot = app.descendants(matching: .any)["agent-bot"].firstMatch
        XCTAssertTrue(bot.exists, "the agent that worked")
        XCTAssertTrue(bot.label.contains("空闲"), bot.label)
        let more = app.descendants(matching: .any)["more-agents"].firstMatch
        XCTAssertTrue(more.exists && more.label.contains("另外 1 个 agent"), more.label)
        press(more)
        XCTAssertTrue(app.descendants(matching: .any)["agent-Cursor"].firstMatch.waitForExistence(timeout: 5), "the folded agent")
        let mac = app.descendants(matching: .any)["machine-row-owner-mac"].firstMatch
        XCTAssertTrue(mac.exists && mac.label.contains("在线"), mac.label)
        // View All Sessions opens the menu; usage is in the account.
        press(app.descendants(matching: .any)["all-sessions"].firstMatch)
        XCTAssertTrue(app.descendants(matching: .any)["drawer-dim"].waitForExistence(timeout: 5), "the menu opens")
        tap("account")
        // Usage in place: the numbers and the weeks of days, no page to open.
        XCTAssertTrue(app.descendants(matching: .any)["usage"].firstMatch.waitForExistence(timeout: 20), "usage")
        XCTAssertTrue(text("近 30 天 · tokens").exists && text("活跃天数").exists && text("连续使用").exists)
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "近 18 周的用量")).firstMatch.exists, "the weeks")
        tap("close")
        app.descendants(matching: .any)["drawer-dim"].tap()
    }

    func search() throws {
        openMenu()
        tap("search")
        // Its own page, the field at the top and focused: the states to
        // search within and the recent sessions first.
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the search page")
        XCTAssertTrue(text("按状态搜索").waitForExistence(timeout: 5), "the states")
        XCTAssertTrue(app.descendants(matching: .any)["session-写 hello"].firstMatch.exists, "a recent session")
        // A state becomes a token: the failed sessions alone.
        tap("search-state-failed")
        XCTAssertTrue(text("2 个结果").waitForExistence(timeout: 10), "the failed ones")
        XCTAssertTrue(app.descendants(matching: .any)["session-崩溃"].firstMatch.exists && app.descendants(matching: .any)["session-限流"].firstMatch.exists)
        XCTAssertFalse(app.descendants(matching: .any)["session-写 hello"].firstMatch.exists, "only what matches")
        cancelSearch()

        tap("search")
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        sleep(1)
        field.typeText("崩溃")
        XCTAssertTrue(text("1 个结果").waitForExistence(timeout: 10))
        let result = app.descendants(matching: .any)["session-崩溃"].firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: 10))
        result.tap()
        XCTAssertFalse(app.searchFields.firstMatch.exists, "the search page closes")
        XCTAssertTrue(app.descendants(matching: .any)["session-failed"].waitForExistence(timeout: 20), "the failure shows")

        // The search is remembered, and cleared.
        openMenu()
        tap("search")
        XCTAssertTrue(text("最近搜索").waitForExistence(timeout: 10), "the recent searches")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "崩溃")).firstMatch.exists)
        tap("clear-recent")
        XCTAssertTrue(text("最近搜索").waitForNonExistence(timeout: 5), "cleared")
        cancelSearch()
    }

    /// Cancels the search, which closes its page: the button beside the
    /// field (whatever it is called).
    func cancelSearch() {
        let field = app.searchFields.firstMatch.frame
        let cancel = app.buttons.allElementsBoundByIndex.first { $0.frame.minX >= field.maxX - 2 && abs($0.frame.midY - field.midY) < 12 }
        XCTAssertNotNil(cancel, "the search's cancel button")
        cancel.map(press)
        XCTAssertTrue(app.searchFields.firstMatch.waitForNonExistence(timeout: 5), "the search page closes")
    }

    /// The relay goes away and the app is opened again: it shows what it
    /// kept at once, says it can't reach Hermesh, and catches up once the
    /// relay is back.
    func offline() throws {
        _ = try control("relay?up=0", method: "POST")
        app.terminate()
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["machine-row-owner-mac"].waitForExistence(timeout: 3), "what it kept shows at once")
        let line = app.descendants(matching: .any)["sync-line"].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), "it says it is connecting")
        try waitFor("it says it can't reach the relay", timeout: 30) { line.exists && line.label.contains("连不上") ? true : nil }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "offline, what was kept"
        shot.lifetime = .keepAlways
        add(shot)
        _ = try control("relay?up=1", method: "POST")
        try waitFor("caught up once the relay is back", timeout: 90) { line.exists ? nil : true }
        let back = XCTAttachment(screenshot: app.screenshot())
        back.name = "caught up"
        back.lifetime = .keepAlways
        add(back)
    }

    /// To the home screen and back, slowly and at once, a few times: each
    /// time the app is current again, and on the way it never says its Mac
    /// can't be reached (it stayed online).
    func backAndForth() throws {
        let line = app.descendants(matching: .any)["sync-line"].firstMatch
        let unreachable = app.descendants(matching: .any)["unreachable"].firstMatch
        for away: UInt32 in [3, 0, 0, 3] {
            XCUIDevice.shared.press(.home)
            sleep(away)
            app.activate()
            XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10))
            let deadline = Date().addingTimeInterval(30)
            repeat {
                XCTAssertFalse(unreachable.exists, "the Mac shown unreachable on the way back")
            } while line.exists && Date() < deadline
            XCTAssertFalse(line.exists, "current again")
            XCTAssertTrue(app.descendants(matching: .any)["machine-row-owner-mac"].exists)
        }
    }

    func notification() throws {
        _ = try control("ask?title=\("要发布吗".addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)", method: "POST")
        XCUIDevice.shared.press(.home)
        try waitFor("the relay sends a notification") { (try self.control("pushed")["pushed"] as? Int ?? 0) > 0 ? true : nil }
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        // On a phone the extension shows the question; a Simulator doesn't
        // run it for simulated pushes, so the generic text shows there, in
        // the Simulator's own language (the app's Chinese is only its own).
        let banner = springboard.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@ OR label CONTAINS %@", "要发布吗", "有事需要你处理", "Something needs you")).firstMatch
        XCTAssertTrue(banner.waitForExistence(timeout: 60), "the notification arrives")
        banner.tap()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10))
        let panel = app.descendants(matching: .any)["question"]
        XCTAssertTrue(panel.waitForExistence(timeout: 30), "its session opens with the question")
        panel.buttons["发布"].tap()
        tap("answer")
        try waitFor("answered") { try self.sessionStatus("要发布吗") != "waiting_input" ? true : nil }
    }

    // MARK: Helpers

    /// An item of the open menu, by the start of its label (a submenu's label also says what it is set to).
    func menuItem(_ title: String) -> XCUIElement {
        // Not the list's own 显示已归档 under the menu.
        let e = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND identifier != 'toggle-archived'", title)).firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: 5), "no menu item \(title)")
        return e
    }

    func waitUntil(timeout: TimeInterval = 10, _ check: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if check() { return true }
            usleep(300_000)
        }
        return false
    }

    /// Text on screen that contains s.
    func text(_ s: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    func tap(_ id: String, timeout: TimeInterval = 15) {
        let e = app.descendants(matching: .any)[id].firstMatch
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "no \(id)")
        press(e)
    }

    /// Taps e; by its frame when XCUITest can't work out a hit point (a
    /// button on Liquid Glass).
    func press(_ e: XCUIElement) {
        if e.isHittable {
            e.tap()
        } else {
            e.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        }
    }

    func openMenu() {
        // Open already (a sheet opened from it was closed): the page behind is dimmed.
        if app.descendants(matching: .any)["drawer-dim"].exists { return }
        // Back to a root page first.
        while app.navigationBars.buttons["BackButton"].exists { app.navigationBars.buttons["BackButton"].tap() }
        let back = app.navigationBars.buttons.element(boundBy: 0)
        if !app.buttons["menu"].exists && back.exists { back.tap() }
        tap("menu")
        XCTAssertTrue(app.descendants(matching: .any)["drawer"].waitForExistence(timeout: 5))
        sleep(1) // until it has slid in
    }

    func openFromMenu(session title: String) {
        openMenu()
        tap("drawer-session-\(title)")
    }

    func toast(containing text: String) -> Bool {
        app.descendants(matching: .any).matching(identifier: "toast").containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: 15)
            || app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: 2)
    }

    /// Taps button, which asks for notifications, and allows them. The tap
    /// is repeated while the button is still there (one during its
    /// appearing transition can miss).
    func allowNotifications(after button: XCUIElement) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons["允许"]
        for _ in 0 ..< 3 {
            if button.exists { press(button) }
            let deadline = Date().addingTimeInterval(8)
            while Date() < deadline {
                if allow.exists {
                    allow.tap()
                    return
                }
                usleep(300_000)
            }
        }
        XCTFail("no notification permission alert")
    }

    func sessionStatus(_ title: String) throws -> String {
        try control("session?title=\(title.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed)!)")["status"] as? String ?? ""
    }

    /// Asks the test's control server (see TestIPhone).
    func control(_ path: String, method: String = "GET") throws -> [String: Any] {
        guard let base = ProcessInfo.processInfo.environment["HERMESH_E2E_CONTROL"], let url = URL(string: "\(base)/\(path)") else {
            throw XCTSkip("run by TestIPhone (HERMESH_E2E_CONTROL)")
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.timeoutInterval = 60
        var out: [String: Any] = [:]
        var failure: Error?
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: req) { data, _, error in
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] { out = obj } else { failure = error ?? URLError(.badServerResponse) }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 70)
        if let failure { throw failure }
        return out
    }

    @discardableResult
    func waitFor<T>(_ what: String, timeout: TimeInterval = 60, _ check: () throws -> T?) throws -> T {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if let v = try check() { return v }
            sleep(1)
        }
        XCTFail("timed out: \(what)")
        throw XCTSkip(what)
    }
}
