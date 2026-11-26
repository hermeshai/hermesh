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

        // Add a Project from the project menu: work-mac's own folders, browsed from here, and a folder made there. Only
        // once work-mac answers (until then the line under the environment says it connects, and Add Project is off).
        XCTAssertTrue(app.descendants(matching: .any)["connection-line"].firstMatch.waitForNonExistence(timeout: 60), "work-mac connected")
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

        // Its branch, read from the folder on work-mac: a worktree starts from the default one; the sheet lists them,
        // the one checked out marked, and can start from the remote. In the folder itself the branch is the one it works on.
        XCTAssertTrue(chip("thread-branch", contains: "基于"), app.buttons["thread-branch"].label)
        tap("thread-branch")
        XCTAssertTrue(app.navigationBars["基于分支"].waitForExistence(timeout: 10), "the base branch sheet")
        XCTAssertTrue(app.descendants(matching: .any)["branch-remote"].firstMatch.exists, "start from the remote")
        let checkedOut = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'branch-item-'")).firstMatch
        XCTAssertTrue(checkedOut.waitForExistence(timeout: 10) && checkedOut.label.contains("当前"), checkedOut.label)
        shot("composer-branches")
        tap("branch-done")
        tap("thread-worktree")
        menuItem("当前目录").tap()
        XCTAssertTrue(waitUntil(timeout: 10) { !self.app.buttons["thread-branch"].label.contains("基于") }, "in the folder: just its branch")
        tap("thread-worktree")
        menuItem("独立 worktree").tap()
        XCTAssertTrue(chip("thread-branch", contains: "基于"), app.buttons["thread-branch"].label)

        // The box is one line until it is tapped: then the model is along its bottom.
        XCTAssertFalse(app.buttons["thread-model"].exists, "closed: no model")
        openBox()
        // The model: one sheet, the providers one under another; a search across them; choosing one closes it.
        tap("thread-model")
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10))
        search.tap()
        search.typeText("fast")
        XCTAssertTrue(app.buttons["model-scripted-fast"].waitForExistence(timeout: 30), "found by its name")
        XCTAssertFalse(app.buttons["model-scripted-pro"].exists, "only what matches")
        shot("composer-model")
        tap("model-scripted-fast")
        XCTAssertTrue(app.buttons["model-scripted-fast"].waitForNonExistence(timeout: 10), "chosen: the sheet goes")
        openBox()
        XCTAssertTrue(chip("thread-model", contains: "Scripted Fast"), app.buttons["thread-model"].label)
        // How hard it thinks: not on the box, but at the top of the model sheet, dragged along a track to a level.
        XCTAssertFalse(app.buttons["thread-thinking"].exists, "not on the box")
        tap("thread-model")
        let thinking = app.descendants(matching: .any)["model-thinking"].firstMatch
        XCTAssertTrue(thinking.waitForExistence(timeout: 10), "at the top of the sheet")
        let firstModel = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'model-' AND identifier != 'model-done'")).firstMatch
        XCTAssertLessThan(thinking.frame.maxY, firstModel.frame.minY, "over the models")
        // Its levels are 低, 中 and 高 (the level before it may have been kept, so where it starts isn't said).
        dragThinking(thinking, to: 0.5)
        XCTAssertTrue(waitUntil(timeout: 5) { thinking.value as? String == "中" }, "\(thinking.value ?? "")")
        dragThinking(thinking, to: 0)
        XCTAssertTrue(waitUntil(timeout: 5) { thinking.value as? String == "低" }, "\(thinking.value ?? "")")
        dragThinking(thinking, to: 1)
        XCTAssertTrue(waitUntil(timeout: 5) { thinking.value as? String == "高" }, "\(thinking.value ?? "")")
        shot("composer-thinking")
        // Dragging doesn't choose a model: the sheet stays until Done.
        XCTAssertTrue(thinking.exists && app.buttons["model-done"].exists, "still open")
        tap("model-done")
        openBox()
        XCTAssertTrue(chip("thread-model", contains: "Scripted Fast"), app.buttons["thread-model"].label)

        // The keyboard goes as on Messages, with no button for it: the page dragged down, or a flick down on the box.
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 10), "typing: the keyboard is up")
        app.descendants(matching: .any)["new-session-title"].firstMatch.swipeDown()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 10), "the page dragged down: the keyboard goes")
        openBox()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 10))
        app.descendants(matching: .any)["thread-composer"].firstMatch.swipeDown()
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 10), "a flick down on the box: the keyboard goes")

        // The box's plus, as Her's but without Connectors: a photo goes with the first message.
        attachPhoto()

        // Start it: the session's own page, on work-mac in the project. The agent got the photo.
        send("say hello_from_work_mac\ninspect")
        // At once, as the session will be: the message and its photo thumbnail, under its title, with no wait for the machine on the composer.
        let first = app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'say hello_from_work_mac' AND label CONTAINS 'inspect'")).firstMatch
        let sentPhoto = app.buttons["prompt-attachment-照片.jpg"]
        XCTAssertTrue(first.waitForExistence(timeout: 5), "the message shows at once")
        XCTAssertTrue(sentPhoto.waitForExistence(timeout: 5), "the photo thumbnail shows on the user's message at once")
        XCTAssertGreaterThanOrEqual(sentPhoto.frame.width, 60)
        XCTAssertLessThanOrEqual(sentPhoto.frame.maxY, first.frame.minY + 4, "above the message's words")
        XCTAssertFalse(app.descendants(matching: .any)["new-session-title"].exists, "no longer the composer")
        // Its first turn makes the worktree, on a line of its own under the message; opened, how and where.
        let workspace = app.buttons["thread-workspace"]
        XCTAssertTrue(workspace.waitForExistence(timeout: 30), "where it works")
        XCTAssertTrue(waitUntil(timeout: 30) { workspace.label.contains("新建了分支") && workspace.label.contains("hermesh/") }, workspace.label)
        XCTAssertGreaterThan(workspace.frame.minY, first.frame.maxY, "under the message")
        XCTAssertTrue(reply("hello_from_work_mac").waitForExistence(timeout: 60), "the first turn's reply")
        XCTAssertGreaterThan(reply("hello_from_work_mac").frame.minY, workspace.frame.maxY, "before the agent's work")
        workspace.tap()
        let details = app.descendants(matching: .any)["thread-workspace-details"].firstMatch
        XCTAssertTrue(details.waitForExistence(timeout: 10), "opened")
        XCTAssertTrue(text("独立 worktree").waitForExistence(timeout: 5), "how it works")
        shot("composer-workspace")
        workspace.tap()
        XCTAssertTrue(details.waitForNonExistence(timeout: 10), "closed")
        XCTAssertTrue(reply("attachments=[照片.jpg]").waitForExistence(timeout: 10), "the photo, with the first message")
        XCTAssertTrue(sentPhoto.exists && sentPhoto.frame.width >= 60, "the photo thumbnail stays on the user's bubble once the session is read")
        XCTAssertFalse(app.descendants(matching: .any)["thread-attached"].exists, "sent: the box holds nothing")
        XCTAssertTrue(text("work-mac/fresh-app").waitForExistence(timeout: 10), "where it runs, under the title")

        // Keep going in it.
        send("say second_turn_done")
        XCTAssertTrue(reply("second_turn_done").waitForExistence(timeout: 60), "the second turn's reply")
        // On a session's page, taps in the box keep the keyboard (they place the caret, a double tap selects a word);
        // a tap on the conversation puts it away.
        let composer = app.descendants(matching: .any)["thread-composer"].firstMatch
        composer.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "typing")
        composer.tap()
        composer.doubleTap()
        sleep(1)
        XCTAssertTrue(app.keyboards.firstMatch.exists, "taps in the box: the keyboard stays")
        reply("second_turn_done").tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 10), "a tap on the conversation: the keyboard goes")
        // Drawn as a session's page: its bar (the title over where it runs, in a capsule that opens its info; more), the
        // owner's words with nothing over them, and no line at the end of a turn that went well.
        let title = app.buttons["session-info"]
        XCTAssertTrue(title.exists, "a session's title")
        XCTAssertTrue(title.label.contains("work-mac/fresh-app"), title.label)
        XCTAssertTrue(app.buttons["session-more"].exists, "a session's actions")
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label == '任务信息'")).firstMatch.exists, "no info button beside it")
        XCTAssertFalse(app.descendants(matching: .any)["prompt-sender"].exists, "the owner's words: no one named over them")
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH '这一轮做完了'")).firstMatch.exists, "no ending line")
        shot("composer-session")
        title.tap()
        XCTAssertTrue(app.navigationBars["任务信息"].waitForExistence(timeout: 10), "the title opens the session's info")
        XCTAssertTrue(text("发起").exists, "who started it")
        tap("close")
        XCTAssertTrue(app.navigationBars["任务信息"].waitForNonExistence(timeout: 10), "closed")

        // While a turn runs: Stop beside Send, and what is sent waits for the next turn.
        send("think weighing it\nsleep 4s\ntool-sleep 4s\nsleep 4s\nsay on it\nsleep 20s")
        XCTAssertTrue(app.buttons["thread-stop"].waitForExistence(timeout: 30), "Stop while it runs")
        // And a line at the end of the conversation, whatever its last row: the mark at work, what the agent does now and how long.
        let working = app.descendants(matching: .any)["thread-working"].firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 30), "said while it runs")
        XCTAssertTrue(waitUntil(timeout: 30) { working.label.contains("思考中") }, "its reasoning streams: \(working.label)")
        XCTAssertTrue(waitUntil(timeout: 15) { working.label.contains("处理中") }, "a tool runs: \(working.label)")
        XCTAssertTrue(waitUntil(timeout: 15) { working.label.contains("工作中") }, "between steps: \(working.label)")
        XCTAssertTrue(waitUntil(timeout: 15) { working.label.contains("回复中") }, "its reply streams: \(working.label)")
        let sent = app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'sleep 20s'")).firstMatch
        XCTAssertGreaterThan(working.frame.minY, sent.frame.maxY, "under the last message")
        XCTAssertLessThan(working.frame.maxY, app.descendants(matching: .any)["thread-composer"].firstMatch.frame.minY, "over the box")
        shot("composer-working")
        send("say after_the_stop")
        XCTAssertTrue(app.descendants(matching: .any)["thread-queued-0"].waitForExistence(timeout: 15), "waiting for the next turn")
        shot("composer-queued")
        tap("thread-stop")
        XCTAssertTrue(reply("after_the_stop").waitForExistence(timeout: 60), "the waiting message, once stopped")
        XCTAssertTrue(app.buttons["thread-stop"].waitForNonExistence(timeout: 30), "nothing runs")
        XCTAssertTrue(working.waitForNonExistence(timeout: 10), "nothing runs: the line goes")

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
        openBox()
        XCTAssertTrue(chip("thread-model", contains: "Scripted Fast"), app.buttons["thread-model"].label)
        tap("thread-model")
        let kept = app.descendants(matching: .any)["model-thinking"].firstMatch
        XCTAssertTrue(kept.waitForExistence(timeout: 10))
        XCTAssertEqual(kept.value as? String, "高", "how hard it thinks, kept")
        tap("model-done")

        // Aimed at owner-mac while it answers: nothing under the environment. Gone offline, the line under the
        // environment says so and what sending does; a tap opens the diagnostics, about owner-mac first. Back, it goes.
        pickMachine("owner-mac")
        let line = app.descendants(matching: .any)["connection-line"].firstMatch
        sleep(2)
        XCTAssertFalse(line.exists, "nothing said while owner-mac answers")
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(line.waitForExistence(timeout: 120), "the line under the environment")
        XCTAssertTrue(waitUntil(timeout: 120) { line.label.contains("owner-mac 离线 · 消息会排队到它上线") }, line.label)
        XCTAssertGreaterThan(line.frame.minY, app.buttons["thread-machine"].frame.maxY - 4, "under the environment")
        XCTAssertLessThan(line.frame.maxY, app.descendants(matching: .any)["thread-composer"].firstMatch.frame.minY, "over the box")
        shot("composer-machine-offline")
        line.tap()
        XCTAssertTrue(app.navigationBars["连接诊断"].waitForExistence(timeout: 10), "the diagnostics")
        XCTAssertTrue(text("owner-mac 离线了").waitForExistence(timeout: 10), "about owner-mac first")
        shot("composer-machine-offline-diagnostics")
        app.navigationBars["连接诊断"].buttons["完成"].tap()
        _ = try control("machine?up=1", method: "POST")
        XCTAssertTrue(line.waitForNonExistence(timeout: 120), "gone once owner-mac is back")

        // Sent while owner-mac is offline: the session's page at once, the message faded and, under it, that it goes
        // once owner-mac is back, with Discard; nothing at work, and the inline connection line at the conversation's end.
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(waitUntil(timeout: 120) { line.exists && line.label.contains("owner-mac 离线") }, "offline again")
        send("say while_offline")
        let waiting = app.descendants(matching: .any)["prompt-note"].firstMatch
        XCTAssertTrue(waiting.waitForExistence(timeout: 30), "said under the message")
        XCTAssertTrue(text("等 owner-mac 在线后送达").waitForExistence(timeout: 30), "it goes once owner-mac is back")
        XCTAssertFalse(app.descendants(matching: .any)["thread-queued-0"].exists, "the message is its bubble, not the queue's")
        XCTAssertFalse(app.descendants(matching: .any)["thread-working"].exists, "nothing at work")
        XCTAssertTrue(line.exists && line.label.contains("owner-mac 离线"), "inline connection line at the conversation's end")
        XCTAssertFalse(app.descendants(matching: .any)["thread-offline-queued"].exists, "nothing more over the box")
        shot("composer-offline-message")
        tap("prompt-discard")
        XCTAssertTrue(toast(containing: "已删除"), "discarded")
        _ = try control("machine?up=1", method: "POST")
    }

    /// Inline connection status (Scheme A) on iPhone: under the environment picker on New Session, merged in place with
    /// WorkLine while a session turn is running, and at the conversation's end while idle/offline-queued; tapping any
    /// of them opens Connection Diagnostics.
    ///
    ///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testConnectionLine go test ./e2e -run TestIPhone -v -timeout 20m
    @objc func testConnectionLine() throws {
        try join()

        openMenu()
        tap("new-session-place")
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].waitForExistence(timeout: 10))
        pickMachine("owner-mac")

        let line = app.descendants(matching: .any)["connection-line"].firstMatch
        sleep(2)
        XCTAssertFalse(line.exists, "nothing said while owner-mac answers")

        // 1. New Session: under the environment picker, over the box; tapping opens Connection Diagnostics.
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(line.waitForExistence(timeout: 120), "the line under the environment")
        XCTAssertTrue(waitUntil(timeout: 120) { line.label.contains("owner-mac 离线 · 消息会排队到它上线") }, line.label)
        XCTAssertGreaterThan(line.frame.minY, app.buttons["thread-machine"].frame.maxY - 4, "under the environment")
        XCTAssertLessThan(line.frame.maxY, app.descendants(matching: .any)["thread-composer"].firstMatch.frame.minY, "over the box")
        line.tap()
        XCTAssertTrue(app.navigationBars["连接诊断"].waitForExistence(timeout: 10), "the diagnostics")
        XCTAssertTrue(text("owner-mac 离线了").waitForExistence(timeout: 10), "about owner-mac first")
        app.navigationBars["连接诊断"].buttons["完成"].tap()
        _ = try control("machine?up=1", method: "POST")
        XCTAssertTrue(line.waitForNonExistence(timeout: 120), "gone once owner-mac is back")

        // 2. Running Session: start a long tool run on owner-mac, then take owner-mac offline so WorkLine merges the
        // connection notice in place.
        send("shell sleep 1800")
        let working = app.descendants(matching: .any)["thread-working"].firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 30), "working line while running")
        XCTAssertFalse(line.exists, "no connection notice while connected")
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(line.waitForExistence(timeout: 120), "connection notice merged in place")
        XCTAssertTrue(working.exists, "still one working container")
        XCTAssertTrue(waitUntil(timeout: 120) { line.label.contains("owner-mac 离线 · 消息会排队到它上线") }, line.label)
        _ = try control("machine?up=1", method: "POST")
        XCTAssertTrue(line.waitForNonExistence(timeout: 120), "connection notice gone once owner-mac is back")

        // 3. Offline-queued Session: nothing at work, inline connection line at the conversation's end.
        openMenu()
        tap("new-session-place")
        XCTAssertTrue(app.descendants(matching: .any)["new-session-title"].waitForExistence(timeout: 10))
        pickMachine("owner-mac")
        _ = try control("machine?up=0", method: "POST")
        XCTAssertTrue(waitUntil(timeout: 120) { line.exists && line.label.contains("owner-mac 离线") }, "offline again")
        send("say while_offline")
        let waiting = app.descendants(matching: .any)["prompt-note"].firstMatch
        XCTAssertTrue(waiting.waitForExistence(timeout: 30), "said under the message")
        XCTAssertFalse(app.descendants(matching: .any)["thread-working"].exists, "nothing at work")
        XCTAssertTrue(line.exists && line.label.contains("owner-mac 离线"), "inline connection line at the conversation's end")
        _ = try control("machine?up=1", method: "POST")
    }

    /// Drags the thinking track's glass from where it is to a place along it (0 the start, 1 the end), as a finger does.
    private func dragThinking(_ track: XCUIElement, to x: CGFloat) {
        let levels = ["低", "中", "高"]
        let from = CGFloat(levels.firstIndex(of: track.value as? String ?? "") ?? 1) / CGFloat(levels.count - 1)
        // The track is the row's lower part, under its title; each level is the middle of a column of it.
        let n = CGFloat(levels.count)
        let at = { (p: CGFloat) in track.coordinate(withNormalizedOffset: CGVector(dx: (0.5 + p * (n - 1)) / n, dy: 0.68)) }
        at(from).press(forDuration: 0.1, thenDragTo: at(x))
    }

    /// Taps into the box, which opens it.
    private func openBox() {
        let box = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(box.waitForExistence(timeout: 10))
        box.tap()
        XCTAssertTrue(app.buttons["thread-model"].waitForExistence(timeout: 10), "open: the model along its bottom")
    }

    /// The plus: a sheet with Photos and Files (no Connectors) and the permission mode; the first photo in the system's
    /// picker, shown in the box once it is uploaded.
    private func attachPhoto() {
        let plus = app.buttons["thread-attach"]
        let box = app.descendants(matching: .any)["thread-composer"].firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 10) && box.exists)
        tap("thread-attach")
        XCTAssertTrue(app.buttons["thread-attach-photos"].waitForExistence(timeout: 10) && app.buttons["thread-attach-files"].exists, "photos and files")
        XCTAssertFalse(app.buttons["连接器"].exists, "no connectors in a session's box")
        XCTAssertEqual(app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'thread-mode-'")).count, 4, "the permission mode, under them")
        XCTAssertTrue(text("智能自动").exists)
        sleep(1) // the sheet settled, for the picture
        shot("composer-attach-sheet")
        tap("thread-attach-photos")
        let photo = app.images.matching(NSPredicate(format: "label BEGINSWITH '照片'")).firstMatch
        XCTAssertTrue(photo.waitForExistence(timeout: 20), "the photo picker: \(app.debugDescription)")
        sleep(1) // the picker settled
        press(photo) // the picker is the system's, out of the app: its photos read as not hittable
        let add = app.buttons["Add"] // the picker's 完成
        XCTAssertTrue(add.waitForExistence(timeout: 10), "Done: \(app.debugDescription)")
        press(add)
        let attached = app.descendants(matching: .any)["thread-attached"]
        XCTAssertTrue(attached.waitForExistence(timeout: 20), "the photo, in the box")
        XCTAssertTrue(waitUntil(timeout: 30) { !self.app.descendants(matching: .any)["thread-attach-loading"].exists }, "uploaded")
        XCTAssertTrue(app.buttons["移除 照片.jpg"].exists, "removable")
        shot("composer-attached")
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
