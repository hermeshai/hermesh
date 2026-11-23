import Foundation

// The one line a page with a box says about its connection (docs/connection-status-demo.html): above the box, about
// the connection that page uses, and only while it is down. The Mac's renderer has the same rules (connection.ts).

/// What the line above a page's box says, while the connection the page uses is down.
public enum ConnectionNotice: Equatable, Sendable {
    /// This device has no network.
    case noNetwork
    /// The page's connection is opening (again), for more than a moment.
    case connecting
    /// The relay has not been reached for a while.
    case cantReachHermesh
    /// The page's machine is offline: what is sent waits for it.
    case offline
    /// The page's machine is online and doesn't answer.
    case notResponding

    /// Something is being tried again meanwhile: the line shimmers.
    public var retrying: Bool { self == .connecting || self == .cantReachHermesh || self == .notResponding }
}

/// What a page knows of its connection, for `connectionNotice`.
public struct ConnectionFacts: Equatable, Sendable {
    /// This device has no network.
    public var noNetwork = false
    public var relayUp = true
    /// How long the relay has been down (0 while it is up).
    public var relayDownFor: TimeInterval = 0
    /// The page's machine is this device: it is reached without the relay.
    public var machineLocal = false
    public var machineOnline = true
    /// The machine is online and hermeshd can't reach it.
    public var machineUnreachable = false
    /// The connection the page uses is up (or the page needs none now).
    public var linkUp = true
    /// How long that connection has been down.
    public var linkDownFor: TimeInterval = 0

    public init(noNetwork: Bool = false, relayUp: Bool = true, relayDownFor: TimeInterval = 0, machineLocal: Bool = false,
                machineOnline: Bool = true, machineUnreachable: Bool = false, linkUp: Bool = true, linkDownFor: TimeInterval = 0) {
        self.noNetwork = noNetwork
        self.relayUp = relayUp
        self.relayDownFor = relayDownFor
        self.machineLocal = machineLocal
        self.machineOnline = machineOnline
        self.machineUnreachable = machineUnreachable
        self.linkUp = linkUp
        self.linkDownFor = linkDownFor
    }
}

/// How long each wait goes unsaid: a short one never shows.
public enum ConnectionWaits {
    public static let connecting: TimeInterval = 1
    public static let cantReachHermesh: TimeInterval = 10
    public static let notResponding: TimeInterval = 15
    /// Once shown, the line stays at least this long, so that it never flashes.
    public static let shownAtLeast: TimeInterval = 0.5
}

/// The line for a page, or nil while its connection is up or has been down only a moment. Nothing is said while the
/// page's own connection is up, whatever else is down (Her reached directly while the relay reconnects).
public func connectionNotice(_ f: ConnectionFacts) -> ConnectionNotice? {
    if f.linkUp { return nil }
    if f.noNetwork { return .noNetwork }
    if !f.machineLocal {
        // While the relay is down, what is known of the machine is from before: the relay is what to say.
        if !f.relayUp {
            if f.relayDownFor >= ConnectionWaits.cantReachHermesh { return .cantReachHermesh }
        } else if !f.machineOnline {
            return .offline
        } else if f.machineUnreachable || f.linkDownFor >= ConnectionWaits.notResponding {
            return .notResponding
        }
    }
    return f.linkDownFor >= ConnectionWaits.connecting ? .connecting : nil
}
