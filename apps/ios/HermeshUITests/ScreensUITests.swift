import XCTest

/// The website download page's iPhone screenshots: the app in the demo
/// world of e2e TestScreens, which serves HERMESH_E2E_CONTROL and names the
/// language (HERMESH_SCREENS_LANG) and the session to show
/// (HERMESH_SCREENS_SESSION). The screenshots are kept in the result
/// bundle, named; it is no test of the app.
final class ScreensUITests: XCTestCase {
    let app = XCUIApplication()

    func testScreens() throws {
        let env = ProcessInfo.processInfo.environment
        guard let lang = env["HERMESH_SCREENS_LANG"], let control = env["HERMESH_E2E_CONTROL"] else {
            throw XCTSkip("run by TestScreens")
        }
        continueAfterFailure = false
        let zh = lang == "zh"
        app.launchArguments = ["-HermeshTestApprove", "-AppleLanguages", zh ? "(zh-Hans)" : "(en)", "-AppleLocale", zh ? "zh_CN" : "en_US"]
        app.launch()

        // Joins the demo world by its connection address; its Mac approves.
        let address = try get(control, "address")["address"] as! String
        tap("own-server")
        let field = app.textFields["connection-address"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText(address)
        tap("continue")
        tap("send-join", timeout: 30)
        let digits = app.textFields["digits"]
        let later = app.buttons["notifications-later"]
        let deadline = Date().addingTimeInterval(60)
        while !digits.exists && !later.exists && Date() < deadline { sleep(1) }
        if digits.exists {
            let d = try wait("the Mac's digits") { (try? self.get(control, "digits")["digits"] as? String).flatMap { $0?.isEmpty == false ? $0 : nil } }
            digits.tap()
            digits.typeText(d)
        }
        XCTAssertTrue(later.waitForExistence(timeout: 30), "joined")
        later.tap()
        XCTAssertTrue(app.buttons["menu"].waitForExistence(timeout: 30), "the overview")
        sleep(6) // the machines' counts and the agents' days come in
        shot("iphone-overview")

        // A finished session, from the menu.
        tap("menu")
        sleep(1)
        shot("iphone-menu")
        tap("drawer-session-\(env["HERMESH_SCREENS_SESSION"] ?? "")")
        sleep(3)
        shot("iphone-session")

        // An agent asking to connect, as the overview shows it.
        while app.navigationBars.buttons["BackButton"].exists { app.navigationBars.buttons["BackButton"].tap() }
        if !app.buttons["menu"].exists { app.navigationBars.buttons.element(boundBy: 0).tap() }
        tap("menu")
        sleep(1)
        app.buttons[zh ? "概览" : "Overview"].tap()
        _ = try post(control, "agent")
        let verify = app.buttons["verify-pairing"]
        XCTAssertTrue(verify.waitForExistence(timeout: 30), "the request on the overview")
        verify.tap()
        sleep(2)
        shot("iphone-request")
    }

    func shot(_ name: String) {
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }

    func tap(_ id: String, timeout: TimeInterval = 15) {
        let e = app.descendants(matching: .any)[id]
        XCTAssertTrue(e.waitForExistence(timeout: timeout), id)
        e.tap()
    }

    func get(_ base: String, _ path: String) throws -> [String: Any] { try call(base, path, "GET") }
    func post(_ base: String, _ path: String) throws -> [String: Any] { try call(base, path, "POST") }

    func call(_ base: String, _ path: String, _ method: String) throws -> [String: Any] {
        var req = URLRequest(url: URL(string: "\(base)/\(path)")!)
        req.httpMethod = method
        req.timeoutInterval = 60
        var out: [String: Any] = [:]
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: req) { data, _, _ in
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] { out = obj }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 70)
        return out
    }

    func wait<T>(_ what: String, timeout: TimeInterval = 60, _ check: () throws -> T?) throws -> T {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if let v = try check() { return v }
            sleep(1)
        }
        XCTFail("timed out: \(what)")
        throw XCTSkip(what)
    }
}
