import CoreGraphics
import Foundation
import HermeshProto

// The overview (docs/overview-usage.md §4): what needs the owner, as a deck;
// the link map of who may use which environment and what goes between them
// now; under it what runs and what finished. All of it is made here from
// what the device already holds, so the page asks for nothing.

/// A thing that needs the owner, by what it is about.
public enum Need: Hashable, Identifiable, Sendable {
    /// A session waits for an answer.
    case question(String)
    /// A request to connect, by its code.
    case pairing(String)
    /// A session's last turn failed.
    case failed(String)
    /// A grant about to end, by its id.
    case expiring(String)
    /// The guide put off has a step left.
    case guide
    /// Her runs nowhere yet.
    case herOff
    case notificationsOff
    /// This phone is the only device that can approve.
    case secondDevice

    public var id: String {
        switch self {
        case .question(let id): "q:" + id
        case .pairing(let code): "p:" + code
        case .failed(let id): "f:" + id
        case .expiring(let id): "g:" + id
        case .guide: "guide"
        case .herOff: "her"
        case .notificationsOff: "notifications"
        case .secondDevice: "device"
        }
    }
}

/// What needs the owner, the most pressing first: someone or something that
/// waits (questions, the latest first; then requests to connect), what went
/// wrong, then reminders.
public func needs(team: TeamState, pairings: [String], expiring: [String], guide: Bool, herOff: Bool, notificationsOff: Bool,
                  secondDevice: Bool) -> [Need] {
    team.waiting.map { .question($0.sessionID) } + pairings.map(Need.pairing) + team.failed.map { .failed($0.sessionID) }
        + expiring.map(Need.expiring) + (guide ? [.guide] : []) + (herOff ? [.herOff] : [])
        + (notificationsOff ? [.notificationsOff] : []) + (secondDevice ? [.secondDevice] : [])
}

/// Which of the needs is in front of the deck: the one that was, while it
/// is still there; else the one that took its place (the next, or the last
/// when it was the last); the first when there was none.
public func deckFront(_ needs: [Need], was id: String?, at index: Int) -> Int {
    if let id, let i = needs.firstIndex(where: { $0.id == id }) { return i }
    return max(0, min(index, needs.count - 1))
}

/// What just came that waits on the owner (a question, a request to
/// connect): it goes in front of the deck, as it is why the phone rang.
public func arrived(_ needs: [Need], before: [String]) -> Need? {
    needs.first { need in
        switch need {
        case .question, .pairing: !before.contains(need.id)
        default: false
        }
    }
}

/// Who may use the owner's environments, the environments, and one line per
/// (sender, environment) pair that has sessions not finished (at work,
/// waiting or failed), weighted by how many there are. Those with a session
/// at work carry moving dots.
public struct LinkMap: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        case sender
        case environment
        /// The ones a side has no room for.
        case more(Int)
    }

    public struct Node: Equatable, Identifiable, Sendable {
        public var id: String
        public var kind: Kind
        public var title: String
        /// An environment's coding agents that can be used.
        public var agents: [String]
        /// An environment that is offline.
        public var offline: Bool

        public init(id: String, kind: Kind, title: String = "", agents: [String] = [], offline: Bool = false) {
            self.id = id
            self.kind = kind
            self.title = title
            self.agents = agents
            self.offline = offline
        }
    }

    public struct Line: Equatable, Identifiable, Sendable {
        public var id: String { LinkMap.line(sender: sender, environment: environment) }
        public var sender: String
        public var environment: String
        /// Unsettled sessions between this sender and environment.
        public var count: Int
        public var atWork: Bool

        public init(sender: String, environment: String, count: Int = 1, atWork: Bool) {
            self.sender = sender
            self.environment = environment
            self.count = count
            self.atWork = atWork
        }

        /// Stroke width: 1.5 pt for a single session, gently stepped up to a 2.8 pt cap.
        public var strokeWidth: CGFloat { LinkMap.strokeWidth(for: count) }
    }

    public static func strokeWidth(for count: Int) -> CGFloat {
        min(2.8, 1.5 + 0.55 * log2(CGFloat(max(count, 1))))
    }

    public var senders: [Node]
    public var environments: [Node]
    public var lines: [Line]

    /// A side shows at most this many; one more is "N more".
    public static let most = 6
    /// The owner as a sender (`Session.created_by` of a session the owner started).
    public static let owner = "owner"

    public static func sender(_ clientID: String) -> String { "s:" + clientID }
    public static func environment(_ machineID: String) -> String { "e:" + machineID }
    public static func line(sender: String, environment: String) -> String { sender + "|" + environment }

    /// Every node a picked node's sessions touch, itself included.
    public func touched(by node: String, sessions: [Session]) -> Set<String> {
        var out: Set<String> = [node]
        for s in sessions where LinkMap.sender(s.createdBy) == node || LinkMap.environment(s.machineID) == node {
            out.insert(LinkMap.sender(s.createdBy))
            out.insert(LinkMap.environment(s.machineID))
        }
        return out
    }
}

