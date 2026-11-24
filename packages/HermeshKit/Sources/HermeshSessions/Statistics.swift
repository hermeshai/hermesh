import Foundation
import HermeshProto

// The statistics (docs/overview-usage.md §6, §7): Usage asks three questions
// of QueryWork for a range and a filter (the page, the menus' choices, the
// weeks of tokens), and Limits shows what each coding agent last said of its
// account's rate limits. What the screens show is made here from the answers
// and the machines; the Mac's are packages/core's usage.ts and limits.ts, by
// the same rules.

/// Only one agent's, one environment's (or one of its projects'), one sender's work; an empty field: any.
public struct UsageFilter: Hashable, Sendable {
    public var agent = ""
    public var machineID = ""
    /// A project is its environment's own: set only with machineID.
    public var projectID = ""
    public var client = ""

    public init(agent: String = "", machineID: String = "", projectID: String = "", client: String = "") {
        self.agent = agent
        self.machineID = machineID
        self.projectID = projectID
        self.client = client
    }

    var group: Hermesh_Local_V1_WorkGroup {
        Hermesh_Local_V1_WorkGroup.with { $0.agent = agent; $0.machineID = machineID; $0.projectID = projectID; $0.client = client }
    }
}

/// What the page's bars are in parts of: nothing (the total), the agents, or where (the environments, or one
/// environment's projects). In the order the control shows them.
public enum UsageGroup: String, CaseIterable, Sendable {
    case total, agent, place
}

/// The page's question: the range ending today by hour (Today) or day, under the filter, with the span before it;
/// grouped as the bars are (where by machine and project, so either can be drawn).
public func usagePageRequest(_ period: WorkPeriod, filter: UsageFilter, group: UsageGroup = .total, now: Date, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
    var r = Hermesh_Local_V1_QueryWorkRequest()
    r.timeZone = calendar.timeZone.identifier
    r.firstDay = dayKey(period.firstDay(now, calendar: calendar), calendar: calendar)
    r.days = UInt32(period.days)
    r.bucket = period.hourly ? .hour : .day
    r.filter = filter.group
    r.previous = true
    switch group {
    case .agent: r.groupBy = [.agent]
    case .place: r.groupBy = [.machine, .project]
    case .total: break
    }
    return r
}

/// The menus' question: the range's totals by all four dimensions, not filtered (each menu's lines are added up
/// from it under the other filters).
public func usageMenusRequest(_ period: WorkPeriod, now: Date, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
    var r = Hermesh_Local_V1_QueryWorkRequest()
    r.timeZone = calendar.timeZone.identifier
    r.firstDay = dayKey(period.firstDay(now, calendar: calendar), calendar: calendar)
    r.days = UInt32(period.days)
    r.bucket = .none
    r.groupBy = [.client, .agent, .machine, .project]
    return r
}

/// The weeks' question: usageRequest's days of turns alone, under the filter (usageYear reads the answer).
public func usageWeeksRequest(filter: UsageFilter, now: Date, weeks: Int = 18, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
    var r = usageRequest(now, weeks: weeks, calendar: calendar)
    r.filter = filter.group
    r.turnsOnly = true
    return r
}

/// What the Usage page shows from the answer to usagePageRequest.
public struct UsagePage: Equatable, Sendable {
    /// Each hour of today or day of the range: sessions at work in it and the tokens of the turns that ended in it.
    public var buckets: [(sessions: Int, tokens: UInt64)]
    /// The bucket now is in: the hour, or the last day. Those after it have not come.
    public var now: Int
    /// Sessions at work in the range (each once), and of them those done now.
    public var sessions: Int
    public var done: Int
    public var failedTurns: Int
    public var busySeconds: UInt64
    public var inputTokens: UInt64
    public var outputTokens: UInt64
    /// Against the span before, in percent; nil when nothing was counted in it.
    public var sessionsDelta: Int?
    public var tokensDelta: Int?

    public var tokens: UInt64 { inputTokens + outputTokens }

    public static func == (a: UsagePage, b: UsagePage) -> Bool {
        a.buckets.map(\.sessions) == b.buckets.map(\.sessions) && a.buckets.map(\.tokens) == b.buckets.map(\.tokens) && a.now == b.now
            && a.sessions == b.sessions && a.done == b.done && a.failedTurns == b.failedTurns && a.busySeconds == b.busySeconds
            && a.inputTokens == b.inputTokens && a.outputTokens == b.outputTokens && a.sessionsDelta == b.sessionsDelta && a.tokensDelta == b.tokensDelta
    }
}

