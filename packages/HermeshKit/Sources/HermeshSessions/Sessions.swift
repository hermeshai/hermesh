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
        status == .crashed || (status == .idle && progress.hasLastFailure && progress.lastFailure.kind != .interrupted)
    }

    var isActive: Bool { status == .queued || status == .preparing || status == .running || (status == .idle && activeBackgroundCount > 0) }

    /// A turn that the owner can interrupt.
    var isInterruptible: Bool { isActive || status == .waitingInput }

    var isArchivable: Bool { (status == .idle && activeBackgroundCount == 0) || status == .crashed }

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

    /// A conversation of the owner's with Her, a session of the her provider (docs/her-conversations.md).
    var isHerConversation: Bool { agent == herAgent || modelSelection.instanceID == herAgent }

    /// The conversation with Her that this work was sent from, if any.
    var herParentID: String? {
        lineage.relationship == herAgent && !lineage.parentSessionID.isEmpty ? lineage.parentSessionID : nil
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

/// The machines a watch message gives, each one being read again (syncing)
/// or while the relay cannot be reached (!relayConnected), which the core
/// gives no state until then, with the state it was last shown with (its
/// coding agents, paused…), as its sessions are held: the app came back to
/// the front, or was launched again.
public func holdingState(_ machines: [Hermesh_Local_V1_Machine], known: [Hermesh_Local_V1_Machine], relayConnected: Bool = true) -> [Hermesh_Local_V1_Machine] {
    machines.map { m in
        guard m.syncing || !relayConnected, !m.hasStatus, let k = known.first(where: { $0.machineID == m.machineID }), k.hasStatus else { return m }
        var held = m
        held.status = k.status
        return held
    }
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
    case status, date, project, agent, machine
}

/// How the session list sorts each section: workbench default (active by creation, others by last activity), creation time, or last activity.
public enum SessionSort: String, CaseIterable, Codable, Sendable {
    case `default`, created, updated
}

/// How the session list is laid out: its grouping, how each section sorts,
/// which direction it goes, and whether rows show their metadata line (grant,
/// coding agent, project and time). The same as the Mac's.
public struct SessionListLayout: Equatable, Codable, Sendable {
    public var group: SessionGrouping = .status
    public var sort: SessionSort = .default
    public var newestFirst = true
    public var metadata = false
    /// The work Her sent from a conversation of the owner's with her sits under it (docs/her-conversations.md §3.4).
    public var nest = true

    /// By status, workbench sort, newest first, titles alone, nested, until the owner picks a view (docs/her-team.md §16).
    public init(group: SessionGrouping = .status, sort: SessionSort = .default, newestFirst: Bool = true, metadata: Bool = false, nest: Bool = true) {
        self.group = group
        self.sort = sort
        self.newestFirst = newestFirst
        self.metadata = metadata
        self.nest = nest
    }

    private enum CodingKeys: String, CodingKey {
        case group, sort, newestFirst, metadata, nest
    }

    public init(from decoder: Swift.Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        group = try c.decodeIfPresent(SessionGrouping.self, forKey: .group) ?? .status
        sort = try c.decodeIfPresent(SessionSort.self, forKey: .sort) ?? .default
        newestFirst = try c.decodeIfPresent(Bool.self, forKey: .newestFirst) ?? true
        metadata = try c.decodeIfPresent(Bool.self, forKey: .metadata) ?? false
        nest = try c.decodeIfPresent(Bool.self, forKey: .nest) ?? true
    }
}

/// The agent of a conversation of the owner's with Her (docs/her-conversations.md).
public let herAgent = "her"

/// The key of the project section that holds Her's conversations: they belong to no project.
public let herSection = "her"

/// Work under a conversation shows five at once; what runs or waits always shows (docs/her-conversations.md D11).
public let childrenInitialLimit = 5

/// The mark a collapsed conversation's row shows: the most pressing of its own and its work's.
public enum TreeMark: String, Equatable, Sendable {
    case waiting, failed
}

/// The work under each conversation with Her, newest first, while the view nests it: only work whose
/// conversation is in `sessions` too, and only while neither is archived (an archived one is listed
/// on its own).
public func childrenOf(_ sessions: [Session], nest: Bool = true) -> [String: [Session]] {
    guard nest else { return [:] }
    let parents = Set(sessions.filter { $0.isHerConversation && $0.status != .archived }.map(\.sessionID))
    var out: [String: [Session]] = [:]
    for s in sessions {
        guard let parent = s.herParentID, parents.contains(parent), s.status != .archived else { continue }
        out[parent, default: []].append(s)
    }
    for k in out.keys {
        out[k]?.sort { $0.updatedDate > $1.updatedDate }
    }
    return out
}

public func childrenOf(_ sessions: [Session], view: SessionListLayout) -> [String: [Session]] {
    childrenOf(sessions, nest: view.nest)
}

/// A conversation and the work under it: the most pressing state of them (needs you, runs, done).
public func treeStatus(_ s: Session, children: [Session] = []) -> SessionBucket {
    let tree = [s] + children
    return tree.contains(where: \.needsAttention) ? .attention : tree.contains(where: \.isActive) ? .active : .done
}

/// A conversation and the work under it: the last time any of them was active.
public func treeUpdated(_ s: Session, children: [Session] = []) -> Date {
    ([s] + children).map(\.updatedDate).max() ?? s.updatedDate
}

/// The mark a collapsed conversation's row shows: the most pressing of its own and its work's (an
/// orange dot while any waits for the owner, a red one when any failed), else nil.
public func treeMark(_ s: Session, children: [Session]) -> TreeMark? {
    let tree = [s] + children
    if tree.contains(where: { $0.status == .waitingInput }) { return .waiting }
    if tree.contains(where: \.isFailed) { return .failed }
    return nil
}

/// A conversation with Her is archived with its work, so only once all of it can be.
public func isTreeArchivable(_ s: Session, children: [Session]) -> Bool {
    s.isArchivable && children.allSatisfy { $0.isArchivable || $0.status == .archived }
}

/// The work a conversation's row shows, and how much stays behind "Show more".
public func sliceChildren(_ children: [Session], expanded: Bool, selectedID: String? = nil) -> SectionSlice {
    if expanded || children.count <= childrenInitialLimit {
        return SectionSlice(visible: children, hiddenCount: 0)
    }
    var keep = Set(children.filter { $0.sessionID == selectedID || $0.needsAttention || $0.isActive }.map(\.sessionID))
    for c in children {
        if keep.count >= childrenInitialLimit { break }
        keep.insert(c.sessionID)
    }
    let visible = children.filter { keep.contains($0.sessionID) }
    return SectionSlice(visible: visible, hiddenCount: children.count - visible.count)
}

/// Pinned, a state or a day: what a section is called when the list groups by status or date.
public enum SessionBucket: String, CaseIterable, Sendable {
    case pinned, attention, active, done, today, yesterday, week, earlier, archived

    public var title: String {
        switch self {
        case .pinned: String(localized: "Pinned", bundle: .module)
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

/// When a session's current state began for activity sorting without intra-turn heartbeat jitter.
public func activityDate(_ s: Session) -> Date {
    s.isActive && !s.needsAttention ? (s.turnStartedDate ?? s.createdDate) : s.updatedDate
}

/// Places `movedID` at insertion slot `slot` (0...currentIDs.count) among `currentIDs`.
public func reorderIDs(_ currentIDs: [String], movedID: String, slot: Int) -> [String] {
    let orig = currentIDs.firstIndex(of: movedID)
    var without = currentIDs.filter { $0 != movedID }
    let idx = if let orig, orig < slot { slot - 1 } else { slot }
    without.insert(movedID, at: max(0, min(without.count, idx)))
    return without
}

/// The day it was last active; "week" is the six days before today, not the calendar week.
public func dayOf(_ s: Session, now: Date, at: Date? = nil, calendar: Calendar = .current) -> SessionBucket {
    let d = at ?? s.updatedDate
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
    /// The grouping, or nil for the pinned and archived sessions' own sections.
    public let grouping: SessionGrouping?
    /// A state or a day (a SessionBucket's raw value), a project, a coding agent, or a machine id.
    public let key: String
    public let sessions: [Session]
    public var id: String { "\(grouping?.rawValue ?? key):\(key)" }
}

private func sortWithManualOrder(
    _ list: [Session],
    timeOf: (Session) -> Date,
    newestFirst: Bool,
    manualOrder: [String] = [],
    manualApplies: (Session) -> Bool = { _ in true }
) -> [Session] {
    func cmp(_ a: Date, _ b: Date) -> Bool { newestFirst ? a > b : a < b }
    if manualOrder.isEmpty {
        return list.sorted { cmp(timeOf($0), timeOf($1)) }
    }
    var present: [String: Session] = [:]
    for s in list where manualApplies(s) { present[s.sessionID] = s }
    let orderedIDs = manualOrder.filter { present[$0] != nil }
    if orderedIDs.isEmpty {
        return list.sorted { cmp(timeOf($0), timeOf($1)) }
    }
    let slotTimes = orderedIDs.map { timeOf(present[$0]!) }.sorted(by: cmp)
    var assigned: [String: (Date, Int)] = [:]
    for (rank, id) in orderedIDs.enumerated() { assigned[id] = (slotTimes[rank], rank) }
    return list.sorted { a, b in
        let ma = assigned[a.sessionID], mb = assigned[b.sessionID]
        let ta = ma?.0 ?? timeOf(a), tb = mb?.0 ?? timeOf(b)
        if ta != tb { return cmp(ta, tb) }
        if let ma, let mb { return ma.1 < mb.1 }
        return false
    }
}

/// The list as the view lays it out. Pinned sessions sit in their own section
/// at the top (whatever the grouping); active sessions sort by creation time
/// (plus any manual drag order) so concurrent tool heartbeats never jump rows,
/// while sessions needing attention or done sort by when they settled.
///
/// alwaysActive: by status, the running section stays while nothing runs, empty, so the menu always shows it
/// (docs/her-team.md §16); not while there is no session at all.
public func sessionSections(_ sessions: [Session], view: SessionListLayout, archived: Bool, now: Date, machines: [String] = [],
                            alwaysActive: Bool = false, pinnedOrder: [String] = [], activeOrder: [String] = [],
                            calendar: Calendar = .current) -> [SessionSection] {
    var by: [String: [Session]] = [:]
    var pinned: [Session] = []
    var gone: [Session] = []
    // Nested, the work under a conversation with Her goes where the conversation goes, and the
    // conversation is placed by all of it (docs/her-conversations.md D8, D9).
    let children = childrenOf(sessions, view: view)
    let nested = Set(children.values.joined().map(\.sessionID))
    func kidsOf(_ s: Session) -> [Session] { children[s.sessionID] ?? [] }
    for s in sessions {
        if s.status == .archived {
            if archived { gone.append(s) }
            continue
        }
        if nested.contains(s.sessionID) { continue }
        if s.pinned {
            pinned.append(s)
            continue
        }
        let k = switch view.group {
        case .status: treeStatus(s, children: kidsOf(s)).rawValue
        case .date: dayOf(s, now: now, at: treeUpdated(s, children: kidsOf(s)), calendar: calendar).rawValue
        case .project: s.isHerConversation ? herSection : s.projectID
        case .agent: s.agent
        case .machine: s.machineID
        }
        by[k, default: []].append(s)
    }
    let days: [SessionBucket] = [.today, .yesterday, .week, .earlier]
    let order: [String] = switch view.group {
    case .status: [SessionBucket.attention, .active, .done].map(\.rawValue)
    case .date: (view.newestFirst ? days : days.reversed()).map(\.rawValue)
    case .project: by.keys.sorted { a, b in (a == herSection) != (b == herSection) ? a == herSection : a.localizedStandardCompare(b) == .orderedAscending }
    case .agent: by.keys.sorted { a, b in (a == herAgent) != (b == herAgent) ? a == herAgent : agentTitle(a).localizedStandardCompare(agentTitle(b)) == .orderedAscending }
    case .machine: by.keys.sorted { (machines.firstIndex(of: $0) ?? .max, $0) < (machines.firstIndex(of: $1) ?? .max, $1) }
    }
    func timeFor(_ s: Session) -> Date {
        if view.sort == .created { return s.createdDate }
        let kids = kidsOf(s)
        // A conversation with work under it moves with its latest activity, whatever is running.
        if !kids.isEmpty { return treeUpdated(s, children: kids) }
        if view.sort == .updated { return activityDate(s) }
        return s.isActive && !s.needsAttention ? s.createdDate : s.updatedDate
    }
    func sorted(_ list: [Session], isActiveBucket: Bool = false) -> [Session] {
        sortWithManualOrder(list, timeOf: timeFor, newestFirst: view.newestFirst, manualOrder: activeOrder) {
            isActiveBucket || ($0.isActive && !$0.needsAttention)
        }
    }
    let active = SessionBucket.active.rawValue
    let keep = alwaysActive && view.group == .status && (!by.isEmpty || !pinned.isEmpty)
    var out: [SessionSection] = []
    if !pinned.isEmpty {
        let pinTime: (Session) -> Date = { view.sort == .updated ? activityDate($0) : $0.createdDate }
        let sortedPinned = sortWithManualOrder(pinned, timeOf: pinTime, newestFirst: view.newestFirst, manualOrder: pinnedOrder)
        out.append(SessionSection(grouping: nil, key: SessionBucket.pinned.rawValue, sessions: sortedPinned))
    }
    for k in order {
        if let list = by[k] {
            out.append(SessionSection(grouping: view.group, key: k, sessions: sorted(list, isActiveBucket: view.group == .status && k == active)))
        } else if keep && k == active {
            out.append(SessionSection(grouping: .status, key: k, sessions: []))
        }
    }
    if !gone.isEmpty { out.append(SessionSection(grouping: nil, key: SessionBucket.archived.rawValue, sessions: sorted(gone))) }
    return out
}

public let sectionInitialLimit = 5
public let sectionExpandStep = 10

public func isCappedGrouping(_ grouping: SessionGrouping?) -> Bool {
    switch grouping {
    case .project, .agent, .machine: true
    case .status, .date, nil: false
    }
}

public struct SectionSlice {
    public let visible: [Session]
    public let hiddenCount: Int
}

/// The rows a sidebar section shows at once, and how many stay folded behind
/// "Show more". Status, date and archived sections never fold; entity sections
/// (project, agent, machine) fold past `limit` unless folding would hide at
/// most one row, while keeping anything active, waiting, failed or selected in
/// view in its place.
public func sliceSectionSessions(
    _ sessions: [Session],
    grouping: SessionGrouping?,
    limit: Int = sectionInitialLimit,
    selectedID: String? = nil
) -> SectionSlice {
    let cap = max(0, limit)
    guard isCappedGrouping(grouping), sessions.count > cap + 1 else {
        return SectionSlice(visible: sessions, hiddenCount: 0)
    }
    let head = Array(sessions.prefix(cap))
    let pinned = sessions.dropFirst(cap).filter { $0.sessionID == selectedID || $0.needsAttention || $0.isActive }
    let hiddenCount = sessions.count - head.count - pinned.count
    if hiddenCount <= 1 {
        return SectionSlice(visible: sessions, hiddenCount: 0)
    }
    return SectionSlice(visible: head + pinned, hiddenCount: hiddenCount)
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
