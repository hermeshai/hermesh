import Foundation
import HermeshProto
import SwiftProtobuf

// What the app shows about sessions (the list and its filter, the
// overview), worked out from the sessions alone so it can be checked
// without a screen. A port of @hermesh/core's sessions.ts.

public typealias Session = Hermesh_Session_V1_Session
public typealias SessionEvent = Hermesh_Session_V1_SessionEvent
public typealias SessionStatus = Hermesh_Session_V1_SessionStatus
public typealias Question = Hermesh_Session_V1_Question
public typealias SessionCursor = Hermesh_Session_V1_SessionCursor

/// A timestamp's date; nil for an unset timestamp.
public func dateOf(_ ts: Google_Protobuf_Timestamp, set: Bool = true) -> Date? {
    set && ts.seconds > 0 ? ts.date : nil
}

private let distantPast = Date(timeIntervalSince1970: -62_135_596_800)

public extension Session {
    /// The title, or the project for a session from an older machine.
    var displayTitle: String { title.isEmpty ? projectID : title }

    /// Waiting on the owner, or stopped by a failure.
    var needsAttention: Bool { status == .waitingInput || isFailed }

    /// Crashed, or its last turn failed (a turn stopped on purpose did not fail).
    var isFailed: Bool {
        status == .crashed || (status != .waitingInput && status != .archived && status != .unspecified && progress.hasLastFailure && progress.lastFailure.kind != .interrupted)
    }

    var isActive: Bool { status == .queued || status == .preparing || status == .running }

    /// A turn that the owner can interrupt.
    var isInterruptible: Bool { isActive || status == .waitingInput }

    var isArchivable: Bool { status == .idle || status == .crashed }

    var updatedDate: Date { dateOf(updatedAt, set: hasUpdatedAt) ?? distantPast }

    var createdDate: Date { dateOf(createdAt, set: hasCreatedAt) ?? updatedDate }

    /// The question the agent waits on, if any.
    var pendingQuestion: Question? {
        status == .waitingInput && progress.hasPendingQuestion ? progress.pendingQuestion : nil
    }

    /// The turn in progress, or the last one (turn_count counts finished ones).
    var currentTurn: Int { Int(progress.turnCount) + (isActive || status == .waitingInput ? 1 : 0) }

    /// When the turn in progress started, if the session says.
    var turnStartedDate: Date? { dateOf(progress.turnStartedAt, set: progress.hasTurnStartedAt) }

    /// Whether this is a later state of its session than b: it has more
    /// events, or as many and changed no earlier.
    func isLaterState(than b: Session) -> Bool {
        let ea = progress.lastEventSeq, eb = b.progress.lastEventSeq
        return ea != eb ? ea > eb : updatedDate >= b.updatedDate
    }
}

/// The list with changes applied (a page, or what a watch pushed): each
/// session replaces an earlier state of itself, or joins the list. Deleted
/// sessions leave it.
public func mergeSessions(_ list: [Session], _ changes: [Session], deleted: [String] = []) -> [Session] {
    let list = deleted.isEmpty ? list : list.filter { !deleted.contains($0.sessionID) }
    if changes.isEmpty { return list }
    var out = list
    var at: [String: Int] = [:]
    for (i, s) in out.enumerated() { at[s.sessionID] = i }
    for c in changes {
        if let i = at[c.sessionID] {
            if c.isLaterState(than: out[i]) { out[i] = c }
        } else {
            at[c.sessionID] = out.count
            out.append(c)
        }
    }
    return out
}

/// The sessions held once a watch's first message (a reset) came: merged,
/// so older pages loaded stay, less the ones it shows were deleted
/// meanwhile. A session held of a machine the message says is read now
/// (readNow: neither unreachable nor syncing), as new as the message's first
/// page reaches (not past next), that the message does not list, was
/// deleted while nobody watched: the app was away, or closed.
public func resetSessions(_ held: [Session], _ reset: [Session], next: SessionCursor?, readNow: Set<String>) -> [Session] {
    let listed = Set(reset.map(\.sessionID))
    let kept = held.filter { s in listed.contains(s.sessionID) || !readNow.contains(s.machineID) || isPast(s, next) }
    return mergeSessions(kept, reset)
}

