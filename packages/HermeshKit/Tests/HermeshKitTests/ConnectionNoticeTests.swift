import Testing
@testable import HermeshKit

/// A page's connection down for `down` seconds, on a machine online, with the relay up.
private func down(_ seconds: Double, _ change: (inout ConnectionFacts) -> Void = { _ in }) -> ConnectionNotice? {
    var f = ConnectionFacts(linkUp: false, linkDownFor: seconds)
    change(&f)
    return connectionNotice(f)
}

@Test func nothingIsSaidWhileThePagesConnectionIsUp() {
    #expect(connectionNotice(ConnectionFacts()) == nil)
    // Her reached directly while the relay reconnects, or the phone between networks: still nothing.
    #expect(connectionNotice(ConnectionFacts(noNetwork: true, relayUp: false, relayDownFor: 60, machineOnline: false)) == nil)
}

@Test func aShortWaitGoesUnsaid() {
    #expect(down(0) == nil)
    #expect(down(0.9) == nil)
    #expect(down(1) == .connecting)
    #expect(down(14) == .connecting)
}

@Test func aMachineOnlineThatDoesntAnswerIsSaidAfterAWhile() {
    #expect(down(15) == .notResponding)
    // hermeshd already says it can't reach it: at once.
    #expect(down(0) { $0.machineUnreachable = true } == .notResponding)
}

@Test func aMachineOfflineIsSaidAtOnce() {
    #expect(down(0) { $0.machineOnline = false } == .offline)
}

@Test func noNetworkComesFirst() {
    #expect(down(0) { $0.noNetwork = true; $0.machineOnline = false } == .noNetwork)
}

@Test func whileTheRelayIsDownTheRelayIsWhatIsSaid() {
    // Down a moment: connecting, whatever was known of the machine before.
    #expect(down(3) { $0.relayUp = false; $0.relayDownFor = 3; $0.machineOnline = false } == .connecting)
    #expect(down(30) { $0.relayUp = false; $0.relayDownFor = 9 } == .connecting)
    #expect(down(30) { $0.relayUp = false; $0.relayDownFor = 10 } == .cantReachHermesh)
}

@Test func thisDevicesOwnMachineNeedsNoRelay() {
    // The Mac's own hermeshd: never offline, never the relay; only connecting while its link opens.
    #expect(down(60) { $0.machineLocal = true; $0.relayUp = false; $0.relayDownFor = 60; $0.machineOnline = false } == .connecting)
    #expect(down(0.5) { $0.machineLocal = true } == nil)
}

@Test func whatShimmers() {
    #expect(ConnectionNotice.connecting.retrying && ConnectionNotice.cantReachHermesh.retrying && ConnectionNotice.notResponding.retrying)
    #expect(!ConnectionNotice.offline.retrying && !ConnectionNotice.noNetwork.retrying)
}