/// A grant as the map draws it.
public struct MapSender: Equatable, Sendable {
    public var clientID: String
    public var title: String

    public init(clientID: String, title: String) {
        self.clientID = clientID
        self.title = title
    }
}

/// An environment as the map draws it.
public struct MapEnvironment: Equatable, Sendable {
    public var machineID: String
    public var title: String
    /// This device's own.
    public var local: Bool
    public var online: Bool
    public var agents: [String]

    public init(machineID: String, title: String, local: Bool = false, online: Bool = true, agents: [String] = []) {
        self.machineID = machineID
        self.title = title
        self.local = local
        self.online = online
        self.agents = agents
    }
}

/// The map of the grants in use (not revoked) and the environments: the
/// senders the busiest first, the owner among them while a session of the
/// owner's has not finished; the environments this device's own first, then
/// those online. owner: what the owner is called.
public func linkMap(senders: [MapSender], environments: [MapEnvironment], team: TeamState, owner: String) -> LinkMap {
    let live = team.running + team.waiting + team.failed
    var count: [String: Int] = [:]
    for s in live { count[s.createdBy, default: 0] += 1 }
    var from = senders
    if count[LinkMap.owner] != nil, !from.contains(where: { $0.clientID == LinkMap.owner }) {
        from.append(MapSender(clientID: LinkMap.owner, title: owner))
    }
    // Stable: grants with as many keep the order they came in.
    let ranked = from.enumerated().sorted { a, b in
        let (x, y) = (count[a.element.clientID] ?? 0, count[b.element.clientID] ?? 0)
        return x != y ? x > y : a.offset < b.offset
    }.map(\.element)
    let placed = environments.enumerated().sorted { a, b in
        let (x, y) = (a.element, b.element)
        if x.local != y.local { return x.local }
        if x.online != y.online { return x.online }
        return a.offset < b.offset
    }.map(\.element)

    // Adding a grant or an environment is rare: the map draws no place for one (the menu and New have them).
    func side(_ nodes: [LinkMap.Node], more: String) -> [LinkMap.Node] {
        guard nodes.count > LinkMap.most else { return nodes }
        return Array(nodes.prefix(LinkMap.most - 1)) + [LinkMap.Node(id: more, kind: .more(nodes.count - LinkMap.most + 1))]
    }
    let left = side(ranked.map { LinkMap.Node(id: LinkMap.sender($0.clientID), kind: .sender, title: $0.title) }, more: "s:…")
    let right = side(placed.map { LinkMap.Node(id: LinkMap.environment($0.machineID), kind: .environment, title: $0.title, agents: $0.agents, offline: !$0.online) },
                     more: "e:…")
    let shown = Set((left + right).map(\.id))
    var seen: Set<String> = []
    var byPair: [String: Int] = [:]
    var lines: [LinkMap.Line] = []
    for s in live where seen.insert(s.sessionID).inserted {
        let (a, b) = (LinkMap.sender(s.createdBy), LinkMap.environment(s.machineID))
        guard shown.contains(a), shown.contains(b) else { continue }
        let key = LinkMap.line(sender: a, environment: b)
        if let i = byPair[key] {
            lines[i].count += 1
            if s.isActive { lines[i].atWork = true }
        } else {
            byPair[key] = lines.count
            lines.append(LinkMap.Line(sender: a, environment: b, count: 1, atWork: s.isActive))
        }
    }
    return LinkMap(senders: left, environments: right, lines: lines)
}

