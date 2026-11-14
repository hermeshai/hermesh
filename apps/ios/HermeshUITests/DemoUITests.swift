import XCTest

/// Exercises the public entry with no account, pairing server or test signer.
final class DemoUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
    }

    func testTaskAndExit() {
        tap("try-demo")
        XCTAssertTrue(app.staticTexts["Demo Mode"].waitForExistence(timeout: 15))
        tap("run-demo-task")
        XCTAssertTrue(app.staticTexts["Which greeting should the sample app show?"].waitForExistence(timeout: 10))
        tap("Welcome aboard!")
        tap("answer")
        let result = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "The sample page now says “Welcome aboard!”")).firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: 15))
        XCTAssertFalse(app.buttons["answer"].exists)
        tap("session-info")
        let file = app.staticTexts["src/welcome.ts"]
        for _ in 0..<4 where !file.isHittable { app.swipeUp() }
        tap("src/welcome.ts")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "+export const greeting = \"Welcome aboard!\"")).firstMatch.waitForExistence(timeout: 10))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("close")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-completed-task"; shot.lifetime = .keepAlways; add(shot)
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
        // Reopening starts a new disposable world, and never initializes the real owner.
        tap("try-demo")
        reach("session-Choose a greeting")
        XCTAssertEqual(app.buttons.matching(identifier: "session-Choose a greeting").count, 1)
        tap("exit-demo")
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["try-demo"].exists)
    }

    func testApprovalsAndMachineControls() {
        tap("try-demo")
        tap("verify-pairing")
        XCTAssertFalse(app.buttons["approve"].isEnabled)
        tap("scope-projects")
        let all = app.switches["All projects (including ones added later)"]
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.switches.firstMatch.exists ? all.switches.firstMatch.tap() : all.tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["approve"].isEnabled)
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["client-connected"].waitForExistence(timeout: 10))
        tap("run-demo-task")
        tap("decline")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "The sample page now says")).firstMatch.waitForExistence(timeout: 15))
        backToOverview()
        tap("menu"); tap("menu-machines")
        tap("Add a Machine")
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 10))
        tap("open-machine")
        let toggle = app.switches["accept-sessions"]
        if !toggle.isHittable { app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        toggle.switches.firstMatch.exists ? toggle.switches.firstMatch.tap() : toggle.tap()
        XCTAssertTrue(NSPredicate(format: "value == %@", "0").evaluate(with: toggle))
        toggle.switches.firstMatch.exists ? toggle.switches.firstMatch.tap() : toggle.tap()
        XCTAssertTrue(NSPredicate(format: "value == %@", "1").evaluate(with: toggle))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-machine"; shot.lifetime = .keepAlways; add(shot)
        app.swipeDown()
        tap("agent-codex")
        tap("test-agent")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Hello from the demo agent! This response is simulated.")).firstMatch.waitForExistence(timeout: 10))
        tap("add-project")
        tap("browse-folders")
        tap("folder-Projects")
        tap("folder-playground")
        tap("choose-folder")
        tap("add-project-confirm")
        XCTAssertTrue(app.buttons["project-menu-playground"].waitForExistence(timeout: 10))
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
    }

    func testSearch() {
        tap("try-demo")
        tap("menu"); tap("search")
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
        field.typeText("greeting\n")
        tap("session-Choose a greeting")
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        tap("exit-demo")
        tap("try-demo")
        tap("menu"); tap("search")
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["clear-recent"].exists)
        let frame = field.frame
        let cancel = app.buttons.allElementsBoundByIndex.first { $0.frame.minX >= frame.maxX - 2 && abs($0.frame.midY - frame.midY) < 12 }
        XCTAssertNotNil(cancel)
        cancel?.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5))
        tap("exit-demo")
    }

    func testAccountAppearance() {
        tap("try-demo")
        tap("menu")
        tap("account")
        tap("appearance-dark")
        XCTAssertTrue(app.buttons["appearance-dark"].isSelected)
        tap("close")
        checkpoint("demo-dark-workspace")
        tap("account")
        XCTAssertTrue(app.buttons["appearance-dark"].isSelected)
        tap("close")
        tap("exit-demo")
        tap("try-demo")
        tap("menu"); tap("account")
        XCTAssertTrue(app.buttons["appearance-system"].isSelected)
        tap("close")
        tap("exit-demo")
    }

    func testChineseEntry() {
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
        let start = app.buttons["start-new"], demo = app.buttons["try-demo"], join = app.buttons["start-join"]
        XCTAssertTrue(demo.waitForExistence(timeout: 15))
        XCTAssertTrue(start.label.contains("开始使用"))
        XCTAssertTrue(demo.label.contains("先体验一下"))
        XCTAssertTrue(demo.isHittable && join.isHittable)
        XCTAssertLessThan(start.frame.maxY, join.frame.midY)
        XCTAssertLessThan(join.frame.maxY, demo.frame.midY)
        let welcome = XCTAttachment(screenshot: app.screenshot())
        welcome.name = "welcome-three-choices-zh"; welcome.lifetime = .keepAlways; add(welcome)
        tap("try-demo")
        XCTAssertTrue(app.staticTexts["演示模式"].waitForExistence(timeout: 15))
        tap("run-demo-task")
        XCTAssertTrue(app.staticTexts["示例应用应该显示哪句问候语？"].waitForExistence(timeout: 10))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-question-zh"; shot.lifetime = .keepAlways; add(shot)
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
    }

    func testReviewGrantLifecycle() {
        tap("try-demo")
        place("Grants"); tap("grant-ChatGPT")
        tap("rename-agent")
        replace(app.alerts.textFields.firstMatch, with: "Review Agent")
        app.alerts.buttons["Rename"].tap()
        XCTAssertTrue(app.navigationBars["Review Agent"].waitForExistence(timeout: 5))
        reach("Sessions"); tap("Sessions")
        XCTAssertTrue(app.descendants(matching: .any)["session-Add a welcome page"].waitForExistence(timeout: 10))
        back()
        reach("renew"); tap("renew"); tap("90 days"); tap("renew-confirm")
        let renewedDate = Date().addingTimeInterval(90 * 86400).formatted(Date.FormatStyle(date: .abbreviated, time: .omitted).locale(Locale(identifier: "en_US")))
        for _ in 0..<3 where !contains("Valid until").exists { app.swipeDown() }
        XCTAssertTrue(contains("Valid until \(renewedDate)").waitForExistence(timeout: 5))
        checkpoint("grant-renewed")
        reach("Change What It Can Use…"); tap("Change What It Can Use…")
        tap("scope-projects")
        let all = app.switches["All projects (including ones added later)"]
        (all.switches.firstMatch.exists ? all.switches.firstMatch : all).tap()
        tap("hello-world")
        back(); tap("Coding agents")
        // All coding agents first, as on the Mac: turned off, none is chosen, and Claude Code alone is.
        let agentsAll = app.switches["all-agents"]
        (agentsAll.switches.firstMatch.exists ? agentsAll.switches.firstMatch : agentsAll).tap()
        tap("scope-agent-claude"); back(); tap("Save")
        XCTAssertTrue(app.buttons["scope-projects"].waitForNonExistence(timeout: 5))
        for _ in 0..<3 where !contains("hello-world").exists { app.swipeDown() }
        XCTAssertTrue(contains("hello-world").waitForExistence(timeout: 5))
        XCTAssertTrue(contains("Valid until \(renewedDate)").exists)
        let agents = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Coding agents,'")).firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 5))
        XCTAssertFalse(agents.label.contains("Codex"))
        XCTAssertTrue(agents.label.contains("Claude Code"))
        checkpoint("grant-scope-saved")
        app.swipeDown(); reach("disconnect"); tap("disconnect"); tap("Disconnect")
        place("Grants")
        XCTAssertFalse(app.buttons["grant-Review Agent"].exists)
        place("All Sessions"); tap("session-Choose a greeting")
        XCTAssertTrue(contains("The simulated task was stopped.").waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["answer"].exists)
        place("Grants"); tap("Connect an Agent")
        tap("scope-projects")
        let every = app.switches["All projects (including ones added later)"]
        (every.switches.firstMatch.exists ? every.switches.firstMatch : every).tap()
        back(); tap("approve"); tap("run-demo-task")
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        tap("exit-demo")
    }

    func testReviewSessionLifecycle() {
        tap("try-demo"); reach("session-Add a welcome page"); tap("session-Add a welcome page")
        tap("session-more"); tap("Copy Session ID")
        XCTAssertTrue(contains("Copied the session ID").waitForExistence(timeout: 5))
        tap("session-more"); tap("Rename")
        replace(app.alerts.textFields.firstMatch, with: "Review Session"); tap("rename-confirm")
        XCTAssertTrue(contains("Review Session").waitForExistence(timeout: 5))
        tap("session-more"); tap("Archive")
        XCTAssertTrue(contains("Archived: read-only").waitForExistence(timeout: 5))
        checkpoint("session-archived")
        tap("Unarchive")
        XCTAssertTrue(contains("Archived: read-only").waitForNonExistence(timeout: 5))
        tap("session-more"); tap("Delete…"); tap("delete-confirm")
        place("All Sessions")
        XCTAssertFalse(app.descendants(matching: .any)["session-Review Session"].exists)
        for group in ["By Date", "By Agent", "By Status", "By Machine"] {
            tap("view-options"); menuItem("Group"); tap(group)
        }
        for order in ["Oldest First", "Newest First"] {
            tap("view-options"); menuItem("Sort"); tap(order)
        }
        tap("view-options"); menuItem("Show"); tap("Metadata")
        tap("toggle-archived"); tap("toggle-archived")
        let headings = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'group-'"))
        XCTAssertGreaterThan(headings.count, 0)
        headings.firstMatch.tap(); headings.firstMatch.tap()
        tap("exit-demo")
    }

    func testReviewFreeAnswerAndInterrupt() {
        tap("try-demo"); tap("run-demo-task")
        replace(app.textFields["answer-text"], with: "Reviewer says hello")
        tap("answer")
        tap("interrupt"); tap("interrupt-confirm")
        XCTAssertTrue(contains("The simulated task was stopped.").waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["interrupt"].exists)
        tap("exit-demo"); tap("try-demo"); tap("run-demo-task")
        replace(app.textFields["answer-text"], with: "Reviewer says hello")
        tap("answer")
        XCTAssertTrue(contains("The sample page now says “Reviewer says hello”").waitForExistence(timeout: 15))
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 15))
    }

    func testReviewMachineLifecycle() {
        tap("try-demo"); place("Machines"); tap("machine-Mac mini")
        tap("machine-menu"); tap("Rename…")
        replace(app.alerts.textFields.firstMatch, with: "review-mac")
        app.alerts.buttons["Rename"].tap()
        XCTAssertTrue(app.staticTexts["review-mac"].waitForExistence(timeout: 5))
        tap("check-again")
        tap("agent-codex")
        reach("default-model"); tap("default-model"); tap("Demo Model")
        tap("default-effort"); tap("High")
        tap("Speed"); tap("Fast")
        reach("project-menu-hello-world"); tap("project-menu-hello-world")
        tap("In Place: Change This Folder Directly")
        XCTAssertTrue(contains("In place").waitForExistence(timeout: 5))
        checkpoint("machine-project-settings")
        tap("project-menu-hello-world"); tap("Stop Using with Codex")
        XCTAssertTrue(app.buttons["project-menu-hello-world"].waitForNonExistence(timeout: 5))
        tap("add-project"); tap("repo-~/Projects/hello-world"); tap("add-project-confirm")
        XCTAssertTrue(app.buttons["project-menu-hello-world"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["project-menu-hello-world-2"].exists)
        back(); reach("Sessions"); tap("Sessions")
        XCTAssertTrue(app.descendants(matching: .any)["session-Choose a greeting"].waitForExistence(timeout: 5))
        back(); tap("machine-menu"); tap("Remove…"); tap("Remove")
        XCTAssertTrue(app.buttons["machine-review-mac"].waitForNonExistence(timeout: 5))
        place("All Sessions"); tap("session-Choose a greeting")
        XCTAssertTrue(contains("The simulated task was stopped.").waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["answer"].exists)
        tap("exit-demo")
    }

    func testReviewAllNewEntries() {
        tap("try-demo")
        tap("new"); tap("Connect an Agent…"); tap("Deny")
        tap("new"); tap("Add a Machine…"); tap("close")
        tap("new"); tap("Scan a Code…"); tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 10))
        tap("close")
        tap("new"); tap("Enter a Pairing Code…")
        let code = app.textFields["pairing-code"]
        let sample = code.value as? String ?? ""
        XCTAssertFalse(sample.isEmpty)
        replace(code, with: "invalid")
        tap("Find")
        XCTAssertTrue(contains("Use Add Machine or Connect Agent").waitForExistence(timeout: 5))
        replace(code, with: sample)
        tap("Find"); tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 10))
        tap("close")
        place("Grants"); tap("Connect an Agent")
        tap("scope-projects")
        let all = app.switches["All projects (including ones added later)"]
        (all.switches.firstMatch.exists ? all.switches.firstMatch : all).tap()
        back(); tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["client-connected"].waitForExistence(timeout: 10))
        tap("close"); tap("exit-demo")
    }

    func testReviewOverviewAndSettings() {
        tap("try-demo")
        reach("work-period")
        for range in ["7 Days", "30 Days", "Today"] { tap(range) }
        reach("flow-client-ChatGPT"); tap("flow-client-ChatGPT"); tap("flow-client-ChatGPT")
        tap("flow-agent-codex"); tap("flow-agent-codex")
        let bands = app.descendants(matching: .any)["flow-bands"].firstMatch
        bands.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        XCTAssertTrue(app.descendants(matching: .any)["flow-tip"].waitForExistence(timeout: 5))
        checkpoint("overview-flow")
        app.otherElements["PopoverDismissRegion"].firstMatch.tap()
        reach("coding-agent-codex")
        let row = app.descendants(matching: .any)["coding-agent-codex"].firstMatch
        let hour = CGFloat(Calendar.current.component(.hour, from: Date()))
        row.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 50 + (row.frame.width - 64) * (hour + 0.5) / 24, dy: row.frame.height - 14)).tap()
        XCTAssertTrue(app.descendants(matching: .any)["heat-tip"].waitForExistence(timeout: 5))
        app.otherElements["PopoverDismissRegion"].firstMatch.tap()
        // The way to all sessions; then the menu: a machine's sessions, and from them the machine's page.
        reach("all-sessions"); tap("all-sessions"); back()
        tap("menu"); tap("menu-machine-Mac mini"); tap("scope-details"); back(); back()
        tap("menu"); tap("account")
        for theme in ["light", "dark", "system"] {
            tap("appearance-\(theme)"); XCTAssertTrue(app.buttons["appearance-\(theme)"].isSelected)
        }
        XCTAssertFalse(app.buttons["erase-everything"].exists)
        XCTAssertFalse(app.buttons["notifications"].exists)
        tap("close"); tap("exit-demo")
    }

    /// Her and her team (docs/her-team.md): Work lists what needs the owner and answers a question where it is
    /// listed; the menu is a map of the team with no session in it, whose rows open a machine's or an agent's
    /// sessions; Her's entry, while she runs nowhere, asks where she should, and Not Now returns to Work.
    func testReviewHerAndHerTeam() {
        tap("try-demo")
        // The question's card says who sent the work to which agent; one tap answers it.
        reach("session-Choose a greeting")
        let card = app.descendants(matching: .any).matching(identifier: "session-Choose a greeting").firstMatch
        XCTAssertTrue(card.label.contains("ChatGPT → Codex"), card.label)
        checkpoint("work-needs-you")
        tap("quick-answer-Hello, world!")
        XCTAssertTrue(app.buttons["quick-answer-Hello, world!"].waitForNonExistence(timeout: 15), "answered: no answers to tap")
        // The menu: Her (not on), Work, the machines with their coding agents, the connected agents.
        tap("menu")
        let drawer = app.descendants(matching: .any).matching(identifier: "drawer").firstMatch
        for id in ["her-place", "work-place", "menu-machines", "menu-machine-Mac mini", "menu-agent-Mac mini-claude", "menu-agent-Linux Studio-codex", "menu-grants", "menu-agent-ChatGPT"] {
            XCTAssertTrue(drawer.buttons[id].waitForExistence(timeout: 10), id)
        }
        XCTAssertTrue(drawer.buttons["her-place"].label.contains("not on"), drawer.buttons["her-place"].label)
        XCTAssertFalse(drawer.staticTexts["Add a welcome page"].exists, "no session in the menu")
        checkpoint("menu-team-map")
        // A coding agent's row opens its sessions on that machine; from them, its page; Back returns to Work.
        tap("menu-agent-Linux Studio-codex")
        XCTAssertTrue(app.navigationBars["Codex on Linux Studio"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["session-Add a welcome page"].waitForExistence(timeout: 10), "its session")
        XCTAssertFalse(app.buttons["session-Choose a greeting"].exists, "not the other machine's")
        tap("scope-details"); back(); back()
        XCTAssertTrue(app.navigationBars["Work"].waitForExistence(timeout: 10))
        // A connected agent's row opens the sessions it started.
        tap("menu"); tap("menu-agent-ChatGPT")
        XCTAssertTrue(app.buttons["session-Add a welcome page"].waitForExistence(timeout: 10))
        back()
        // Her: asleep, asking where she should run. Not Now returns to Work.
        tap("menu"); tap("her-place")
        XCTAssertTrue(app.buttons["her-not-now"].waitForExistence(timeout: 10), "Her's first run")
        checkpoint("her-first-run")
        tap("her-not-now")
        XCTAssertTrue(app.navigationBars["Work"].waitForExistence(timeout: 10))
        reach("her-off"); tap("her-off")
        XCTAssertTrue(app.buttons["her-not-now"].waitForExistence(timeout: 10), "Work's banner leads to her first run")
        tap("exit-demo")
    }

    func testReviewToolOutputsAndClipboard() {
        tap("try-demo"); reach("session-Add a welcome page"); tap("session-Add a welcome page")
        let group = app.buttons.matching(NSPredicate(format: "label CONTAINS 'Edited 1 file'")).firstMatch
        XCTAssertTrue(group.waitForExistence(timeout: 10)); group.tap()
        let shell = app.buttons.matching(NSPredicate(format: "label CONTAINS 'npm test'")).firstMatch
        XCTAssertTrue(shell.waitForExistence(timeout: 5)); shell.tap()
        let output = app.descendants(matching: .any)["tool-output"].firstMatch
        XCTAssertTrue(output.waitForExistence(timeout: 5))
        XCTAssertTrue((output.value as? String ?? output.label).contains("2 tests passed"))
        checkpoint("expanded-tool-output")
        shell.tap()
        let edit = app.buttons.matching(NSPredicate(format: "label CONTAINS 'src/welcome.ts'")).firstMatch
        edit.tap()
        XCTAssertTrue(output.waitForExistence(timeout: 5))
        XCTAssertTrue((output.value as? String ?? output.label).contains("Updated the sample welcome page."))
        tap("session-more"); tap("Copy Session ID")
        place("Overview"); tap("run-demo-task")
        let answer = app.textFields["answer-text"]
        answer.press(forDuration: 1.2)
        tap("Paste")
        XCTAssertEqual(answer.value as? String, "demo-finished-session")
        tap("answer")
        let reply = contains("The sample page now says “demo-finished-session”")
        XCTAssertTrue(reply.waitForExistence(timeout: 15))
        reply.press(forDuration: 1.2)
        let copy = app.buttons["Copy"].firstMatch
        XCTAssertTrue(copy.waitForExistence(timeout: 5))
        copy.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        XCTAssertTrue(copy.waitForNonExistence(timeout: 5))
        place("Overview"); tap("run-demo-task")
        let pasted = app.textFields["answer-text"]
        pasted.press(forDuration: 1.2); tap("Paste")
        XCTAssertTrue((pasted.value as? String ?? "").contains("The sample page now says “demo-finished-session”"))
        tap("exit-demo")
    }

    func testReviewSearchFilters() {
        tap("try-demo")
        for state in ["failed", "waiting", "done"] {
            tap("menu"); tap("search")
            tap("search-state-\(state)")
            if state == "failed" {
                XCTAssertTrue(app.descendants(matching: .any)["no-results"].waitForExistence(timeout: 5))
            } else {
                let title = state == "waiting" ? "Choose a greeting" : "Add a welcome page"
                XCTAssertTrue(app.descendants(matching: .any)["session-\(title)"].waitForExistence(timeout: 5))
            }
            cancelSearch()
            tap("drawer-dim")
        }
        tap("exit-demo")
    }

    func testReviewFolderChoices() {
        tap("try-demo"); place("Machines"); tap("machine-Mac mini"); tap("agent-claude")
        reach("add-project"); tap("add-project"); tap("repo-~/Projects/hello-world")
        XCTAssertFalse(app.buttons["add-project-confirm"].isEnabled)
        tap("chosen-folder")
        tap("folder-menu"); tap("Show Hidden Folders")
        tap("folder-menu"); tap("Type a Path…")
        replace(app.alerts.textFields.firstMatch, with: "~/Projects/reviewer")
        app.alerts.buttons["Go"].tap()
        tap("choose-folder")
        tap("workspace-in-place"); tap("workspace-worktree")
        tap("base-branch"); tap("main")
        tap("add-project-confirm")
        XCTAssertTrue(app.buttons["project-menu-reviewer"].waitForExistence(timeout: 5))
        tap("project-menu-reviewer"); tap("Stop Using with Claude Code")
        XCTAssertTrue(app.buttons["project-menu-reviewer"].waitForNonExistence(timeout: 5))
        tap("exit-demo")
    }

    func testReviewBackgroundAndReset() {
        tap("try-demo"); tap("run-demo-task")
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        XCUIDevice.shared.press(.home); app.activate()
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        tap("Hello, world!"); tap("answer")
        XCUIDevice.shared.press(.home); app.activate()
        XCTAssertTrue(contains("The sample page now says “Hello, world!”").waitForExistence(timeout: 15))
        tap("exit-demo"); tap("try-demo"); reach("session-Choose a greeting")
        XCTAssertEqual(app.buttons.matching(identifier: "session-Choose a greeting").count, 1)
        tap("exit-demo")
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 15))
    }

    /// Host-side audit seeds real preferences, then compares the app container
    /// before/after this UI-only flow. No production test hooks are used.
    func testReviewPersistentSettingsIsolation() {
        tap("try-demo")
        reach("work-period"); tap("30 Days")
        place("All Sessions")
        tap("view-options"); menuItem("Group"); tap("By Machine")
        tap("view-options"); menuItem("Show"); tap("Metadata")
        tap("toggle-archived")
        app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'group-'")).firstMatch.tap()
        backToOverview(); tap("menu"); tap("search")
        XCTAssertFalse(contains("real-search-sentinel").exists)
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
        field.typeText("greeting\n")
        tap("session-Choose a greeting")
        place("Machines"); tap("machine-Mac mini"); tap("agent-claude")
        reach("add-project"); tap("add-project"); tap("browse-folders")
        tap("folder-menu"); tap("Show Hidden Folders")
        back(); tap("close")
        place("Overview"); tap("menu"); tap("account")
        tap("appearance-dark"); tap("close")
        tap("exit-demo")
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 10))
        app.terminate()
    }

    private func cancelSearch() {
        let field = app.searchFields.firstMatch
        let frame = field.frame
        let cancel = app.buttons.allElementsBoundByIndex.first { $0.frame.minX >= frame.maxX - 2 && abs($0.frame.midY - frame.midY) < 12 }
        XCTAssertNotNil(cancel); cancel?.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5))
    }

    private func menuItem(_ prefix: String) {
        let item = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", prefix + ",")).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5)); item.tap()
    }

    private func checkpoint(_ name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name; shot.lifetime = .keepAlways; add(shot)
    }

    private func contains(_ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func replace(_ field: XCUIElement, with value: String) {
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        let old = field.value as? String ?? ""
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count) + value)
    }

    private func back() { app.navigationBars.buttons.element(boundBy: 0).tap() }

    /// A place by its old name: Work, the pages the menu's headings open, and All Sessions, which Work leads to.
    private func place(_ title: String) {
        backToOverview()
        // A tap while the page under it is still coming back (after Back or a delete) can be lost: again, once.
        tap("menu")
        if !app.descendants(matching: .any)["drawer"].firstMatch.waitForExistence(timeout: 3) { tap("menu") }
        switch title {
        case "Overview": tap("work-place")
        case "Grants": tap("menu-grants")
        case "Machines": tap("menu-machines")
        case "All Sessions": tap("work-place"); reach("all-sessions"); tap("all-sessions")
        default: tap(title)
        }
    }

    private func reach(_ identifier: String) {
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        // Down the page first, then back up: Work can open scrolled past what is near its top.
        for _ in 0..<6 {
            if item.exists && item.isHittable { return }
            app.swipeUp()
        }
        for _ in 0..<10 {
            if item.exists && item.isHittable { return }
            app.swipeDown()
        }
        XCTAssertTrue(item.exists && item.isHittable, identifier)
    }

    private func backToOverview() {
        while !app.buttons["menu"].exists {
            let back = app.navigationBars.buttons.element(boundBy: 0)
            XCTAssertTrue(back.exists)
            back.tap()
        }
    }

    private func tap(_ identifier: String) {
        let all = app.descendants(matching: .any).matching(identifier: identifier)
        XCTAssertTrue(all.firstMatch.waitForExistence(timeout: 20), identifier)
        // The same identifier can be on Work under a sheet or the menu (a session's card and its search result):
        // the one in front.
        let deadline = Date().addingTimeInterval(5)
        var front = all.allElementsBoundByIndex.first { $0.isHittable }
        while front == nil && Date() < deadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
            front = all.allElementsBoundByIndex.first { $0.isHittable }
        }
        (front ?? all.firstMatch).tap()
    }
}