public func usagePage(_ res: Hermesh_Local_V1_QueryWorkResponse?, period: WorkPeriod, now: Date, calendar: Calendar = .current) -> UsagePage {
    var buckets = Array(repeating: (sessions: 0, tokens: UInt64(0)), count: period.hourly ? 24 : period.days)
    for x in res?.sessions ?? [] where Int(x.bucket) < buckets.count { buckets[Int(x.bucket)].sessions += Int(x.sessions) }
    for x in res?.turns ?? [] where Int(x.bucket) < buckets.count { buckets[Int(x.bucket)].tokens += x.inputTokens + x.outputTokens }
    let sessions = (res?.rangeSessions ?? []).reduce(0) { $0 + Int($1.sessions) }
    let input = (res?.rangeTurns ?? []).reduce(UInt64(0)) { $0 + $1.inputTokens }
    let output = (res?.rangeTurns ?? []).reduce(UInt64(0)) { $0 + $1.outputTokens }
    let before = (res?.previousSessions ?? []).reduce(0) { $0 + Int($1.sessions) }
    let tokensBefore = (res?.previousTurns ?? []).reduce(UInt64(0)) { $0 + $1.inputTokens + $1.outputTokens }
    func delta(_ cur: Double, _ prev: Double) -> Int? { prev > 0 ? Int(((cur - prev) / prev * 100).rounded()) : nil }
    return UsagePage(buckets: buckets, now: period.hourly ? calendar.component(.hour, from: now) : buckets.count - 1, sessions: sessions,
                     done: (res?.rangeSessions ?? []).reduce(0) { $0 + Int($1.done) }, failedTurns: (res?.rangeTurns ?? []).reduce(0) { $0 + Int($1.failedTurns) },
                     busySeconds: (res?.rangeTurns ?? []).reduce(UInt64(0)) { $0 + $1.busySeconds }, inputTokens: input, outputTokens: output,
                     sessionsDelta: delta(Double(sessions), Double(before)), tokensDelta: delta(Double(input + output), Double(tokensBefore)))
}

/// A part of the page's bars: what it is, its colour (0 to 5; -1: none of its own, it is among Other or is Other),
/// its whole in the range and its share of each bucket.
public struct UsagePart: Equatable, Identifiable, Sendable {
    public struct Bucket: Equatable, Sendable {
        public var sessions = 0
        public var tokens: UInt64 = 0

        public init(sessions: Int = 0, tokens: UInt64 = 0) {
            self.sessions = sessions
            self.tokens = tokens
        }
    }

    public var id: String
    public var color = -1
    public var sessions = 0
    public var tokens: UInt64 = 0
    public var buckets: [Bucket]

    /// The id of the part the least busy are added up as.
    public static let other = ""
}

/// What the bars are in parts of: agents, machines, or one machine's projects (its id in machineID). all: every
/// part, the busiest first. drawn: those the bars are coloured by, all of them when they are few, else the
/// busiest and the rest as one, Other.
public struct UsageParts: Equatable, Sendable {
    public enum By: Sendable { case agent, machine, project }

    public var by: By
    public var machineID = ""
    public var all: [UsagePart] = []
    public var drawn: [UsagePart] = []

    /// The parts added up as Other.
    public var folded: Int { all.count - drawn.count(where: { $0.id != UsagePart.other }) }
}

/// The colours the parts are told apart by (the Mac's palette).
public let usagePartColors = 6

