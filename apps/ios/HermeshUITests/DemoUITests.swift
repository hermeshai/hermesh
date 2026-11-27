import XCTest

/// Exercises the public entry with no account, pairing server or test signer.
final class DemoUITests: XCTestCase {
    let app = XCUIApplication()

    override func setUp() {
        continueAfterFailure = false
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
    }

    func testPrivacyPolicy() {
        let safari = XCUIApplication(bundleIdentifier: "com.apple.mobilesafari")

        func openPolicy(_ id: String) {
            let link = app.descendants(matching: .any)[id].firstMatch
            XCTAssertTrue(link.waitForExistence(timeout: 10))
            XCTAssertTrue(link.isHittable)
            XCTAssertEqual(link.label, "Privacy Policy")
            link.tap()
            XCTAssertTrue(safari.wait(for: .runningForeground, timeout: 15))
            // Quit, so each link opens Safari as the first does: one left in the
            // background did not come forward for the second on CI.
            safari.terminate()
            app.activate()
        }

        openPolicy("welcome-privacy-policy")
        enterWorkDemo()
        tap("menu"); tap("account")
        reachInSheet("account-privacy-policy")
        openPolicy("account-privacy-policy")
        XCTAssertFalse(app.buttons["delete-hosted-account"].exists)
    }

