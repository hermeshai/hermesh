import XCTest

/// Conversations with Her besides her main one on the iPhone (docs/her-conversations.md §3.10, §6), against
/// her itself with her-mock as its model. Mirrors apps/desktop/e2e/her-conversations.spec.ts:
/// Her turned on and connected; a new session goes to Agent the first time, Her small on the right;
/// tapping Her switches the page to her question, her machine and her models (first in the model sheet,
/// her default badged); starting a conversation opens HerConversationPage with Her's animated avatar and
/// floating team capsule; Her asks before starting a coding agent; the child nests under the conversation in the drawer, folds
/// and unfolds, blocks archiving while running, has no pin of its own, groups under Her first by project,
/// and un-nests when "嵌套子任务" is off; the child's news comes back to this conversation while Her's main
/// conversation never hears of it; and the next new session remembers Her across restarts too.
///
///     TMPDIR=/tmp HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerConversations go test ./e2e -run '^TestIPhone$' -v -timeout 35m
extension HermeshUITests {
    @objc func testHerConversations() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        try herTurnOn()
        try herOnboarding()

        let any = app.descendants(matching: .any)

        // 1. First New Session defaults to Agent, with Her small on the right.
        openMenu()
        tap("new-session-place")
        let presence = any["presence"].firstMatch
        XCTAssertTrue(presence.waitForExistence(timeout: 10), "role switcher on New Session")
        let agentRole = app.buttons["presence-agent"]
        let herRole = app.buttons["presence-her"]
        XCTAssertTrue(agentRole.exists && herRole.exists)
        XCTAssertLessThan(agentRole.frame.midX, herRole.frame.midX, "Agent on the left, Her on the right")
        XCTAssertTrue(agentRole.isSelected, "first time goes to Agent")
        XCTAssertEqual(any["presence-name"].firstMatch.label, "Agent")
        XCTAssertTrue(any["new-session-title"].firstMatch.exists)
        herShot("Her conversations: new session defaults to Agent")

        // On Agent, the model sheet lists coding agent providers and hides Her.
        let box = any["thread-composer"].firstMatch
        box.tap()
        XCTAssertTrue(app.buttons["thread-model"].waitForExistence(timeout: 10))
        tap("thread-model")
        let otherProviders = any.matching(NSPredicate(format: "identifier BEGINSWITH 'provider-' AND identifier != 'provider-her'"))
        XCTAssertTrue(otherProviders.firstMatch.waitForExistence(timeout: 30), "agent providers in the model sheet on Agent")
        XCTAssertFalse(any["provider-her"].firstMatch.exists, "Her hidden in the model sheet on Agent")
        tap("model-done")

        // 2. Tapping the small cat gives the new session to Her: her question, her machine, her placeholder.
        press(herRole)
        XCTAssertTrue(waitUntil { herRole.isSelected }, "Her selected")
        XCTAssertEqual(any["presence-name"].firstMatch.label, "Her")
        XCTAssertEqual(any["new-session-title"].firstMatch.label, "要 Her 做什么？")
        let whereLine = any["launch-her-where"].firstMatch
        XCTAssertTrue(whereLine.waitForExistence(timeout: 10) && whereLine.label.contains("Her 在 owner-mac"), whereLine.label)
        XCTAssertEqual(box.placeholderValue, "发消息给 Her")
        herShot("Her conversations: new session given to Her")

        // 3. Model sheet on Her: shows only Her's models with her default marked "默认" (no coding agent providers).
        box.tap()
        XCTAssertTrue(app.buttons["thread-model"].waitForExistence(timeout: 10))
        tap("thread-model")
        let herProvider = any["provider-her"].firstMatch
        XCTAssertTrue(herProvider.waitForExistence(timeout: 30), "Her provider in the model sheet")
        XCTAssertFalse(otherProviders.firstMatch.exists, "coding agent providers hidden in the model sheet on Her")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'model-' AND label CONTAINS '默认'")).firstMatch.exists, "default model marked")
        herShot("Her conversations: Her models only in ModelSheet")
        tap("model-done")