/// The cursor naming s as where a page ends.
public func cursorAt(_ s: Session) -> SessionCursor {
    var c = SessionCursor()
    c.updatedAt = s.updatedAt
    c.sessionID = s.sessionID
    return c
}

/// Whether s comes after the cursor, in the lists' order (newest first, then
/// by id); never without a cursor. As the machines' Past.
public func isPast(_ s: Session, _ c: SessionCursor?) -> Bool {
    guard let c else { return false }
    let a = (s.updatedAt.seconds, s.updatedAt.nanos), b = (c.updatedAt.seconds, c.updatedAt.nanos)
    return a != b ? a < b : s.sessionID < c.sessionID
}

/// The sessions as screens show them: one on a machine that cannot be read
/// now has an unspecified status, which every check treats as neither in
/// progress, waiting nor failed, and which reads "State unknown"
/// (docs/state-sync.md §6). Archived ones stay archived.
public func withKnownState(_ sessions: [Session], unreachable machineIDs: Set<String>) -> [Session] {
    if machineIDs.isEmpty { return sessions }
    return sessions.map { s in
        guard s.status != .archived, machineIDs.contains(s.machineID) else { return s }
        var u = s
        u.status = .unspecified
        return u
    }
}

/// Where older pages go on after the first page was listed again: from the
/// newer of where the loaded pages ended and where the new first page ends.
public func relistedCursor(loaded: SessionCursor?, first: SessionCursor?) -> SessionCursor? {
    func key(_ c: SessionCursor?) -> (Double, String) {
        guard let c else { return (-.infinity, "") }
        return (dateOf(c.updatedAt, set: c.hasUpdatedAt)?.timeIntervalSince1970 ?? 0, c.sessionID)
    }
    let (la, li) = key(loaded), (fa, fi) = key(first)
    return la > fa || (la == fa && li > fi) ? loaded : first
}

/// How the session list groups its sessions.
public enum SessionGrouping: String, CaseIterable, Codable, Sendable {
    case status, date, agent, machine
}

/// How the session list is laid out: its grouping, which way it sorts by
/// last activity, and whether rows show their metadata line (grant, coding
/// agent, project and time). The same as the Mac's.
public struct SessionListLayout: Equatable, Codable, Sendable {
    public var group: SessionGrouping = .status
    public var newestFirst = true
    public var metadata = true

    public init(group: SessionGrouping = .status, newestFirst: Bool = true, metadata: Bool = true) {
        self.group = group
        self.newestFirst = newestFirst
        self.metadata = metadata
    }
}

/// A state or a day: what a section is called when the list groups by status or date.
public enum SessionBucket: String, CaseIterable, Sendable {
    case attention, active, done, today, yesterday, week, earlier, archived

    public var title: String {
        switch self {
        case .attention: String(localized: "Needs You", bundle: .module)
        case .active: String(localized: "In Progress", bundle: .module)
        case .done: String(localized: "Done", bundle: .module)
        case .today: String(localized: "Today", bundle: .module)
        case .yesterday: String(localized: "Yesterday", bundle: .module)
        case .week: String(localized: "Earlier This Week", bundle: .module)
        case .earlier: String(localized: "Earlier", bundle: .module)
        case .archived: String(localized: "Archived", bundle: .module)
        }
    }
}

/// Needs the owner, runs, or is done.
public func statusOf(_ s: Session) -> SessionBucket {
    s.needsAttention ? .attention : s.isActive ? .active : .done
}

