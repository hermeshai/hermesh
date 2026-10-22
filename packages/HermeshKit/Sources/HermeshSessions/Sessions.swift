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
        status == .crashed || (status != .waitingInput && status != .archived && progress.hasLastFailure && progress.lastFailure.kind != .interrupted)
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
/// session replaces an earlier state of itself, or joins the list.
public func mergeSessions(_ list: [Session], _ changes: [Session]) -> [Session] {
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

/// Where a session sits in the list, in the list's order.
public enum SessionBucket: String, CaseIterable, Sendable {
    case attention, active, today, yesterday, week, earlier, archived

    public var title: String {
        switch self {
        case .attention: String(localized: "Needs You", bundle: .module)
        case .active: String(localized: "In Progress", bundle: .module)
        case .today: String(localized: "Today", bundle: .module)
        case .yesterday: String(localized: "Yesterday", bundle: .module)
        case .week: String(localized: "Earlier This Week", bundle: .module)
        case .earlier: String(localized: "Earlier", bundle: .module)
        case .archived: String(localized: "Archived", bundle: .module)
        }
    }
}

/// "week" is the six days before today, not the calendar week.
public func bucketOf(_ s: Session, now: Date, calendar: Calendar = .current) -> SessionBucket {
    if s.status == .archived { return .archived }
    if s.needsAttention { return .attention }
    if s.isActive { return .active }
    let d = s.updatedDate
    if calendar.isDate(d, inSameDayAs: now) { return .today }
    if isYesterday(d, now: now, calendar: calendar) { return .yesterday }
    if withinWeek(d, now: now, calendar: calendar) { return .week }
    return .earlier
}

/// The session list's filter: by grant (its client id), project, coding
/// agent or machine (empty matches all), and whether archived sessions show.
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
    public let bucket: SessionBucket
    public let sessions: [Session]
    public var id: SessionBucket { bucket }
}

/// The list: what needs the owner first (questions before failures), then
/// what runs (newest first), then the rest by when it last changed.
public func sessionSections(_ sessions: [Session], filter: SessionFilter, now: Date, calendar: Calendar = .current) -> [SessionSection] {
    var by: [SessionBucket: [Session]] = [:]
    for s in sessions where filter.matches(s) {
        let b = bucketOf(s, now: now, calendar: calendar)
        if b == .archived && !filter.archived { continue }
        by[b, default: []].append(s)
    }
    return SessionBucket.allCases.compactMap { b in
        guard var list = by[b] else { return nil }
        switch b {
        case .attention:
            list.sort { a, c in
                let qa = a.status == .waitingInput, qc = c.status == .waitingInput
                return qa != qc ? qa : a.updatedDate > c.updatedDate
            }
        case .active: list.sort { $0.createdDate > $1.createdDate }
        default: list.sort { $0.updatedDate > $1.updatedDate }
        }
        return SessionSection(bucket: b, sessions: list)
    }
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

/// The overview's "Today": what the sessions that ran today did.
public struct TodayStats: Equatable {
    public var sessions = 0
    public var done = 0
    public var failed = 0
}

public func todayStats(_ all: [Session], now: Date, calendar: Calendar = .current) -> TodayStats {
    let today = all.filter { calendar.isDate($0.updatedDate, inSameDayAs: now) || calendar.isDate($0.createdDate, inSameDayAs: now) || $0.isActive }
    return TodayStats(sessions: today.count,
                      done: today.filter { !$0.isFailed && ($0.status == .idle || $0.status == .archived) }.count,
                      failed: today.filter(\.isFailed).count)
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
