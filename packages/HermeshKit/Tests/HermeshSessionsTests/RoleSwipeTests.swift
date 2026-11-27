import HermeshSessions
import XCTest

final class RoleSwipeTests: XCTestCase {
    func testDragProgressTracksOneToOneInsideRange() {
        XCTAssertEqual(RoleSwipeMath.dragProgress(start: 0, translationX: 0), 0, accuracy: 1e-9)
        XCTAssertEqual(RoleSwipeMath.dragProgress(start: 0, translationX: -44), 0.5, accuracy: 1e-9)
        XCTAssertEqual(RoleSwipeMath.dragProgress(start: 0, translationX: -88), 1.0, accuracy: 1e-9)
        XCTAssertEqual(RoleSwipeMath.dragProgress(start: 1, translationX: 44), 0.5, accuracy: 1e-9)
        XCTAssertEqual(RoleSwipeMath.dragProgress(start: 1, translationX: 88), 0.0, accuracy: 1e-9)
    }

    func testRubberBandResistsAndBoundsOverscroll() {
        let mildRight = RoleSwipeMath.dragProgress(start: 0, translationX: 44)
        XCTAssertLessThan(mildRight, 0)
        XCTAssertGreaterThan(mildRight, -RoleSwipeMath.overscrollLimit)

        let hardLeft = RoleSwipeMath.dragProgress(start: 1, translationX: -500)
        XCTAssertGreaterThan(hardLeft, 1)
        XCTAssertLessThan(hardLeft, 1 + RoleSwipeMath.overscrollLimit)

        // Monotonicity past upper bound
        let mediumLeft = RoleSwipeMath.dragProgress(start: 1, translationX: -88)
        XCTAssertLessThan(mediumLeft, hardLeft)
    }

    func testLandingUsesDistanceAndFlickVelocity() {
        // Slow drag just short of halfway stays on Agent; past halfway lands on Her.
        XCTAssertFalse(RoleSwipeMath.landsOnHer(start: 0, translationX: -40, velocityX: 0))
        XCTAssertTrue(RoleSwipeMath.landsOnHer(start: 0, translationX: -48, velocityX: 0))

        // Quick light flick left from Agent commits to Her even before halfway.
        XCTAssertTrue(RoleSwipeMath.landsOnHer(start: 0, translationX: -20, velocityX: -260))

        // Dragged past halfway then flicked back right cancels to Agent.
        XCTAssertFalse(RoleSwipeMath.landsOnHer(start: 0, translationX: -56, velocityX: 280))

        // Quick flick right from Her returns to Agent.
        XCTAssertFalse(RoleSwipeMath.landsOnHer(start: 1, translationX: 18, velocityX: 260))
        XCTAssertTrue(RoleSwipeMath.landsOnHer(start: 1, translationX: 30, velocityX: 0))
    }

    func testInitialSpringVelocityClampsAndIgnoresTinyRemainder() {
        XCTAssertEqual(RoleSwipeMath.initialSpringVelocity(current: 0.98, target: 1, velocityX: -500), 0)

        let forward = RoleSwipeMath.initialSpringVelocity(current: 0.5, target: 1, velocityX: -220)
        XCTAssertEqual(forward, 5.0, accuracy: 1e-9)

        let clampedHigh = RoleSwipeMath.initialSpringVelocity(current: 0.5, target: 1, velocityX: -2000)
        XCTAssertEqual(clampedHigh, 6.0, accuracy: 1e-9)

        let clampedLow = RoleSwipeMath.initialSpringVelocity(current: 1.2, target: 1, velocityX: -2000)
        XCTAssertEqual(clampedLow, -4.0, accuracy: 1e-9)
    }

    func testHapticDetentHasDeadbandAroundMidpoint() {
        // Starting on Agent: does not flip at 0.50 or 0.51, flips at 0.52.
        XCTAssertFalse(RoleSwipeMath.hapticDetent(progress: 0.50, currentIsHer: false))
        XCTAssertFalse(RoleSwipeMath.hapticDetent(progress: 0.51, currentIsHer: false))
        XCTAssertTrue(RoleSwipeMath.hapticDetent(progress: 0.52, currentIsHer: false))

        // Starting on Her: stays Her at 0.50 and 0.49, flips to Agent at 0.48.
        XCTAssertTrue(RoleSwipeMath.hapticDetent(progress: 0.50, currentIsHer: true))
        XCTAssertTrue(RoleSwipeMath.hapticDetent(progress: 0.49, currentIsHer: true))
        XCTAssertFalse(RoleSwipeMath.hapticDetent(progress: 0.48, currentIsHer: true))
    }
}