/// The parts of the page's bars from the answer to its grouped question (the Mac's usageParts, by the same rules):
/// the agents; or the machines, or (one machine chosen, or only one that worked) its projects. A part's colour
/// follows it, not its rank: its place in order (the coding agents' own list, the machines as the app lists them,
/// projects by name) when all of those drawn fall within the colours, else its place among them in that order.
/// More parts than can be drawn (the phone draws four, the Mac six): the busiest, and the rest as Other.
public func usageParts(_ res: Hermesh_Local_V1_QueryWorkResponse?, period: WorkPeriod, group: UsageGroup, filter: UsageFilter,
                       agents: [String], machines: [String], drawn most: Int = 4) -> UsageParts? {
    if group == .total { return nil }
    let n = period.hourly ? 24 : period.days
    let ids = Set((res?.rangeSessions ?? []).map(\.group.machineID) + (res?.rangeTurns ?? []).map(\.group.machineID))
    let machineID = group == .place ? (filter.machineID.isEmpty ? (ids.count == 1 ? ids.first! : "") : filter.machineID) : ""
    let by: UsageParts.By = group == .agent ? .agent : machineID.isEmpty ? .machine : .project
    func key(_ g: Hermesh_Local_V1_WorkGroup) -> String? {
        switch by {
        case .agent: g.agent
        case .machine: g.machineID
        case .project: g.machineID == machineID ? g.projectID : nil
        }
    }
    var parts: [String: UsagePart] = [:]
    func add(_ g: Hermesh_Local_V1_WorkGroup, _ change: (inout UsagePart) -> Void) {
        // A group with no name is the answer to another question (the last one, shown while this is asked): no part.
        guard let id = key(g), !id.isEmpty else { return }
        change(&parts[id, default: UsagePart(id: id, buckets: Array(repeating: .init(), count: n))])
    }
    for x in res?.rangeSessions ?? [] { add(x.group) { $0.sessions += Int(x.sessions) } }
    for x in res?.rangeTurns ?? [] { add(x.group) { $0.tokens += x.inputTokens + x.outputTokens } }
    for x in res?.sessions ?? [] where Int(x.bucket) < n { add(x.group) { $0.buckets[Int(x.bucket)].sessions += Int(x.sessions) } }
    for x in res?.turns ?? [] where Int(x.bucket) < n { add(x.group) { $0.buckets[Int(x.bucket)].tokens += x.inputTokens + x.outputTokens } }

    var all = parts.values.filter { $0.sessions > 0 || $0.tokens > 0 }.sorted { ($0.sessions, $0.tokens, $1.id) > ($1.sessions, $1.tokens, $0.id) }
    let top = all.count > most ? most - 1 : all.count
    let known = by == .agent ? agents : by == .machine ? machines : all[..<top].map(\.id).sorted()
    func place(_ id: String) -> Int { known.firstIndex(of: id) ?? Int.max }
    let inOrder = all[..<top].map(\.id).sorted { (place($0), $0) < (place($1), $1) }
    let fits = all[..<top].allSatisfy { place($0.id) < usagePartColors }
    for i in 0 ..< top { all[i].color = fits ? place(all[i].id) : inOrder.firstIndex(of: all[i].id)! }
    var drawn = Array(all[..<top])
    if top < all.count {
        var other = UsagePart(id: UsagePart.other, buckets: Array(repeating: .init(), count: n))
        for p in all[top...] {
            other.sessions += p.sessions
            other.tokens += p.tokens
            for i in 0 ..< n {
                other.buckets[i].sessions += p.buckets[i].sessions
                other.buckets[i].tokens += p.buckets[i].tokens
            }
        }
        drawn.append(other)
    }
    return UsageParts(by: by, machineID: machineID, all: all, drawn: drawn)
}

/// A choice of a filter's menu: what it is, and its sessions and tokens in the range under the other filters.
public struct UsageChoice: Equatable, Identifiable, Sendable {
    public var id: String
    public var sessions: Int
    public var tokens: UInt64
    /// An environment's projects.
    public var projects: [UsageChoice] = []
}

public struct UsageChoices: Equatable, Sendable {
    public var agents: [UsageChoice] = []
    /// Each environment with its projects (a project's id is its environment's own).
    public var machines: [UsageChoice] = []
    public var clients: [UsageChoice] = []
}

/// Each menu's choices from the answer to usageMenusRequest: what worked in the range under the other filters (so
/// a choice never leads to nothing), the busiest first; the one chosen stays listed when it has no work.
public func usageChoices(_ res: Hermesh_Local_V1_QueryWorkResponse?, filter f: UsageFilter) -> UsageChoices {
    var agents: [String: UsageChoice] = [:], machines: [String: UsageChoice] = [:], clients: [String: UsageChoice] = [:]
    var projects: [String: [String: UsageChoice]] = [:]
    func add(_ to: inout [String: UsageChoice], _ id: String, _ sessions: Int, _ tokens: UInt64) {
        to[id, default: UsageChoice(id: id, sessions: 0, tokens: 0)].sessions += sessions
        to[id]!.tokens += tokens
    }
    func each(_ g: Hermesh_Local_V1_WorkGroup, _ sessions: Int, _ tokens: UInt64) {
        let agent = f.agent.isEmpty || g.agent == f.agent
        let place = (f.machineID.isEmpty || g.machineID == f.machineID) && (f.projectID.isEmpty || g.projectID == f.projectID)
        let client = f.client.isEmpty || g.client == f.client
        if place && client { add(&agents, g.agent, sessions, tokens) }
        if agent && place { add(&clients, g.client, sessions, tokens) }
        if agent && client {
            add(&machines, g.machineID, sessions, tokens)
            add(&projects[g.machineID, default: [:]], g.projectID, sessions, tokens)
        }
    }
    for x in res?.rangeSessions ?? [] { each(x.group, Int(x.sessions), 0) }
    for x in res?.rangeTurns ?? [] { each(x.group, 0, x.inputTokens + x.outputTokens) }
    func list(_ m: [String: UsageChoice], chosen: String) -> [UsageChoice] {
        var out = m.values.sorted { ($0.sessions, $0.tokens, $1.id) > ($1.sessions, $1.tokens, $0.id) }
        if !chosen.isEmpty, !out.contains(where: { $0.id == chosen }) { out.append(UsageChoice(id: chosen, sessions: 0, tokens: 0)) }
        return out
    }
    return UsageChoices(agents: list(agents, chosen: f.agent),
                        machines: list(machines, chosen: f.machineID).map { m in
                            var m = m
                            m.projects = list(projects[m.id] ?? [:], chosen: m.id == f.machineID ? f.projectID : "")
                            return m
                        },
                        clients: list(clients, chosen: f.client))
}

