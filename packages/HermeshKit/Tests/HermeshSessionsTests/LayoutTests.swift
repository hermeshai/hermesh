import HermeshSessions
import XCTest

final class LayoutTests: XCTestCase {
    private func layout(_ width: Double, expanded: Bool = true) -> WideLayout {
        wideLayout(width: width, menu: 322, expanded: expanded)
    }

    func testTheMenuIsTheOwnersChoiceWhileItFits() {
        XCTAssertEqual(layout(956), WideLayout(rail: false, menuFits: true))
        XCTAssertEqual(layout(956, expanded: false), WideLayout(rail: true, menuFits: true))
    }

    func testTheMenuGivesWayToItsRail() {
        // 322 + 400 = 722: under it the menu is its rail, expanded only over the page when asked.
        XCTAssertEqual(layout(722), WideLayout(rail: false, menuFits: true))
        XCTAssertEqual(layout(721), WideLayout(rail: true, menuFits: false))
    }
}