        // 4. Send prompt asking Her to start a coding agent: opens HerConversationPage under its compact bar, asks approval first.
        let prompt = #"[[mock:mesh:session_start:{"project":"owner-mac/demo","agent":"gemini","prompt":"sleep 70s\nsay 做完了","title":"对话派的活"}]] 开工"#
        box.tap()
        box.typeText(prompt)
        let sendBtn = app.buttons["thread-send"]
        XCTAssertTrue(waitUntil { sendBtn.isEnabled }, "Send enabled")
        press(sendBtn)
        let aiAllow = app.alerts["与第三方 AI 共享数据？"].buttons["允许与 AI 共享"]
        if aiAllow.waitForExistence(timeout: 3) { aiAllow.tap() }

        let avatar = app.buttons["her-avatar"]
        XCTAssertTrue(avatar.waitForExistence(timeout: 60), "HerConversationPage shares Her's animated avatar")
        XCTAssertTrue(app.buttons["her-tag"].exists, "HerConversationPage shares Her's floating team capsule")
        XCTAssertTrue(waitUntil(timeout: 30) { app.buttons["her-progress"].exists }, "HerConversationPage shows Progress button")

        let approval = any["her-approval"].firstMatch
        XCTAssertTrue(approval.waitForExistence(timeout: 60), "Her asks before starting the coding agent")
        herShot("Her conversations: approval on HerConversationPage")
        tap("her-allow-once")