// MARK: Limits

/// Left of a window at or below this is a thing that needs the owner.
public let limitLow = 20

/// A window as the page shows it: how long, how much is left of it and of its time, and when it starts again; and
/// the models it counts, when the agent counts groups of them apart ("" for the account's). resetsAt nil: it has not
/// started (no reset time said, as agy says 1970 for a window not yet used), so it is whole until first used, its
/// time too.
public struct LimitShown: Equatable, Identifiable, Sendable {
    public var minutes: Int
    public var left: Int
    public var timeLeft: Int
    public var resetsAt: Date?
    public var group: String = ""
    public var id: String { "\(group)/\(minutes)" }
}

public struct AgentLimitsCard: Equatable, Identifiable, Sendable {
    public var agent: String
    public var id: String { agent }
    /// The machine that said it last, and when; "" and nil when no machine has it.
    public var machineID: String
    public var observedAt: Date?
    public var plan: String
    /// Not yet reset, by group as the agent gave them, the shortest first in each; empty when none was said or all have
    /// reset since.
    public var windows: [LimitShown]
    /// It said some, and every one of them has reset since: what is left now is not known.
    public var stale: Bool

    /// The window with the least left.
    public var least: LimitShown? { windows.min { $0.left < $1.left } }
}

/// A card for each coding agent available on a machine, from what the machines say (one an agent: they do not name
/// the account, so the one said last): the one with the least left first, then those that said nothing.
public func agentLimits(_ machines: [(machineID: String, agents: [Hermesh_Machine_V1_AgentStatus])], now: Date) -> [AgentLimitsCard] {
    var cards: [String: AgentLimitsCard] = [:], order: [String] = []
    for m in machines {
        for a in m.agents where a.available || a.hasLimits {
            let at = a.hasLimits && a.limits.hasObservedAt ? a.limits.observedAt.date : nil
            if let had = cards[a.name], at == nil || (had.observedAt.map { $0 >= at! } ?? false) { continue }
            let said = a.hasLimits ? a.limits.windows : []
            var groups: [String] = []
            for w in said where !groups.contains(w.group) { groups.append(w.group) }
            // One that has reset since is not shown (what is left now is not known); one not started is.
            let windows = said.compactMap { w -> LimitShown? in
                let at = w.hasResetsAt && w.resetsAt.seconds > 0 ? w.resetsAt.date : nil
                if let at, at <= now { return nil }
                return LimitShown(minutes: Int(w.minutes), left: max(0, 100 - Int(w.usedPercent)),
                                  timeLeft: at.map { min(100, max(0, Int(($0.timeIntervalSince(now) / (Double(w.minutes) * 60) * 100).rounded()))) } ?? 100,
                                  resetsAt: at, group: w.group)
            }.sorted { (groups.firstIndex(of: $0.group)!, $0.minutes) < (groups.firstIndex(of: $1.group)!, $1.minutes) }
            if cards[a.name] == nil { order.append(a.name) }
            cards[a.name] = AgentLimitsCard(agent: a.name, machineID: at == nil ? "" : m.machineID, observedAt: at, plan: a.hasLimits ? a.limits.plan : "",
                                            windows: windows, stale: !said.isEmpty && windows.isEmpty)
        }
    }
    let least = { (c: AgentLimitsCard) in c.least?.left ?? 101 }
    return order.compactMap { cards[$0] }.enumerated().sorted { (least($0.element), $0.offset) < (least($1.element), $1.offset) }.map(\.element)
}

/// The cards whose least left is at or below limitLow: what needs the owner.
public func lowLimits(_ cards: [AgentLimitsCard]) -> [AgentLimitsCard] {
    cards.filter { ($0.least?.left ?? 101) <= limitLow }
}