/// The day it was last active; "week" is the six days before today, not the calendar week.
public func dayOf(_ s: Session, now: Date, calendar: Calendar = .current) -> SessionBucket {
    let d = s.updatedDate
    if calendar.isDate(d, inSameDayAs: now) { return .today }
    if isYesterday(d, now: now, calendar: calendar) { return .yesterday }
    if withinWeek(d, now: now, calendar: calendar) { return .week }
    return .earlier
}

/// A filter by grant (its client id), project, coding agent or machine
/// (empty matches all), and whether archived sessions show: for search, and
/// the sessions of one grant or machine.
public struct SessionFilter: Equatable, Sendable {
    public var grant = ""
    public var project = ""
    public var agent = ""
    public var machine = ""
    public var archived = false

    public init(grant: String = "", project: String = "", agent: String = "", machine: String = "", archived: Bool = false) {
        self.grant = grant
        self.project = project
        self.agent = agent
        self.machine = machine
        self.archived = archived
    }

    public var narrowed: Bool { !grant.isEmpty || !project.isEmpty || !agent.isEmpty || !machine.isEmpty }
    public var any: Bool { narrowed || archived }

    public func matches(_ s: Session) -> Bool {
        (grant.isEmpty || s.createdBy == grant) && (project.isEmpty || s.projectID == project)
            && (agent.isEmpty || s.agent == agent) && (machine.isEmpty || s.machineID == machine)
    }
}

public struct SessionSection: Identifiable {
    /// The grouping, or nil for the archived sessions' own section.
    public let grouping: SessionGrouping?
    /// A state or a day (a SessionBucket's raw value), a coding agent, or a machine id.
    public let key: String
    public let sessions: [Session]
    public var id: String { "\(grouping?.rawValue ?? "archived"):\(key)" }
    /// What needs the owner and what runs show how many they hold.
    public var counted: Bool { grouping == .status && (key == SessionBucket.attention.rawValue || key == SessionBucket.active.rawValue) }
}

/// The list as the view lays it out, each section sorted by last activity.
/// States keep their order (needs you, running, done); days follow the sort;
/// coding agents go by name, machines in the order given (unknown ones
/// last). Archived sessions, when shown, are a section of their own at the end.
public func sessionSections(_ sessions: [Session], view: SessionListLayout, archived: Bool, now: Date, machines: [String] = [],
                            calendar: Calendar = .current) -> [SessionSection] {
    var by: [String: [Session]] = [:]
    var gone: [Session] = []
    for s in sessions {
        if s.status == .archived {
            if archived { gone.append(s) }
            continue
        }
        let k = switch view.group {
        case .status: statusOf(s).rawValue
        case .date: dayOf(s, now: now, calendar: calendar).rawValue
        case .agent: s.agent
        case .machine: s.machineID
        }
        by[k, default: []].append(s)
    }
    let days: [SessionBucket] = [.today, .yesterday, .week, .earlier]
    let order: [String] = switch view.group {
    case .status: [SessionBucket.attention, .active, .done].map(\.rawValue)
    case .date: (view.newestFirst ? days : days.reversed()).map(\.rawValue)
    case .agent: by.keys.sorted { agentTitle($0).localizedStandardCompare(agentTitle($1)) == .orderedAscending }
    case .machine: by.keys.sorted { (machines.firstIndex(of: $0) ?? .max, $0) < (machines.firstIndex(of: $1) ?? .max, $1) }
    }
    func sorted(_ list: [Session]) -> [Session] {
        list.sorted { view.newestFirst ? $0.updatedDate > $1.updatedDate : $0.updatedDate < $1.updatedDate }
    }
    var out = order.compactMap { k in by[k].map { SessionSection(grouping: view.group, key: k, sessions: sorted($0)) } }
    if !gone.isEmpty { out.append(SessionSection(grouping: nil, key: SessionBucket.archived.rawValue, sessions: sorted(gone))) }
    return out
}

