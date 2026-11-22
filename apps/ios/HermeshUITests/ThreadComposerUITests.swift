import XCTest

/// New sessions on the iPhone (docs/thread-composer.md, the third phase), as a person makes them, in Chinese,
/// against two machines besides owner-mac (otherMacThreads in e2e/desktop_test.go): work-mac, joined over the
/// relay with the scripted agent for its threads and repositories in its home folder, and old-mac, a Hermesh
/// from before threads. A folder made on work-mac from the iPhone becomes a project; a session starts there on
/// a chosen model, keeps going, waits behind a turn and is stopped; the project's group starts the next one.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testThreadComposer go test ./e2e -run TestIPhone -v -timeout 40m
extension HermeshUITests {
    @objc func testThreadComposer() throws {
        try join()

        // New Session sits over Her in the menu, and opens the composer.
        openMenu()
        let place = app.buttons["new-session-place"]
        XCTAssertTrue(place.waitForExistence(timeout: 10))
        XCTAssertLessThan(place.frame.minY, app.descendants(matching: .any)["her-place"].firstMatch.frame.minY, "over Her")
        press(place)
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].waitForExistence(timeout: 10))

        // work-mac has no project yet: the session would use its temporary workspace.
        pickMachine("work-mac")
        XCTAssertTrue(chip("thread-project", contains: "临时工作区"), app.buttons["thread-project"].label)

        // Add a Project from the project menu: work-mac's own folders, browsed from here, and a folder made there.
        tap("thread-project")
        tap("thread-add-project")
        XCTAssertTrue(app.navigationBars["添加项目"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["repo-~/code/mono"].waitForExistence(timeout: 30), "work-mac's recent repositories")
        tap("browse-folders")
        tap("folder-code", timeout: 30)
        XCTAssertTrue(app.buttons["folder-recent-app"].waitForExistence(timeout: 30), "the folders on work-mac")
        tap("folder-menu")
        tap("new-folder")
        let field = app.alerts.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.typeText("fresh-app")
        tapSettled(app.alerts.buttons["建为 git 仓库"])
        XCTAssertTrue(text("这是一个 git 仓库").waitForExistence(timeout: 30), "made as a repository, and opened")
        shot("composer-new-folder")
        tap("choose-folder")
        XCTAssertTrue(text("fresh-app").waitForExistence(timeout: 20), "the folder chosen")
        // The agent connected with every project (the fixture's bot) could use it at once: said before adding.
        XCTAssertTrue(text("bot能用这个环境里的所有项目").waitForExistence(timeout: 5), "the grants it reaches")
        tap("add-project-confirm")
        XCTAssertTrue(toast(containing: "已添加项目「fresh-app」"))
        // The new session takes it, in a worktree of its own as a repository's sessions are.
        XCTAssertTrue(chip("thread-project", contains: "fresh-app"), app.buttons["thread-project"].label)
        XCTAssertTrue(app.descendants(matching: .any)["thread-worktree"].waitForExistence(timeout: 10), "a repository: its own worktree offered")

        // The model: one sheet. A search across providers, a provider's models, how hard it thinks.
        tap("thread-model")
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10))
        search.tap()
        search.typeText("fast")
        XCTAssertTrue(app.buttons["model-scripted-fast"].waitForExistence(timeout: 30), "found by its name")
        XCTAssertFalse(app.buttons["model-scripted-pro"].exists, "only what matches")
        tap("model-scripted-fast")
        let high = app.segmentedControls["thinking"].buttons["高"]
        XCTAssertTrue(high.waitForExistence(timeout: 10), "the levels of the model chosen")
        high.tap()
        shot("composer-model")
        tap("model-done")
        XCTAssertTrue(chip("thread-model", contains: "Scripted Fast 高"), app.buttons["thread-model"].label)

        // Start it: the session's own page, on work-mac in the project.
        send("say hello_from_work_mac")
        XCTAssertTrue(reply("hello_from_work_mac").waitForExistence(timeout: 60), "the first turn's reply")
        XCTAssertTrue(text("work-mac · fresh-app").waitForExistence(timeout: 10), "where it runs, fixed")

        // Keep going in it.
        send("say second_turn_done")
        XCTAssertTrue(reply("second_turn_done").waitForExistence(timeout: 60), "the second turn's reply")
        // Drawn as a session's page: its toolbar (info, more) and its ending.
        XCTAssertTrue(app.buttons["session-info"].exists, "a session's info")
        XCTAssertTrue(app.buttons["session-more"].exists, "a session's actions")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH '这一轮做完了'")).firstMatch.waitForExistence(timeout: 10), "a session's ending")
        shot("composer-session")

        // While a turn runs: Stop beside Send, and what is sent waits for the next turn.
        send("sleep 20s")
        XCTAssertTrue(app.buttons["thread-stop"].waitForExistence(timeout: 30), "Stop while it runs")
        send("say after_the_stop")
        XCTAssertTrue(app.descendants(matching: .any)["thread-queued-0"].waitForExistence(timeout: 15), "waiting for the next turn")
        shot("composer-queued")
        tap("thread-stop")
        XCTAssertTrue(reply("after_the_stop").waitForExistence(timeout: 60), "the waiting message, once stopped")
        XCTAssertTrue(app.buttons["thread-stop"].waitForNonExistence(timeout: 30), "nothing runs")

        // Archived from its ⋯ menu, as a session: read-only, said at the top with Unarchive, and no box.
        tap("session-more")
        menuItem("归档").tap()
        let note = app.descendants(matching: .any)["archived-note"]
        XCTAssertTrue(note.waitForExistence(timeout: 15), "archived, said so")
        XCTAssertTrue(app.descendants(matching: .any)["thread-composer"].waitForNonExistence(timeout: 10), "read-only")
        app.buttons["取消归档"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["thread-composer"].firstMatch.waitForExistence(timeout: 15), "back to work")

        // The session is in the menu's list, with the rest.
        openMenu()
        let row = app.descendants(matching: .any)["drawer"].descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'session-' AND identifier CONTAINS %@", "hello_from_work_mac")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "listed")

        // Grouped by project, the project's group starts a new session in it, on work-mac.
        tap("view-options")
        menuItem("分组").tap()
        menuItem("按项目").tap()
        // The menu scrolls on its own, beside the page.
        let plus = app.buttons["new-in-fresh-app"]
        XCTAssertTrue(plus.waitForExistence(timeout: 10))
        let drawer = app.descendants(matching: .any)["drawer"].firstMatch
        for _ in 0..<6 where !plus.isHittable { drawer.swipeUp() }
        press(plus)
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].waitForExistence(timeout: 10))
        XCTAssertTrue(chip("thread-machine", contains: "work-mac"), app.buttons["thread-machine"].label)
        XCTAssertTrue(chip("thread-project", contains: "fresh-app"), app.buttons["thread-project"].label)

        // New Session afterwards opens on what the last session used: work-mac, the project, the model.
        showOverview()
        openMenu()
        tap("new-session-place")
        XCTAssertTrue(chip("thread-machine", contains: "work-mac"), app.buttons["thread-machine"].label)
        XCTAssertTrue(chip("thread-project", contains: "fresh-app"), app.buttons["thread-project"].label)
        XCTAssertTrue(chip("thread-model", contains: "Scripted Fast 高"), app.buttons["thread-model"].label)

        // old-mac runs a Hermesh from before threads: said so, and nothing can be sent there; marked in the menu.
        pickMachine("old-mac")
        XCTAssertTrue(text("old-mac 上的 Hermesh 需要更新才能新建会话").waitForExistence(timeout: 10))
        tap("thread-machine")
        XCTAssertTrue(app.buttons["machine-old-mac"].waitForExistence(timeout: 10))
        // A menu item's subtitle is a text of its own, not in the item's label.
        XCTAssertTrue(text("需要更新").waitForExistence(timeout: 5), "marked as needing an update")
        shot("composer-needs-update-menu")
        tap("machine-old-mac")
        let box = app.descendants(matching: .any)["thread-composer"].firstMatch
        box.tap()
        box.typeText("say hi")
        XCTAssertFalse(app.buttons["thread-send"].isEnabled, "can't be sent")
        shot("composer-needs-update")
    }

    private func pickMachine(_ name: String) {
        tap("thread-machine")
        tap("machine-\(name)")
        XCTAssertTrue(chip("thread-machine", contains: name), app.buttons["thread-machine"].label)
    }

    /// A menu under the box shows what's chosen, once it does.
    private func chip(_ id: String, contains s: String) -> Bool {
        waitUntil(timeout: 20) { self.app.buttons[id].label.contains(s) }
    }

    private func send(_ s: String) {
        let box = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(box.waitForExistence(timeout: 10))
        box.tap()
        box.typeText(s)
        let send = app.buttons["thread-send"]
        XCTAssertTrue(waitUntil { send.isEnabled }, "Send ready")
        press(send)
    }

    /// The agent's words, drawn as a session's (StepView): not what was sent ("say …"), nor the title.
    private func reply(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@ AND NOT (label CONTAINS 'say ')", s)).firstMatch
    }

    private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
