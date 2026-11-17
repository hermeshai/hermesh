import XCTest

/// Turning Her on where the Mac's Hermesh is from before Her (docs/version-skew.md §6.7): TestIPhone's before-her
/// restarts the Mac's hermeshd as one from 0.1.4, whose status has no Her. Where Her runs shows the Mac, saying its
/// Hermesh is older and needs an update there, Turn On disabled, never "unknown admin command". Updated (started again
/// as it is), Turn On is enabled by itself, and Her goes on there with Face ID.
///
///     HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerOnAMacFromBeforeHer go test ./e2e -run 'TestIPhone$' -v -timeout 40m
extension HermeshUITests {
    @objc func testHerOnAMacFromBeforeHer() throws {
        guard ProcessInfo.processInfo.environment["HERMESH_E2E_HER"] != nil else { throw XCTSkip("run with Her built") }
        try join()
        _ = try control("before-her", method: "POST")
        openMenu()
        tap("her-place")
        XCTAssertTrue(text("选择 Her 在哪里运行").waitForExistence(timeout: 10))
        let turnOn = app.buttons["her-turn-on-owner-mac"]
        XCTAssertTrue(text("这台电脑上的 Hermesh 版本较旧，在那里更新后才能运行 Her").waitForExistence(timeout: 60), "the Mac needs an update")
        XCTAssertTrue(turnOn.exists && !turnOn.isEnabled, "Turn On disabled")
        herShot("Her: a Mac from before Her")

        _ = try control("before-her", method: "DELETE")
        try waitFor("Turn On enabled once the Mac is updated", timeout: 90) { turnOn.isEnabled ? true : nil }
        XCTAssertFalse(text("这台电脑上的 Hermesh 版本较旧，在那里更新后才能运行 Her").exists)
        turnOn.tap()
        XCTAssertTrue(text("在机器「owner-mac」上开启 Her").waitForExistence(timeout: 30), "the signing sheet")
        tap("sign-approve")
        XCTAssertTrue(text("开始之前，我需要接上一个模型").waitForExistence(timeout: 180), "Her on, greeting")
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'unknown admin command'")).firstMatch.exists)
    }
}