/// A cubic curve of the map.
public struct MapCurve: Equatable, Sendable {
    public var from, control1, control2, to: CGPoint
}

/// Where the map's nodes, links and lines are in a given width: the senders
/// down the left, Hermesh in the middle, the environments down the right.
public struct LinkMapLayout: Equatable, Sendable {
    public var size: CGSize
    public var hub: CGPoint
    public var hubRadius: CGFloat
    /// A node's frame, by its id.
    public var frames: [String: CGRect]
    /// A node's plain link to the hub, by its id.
    public var links: [String: MapCurve]
    /// A line's way, by its id: to the hub, and from it (joined by a straight run through the hub).
    public var ways: [String: [MapCurve]]

    public static func height(for map: LinkMap) -> CGFloat {
        let senderGap: CGFloat = map.senders.count > 2 ? 48 : 54, environmentGap: CGFloat = 60
        return max(CGFloat(map.senders.count) * senderGap, CGFloat(map.environments.count) * environmentGap, 132)
    }

    public init(_ map: LinkMap, width: CGFloat) {
        let senderWidth = min(158, (width * 0.325).rounded()), environmentWidth = min(170, (width * 0.34).rounded())
        let senderGap: CGFloat = map.senders.count > 2 ? 48 : 54, environmentGap: CGFloat = 60
        let height = Self.height(for: map)
        let radius: CGFloat = 25
        let center = CGPoint(x: senderWidth + (width - senderWidth - environmentWidth) / 2, y: height / 2 - 10)
        let hubLeft = center.x - radius - 3, hubRight = center.x + radius + 3
        func y(_ i: Int, of n: Int, gap: CGFloat) -> CGFloat { height / 2 + (CGFloat(i) - CGFloat(n - 1) / 2) * gap }
        func curve(_ a: CGPoint, _ b: CGPoint) -> MapCurve {
            let k = (b.x - a.x) * 0.55
            return MapCurve(from: a, control1: CGPoint(x: a.x + k, y: a.y), control2: CGPoint(x: b.x - k, y: b.y), to: b)
        }
        var frames: [String: CGRect] = [:], links: [String: MapCurve] = [:]
        var senderY: [String: CGFloat] = [:], environmentY: [String: CGFloat] = [:]
        for (i, n) in map.senders.enumerated() {
            let cy = y(i, of: map.senders.count, gap: senderGap)
            let h: CGFloat = n.kind == .sender ? 40 : 34
            frames[n.id] = CGRect(x: 0, y: cy - h / 2, width: senderWidth, height: h)
            links[n.id] = curve(CGPoint(x: senderWidth, y: cy), CGPoint(x: hubLeft, y: center.y))
            senderY[n.id] = cy
        }
        for (i, n) in map.environments.enumerated() {
            let cy = y(i, of: map.environments.count, gap: environmentGap)
            let h: CGFloat = n.kind == .environment ? 50 : 34
            frames[n.id] = CGRect(x: width - environmentWidth, y: cy - h / 2, width: environmentWidth, height: h)
            links[n.id] = curve(CGPoint(x: hubRight, y: center.y), CGPoint(x: width - environmentWidth, y: cy))
            environmentY[n.id] = cy
        }
        var ways: [String: [MapCurve]] = [:]
        for line in map.lines {
            guard let a = senderY[line.sender], let b = environmentY[line.environment] else { continue }
            ways[line.id] = [curve(CGPoint(x: senderWidth, y: a), CGPoint(x: hubLeft, y: center.y)),
                             curve(CGPoint(x: hubRight, y: center.y), CGPoint(x: width - environmentWidth, y: b))]
        }
        self.size = CGSize(width: width, height: height)
        self.hub = center
        self.hubRadius = radius
        self.frames = frames
        self.links = links
        self.ways = ways
    }
}

/// The sessions through a node of the map (a sender's, or an environment's):
/// those that need the owner, then those at work, then those that finished,
/// each in the team's order.
public func sessions(through node: String, team: TeamState) -> [Session] {
    (team.waiting + team.failed + team.running + team.finished).filter {
        LinkMap.sender($0.createdBy) == node || LinkMap.environment($0.machineID) == node
    }
}