    func testTaskAndExit() {
        enterWorkDemo()
        XCTAssertTrue(app.staticTexts["Demo Mode"].waitForExistence(timeout: 15))
        tap("run-demo-task")
        XCTAssertTrue(app.staticTexts["Which greeting should the sample app show?"].waitForExistence(timeout: 10))
        tap("Welcome aboard!")
        tap("answer")
        let result = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "The sample page now says “Welcome aboard!”")).firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: 15))
        XCTAssertFalse(app.buttons["answer"].exists)
        // ChatGPT sent the task in, not the owner: its prompt has ChatGPT's face on the corner, and no name over it.
        let sender = app.descendants(matching: .any)["prompt-sender"].firstMatch
        XCTAssertTrue(sender.waitForExistence(timeout: 10), "who sent it")
        XCTAssertEqual(sender.label, "Sent by ChatGPT")
        XCTAssertFalse(app.staticTexts["Sent by ChatGPT"].exists, "a face, not a line of text")
        tap("session-info")
        XCTAssertTrue(app.buttons["close"].waitForExistence(timeout: 10), "Task Info sheet")
        let file = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "src/welcome.ts")).firstMatch
        for _ in 0..<5 where !file.exists || !file.isHittable || file.frame.maxY > app.frame.height - 120 { app.swipeUp() }
        XCTAssertTrue(file.exists, "src/welcome.ts")
        file.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "+export const greeting = \"Welcome aboard!\"")).firstMatch.waitForExistence(timeout: 10))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tap("close")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-completed-task"; shot.lifetime = .keepAlways; add(shot)
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
        // Reopening starts a new disposable world, and never initializes the real owner.
        enterWorkDemo()
        reach("session-Choose a greeting")
        XCTAssertEqual(app.buttons.matching(identifier: "session-Choose a greeting").count, 1)
        tap("exit-demo")
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["try-demo"].exists)
    }

    func testMultiQuestionStepAndRevise() {
        app.terminate()
        app.launchArguments.append("-HermeshDemoMultiQuestion")
        app.launch()
        enterWorkDemo()
        tap("run-demo-task")

        XCTAssertTrue(app.staticTexts["1/3"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Which greeting should the sample app show?"].exists)
        XCTAssertFalse(app.staticTexts["Which layout style should it use?"].exists, "only one question is presented at a time")
        XCTAssertFalse(app.buttons["answer"].isEnabled)

        // Pick Q1 -> auto-advances to 2/3.
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Welcome aboard!")).firstMatch.tap()
        XCTAssertTrue(app.staticTexts["2/3"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Which layout style should it use?"].exists)

        // Pick Q2 -> auto-advances to 3/3.
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Compact card")).firstMatch.tap()
        XCTAssertTrue(app.staticTexts["3/3"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Run tests after updating?"].exists)

        // Pick Q3 -> stays on 3/3 with Answer enabled so the owner can review/revise.
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Run unit tests")).firstMatch.tap()
        XCTAssertTrue(app.buttons["answer"].wait(for: \.isEnabled, toEqual: true, timeout: 5))

        // Step back to 2/3 and revise choice to Full banner, then submit Answer.
        tap("question-previous")
        XCTAssertTrue(app.staticTexts["2/3"].waitForExistence(timeout: 5))
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Full banner")).firstMatch.tap()
        tap("answer")

        let reply = contains("Welcome aboard! / Full banner / (Recommended) Run unit tests")
        XCTAssertTrue(reply.waitForExistence(timeout: 15))
    }

    func testApprovalsAndMachineControls() {
        enterWorkDemo()
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
        place("Machines")
        reachInSheet("add-environment"); tap("add-environment")
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 10))
        tap("open-machine")
        // Pausing is a button; paused, a banner resumes it (docs/settings-environments.md, D12).
        reach("accept-sessions"); tap("accept-sessions")
        XCTAssertTrue(app.buttons["env-resume"].waitForExistence(timeout: 10))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-machine"; shot.lifetime = .keepAlways; add(shot)
        reach("env-resume"); tap("env-resume")
        XCTAssertTrue(app.buttons["env-resume"].waitForNonExistence(timeout: 10))
        reach("env-providers"); tap("env-providers")
        tap("agent-codex")
        tap("test-agent")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Hello from the demo agent! This response is simulated.")).firstMatch.waitForExistence(timeout: 10))
        back(); back()
        reach("env-projects"); tap("env-projects")
        tap("add-project")
        tap("browse-folders")
        tap("folder-Projects")
        tap("folder-playground")
        tap("choose-folder")
        tap("add-project-confirm")
        XCTAssertTrue(app.buttons["project-playground"].waitForExistence(timeout: 10))
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
    }

    /// Haptics (Apple's HIG, Playing haptics), as the log the app keeps under -HermeshHapticLog has them: a question
    /// already there when its page opens plays none; the answer taken plays success, so does the turn it lets finish
    /// while the page shows, and so does a pairing approved.
    func testHaptics() {
        app.terminate()
        app.launchArguments += ["-HermeshHapticLog"]
        app.launch()
        enterWorkDemo()
        tap("run-demo-task")
        XCTAssertTrue(app.staticTexts["Which greeting should the sample app show?"].waitForExistence(timeout: 10))
        sleep(2)
        played("")
        tap("Welcome aboard!")
        tap("answer")
        // The answer at once; the turn it lets finish, when the demo's task does (8 seconds on).
        played("success success", within: 15)
        backToOverview()
        tap("verify-pairing")
        tap("scope-projects")
        let all = app.switches["All projects (including ones added later)"]
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.switches.firstMatch.exists ? all.switches.firstMatch.tap() : all.tap()
        back()
        tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["client-connected"].waitForExistence(timeout: 10))
        played("success success success")
        tap("exit-demo")
    }

    /// The haptics played so far, by name, in order.
    private func played(_ names: String, within seconds: TimeInterval = 5, file: StaticString = #filePath, line: UInt = #line) {
        let log = app.descendants(matching: .any).matching(identifier: "haptics").firstMatch
        let heard = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", names), object: log)
        if XCTWaiter().wait(for: [heard], timeout: seconds) != .completed {
            XCTFail("haptics: \(String(describing: log.value)), not \(names)", file: file, line: line)
        }
    }

    func testSearch() {
        enterWorkDemo()
        tap("menu"); tap("search")
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
        // Live results update on every keystroke without .searchSuggestions occlusion.
        field.typeText("greeting")
        tap("session-Choose a greeting")
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        tap("exit-demo")
        enterWorkDemo()
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

    /// Management pages belong to Account and Devices, including their details and nested sheets.
    func testAccountNavigation() {
        enterWorkDemo()
        tap("menu"); tap("account")
        // Settings (docs/settings-environments.md §4.2): the app's own settings, then the environments.
        let appearance = app.buttons["account-appearance"]
        XCTAssertTrue(appearance.waitForExistence(timeout: 5))
        reach("account-machines")
        XCTAssertLessThan(appearance.frame.minY, app.staticTexts["account-machines"].frame.minY)
        tap("machine-Mac mini"); back()
        XCTAssertTrue(app.navigationBars["Settings"].waitForExistence(timeout: 5))
        tap("machine-Mac mini")
        swipeMenuOut()
        XCTAssertTrue(app.navigationBars["Settings"].waitForExistence(timeout: 5), "swipe returns to settings")
        reach("account-grants"); tap("account-grants")
        tap("grant-ChatGPT")
        reach("change-scope"); tap("change-scope")
        tap("close")
        XCTAssertTrue(app.buttons["change-scope"].waitForExistence(timeout: 5), "nested sheet returns to the grant")
        back(); swipeMenuOut()
        XCTAssertTrue(app.navigationBars["Settings"].waitForExistence(timeout: 5))
        reach("account-about")
        checkpoint("account-order-and-navigation")
        tap("close"); tap("exit-demo")
    }

    func testAccountAppearance() {
        enterWorkDemo()
        tap("menu")
        tap("account")
        tap("account-appearance")
        reach("appearance-dark"); tap("appearance-dark")
        XCTAssertTrue(app.buttons["appearance-dark"].isSelected)
        back(); tap("close")
        checkpoint("demo-dark-workspace")
        tap("account")
        tap("account-appearance")
        reach("appearance-dark")
        XCTAssertTrue(app.buttons["appearance-dark"].isSelected)
        back(); tap("close")
        tap("exit-demo")
        enterWorkDemo()
        tap("menu"); tap("account")
        tap("account-appearance")
        reach("appearance-system")
        XCTAssertTrue(app.buttons["appearance-system"].isSelected)
        back(); tap("close")
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
        let privacy = app.descendants(matching: .any)["welcome-privacy-policy"].firstMatch
        XCTAssertTrue(privacy.isHittable)
        XCTAssertEqual(privacy.label, "隐私政策")
        XCTAssertTrue(demo.isHittable && join.isHittable)
        XCTAssertLessThan(start.frame.maxY, join.frame.midY)
        XCTAssertLessThan(join.frame.maxY, demo.frame.midY)
        let welcome = XCTAttachment(screenshot: app.screenshot())
        welcome.name = "welcome-three-choices-zh"; welcome.lifetime = .keepAlways; add(welcome)
        enterWorkDemo()
        XCTAssertTrue(app.staticTexts["演示模式"].waitForExistence(timeout: 15))
        // The overview in Chinese: what needs the owner, the link map, what finished.
        XCTAssertTrue(app.staticTexts["需要你处理"].waitForExistence(timeout: 10) && app.staticTexts["端到端加密"].exists && app.staticTexts["最近完成"].exists)
        let overview = XCTAttachment(screenshot: app.screenshot())
        overview.name = "overview-zh"; overview.lifetime = .keepAlways; add(overview)
        tap("run-demo-task")
        XCTAssertTrue(app.staticTexts["示例应用应该显示哪句问候语？"].waitForExistence(timeout: 10))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "demo-question-zh"; shot.lifetime = .keepAlways; add(shot)
        tap("exit-demo")
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 10))
    }

    func testReviewGrantLifecycle() {
        enterWorkDemo()
        place("Grants"); tap("grant-ChatGPT")
        tap("rename-agent")
        replace(app.alerts.textFields.firstMatch, with: "Review Agent")
        app.alerts.buttons["Rename"].tap()
        XCTAssertTrue(app.navigationBars["Review Agent"].waitForExistence(timeout: 5))
        // Its sessions, counted (the menu lists them, grouped as the owner likes).
        XCTAssertTrue(app.staticTexts["Tasks"].waitForExistence(timeout: 10))
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
        enterWorkDemo(); reach("session-Add a welcome page"); tap("session-Add a welcome page")
        tap("session-more")
        XCTAssertFalse(app.buttons["Regenerate Title"].exists)
        tap("Copy Task ID")
        XCTAssertTrue(contains("Copied the task ID").waitForExistence(timeout: 5))
        tap("session-more"); tap("Pin")
        place("All Sessions")
        XCTAssertTrue(app.buttons["group-Pinned"].waitForExistence(timeout: 5))
        tap("session-Add a welcome page")
        tap("session-more"); tap("Unpin")
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
        XCTAssertFalse(app.buttons["group-Pinned"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["session-Review Session"].exists)
        for group in ["By Date", "By Agent", "By Status", "By Machine"] {
            tap("view-options"); menuItem("Group"); tap(group)
        }
        for sortBy in ["Created", "Last Active", "Default (In Progress by Created)"] {
            tap("view-options"); menuItem("Sort"); tap(sortBy)
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
        let holdTask = "-HermeshHoldDemoWorkTask"
        app.terminate()
        app.launchArguments.append(holdTask)
        app.launch()

        enterWorkDemo(); tap("run-demo-task")
        answerWithText("Reviewer says hello")
        tap("interrupt"); tap("interrupt-confirm")
        XCTAssertTrue(contains("The simulated task was stopped.").waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["interrupt"].exists)
        XCTAssertFalse(contains("The sample page now says").exists)
        tap("exit-demo")
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 15))

        app.terminate()
        app.launchArguments.removeAll { $0 == holdTask }
        app.launch()
        enterWorkDemo(); tap("run-demo-task")
        answerWithText("Reviewer says hello")
        XCTAssertTrue(contains("The sample page now says “Reviewer says hello”").waitForExistence(timeout: 15))
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 15))
    }

    func testReviewMachineLifecycle() {
        enterWorkDemo(); place("Machines"); tap("machine-Mac mini")
        tap("env-name")
        replace(app.textFields["env-name-field"], with: "review-mac")
        tap("Save")
        XCTAssertTrue(app.staticTexts["review-mac"].waitForExistence(timeout: 5))
        tap("env-providers")
        tap("check-again")
        tap("agent-codex")
        reach("default-model"); tap("default-model"); tap("Demo Model")
        tap("default-effort"); tap("High")
        tap("default-speed"); tap("Fast")
        back(); back()
        // Projects are the environment's, for every provider (docs/settings-environments.md §5.6).
        tap("env-projects")
        tap("project-hello-world")
        tap("workspace-in-place")
        back()
        XCTAssertTrue(contains("In place").waitForExistence(timeout: 5))
        checkpoint("machine-project-settings")
        tap("project-hello-world"); tap("project-remove"); tap("Remove")
        XCTAssertTrue(app.buttons["project-hello-world"].waitForNonExistence(timeout: 5))
        tap("add-project"); tap("repo-~/Projects/hello-world"); tap("add-project-confirm")
        XCTAssertTrue(app.buttons["project-hello-world"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["project-hello-world-2"].exists)
        back()
        // Storage: the tasks' worktrees, the idle ones released.
        reach("env-storage"); tap("env-storage")
        XCTAssertTrue(app.descendants(matching: .any)["storage-summary"].firstMatch.waitForExistence(timeout: 10))
        checkpoint("machine-storage")
        back()
        // Her runs there: removing asks about her data too.
        reach("env-remove"); tap("env-remove"); tap("Remove, Keep Her’s Data")
        XCTAssertTrue(app.buttons["machine-review-mac"].waitForNonExistence(timeout: 5))
        place("All Sessions"); tap("session-Choose a greeting")
        XCTAssertTrue(contains("The simulated task was stopped.").waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["answer"].exists)
        tap("exit-demo")
    }

    func testReviewAllNewEntries() {
        enterWorkDemo()
        new("Connect an Agent…"); tap("Deny")
        new("Add Environment…"); tap("close")
        new("Scan a Code…"); tap("approve")
        XCTAssertTrue(app.descendants(matching: .any)["machine-joined"].waitForExistence(timeout: 10))
        tap("close")
        new("Enter a Pairing Code…")
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

    /// The overview is only now (docs/overview-usage.md §4): what needs the owner as a deck, the link map,
    /// what runs and what finished, with every session one tap away; the counts are on a page of their own.
    func testOverviewNow() {
        enterWorkDemo()
        let any = app.descendants(matching: .any)
        let map = any["link-map"].firstMatch
        XCTAssertTrue(map.waitForExistence(timeout: 15), "the link map")
        XCTAssertFalse(any["filter-range"].exists, "no counts on the overview")
        checkpoint("overview-now")

        // The deck: one card in front; its edge, or a swipe, brings the next.
        let front = any["needs-place"].firstMatch
        XCTAssertTrue(front.waitForExistence(timeout: 5), "the deck says which card is in front")
        XCTAssertTrue(front.label.hasPrefix("1 / "), front.label)
        tap("needs-next")
        XCTAssertTrue(wait { front.label.hasPrefix("2 / ") }, front.label)
        checkpoint("overview-deck-second")
        any["needs"].firstMatch.swipeRight()
        XCTAssertTrue(wait { front.label.hasPrefix("1 / ") }, front.label)

        // A limit nearly used up puts a dot on the Limits tab (no heavy card in the deck); tapping Limits clears it.
        XCTAssertFalse(any["limit-claude"].firstMatch.exists, "no heavy limit card in the deck")
        XCTAssertTrue(any["limits-low-dot"].firstMatch.waitForExistence(timeout: 5), "the orange dot on the Limits tab")
        checkpoint("overview-limit-dot")
        view(2)
        XCTAssertTrue(any["limit-claude-300"].firstMatch.waitForExistence(timeout: 5), "Limits: the overview's view")
        XCTAssertFalse(any["limits-low-dot"].firstMatch.exists, "dot disappears once Limits is tapped")
        XCTAssertFalse(map.exists, "in place of Live")
        view(0)
        XCTAssertTrue(map.waitForExistence(timeout: 10))
        XCTAssertFalse(any["limits-low-dot"].firstMatch.exists, "dot stays gone back on Live")

        // A node picked: every session through it, in place of what runs and what finished.
        tap("map-Mac mini")
        XCTAssertTrue(any["through"].firstMatch.waitForExistence(timeout: 5), "the sessions through the node")
        checkpoint("overview-picked")
        tap("show-all")
        XCTAssertFalse(any["show-all"].firstMatch.waitForExistence(timeout: 1))

        // Answered on the card: it goes, the next comes up, and the session runs as a tile.
        let before = Int(front.label.split(separator: " ").last ?? "") ?? 0
        tap("quick-answer-Hello, world!")
        XCTAssertTrue(wait { !front.exists || (Int(front.label.split(separator: " ").last ?? "") ?? 0) == before - 1 }, "the answered card left the deck")
        checkpoint("overview-answered")

        // Every session, one tap from what finished.
        reach("see-all"); tap("see-all")
        XCTAssertTrue(any["tasks"].firstMatch.waitForExistence(timeout: 5), "every session")
        checkpoint("overview-tasks")
        back()

        // The numbers are the overview's other views, picked at its top; the menu has no place of its own for them.
        XCTAssertFalse(any["statistics-place"].exists)
        view(1)
        reach("filter-range")
        view(0)
        XCTAssertTrue(map.waitForExistence(timeout: 10))
    }

    /// Where the screen has side safe-area insets (iPhone Duo's side bar, or an iPhone turned on its side),
    /// the deck clips at the column's edge and pages by the card's width, never bleeding the next card into the margin.
    func testOverviewDeckSafeAreaClipping() {
        app.terminate()
        app.launchArguments += ["-HermeshTestTurns", "YES"]
        XCUIDevice.shared.orientation = .portrait
        app.launch()
        addTeardownBlock { XCUIDevice.shared.orientation = .portrait }

        enterWorkDemo()
        let any = app.descendants(matching: .any)
        let views = any["overview-views"].firstMatch
        let deck = any["needs"].firstMatch
        let front = any["needs-place"].firstMatch
        let answer = any["quick-answer-Hello, world!"].firstMatch
        let verify = any["verify-pairing"].firstMatch
        XCTAssertTrue(views.waitForExistence(timeout: 15) && front.waitForExistence(timeout: 5))

        for orientation in [UIDeviceOrientation.portrait, .landscapeLeft] {
            XCUIDevice.shared.orientation = orientation
            sleep(2)
            XCTAssertTrue(front.label.hasPrefix("1 / "), front.label)
            let column = views.frame
            // The deck's scroller stays inside the safe-area column rather than bleeding into side bars or margins.
            XCTAssertEqual(deck.frame.minX, column.minX, accuracy: 2, "deck leading edge matches column (\(orientation.rawValue))")
            XCTAssertEqual(deck.frame.maxX, column.maxX, accuracy: 2, "deck trailing edge matches column (\(orientation.rawValue))")
            XCTAssertGreaterThanOrEqual(answer.frame.minX, column.minX - 1)
            XCTAssertLessThanOrEqual(answer.frame.maxX, column.maxX + 1)
            XCTAssertGreaterThan(verify.frame.minX, column.maxX, "next card clipped outside column (\(orientation.rawValue))")

            if !answer.isHittable || answer.frame.maxY > app.frame.height - 65 {
                let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
                let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
                from.press(forDuration: 0.05, thenDragTo: to)
            }
            checkpoint("overview-deck-1-\(orientation.rawValue)")
            deck.swipeLeft()
            XCTAssertTrue(wait { front.label.hasPrefix("2 / ") }, front.label)
            XCTAssertTrue(verify.waitForExistence(timeout: 5) && verify.isHittable)
            XCTAssertGreaterThanOrEqual(verify.frame.minX, column.minX - 1, "second card aligned with column leading edge (\(orientation.rawValue))")
            XCTAssertLessThanOrEqual(verify.frame.maxX, column.maxX + 1, "second card aligned with column trailing edge (\(orientation.rawValue))")
            checkpoint("overview-deck-2-\(orientation.rawValue)")

            deck.swipeRight()
            XCTAssertTrue(wait { front.label.hasPrefix("1 / ") }, front.label)
            if orientation == .portrait {
                // Keep the scroll at the top before turning sideways.
                app.swipeDown()
            }
        }
    }

    /// Multiple concurrent sessions between the same nodes merge into one weighted line, hosted MCP labels show
    /// their short app names, and picking a node filters the overview to its sessions.
    func testLinkMapMergedLinesAndShortNames() {
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-DemoLinkMap"]
        app.launch()
        enterWorkDemo()
        let any = app.descendants(matching: .any)
        let map = any["link-map"].firstMatch
        XCTAssertTrue(map.waitForExistence(timeout: 15), "the link map")

        // Hosted MCP domains are stripped to clean short names; owner and environments all appear.
        for node in ["map-You", "map-Claude", "map-Her", "map-Muse", "map-yee-homebook", "map-yi-mac-mini"] {
            XCTAssertTrue(any[node].firstMatch.waitForExistence(timeout: 5), node)
        }
        XCTAssertFalse(any["map-Claude (claude.ai)"].exists)
        XCTAssertFalse(any["map-Muse (agent.meta.ai)"].exists)

        // Scroll past the demo banner so the link map sits at the top above the 2-column In Progress tiles.
        let bar = app.navigationBars.firstMatch
        if bar.exists {
            let targetY = bar.frame.maxY + 14
            let dy = map.frame.minY - targetY
            if dy > 10 {
                let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
                let end = start.withOffset(CGVector(dx: 0, dy: -dy))
                start.press(forDuration: 0.08, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.3)
                RunLoop.current.run(until: Date().addingTimeInterval(0.6))
            }
        }
        checkpoint("linkmap-multi-overview")

        // Picking "You" dims unrelated nodes and lists the owner's five sessions underneath.
        tap("map-You")
        XCTAssertTrue(any["through"].firstMatch.waitForExistence(timeout: 5))
        checkpoint("linkmap-picked-owner")

        // Tapping the central Hermesh hub clears the selection.
        tap("map-hub")
        XCTAssertFalse(any["through"].firstMatch.waitForExistence(timeout: 1))
    }

    private func wait(_ timeout: TimeInterval = 5, _ done: () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while !done() && Date() < end { RunLoop.current.run(until: Date().addingTimeInterval(0.2)) }
        return done()
    }

    func testReviewOverviewAndSettings() {
        enterWorkDemo()
        // The link map draws what there is: adding an agent or an environment is rare, and has its place elsewhere.
        XCTAssertTrue(app.descendants(matching: .any)["overview-views"].waitForExistence(timeout: 10))
        for label in ["Connect an Agent", "Add an Environment"] {
            XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", label)).firstMatch.exists, "no \(label) on the map")
        }
        view(1)
        reach("filter-range")
        for range in ["30 Days", "Today", "7 Days"] { pick("filter-range", range) }
        // The bars are in parts of the agents to start with, each named over them with its number and share; a part
        // tapped there is seen alone, and the total has none.
        let group = app.segmentedControls["usage-group"]
        XCTAssertTrue(group.waitForExistence(timeout: 5) && group.buttons["Agents"].isSelected, "grouped by agent to start with")
        let codex = app.buttons["usage-sessions-part-codex"]
        XCTAssertTrue(codex.waitForExistence(timeout: 5) && codex.label.hasPrefix("Codex, ") && codex.label.hasSuffix("%"), codex.label)
        // The legend settles into its columns as the answer comes: tapped once it is still.
        XCTAssertTrue(wait(10) { codex.isHittable }, "Codex's cell can be tapped")
        codex.tap()
        XCTAssertTrue(wait { codex.isSelected }, "Codex alone in the charts")
        XCTAssertTrue(wait(10) { codex.isHittable }, "Codex's cell, still again")
        codex.tap()
        XCTAssertTrue(wait { !codex.isSelected })
        // A bar tapped says what it stands for: today's, over the axis's Today.
        let today = app.staticTexts.matching(NSPredicate(format: "label == %@", "Today")).firstMatch
        XCTAssertTrue(today.waitForExistence(timeout: 5))
        today.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -1.5)).tap()
        let callout = app.descendants(matching: .any)["usage-sessions-callout"].firstMatch
        XCTAssertTrue(callout.waitForExistence(timeout: 5) && callout.label.contains("task"), "today's bar says its tasks")
        checkpoint("overview-usage-day")
        today.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -1.5)).tap()
        XCTAssertTrue(callout.waitForNonExistence(timeout: 5))
        group.buttons["None"].tap()
        XCTAssertTrue(codex.waitForNonExistence(timeout: 5), "the total is not in parts")
        group.buttons["Agents"].tap()
        // The filters are behind one button; one in force is said under the row, and let go there.
        filter("filter-agent", "Codex")
        let inForce = app.buttons["filter-in-force-agent"]
        XCTAssertTrue(inForce.waitForExistence(timeout: 5) && inForce.label.contains("Codex"), "Codex's work alone")
        XCTAssertTrue(app.descendants(matching: .any)["usage-sessions"].firstMatch.waitForExistence(timeout: 5))
        checkpoint("overview-usage")
        inForce.tap()
        XCTAssertTrue(inForce.waitForNonExistence(timeout: 5), "every agent's again")
        view(2)
        // The least left first: Claude Code's five hours, 18% left.
        let five = app.descendants(matching: .any)["limit-claude-300"].firstMatch
        XCTAssertTrue(five.waitForExistence(timeout: 5))
        XCTAssertTrue(five.label.contains("18%"), five.label)
        XCTAssertTrue(app.descendants(matching: .any)["limits-codex"].firstMatch.exists)
        checkpoint("overview-limits")
        view(0)
        // The environments, in settings; an environment's page from them.
        place("Machines"); tap("machine-Mac mini"); back()
        reachInSheet("account-appearance"); tap("account-appearance")
        for theme in ["light", "dark", "system"] {
            tap("appearance-\(theme)"); XCTAssertTrue(app.buttons["appearance-\(theme)"].isSelected)
        }
        back()
        XCTAssertFalse(app.buttons["erase-everything"].exists)
        XCTAssertFalse(app.buttons["notifications"].exists)
        tap("close"); tap("exit-demo")
    }

    /// Her and her team (docs/her-team.md): the overview lists what needs the owner and answers a question where
    /// it is listed; the menu is Her, the overview and every session (§16), a session it opens a place of the menu's;
    /// Her's entry opens the public sample conversation without configuring a host.
    func testReviewHerAndHerTeam() {
        enterWorkDemo()
        // The question's card says who sent the work to which agent; one tap answers it.
        reach("session-Choose a greeting")
        let card = app.descendants(matching: .any).matching(identifier: "session-Choose a greeting").firstMatch
        XCTAssertTrue(card.label.contains("ChatGPT → Codex"), card.label)
        checkpoint("work-needs-you")
        tap("quick-answer-Hello, world!")
        XCTAssertTrue(app.buttons["quick-answer-Hello, world!"].waitForNonExistence(timeout: 15), "answered: no answers to tap")
        // The menu: Her, the overview, then the sessions by status, titles alone; no map of the team.
        tap("menu")
        let drawer = app.descendants(matching: .any).matching(identifier: "drawer").firstMatch
        for id in ["her-place", "overview-place", "view-options", "session-Add a welcome page"] {
            XCTAssertTrue(drawer.descendants(matching: .any)[id].firstMatch.waitForExistence(timeout: 10), id)
        }
        XCTAssertFalse(drawer.buttons["her-place"].label.contains("not on"), drawer.buttons["her-place"].label)
        for id in ["menu-machines", "menu-grants", "menu-machine-Mac mini", "menu-agent-ChatGPT"] {
            XCTAssertFalse(drawer.buttons[id].exists, "\(id): no map of the team")
        }
        XCTAssertTrue(drawer.descendants(matching: .any)["group-Done"].firstMatch.exists || drawer.descendants(matching: .any)["group-In Progress"].firstMatch.exists, "by status")
        let row = drawer.buttons["session-Add a welcome page"]
        XCTAssertFalse(row.label.contains("ChatGPT"), "titles alone: \(row.label)")
        checkpoint("menu-sessions")
        // A session the menu opened is a place of the menu's: the menu's button, not Back; a swipe right brings the menu out.
        row.tap()
        XCTAssertTrue(app.buttons["session-info"].waitForExistence(timeout: 10), "the session")
        XCTAssertTrue(app.buttons["menu"].waitForExistence(timeout: 10), "the menu's button, not Back")
        swipeMenuOut()
        XCTAssertTrue(drawer.buttons["overview-place"].isHittable, "the menu")
        XCTAssertTrue(drawer.buttons["session-Add a welcome page"].isSelected, "its row the current one")
        tap("overview-place")
        XCTAssertTrue(app.navigationBars["Overview"].waitForExistence(timeout: 10))
        // Her: the sample conversation, with no model or real machine setup.
        tap("menu"); tap("her-place")
        XCTAssertTrue(app.descendants(matching: .any)["her-composer"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Delegate a sample task"].exists)
        XCTAssertFalse(app.buttons["her-not-now"].exists)
        checkpoint("her-public-demo")
        tap("exit-demo")
    }

    func testReviewToolOutputsAndClipboard() {
        enterWorkDemo(); reach("session-Add a welcome page"); tap("session-Add a welcome page")
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
        tap("session-more")
        XCTAssertFalse(app.buttons["Regenerate Title"].exists)
        tap("Copy Task ID")
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
        enterWorkDemo()
        let results = app.descendants(matching: .any)["session-search"].firstMatch
        for state in ["failed", "waiting", "done"] {
            tap("menu"); tap("search")
            XCTAssertTrue(results.waitForExistence(timeout: 10))
            tap("search-filter-status")
            tap("search-state-\(state)")
            if state == "failed" {
                XCTAssertTrue(results.descendants(matching: .any)["no-results"].waitForExistence(timeout: 5))
            } else {
                let title = state == "waiting" ? "Choose a greeting" : "Add a welcome page"
                XCTAssertTrue(results.descendants(matching: .any)["session-\(title)"].waitForExistence(timeout: 5))
            }
            cancelSearch()
            tap("drawer-dim")
        }

        // Branch match + long-press preview + archived task discovery in search.
        tap("menu"); tap("search")
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
        field.typeText("hermesh/welcome")
        let welcome = results.descendants(matching: .any)["session-Add a welcome page"].firstMatch
        XCTAssertTrue(welcome.waitForExistence(timeout: 5))
        let searchShot = XCTAttachment(screenshot: app.screenshot())
        searchShot.name = "iphone-search-live"; searchShot.lifetime = .keepAlways; add(searchShot)
        welcome.press(forDuration: 1.0)
        XCTAssertTrue(app.buttons["session-archive"].waitForExistence(timeout: 10))
        let previewShot = XCTAttachment(screenshot: app.screenshot())
        previewShot.name = "iphone-search-preview"; previewShot.lifetime = .keepAlways; add(previewShot)
        tap("session-archive")
        // Once archived, default search hides it and offers 1-tap reveal of archived matches.
        XCTAssertTrue(welcome.waitForNonExistence(timeout: 5))
        tap("show-archived-matches")
        XCTAssertTrue(welcome.waitForExistence(timeout: 5))
        cancelSearch()
        tap("drawer-dim")
        tap("exit-demo")
    }

    func testReviewFolderChoices() {
        enterWorkDemo(); place("Machines"); tap("machine-Mac mini"); tap("env-projects")
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
        XCTAssertTrue(app.buttons["project-reviewer"].waitForExistence(timeout: 5))
        tap("project-reviewer"); tap("project-remove"); tap("Remove")
        XCTAssertTrue(app.buttons["project-reviewer"].waitForNonExistence(timeout: 5))
        tap("exit-demo")
    }

    func testReviewBackgroundAndReset() {
        enterWorkDemo(); tap("run-demo-task")
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        XCUIDevice.shared.press(.home); app.activate()
        XCTAssertTrue(app.buttons["answer"].waitForExistence(timeout: 10))
        tap("Hello, world!"); tap("answer")
        XCUIDevice.shared.press(.home); app.activate()
        XCTAssertTrue(contains("The sample page now says “Hello, world!”").waitForExistence(timeout: 15))
        tap("exit-demo"); enterWorkDemo(); reach("session-Choose a greeting")
        XCTAssertEqual(app.buttons.matching(identifier: "session-Choose a greeting").count, 1)
        tap("exit-demo")
        app.terminate(); app.launch()
        XCTAssertTrue(app.buttons["start-new"].waitForExistence(timeout: 15))
    }

    /// The Usage page at a very busy owner's numbers (billions of tokens, hundreds of hours, many agents, long project
    /// names): every legend is uniformly one vertical column, normal names stay one line, and long names wrap cleanly
    /// inside the card with their numbers aligned.
    func testUsageAtLargeNumbers() {
        app.terminate()
        app.launchArguments += ["-DemoBigUsage", "-DemoSessions", "300"]
        app.launch()
        enterWorkDemo()
        view(1)
        reach("filter-range"); pick("filter-range", "30 Days")
        let any = app.descendants(matching: .any)
        let big = any["usage-tokens-big"].firstMatch
        XCTAssertTrue(big.waitForExistence(timeout: 10))
        XCTAssertTrue(wait { big.label.contains("M") }, big.label)
        // Both cards use one vertical column; normal agent names stay on one line with full names.
        for prefix in ["usage-sessions-part-", "usage-tokens-part-"] {
            let cells = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", prefix)).allElementsBoundByIndex
            XCTAssertGreaterThanOrEqual(cells.count, 3, prefix)
            for c in cells { XCTAssertLessThanOrEqual(c.frame.height, 36, "\(c.identifier) is one line: \(c.label)") }
            let frames = cells.map(\.frame)
            XCTAssertEqual(Set(frames.map { Int($0.minX.rounded()) }).count, 1, "\(prefix) is one vertical column")
            XCTAssertEqual(Set(frames.map { Int($0.maxX.rounded()) }).count, 1, "\(prefix) cells end together")
        }
        let others = any["usage-tokens-others"].firstMatch
        XCTAssertTrue(others.exists && others.frame.height <= 36, "Other is one line")
        // The big numbers and the figures stay on their lines, within the card.
        let card = any["usage-tokens"].firstMatch.frame
        XCTAssertLessThanOrEqual(big.frame.maxX, card.maxX)
        XCTAssertLessThanOrEqual(big.frame.height, 64, "the number is one line")
        let busy = any.matching(NSPredicate(format: "label BEGINSWITH %@", "Time at work")).firstMatch
        XCTAssertTrue(busy.exists && busy.frame.height <= 48, "the time at work is one line under its name: \(busy.label), \(busy.frame.height)")
        checkpoint("usage-large-numbers")
        // Other's row lines up in the same single column with the parts above it.
        let frames = (app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "usage-sessions-part-")).allElementsBoundByIndex + [app.buttons["usage-sessions-others"]]).map(\.frame)
        XCTAssertEqual(Set(frames.map { Int($0.minX.rounded()) }).count, 1, "single vertical column")
        XCTAssertEqual(Set(frames.map { Int($0.maxX.rounded()) }).count, 1, "the cells end together")
        // Long names wrap cleanly in the single-column legend while keeping the trailing edge aligned,
        // and the bar callout stays within the card width.
        let group = app.segmentedControls["usage-group"]
        XCTAssertTrue(group.buttons["Machines"].waitForExistence(timeout: 5), "Machines segment")
        group.buttons["Machines"].tap()
        let longRow = app.buttons["usage-sessions-part-demo-linux"]
        XCTAssertTrue(longRow.waitForExistence(timeout: 10), "long machine row")
        let shortRow = app.buttons["usage-sessions-part-demo-mac"]
        XCTAssertTrue(shortRow.waitForExistence(timeout: 5), "short machine row")
        let sessionsCard = any["usage-sessions"].firstMatch.frame
        XCTAssertGreaterThan(longRow.frame.height, shortRow.frame.height, "long name wraps instead of squeezing into one line")
        XCTAssertEqual(Int(longRow.frame.minX.rounded()), Int(shortRow.frame.minX.rounded()), "aligned leading edge")
        XCTAssertEqual(Int(longRow.frame.maxX.rounded()), Int(shortRow.frame.maxX.rounded()), "aligned trailing edge")
        XCTAssertLessThanOrEqual(longRow.frame.maxX, sessionsCard.maxX, "stays within card")
        // Tapping today's bar shows the callout inside the card even with a long name.
        let today = app.staticTexts.matching(NSPredicate(format: "label == %@", "Today")).firstMatch
        XCTAssertTrue(today.waitForExistence(timeout: 5))
        today.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -1.5)).tap()
        let callout = any["usage-sessions-callout"].firstMatch
        XCTAssertTrue(callout.waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(callout.frame.minX, sessionsCard.minX - 1, "callout stays inside card leading edge")
        XCTAssertLessThanOrEqual(callout.frame.maxX, sessionsCard.maxX + 1, "callout stays inside card trailing edge")
        checkpoint("usage-large-numbers-long-names")
        // Back to Agents: Other opened lists every agent (five here: the busiest three and Other's two), each on one line.
        group.buttons["Agents"].tap()
        XCTAssertTrue(others.waitForExistence(timeout: 5))
        others.tap()
        XCTAssertTrue(wait { app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "usage-tokens-part-")).count == 5 })
        for c in app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "usage-tokens-part-")).allElementsBoundByIndex {
            XCTAssertLessThanOrEqual(c.frame.height, 36, "\(c.identifier) is one line: \(c.label)")
        }
        checkpoint("usage-large-numbers-all")
    }

    /// Host-side audit seeds real preferences, then compares the app container
    /// before/after this UI-only flow. No production test hooks are used.
    func testReviewPersistentSettingsIsolation() {
        enterWorkDemo()
        view(1)
        reach("filter-range"); pick("filter-range", "30 Days")
        place("All Sessions")
        tap("view-options"); menuItem("Group"); tap("By Machine")
        tap("view-options"); menuItem("Show"); tap("Metadata")
        tap("toggle-archived")
        app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'group-'")).firstMatch.tap()
        tap("search")
        XCTAssertFalse(contains("real-search-sentinel").exists)
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        if app.buttons["Continue"].exists { app.buttons["Continue"].tap() }
        field.typeText("greeting\n")
        tap("session-Choose a greeting")
        place("Machines"); tap("machine-Mac mini"); tap("env-projects")
        reach("add-project"); tap("add-project"); tap("browse-folders")
        tap("folder-menu"); tap("Show Hidden Folders")
        back(); tap("close")
        place("Overview"); tap("menu"); tap("account"); tap("account-appearance")
        reach("appearance-dark"); tap("appearance-dark"); back(); tap("close")
        tap("exit-demo")
        XCTAssertTrue(app.buttons["try-demo"].waitForExistence(timeout: 10))
        app.terminate()
    }

    func testNewSessionAskFadesWhenCrowded() {
        enterWorkDemo()
        tap("menu")
        tap("new-session-place")

        let title = app.descendants(matching: .any)["new-session-title"].firstMatch
        let machine = app.buttons["thread-machine"]
        let worktree = app.buttons["thread-worktree"]
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        tap("thread-project")
        tap("project-hello-world")
        XCTAssertTrue(worktree.waitForExistence(timeout: 5))
        XCTAssertTrue(machine.exists && composer.exists)
        XCTAssertLessThan(machine.frame.maxY, worktree.frame.minY, "ask sits cleanly above the working-copy bar")

        composer.tap()
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 10))
        composer.typeText("https://github.com/pingdotgg/t3code\nstudy how session titles are generated\nand summarize the approach")
        sleep(1)
        if app.frame.height <= 700 {
            XCTAssertTrue(title.waitForNonExistence(timeout: 5), "compact height: ask fades out when composer expands")
            XCTAssertFalse(machine.exists)
        } else if title.exists {
            XCTAssertLessThan(machine.frame.maxY, worktree.frame.minY, "never overlaps working-copy bar")
        }
        checkpoint("new-session-composer-default")

        // When vertical room cannot hold ask above the expanded multi-line composer and keyboard, ask fades out
        // cleanly and comes back once the keyboard is dismissed.
        app.terminate()
        app.launchArguments = [
            "-AppleLanguages", "(en)", "-AppleLocale", "en_US",
            "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityMedium",
        ]
        app.launch()
        enterWorkDemo()
        tap("menu")
        tap("new-session-place")
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        tap("thread-project")
        tap("project-hello-world")
        XCTAssertTrue(worktree.waitForExistence(timeout: 5))
        XCTAssertTrue(machine.exists)
        XCTAssertLessThan(machine.frame.maxY, worktree.frame.minY, "with keyboard down, ask fits above working-copy bar")

        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 10))
        composer.typeText("line 1\nline 2\nline 3\nline 4\nline 5")
        XCTAssertTrue(title.waitForNonExistence(timeout: 5), "crowded: ask fades out")
        XCTAssertFalse(machine.exists, "crowded: machine picker hides with ask")
        XCTAssertTrue(worktree.isHittable && composer.isHittable)
        checkpoint("new-session-composer-crowded-hidden")

        let topClearY = (app.navigationBars.firstMatch.frame.maxY + worktree.frame.minY) / 2
        if worktree.frame.minY - app.navigationBars.firstMatch.frame.maxY >= 24 {
            app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: app.frame.midX, dy: topClearY)).tap()
        } else {
            composer.swipeDown()
        }
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 10), "empty area above composer or flick dismisses keyboard")
        XCTAssertTrue(title.waitForExistence(timeout: 5), "keyboard dismissed: ask fades back in")
        XCTAssertTrue(machine.exists)
        XCTAssertLessThan(machine.frame.maxY, worktree.frame.minY, "restored cleanly above working-copy bar")
        checkpoint("new-session-composer-restored")
    }

    private func cancelSearch() {
        let field = app.searchFields.firstMatch
        let frame = field.frame
        let cancel = app.buttons.allElementsBoundByIndex.first { $0.frame.minX >= frame.maxX - 2 && abs($0.frame.midY - frame.midY) < 12 }
        XCTAssertNotNil(cancel); cancel?.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5))
    }

    /// The overview's view at index (Live, Usage, Limits), from the control at its top.
    private func view(_ index: Int) {
        let views = app.segmentedControls["overview-views"]
        if !views.isHittable { app.swipeDown(); app.swipeDown() }
        XCTAssertTrue(views.waitForExistence(timeout: 10), "the overview's views")
        views.buttons.element(boundBy: index).tap()
    }

    /// Opens a filter's menu, chooses item in it, and waits for the button to say it (or shows).
    private func pick(_ button: String, _ item: String, shows: String? = nil) {
        tap(button)
        let line = app.buttons.matching(NSPredicate(format: "label == %@ OR label BEGINSWITH %@", item, item + ",")).firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), item)
        line.tap()
        // A choice taken while the menu still opened can leave it up, chosen: closed by a tap on the page's title.
        let b = app.buttons[button]
        for _ in 0 ..< 3 where !wait(3, { b.isHittable && b.label == (shows ?? item) }) {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08)).tap()
        }
        XCTAssertTrue(wait { b.isHittable && b.label == (shows ?? item) }, "\(button): \(b.label)")
    }

    /// Chooses item in one of the filters' menus, which are inside the Filters button's.
    private func filter(_ menu: String, _ item: String) {
        tap("filters"); tap(menu)
        let line = app.buttons.matching(NSPredicate(format: "label == %@ OR label BEGINSWITH %@", item, item + ",")).firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), item)
        checkpoint("filter-menu-\(menu)")
        line.tap()
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

    private func answerWithText(_ text: String) {
        let field = app.textFields["answer-text"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        XCTAssertTrue(field.wait(for: \.isHittable, toEqual: true, timeout: 5))
        field.tap()
        let keyboard = app.keyboards.firstMatch
        if !keyboard.waitForExistence(timeout: 3) { field.tap() }
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "the free answer field has the keyboard")
        // A new question has an empty answer. Its accessibility value can be the placeholder.
        field.typeText(text)
        XCTAssertEqual(field.value as? String, text)
        tap("answer")
    }

    private func replace(_ field: XCUIElement, with value: String) {
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        let old = field.value as? String ?? ""
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count) + value)
    }

    /// Back: a session's page has its own button (it draws its bar), the others the system's.
    func testNewSessionRoleSwipe() {
        tap("try-demo")
        tap("menu")
        tap("new-session-place")

        let any = app.descendants(matching: .any)
        let presence = any["presence"].firstMatch
        XCTAssertTrue(presence.waitForExistence(timeout: 15), "role switcher on New Session")
        let agentRole = app.buttons["presence-agent"]
        let herRole = app.buttons["presence-her"]
        let roleName = any["presence-name"].firstMatch
        let drawerDim = any["drawer-dim"].firstMatch

        XCTAssertTrue(agentRole.waitForExistence(timeout: 10) && herRole.exists)
        XCTAssertTrue(agentRole.isSelected, "starts on Agent")
        XCTAssertEqual(roleName.label, "Agent")

        // 1. Swipe left on the switcher: Agent -> Her.
        presence.swipeLeft()
        XCTAssertTrue(herRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "swipe left switches to Her")
        XCTAssertEqual(roleName.label, "Her")
        XCTAssertEqual(any["new-session-title"].firstMatch.label, "What should Her do?")
        XCTAssertFalse(drawerDim.isHittable, "drawer stays closed")

        // 2. Swipe right on the switcher while on Her: Her -> Agent without opening the side menu.
        presence.swipeRight()
        XCTAssertTrue(agentRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "swipe right switches back to Agent")
        XCTAssertEqual(roleName.label, "Agent")
        XCTAssertFalse(drawerDim.isHittable, "swiping Her back to Agent does not open the drawer")

        // 3. Swipe left & right on the empty canvas area above the composer.
        let canvasLeft = app.coordinate(withNormalizedOffset: CGVector(dx: 0.72, dy: 0.30))
        let canvasRight = app.coordinate(withNormalizedOffset: CGVector(dx: 0.28, dy: 0.30))
        canvasLeft.press(forDuration: 0.02, thenDragTo: canvasRight, withVelocity: 600, thenHoldForDuration: 0)
        XCTAssertTrue(herRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "canvas swipe left switches to Her")

        canvasRight.press(forDuration: 0.02, thenDragTo: canvasLeft, withVelocity: 600, thenHoldForDuration: 0)
        XCTAssertTrue(agentRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "canvas swipe right switches back to Agent")
        XCTAssertFalse(drawerDim.isHittable, "canvas swipe right on Her does not open the drawer")

        // 4. Already on Agent: swiping right still opens the side menu drawer.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.20, dy: 0.30))
            .press(forDuration: 0.02, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.30)), withVelocity: 650, thenHoldForDuration: 0)
        XCTAssertTrue(drawerDim.wait(for: \.isHittable, toEqual: true, timeout: 5), "swipe right on Agent opens drawer")
        tap("drawer-dim")
        XCTAssertTrue(drawerDim.wait(for: \.isHittable, toEqual: false, timeout: 5))

        // 5. Tapping the smaller icon still switches roles cleanly.
        herRole.tap()
        XCTAssertTrue(herRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "tap switches to Her")
        agentRole.tap()
        XCTAssertTrue(agentRole.wait(for: \.isSelected, toEqual: true, timeout: 5), "tap switches back to Agent")
    }

    private func back() {
        let own = app.buttons["BackButton"]
        (own.exists && own.isHittable ? own : app.navigationBars.buttons.element(boundBy: 0)).tap()
    }

    /// The system's swipe back, from the screen's left edge across the bar: on a place of the menu's, the menu.
    private func swipeMenuOut() {
        // A session's page draws its bar: its title is in it.
        let bar = app.navigationBars.firstMatch.exists ? app.navigationBars.firstMatch : app.buttons["session-info"]
        let y = bar.frame.midY / app.frame.height
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: y))
            .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: y)), withVelocity: 600, thenHoldForDuration: 0.1)
        sleep(1)
    }

    /// A place: the overview, the sessions (the menu itself, open), and Machines and Grants, from the account.
    private func place(_ title: String) {
        if app.descendants(matching: .any)["drawer-dim"].isHittable { tap("drawer-dim") }
        backToOverview()
        // A tap while the page under it is still coming back (after Back or a delete) can be lost: again, once.
        if !app.descendants(matching: .any)["drawer-dim"].isHittable { tap("menu") }
        if !app.descendants(matching: .any)["drawer"].firstMatch.waitForExistence(timeout: 3) { tap("menu") }
        switch title {
        case "Overview": tap("overview-place")
        case "Grants": tap("account"); reachInSheet("account-grants"); tap("account-grants")
        case "Machines": tap("account"); reachInSheet("account-machines")
        case "All Sessions": break
        default: tap(title)
        }
    }

    /// Scrolls the sheet until what id names is clear of the demo's banner at the screen's foot.
    private func reachInSheet(_ identifier: String) {
        // The sheet's list makes its rows as they scroll in: up until it is there and clear of the banner.
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        XCTAssertTrue(app.buttons["close"].waitForExistence(timeout: 10), "the sheet")
        for _ in 0..<5 where !item.exists || !item.isHittable || item.frame.maxY > app.frame.height - 120 { app.swipeUp() }
        XCTAssertTrue(item.exists, identifier)
    }

    private func reach(_ identifier: String) {
        let item = app.descendants(matching: .any).matching(identifier: identifier).firstMatch
        // Down the page first, then back up: Work can open scrolled past what is near its top.
        for _ in 0..<6 {
            if item.exists && item.isHittable && item.frame.midY < app.frame.height - 75 { return }
            app.swipeUp()
        }
        for _ in 0..<10 {
            if item.exists && item.isHittable && item.frame.midY < app.frame.height - 75 { return }
            app.swipeDown()
        }
        XCTAssertTrue(item.exists && item.isHittable && item.frame.midY < app.frame.height - 75, "\(identifier): \(item.frame)")
    }

    private func backToOverview() {
        // A page may still be going (after a delete): until the menu's button or a Back shows.
        _ = app.buttons["menu"].waitForExistence(timeout: 5) || app.buttons["BackButton"].waitForExistence(timeout: 5)
        while !app.buttons["menu"].isHittable && !app.descendants(matching: .any)["drawer-dim"].isHittable {
            XCTAssertTrue(app.buttons["BackButton"].exists || app.navigationBars.buttons.element(boundBy: 0).exists)
            back()
        }
    }

    /// An item of the New menu. A tap while the menu is still coming up is dropped, and the menu stays: tapped again
    /// until the menu has gone.
    private func new(_ item: String) {
        tap("new")
        let entry = app.buttons[item]
        XCTAssertTrue(entry.waitForExistence(timeout: 20), item)
        for _ in 0..<4 where entry.exists {
            if entry.isHittable { entry.tap() }
            _ = entry.waitForNonExistence(timeout: 1.5)
        }
        XCTAssertFalse(entry.exists, "the menu closed on \(item)")
    }

    private func enterWorkDemo() {
        tap("try-demo")
        tap("menu")
        tap("overview-place")
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
