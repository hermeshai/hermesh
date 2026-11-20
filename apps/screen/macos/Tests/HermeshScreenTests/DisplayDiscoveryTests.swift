import XCTest
@testable import hermesh_screen

final class DisplayDiscoveryTests: XCTestCase {
    func testDisplayAppearsAfterWake() async throws {
        var calls = 0
        let display = try await Capture.findDisplay {
            calls += 1
            return calls == 1 ? nil : 42
        }
        XCTAssertEqual(display, 42)
        XCTAssertEqual(calls, 2)
    }

    func testNoDisplayRemainsAnError() async {
        var calls = 0
        do {
            let _: Int = try await Capture.findDisplay {
                calls += 1
                return nil
            }
            XCTFail("expected the missing display error")
        } catch {
            XCTAssertEqual(error.localizedDescription, "no display")
            XCTAssertEqual(calls, 6)
        }
    }

    func testDiscoveryErrorIsPropagatedImmediately() async {
        let failure = NSError(domain: "test-screen-discovery", code: 1)
        var calls = 0
        do {
            let _: Int = try await Capture.findDisplay {
                calls += 1
                throw failure
            }
            XCTFail("expected the discovery error")
        } catch {
            XCTAssertEqual(error as NSError, failure)
            XCTAssertEqual(calls, 1)
        }
    }
}