        let card = any["her-session-对话派的活"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 60), "the started coding session's card in Her's conversation")
        XCTAssertTrue(any["her-reply"].firstMatch.waitForExistence(timeout: 30), "Her's turn finishes after starting the child")

        // 5. In the drawer: the Her conversation and its nested child under 进行中, folding and unfolding.
        openMenu()
        let drawer = any["drawer"].firstMatch
        let tree = drawer.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'tree-'")).firstMatch
        XCTAssertTrue(tree.waitForExistence(timeout: 30), "subtask tree in the drawer")
        let childRow = drawer.buttons["session-对话派的活"]
        XCTAssertTrue(childRow.waitForExistence(timeout: 15), "nested child row visible")
        let parentBtn = tree.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'session-' AND identifier != 'session-对话派的活'")).firstMatch
        XCTAssertTrue(waitUntil(timeout: 20) { (parentBtn.value as? String) == "运行中" }, "parent row stays visibly running while child runs")
        let activeHeader = drawer.buttons["group-进行中"]
        let doneHeader = drawer.buttons["group-已结束"]
        if activeHeader.exists && doneHeader.exists {
            XCTAssertGreaterThan(tree.frame.minY, activeHeader.frame.minY, "under 进行中 while child runs")
            XCTAssertLessThan(tree.frame.minY, doneHeader.frame.minY, "above 已结束 while child runs")
        }
        herShot("Her conversations: nested subtask tree in drawer")

        // Fold and unfold via the left chevron: parent row stays 运行中 while folded too.
        let twist = tree.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'twist-'")).firstMatch
        XCTAssertTrue(twist.exists && twist.label == "收起", twist.label)
        press(twist)
        XCTAssertTrue(childRow.waitForNonExistence(timeout: 5), "folded: child hidden")
        XCTAssertEqual(twist.label, "展开")
        XCTAssertEqual(parentBtn.value as? String, "运行中", "folded parent row still reports running")
        press(twist)
        XCTAssertTrue(childRow.waitForExistence(timeout: 5), "unfolded: child visible again")

        // Reopen the Her conversation while the child has plenty of runtime left: ⋯ offers Her's Settings and disables Archive with "还有任务在进行" (D14).
        press(parentBtn)
        XCTAssertTrue(any["drawer-dim"].waitForNonExistence(timeout: 5))
        tap("session-more")
        XCTAssertTrue(app.buttons["her-conversation-settings"].waitForExistence(timeout: 10), "Her's Settings in ⋯ menu")
        let archiveInMore = app.buttons["archive-still-running"]
        XCTAssertTrue(archiveInMore.waitForExistence(timeout: 10), "Archive renders '还有任务在进行' subtitle while child runs")
        XCTAssertFalse(archiveInMore.isEnabled, "cannot archive while child runs")
        herShot("Her conversations: archive disabled in menu")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.15, dy: 0.5)).tap()
        XCTAssertTrue(archiveInMore.waitForNonExistence(timeout: 5))

        // 6. Group by Project: Her section comes first, and turning off "嵌套子任务" puts the child back in demo.
        openMenu()
        tap("view-options")
        menuItem("分组").tap()
        menuItem("按项目").tap()
        let herGroup = drawer.buttons["group-Her"]
        XCTAssertTrue(herGroup.waitForExistence(timeout: 10), "Her project group")
        let demoGroup = drawer.buttons["group-demo"]
        if demoGroup.exists {
            XCTAssertLessThan(herGroup.frame.minY, demoGroup.frame.minY, "Her group comes before projects")
        }
        XCTAssertTrue(tree.exists && childRow.exists, "child stays nested under Her in By Project")
        herShot("Her conversations: grouped by project")

        tap("view-options")
        menuItem("显示").tap()
        menuItem("嵌套子任务").tap()
        XCTAssertTrue(tree.waitForNonExistence(timeout: 5), "nesting off: no tree")
        XCTAssertTrue(drawer.buttons["group-demo"].waitForExistence(timeout: 10), "child returned to its own project demo")

        tap("view-options")
        menuItem("显示").tap()
        menuItem("嵌套子任务").tap()
        XCTAssertTrue(tree.waitForExistence(timeout: 10), "nesting back on")

        tap("view-options")
        menuItem("分组").tap()
        menuItem("按状态").tap()

        // 7. Reopen the Her conversation: child's news arrives here, while Her's main conversation never hears of it.
        XCTAssertTrue(parentBtn.waitForExistence(timeout: 10))
        press(parentBtn)
        XCTAssertTrue(any["drawer-dim"].waitForNonExistence(timeout: 5))
        let news = any.matching(identifier: "her-reply").containing(NSPredicate(format: "label CONTAINS %@", "turn_done")).firstMatch
        XCTAssertTrue(news.waitForExistence(timeout: 120), "child's news arrived in the Her conversation that started it")
        herShot("Her conversations: news back in the conversation")

        // Once the whole tree has stopped: Archive is enabled and Delete warns that sent tasks are kept (D14); child row has no Pin button (D13).
        sleep(2)
        tap("session-more")
        let readyArchive = app.buttons["session-archive"]
        XCTAssertTrue(readyArchive.waitForExistence(timeout: 15), "Archive ready once whole tree stopped")
        XCTAssertTrue(readyArchive.isEnabled, "parent can archive once child has stopped")
        app.buttons["删除…"].firstMatch.tap()
        XCTAssertTrue(text("它派出的任务会保留。").waitForExistence(timeout: 5), "delete dialog notes child tasks are kept")
        if app.buttons["取消"].exists {
            app.buttons["取消"].firstMatch.tap()
        } else {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)).tap()
        }
        XCTAssertTrue(text("它派出的任务会保留。").waitForNonExistence(timeout: 5))

        openMenu()
        if drawer.buttons["group-进行中"].exists {
            press(drawer.buttons["group-进行中"])
        }
        XCTAssertTrue(childRow.waitForExistence(timeout: 15))
        if doneHeader.exists {
            XCTAssertGreaterThan(tree.frame.minY, doneHeader.frame.minY, "tree moved under 已结束 once child finished")
        }
        childRow.press(forDuration: 1.0)
        XCTAssertTrue(app.buttons["重命名"].waitForExistence(timeout: 10), "child context menu opened")
        XCTAssertFalse(app.buttons["置顶"].exists, "nested child has no Pin button")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.45, dy: 0.08)).tap()
        XCTAssertTrue(app.buttons["重命名"].waitForNonExistence(timeout: 5))
        openMenu()
        tap("her-place")
        XCTAssertTrue(text("好了，我会用").waitForExistence(timeout: 30), "main Her conversation intact")
        XCTAssertFalse(any.matching(identifier: "her-reply").containing(NSPredicate(format: "label CONTAINS %@", "turn_done")).firstMatch.exists,
                       "main Her conversation never heard the child's news")

        // 8. Next New Session remembers Her, and across app restart too.
        openMenu()
        tap("new-session-place")
        XCTAssertTrue(any["presence"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["presence-her"].isSelected, "next new session remembers Her")

        app.terminate()
        app.launch()
        openMenu()
        tap("new-session-place")
        XCTAssertTrue(any["presence"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["presence-her"].isSelected, "remembered Her after restart")
    }
}