/// Sessions matching a search, those needing the owner first, then running
/// ones, then by when they last changed. text gives each session's
/// searchable words (title, project, grant, agent, machine).
public func searchSessions(_ sessions: [Session], query: String, filter: SessionFilter, text: (Session) -> String) -> [Session] {
    let q = query.trimmingCharacters(in: .whitespaces).lowercased()
    func rank(_ s: Session) -> Int { s.needsAttention ? 0 : s.isActive ? 1 : 2 }
    return sessions
        .filter { (filter.archived || $0.status != .archived) && filter.matches($0) && (q.isEmpty || text($0).lowercased().contains(q)) }
        .sorted { rank($0) != rank($1) ? rank($0) < rank($1) : $0.updatedDate > $1.updatedDate }
}

/// The range the overview counts work over (docs/work-stats.md §7): today
/// by hour, or the last 7 or 30 days, today included, by day.
public enum WorkPeriod: String, CaseIterable, Sendable {
    case today, week, month

    public var days: Int {
        switch self {
        case .today: 1
        case .week: 7
        case .month: 30
        }
    }

    public var hourly: Bool { self == .today }

    /// Its first day, at midnight, for now.
    public func firstDay(_ now: Date, calendar: Calendar = .current) -> Date {
        calendar.date(byAdding: .day, value: 1 - days, to: calendar.startOfDay(for: now)) ?? now
    }

    /// The QueryWork question for now: grouped by who and which coding agent.
    public func request(_ now: Date, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
        var r = Hermesh_Local_V1_QueryWorkRequest()
        r.timeZone = calendar.timeZone.identifier
        r.firstDay = dayKey(firstDay(now, calendar: calendar), calendar: calendar)
        r.days = UInt32(days)
        r.bucket = hourly ? .hour : .day
        r.groupBy = [.client, .agent]
        return r
    }
}

/// Work over a range as the counts say (QueryWork): the sessions at work in
/// each hour or day, in all and by the coding agent that ran them; the
/// range's sessions and those of them now done, its turns and tokens; and,
/// for each client and agent, the sessions the client started and the turns
/// it asked for.
public struct RangeWork: Equatable, Sendable {
    /// The work of one client with one coding agent.
    public struct Pair: Equatable, Sendable {
        public var client: String
        public var agent: String
        /// Sessions the client started, at work in the range.
        public var sessions: Int
        /// Turns the client asked for that ended in the range, and their tokens.
        public var turns: Int
        public var tokens: UInt64

        public init(client: String, agent: String, sessions: Int, turns: Int, tokens: UInt64) {
            self.client = client
            self.agent = agent
            self.sessions = sessions
            self.turns = turns
            self.tokens = tokens
        }
    }

    public var period: WorkPeriod
    /// Its first day, YYYY-MM-DD.
    public var firstDay: String
    /// Sessions at work in each hour (today) or day, the first first.
    public var buckets: [Int]
    public var byAgent: [String: [Int]]
    public var sessions: Int
    public var done: Int
    public var turns: Int
    public var tokens: UInt64
    public var pairs: [Pair]
    /// The machines counted, and how current each one's counts are.
    public var sources: [Hermesh_Local_V1_WorkSource]

    public init(period: WorkPeriod, firstDay: String, _ r: Hermesh_Local_V1_QueryWorkResponse) {
        self.period = period
        self.firstDay = firstDay
        let count = max(period.hourly ? 24 : period.days, (r.sessions.map { Int($0.bucket) }.max() ?? -1) + 1)
        var all = Array(repeating: 0, count: count)
        var by: [String: [Int]] = [:]
        for x in r.sessions {
            let b = Int(x.bucket)
            all[b] += Int(x.sessions)
            by[x.group.agent, default: Array(repeating: 0, count: count)][b] += Int(x.sessions)
        }
        buckets = all
        byAgent = by
        sessions = r.rangeSessions.reduce(0) { $0 + Int($1.sessions) }
        done = r.rangeSessions.reduce(0) { $0 + Int($1.done) }
        turns = r.rangeTurns.reduce(0) { $0 + Int($1.turns) }
        tokens = r.rangeTurns.reduce(0) { $0 + $1.inputTokens + $1.outputTokens }
        var merged: [Pair] = []
        func pair(_ g: Hermesh_Local_V1_WorkGroup) -> Int {
            if let i = merged.firstIndex(where: { $0.client == g.client && $0.agent == g.agent }) { return i }
            merged.append(Pair(client: g.client, agent: g.agent, sessions: 0, turns: 0, tokens: 0))
            return merged.count - 1
        }
        for x in r.rangeSessions { merged[pair(x.group)].sessions += Int(x.sessions) }
        for x in r.rangeTurns {
            let i = pair(x.group)
            merged[i].turns += Int(x.turns)
            merged[i].tokens += x.inputTokens + x.outputTokens
        }
        pairs = merged
        sources = r.sources
    }

