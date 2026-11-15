import HermeshSessions
import XCTest

final class LayoutTests: XCTestCase {
    private func layout(_ width: Double, expanded: Bool = true, list: Bool = false) -> WideLayout {
        wideLayout(width: width, menu: 322, rail: 74, expanded: expanded, list: list)
    }

    func testTheMenuIsTheOwnersChoiceWhileItFits() {
        XCTAssertEqual(layout(956), WideLayout(rail: false, list: false, menuFits: true))
        XCTAssertEqual(layout(956, expanded: false), WideLayout(rail: true, list: false, menuFits: true))
        XCTAssertEqual(layout(1400, list: true), WideLayout(rail: false, list: true, menuFits: true))
    }

    func testTheMenuGivesWayToItsRailFirst() {
        // 322 + 320 + 400 = 1042: under it the menu is its rail, and the list still stands beside the session.
        XCTAssertEqual(layout(1042, list: true), WideLayout(rail: false, list: true, menuFits: true))
        XCTAssertEqual(layout(1041, list: true), WideLayout(rail: true, list: true, menuFits: false))
        XCTAssertEqual(layout(956, list: true), WideLayout(rail: true, list: true, menuFits: false))
        // Without a list, under 322 + 400.
        XCTAssertEqual(layout(721), WideLayout(rail: true, list: false, menuFits: false))
    }

    func testThenTheListBesideASession() {
        // 74 + 320 + 400 = 794.
        XCTAssertEqual(layout(794, list: true), WideLayout(rail: true, list: true, menuFits: false))
        XCTAssertEqual(layout(793, list: true), WideLayout(rail: true, list: false, menuFits: false))
    }
}
