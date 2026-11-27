import UIKit
import XCTest

/// Her's page as it reads on a phone (docs/her-server.md §9, docs/her-team.md §4.3), from turning her on to typing to
/// her. In the largest text sizes, a machine's name keeps the row's width, on Machines and where Her is turned on, and
/// the signing sheet's buttons are one over the other, in sight unscrolled (as Approve is at the usual size). Turned on where she takes a while to start
/// (TestIPhone's her waits while the test says so, POST /her-slow), her page is the whole screen: her mark at the top,
/// the team panel down from there over a dimmed page; past the usual while, it says so and offers her settings or
/// another machine, and so does it when she couldn't start. Running: the panel stops short of the composer, and her
/// mark over it closes it; while the keyboard is up her header stays in place, and the conversation
/// fades as it reaches the top instead of reading under the clock.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerPage go test ./e2e -run 'TestIPhone$' -v -timeout 40m
extension HermeshUITests {
    @objc func testHerPage() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        let normal = app.launchArguments
        try XCTContext.runActivity(named: "the largest text: names keep the row, the signing sheet's buttons in sight") { _ in
            app.terminate()
            app.launchArguments = normal + ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityL"]
            app.launch()
            settled()
            try herLargeText()
        }
        try XCTContext.runActivity(named: "starting: the whole page, the panel from the top, then what to do meanwhile") { _ in
            app.terminate()
            app.launchArguments = normal
            app.launch()
            settled()
            try herStartingSlowly()
        }
        try herOnboarding()
        try XCTContext.runActivity(named: "the panel stops short of the composer; her mark closes it") { _ in try herPanelRoom() }
        try XCTContext.runActivity(named: "typing: the header stays in place, the conversation fading at the top") { _ in try herTypingRoom() }
    }

    /// Relaunched, the app has read its machines (Work's banner says Her is off): the menu opened now stays open.
    func settled() {
        XCTAssertTrue(app.descendants(matching: .any)["her-off"].firstMatch.waitForExistence(timeout: 90), "Work, its machines read")
        sleep(1)
    }

    func herLargeText() throws {
        // Machines (one element to VoiceOver, so seen in the picture: the state under the name, which has the row).
        openMachines()
        XCTAssertTrue(app.descendants(matching: .any)["machine-owner-mac"].firstMatch.waitForExistence(timeout: 20), "the machine's row")
        herShot("Large text: Machines")
        // Where Her runs: Turn On under the machine.
        openMenu()
        tap("her-place")
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 10))
        let turnOn = app.buttons["her-turn-on-owner-mac"]
        let machine = app.staticTexts["owner-mac"]
        XCTAssertTrue(turnOn.waitForExistence(timeout: 30) && machine.exists)
        XCTAssertGreaterThanOrEqual(turnOn.frame.minY, machine.frame.maxY - 1, "Turn On under the machine: \(turnOn.frame), \(machine.frame)")
        herShot("Large text: where Her runs")
        reach(turnOn)
        press(turnOn)
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        let approve = app.buttons["sign-approve"]
        let deny = app.buttons["拒绝"]
        XCTAssertTrue(approve.waitForExistence(timeout: 10) && deny.exists)
        XCTAssertTrue(approve.isHittable && deny.isHittable, "both in sight, unscrolled")
        XCTAssertGreaterThanOrEqual(deny.frame.minY, approve.frame.maxY - 1, "Approve over Deny: \(approve.frame), \(deny.frame)")
        herShot("Large text: the signing sheet")
        // Not yet: she is turned on at the usual size (herStartingSlowly).
        deny.tap()
        let alert = app.alerts.firstMatch
        if alert.waitForExistence(timeout: 5) { alert.buttons.firstMatch.tap() }
        XCTAssertTrue(turnOn.waitForExistence(timeout: 10), "still where Her runs")
    }

    func herStartingSlowly() throws {
        // Turned on where she waits to start: the signing sheet's Approve in sight, unscrolled.
        _ = try control("her-slow", method: "POST")
        openMenu()
        tap("her-place")
        tap("her-turn-on-owner-mac", timeout: 30)
        XCTAssertTrue(text("在环境「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        let approve = app.buttons["sign-approve"]
        XCTAssertTrue(approve.waitForExistence(timeout: 10) && approve.isHittable, "Approve in sight, unscrolled")
        herShot("Her: turning her on")
        approve.tap()
        XCTAssertTrue(text("Her 正在 owner-mac 上启动").waitForExistence(timeout: 20), "her starting")
        herShot("Her: starting")
        let screen = app.windows.firstMatch.frame
        // At the top, as on her page once she runs: not down in the middle with what is shown while she starts.
        for id in ["her-avatar", "menu"] {
            let e = app.buttons[id]
            XCTAssertTrue(e.exists, id)
            XCTAssertLessThan(e.frame.minY, screen.height * 0.2, "\(id) at the top: \(e.frame) in \(screen)")
        }
        tap("her-tag")
        let panel = app.descendants(matching: .any)["team-panel"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10), "the team panel")
        sleep(1) // grown out of the tag
        herShot("Her: starting, the team panel")
        XCTAssertLessThan(panel.frame.minY, screen.height * 0.2, "the panel from the top: \(panel.frame)")
        // The dimmed page under it, all of the screen: a tap under the panel closes it.
        let dim = app.descendants(matching: .any)["team-close"].firstMatch
        XCTAssertGreaterThan(dim.frame.maxY, screen.height * 0.9, "the dim to the foot: \(dim.frame)")
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: screen.midX, dy: panel.frame.maxY + 40)).tap()
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "closed by a tap under it")
        // Past the usual while: it says so, and where to go meanwhile.
        XCTAssertTrue(app.descendants(matching: .any)["her-starting-long"].firstMatch.waitForExistence(timeout: 20), "taking longer than usual")
        XCTAssertTrue(app.buttons["her-status-settings"].isHittable && app.buttons["her-status-another"].isHittable, "her settings, another machine")
        herShot("Her: starting longer than usual")
        // hermeshd gives up waiting for her (30 s): she couldn't start, and the same ways out show.
        XCTAssertTrue(text("Her 没能在 owner-mac 上启动").waitForExistence(timeout: 40), "her failing to start")
        XCTAssertTrue(app.buttons["her-status-settings"].waitForExistence(timeout: 5) && app.buttons["her-status-another"].exists, "the ways out")
        herShot("Her: couldn't start")
        // The other machine's sheet opens, and closes.
        tap("her-status-another")
        let cancel = app.buttons["取消"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 10), "another machine's sheet")
        cancel.tap()
        // Let her start: hermeshd tries again, and she runs.
        _ = try control("her-slow", method: "DELETE")
    }

    func herPanelRoom() throws {
        // Her's page with a model: the panel ends above the composer's row, the plus and Send still under the dim.
        let attach = app.descendants(matching: .any)["her-attach"].firstMatch
        XCTAssertTrue(attach.waitForExistence(timeout: 30), "the composer")
        tap("her-tag")
        let panel = app.descendants(matching: .any)["team-panel"].firstMatch
        XCTAssertTrue(panel.waitForExistence(timeout: 10))
        sleep(1)
        XCTAssertLessThanOrEqual(panel.frame.maxY, attach.frame.minY + 1, "short of the composer: \(panel.frame), \(attach.frame)")
        herShot("Her: the team panel, short of the composer")
        // Her mark, over it, closes it; her page stays shut.
        tap("her-avatar")
        XCTAssertTrue(panel.waitForNonExistence(timeout: 5), "her mark closes the panel")
        XCTAssertFalse(app.buttons["her-tab-work"].exists, "not her page")
    }

    func herTypingRoom() throws {
        let avatar = app.buttons["her-avatar"]
        let tag = app.buttons["her-tag"]
        XCTAssertGreaterThan(avatar.frame.height, 60, "her mark large: \(avatar.frame)")
        let avatarFrame = avatar.frame
        let tagFrame = tag.frame
        // The owner's long message, then replies long enough to scroll it away.
        let mine = "我的这条消息很长，蓝色的气泡要占好几行，滚到顶上的时候正好在时钟下面，看它会不会和时钟叠在一起读不清。"
        herSend("[[mock:send:收到]] " + mine)
        XCTAssertTrue(app.staticTexts["收到"].waitForExistence(timeout: 60), "her reply")
        for i in 1...3 { herSend("[[mock:send:第 \(i) 段回复，写得长一些，好让对话长过一屏，往上滚的时候能看到顶部怎么处理。第 \(i) 段回复，写得长一些，再长一些。]] 第 \(i) 条") }
        XCTAssertTrue(text("第 3 段回复").waitForExistence(timeout: 60), "the last reply")
        XCTAssertTrue(waitUntil { self.app.keyboards.count == 0 }, "sent: the keyboard goes")
        // Typing does not resize or move the header.
        app.descendants(matching: .any)["her-composer"].firstMatch.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard up")
        XCTAssertEqual(avatar.frame.height, avatarFrame.height, accuracy: 1)
        XCTAssertEqual(avatar.frame.midY, avatarFrame.midY, accuracy: 1)
        XCTAssertEqual(tag.frame.midY, tagFrame.midY, accuracy: 1)
        let menu = app.buttons["menu"]
        XCTAssertTrue(tag.isHittable && tag.label.hasPrefix("Her"), tag.label)
        herShot("Her: typing")
        // The owner's message scrolled up under the clock: gone there, not read over it.
        let bubble = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "我的这条消息很长")).firstMatch
        XCTAssertTrue(bubble.exists)
        // Scrolled in small steps, held still at the end of each, until the message is under the clock.
        let middle = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: app.windows.firstMatch.frame.midX, dy: 250))
        for _ in 0..<12 where abs(bubble.frame.midY - 30) > 15 {
            let by = max(-120, min(120, 30 - bubble.frame.midY))
            middle.press(forDuration: 0.05, thenDragTo: middle.withOffset(CGVector(dx: 0, dy: by)), withVelocity: 200, thenHoldForDuration: 0.5)
            sleep(1)
        }
        // The status bar and the row of buttons, down to their middle (the conversation fades out below that, at the
        // bar's edge), as far as the message reaches.
        let under = CGRect(x: max(bubble.frame.minX, 0), y: 2, width: bubble.frame.width, height: min(menu.frame.midY, bubble.frame.maxY - 1) - 2)
        XCTAssertTrue(bubble.frame.minY < under.minY && under.maxY > menu.frame.minY, "the message under the clock and the buttons: \(bubble.frame), \(under)")
        let shot = XCUIScreen.main.screenshot()
        let blue = blueShare(shot.image, in: under)
        herShot("Her: the conversation under the clock and the buttons, \(Int(blue * 100))% blue")
        XCTAssertLessThan(blue, 0.05, "the bubble gone under the clock and the buttons: \(Int(blue * 100))% of it reads blue there")
        // Dismissing the keyboard also leaves the header in place.
        middle.press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.98)), withVelocity: 600, thenHoldForDuration: 0.2)
        XCTAssertTrue(waitUntil { self.app.keyboards.count == 0 }, "the keyboard down")
        XCTAssertEqual(avatar.frame.height, avatarFrame.height, accuracy: 1)
        XCTAssertGreaterThan(tag.frame.midY, avatar.frame.midY, "the tag under her mark")
    }

    /// The share of the points in rect (in points) whose colour is the owner's bubble's blue, however faint.
    func blueShare(_ image: UIImage, in rect: CGRect) -> Double {
        guard let cg = image.cgImage else { return 1 }
        let scale = CGFloat(cg.width) / app.windows.firstMatch.frame.width
        let px = rect.applying(CGAffineTransform(scaleX: scale, y: scale)).integral.intersection(CGRect(x: 0, y: 0, width: cg.width, height: cg.height))
        guard let crop = cg.cropping(to: px) else { return 1 }
        let w = crop.width, h = crop.height
        var data = [UInt8](repeating: 0, count: w * h * 4)
        let ctx = CGContext(data: &data, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4, space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.draw(crop, in: CGRect(x: 0, y: 0, width: w, height: h))
        var blue = 0
        for i in stride(from: 0, to: data.count, by: 4) {
            let (r, g, b) = (Int(data[i]), Int(data[i + 1]), Int(data[i + 2]))
            if b > r + 40 && b > g + 25 { blue += 1 } // the bubble's blue, or a tint of it on the page
        }
        return Double(blue) / Double(max(w * h, 1))
    }
}