    public func turns(agent: String) -> Int { pairs.filter { $0.agent == agent }.reduce(0) { $0 + $1.turns } }
    public func sessions(agent: String) -> Int { pairs.filter { $0.agent == agent }.reduce(0) { $0 + $1.sessions } }
}

/// The work of a range as a flow from the clients that asked for it to the
/// coding agents that did it, by turns: the busiest few on each side, the
/// rest together as one ("" names it). Work without an agent is left out.
public struct WorkFlow: Equatable, Sendable {
    public struct Node: Equatable, Sendable {
        /// A client id or an agent; "" for the rest together.
        public var id: String
        public var turns: Int
        /// For "", how many it stands for.
        public var count = 1
    }

    public struct Band: Equatable, Sendable {
        public var client: String
        public var agent: String
        public var turns: Int
        /// Those turns' tokens.
        public var tokens: UInt64 = 0
    }

    public var clients: [Node]
    public var agents: [Node]
    public var bands: [Band]

    public init(_ pairs: [RangeWork.Pair], keep: Int = 3) {
        let known = pairs.filter { !$0.agent.isEmpty }
        func side(_ key: (RangeWork.Pair) -> String) -> (nodes: [Node], kept: Set<String>) {
            var turns: [String: Int] = [:]
            var sessions: [String: Int] = [:]
            for p in known {
                turns[key(p), default: 0] += p.turns
                sessions[key(p), default: 0] += p.sessions
            }
            let order = turns.keys.sorted { (turns[$0]!, sessions[$0]!, $1) > (turns[$1]!, sessions[$1]!, $0) }
            let shown = order.count > keep ? Array(order.prefix(keep - 1)) : order
            var nodes = shown.map { Node(id: $0, turns: turns[$0]!) }
            let rest = order.dropFirst(shown.count)
            if !rest.isEmpty { nodes.append(Node(id: "", turns: rest.reduce(0) { $0 + turns[$1]! }, count: rest.count)) }
            return (nodes, Set(shown))
        }
        let c = side(\.client), a = side(\.agent)
        clients = c.nodes
        agents = a.nodes
        var merged: [Band] = []
        for p in known {
            let b = Band(client: c.kept.contains(p.client) ? p.client : "", agent: a.kept.contains(p.agent) ? p.agent : "", turns: p.turns, tokens: p.tokens)
            if let i = merged.firstIndex(where: { $0.client == b.client && $0.agent == b.agent }) {
                merged[i].turns += b.turns
                merged[i].tokens += b.tokens
            } else {
                merged.append(b)
            }
        }
        bands = merged
    }
}

// MARK: Calendar

public func isYesterday(_ d: Date, now: Date, calendar: Calendar = .current) -> Bool {
    guard let y = calendar.date(byAdding: .day, value: -1, to: now) else { return false }
    return calendar.isDate(d, inSameDayAs: y)
}

/// On or after the start of the day six days before now's.
public func withinWeek(_ d: Date, now: Date, calendar: Calendar = .current) -> Bool {
    guard let start = calendar.date(byAdding: .day, value: -6, to: calendar.startOfDay(for: now)) else { return false }
    return d >= start
}
