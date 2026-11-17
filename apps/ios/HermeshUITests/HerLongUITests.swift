import XCTest

/// A long conversation with Her, read from its end to its first message, as on the Mac (her-long.spec.ts): the owner
/// Mac's Her has had HERMESH_E2E_HER_LONG messages in English, short and long, a paragraph or many, with headings,
/// lists, code and tables (TestIPhoneHerLong, longChatWith). The iPhone opens on the last page of them; the owner drags
/// the conversation down, a long way at a time, and the earlier pages come in before the top shows. What is in sight
/// moves with the finger each time, as far as the first time, and never to another page: earlier messages put above
/// it leave it where it is, until the first message.
///
///     HERMESH_IPHONE_E2E=1 go test ./e2e -run TestIPhoneHerLong -v -timeout 40m
extension HermeshUITests {
    @objc func testHerLong() throws {
        guard let total = ProcessInfo.processInfo.environment["HERMESH_E2E_HER_LONG"].flatMap(Int.init) else {
            throw XCTSkip("run by TestIPhoneHerLong")
        }
        try join(herRuns: true)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Reply \(total):")).firstMatch.waitForExistence(timeout: 60),
                      "the conversation, open at its end")
        sleep(2)
        let window = app.windows.firstMatch
        var steps = 0, pages = 0, moves: [String] = []
        var stride: CGFloat? // how far the first drag moved what was in sight
        var before = try herLongSight()
        while !(before.first == "M1" && !app.descendants(matching: .any)["her-earlier"].exists) {
            steps += 1
            XCTAssertLessThan(steps, 400, "the first message is reached")
            // A drag held still before the finger lifts: the conversation moves with it, and does not glide on.
            let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
            from.press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.9)),
                       withVelocity: 800, thenHoldForDuration: 0.4)
            let after = try herLongSight()
            if after.count > before.count { pages += 1 }
            // At the first message, the drag pulls the conversation's top down and it springs back.
            let top = after.first == "M1" && !app.descendants(matching: .any)["her-earlier"].exists
            // The message that was first in sight, wherever it is now: as far down as a drag takes it.
            if let anchor = before.first, let y = before.y {
                let moved = try XCTUnwrap(after.positions[anchor], "\(anchor) is still in the conversation") - y
                moves.append("\(anchor) moved \(Int(moved)), \(after.count) messages")
                if stride == nil {
                    stride = moved
                } else if let stride, !top {
                    XCTAssertLessThan(abs(moved - stride), stride * 0.4, "step \(steps): \(anchor) moved \(Int(moved)) for a drag that moves \(Int(stride))")
                }
            }
            before = after
        }
        XCTContext.runActivity(named: "moves") { a in a.add(XCTAttachment(string: moves.joined(separator: "\n"))) }
        XCTAssertGreaterThanOrEqual(pages, 2, "earlier pages were read on the way")
    }

    /// The conversation's messages by "M<n>" / "R<n>": where each one's first line is, the one to follow (the first in
    /// sight, under the top bar and over the composer, else the nearest the middle) and where, and how many there are.
    private func herLongSight() throws -> (positions: [String: CGFloat], first: String?, y: CGFloat?, count: Int) {
        let snap = try app.snapshot()
        let frame = app.windows.firstMatch.frame
        var all: [(String, CGFloat)] = []
        func walk(_ s: XCUIElementSnapshot) {
            if s.elementType == .staticText, let m = s.label.firstMatch(of: /^(Msg|Reply) (\d+):/) {
                all.append(("\(m.1.first!)\(m.2)", s.frame.minY))
            }
            s.children.forEach(walk)
        }
        walk(snap)
        // The first message in sight, else (a long one fills the screen) the one nearest its middle.
        let sight = all.filter { $0.1 > frame.minY + 100 && $0.1 < frame.maxY - 160 }.min { $0.1 < $1.1 } ?? all.min { abs($0.1 - frame.midY) < abs($1.1 - frame.midY) }
        return (Dictionary(all, uniquingKeysWith: { a, _ in a }), sight?.0, sight?.1, all.count)
    }
}
