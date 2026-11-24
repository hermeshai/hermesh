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

/// The page's question: the range ending today by hour (Today) or day, under the filter, with the span before it.
public func usagePageRequest(_ period: WorkPeriod, filter: UsageFilter, now: Date, calendar: Calendar = .current) -> Hermesh_Local_V1_QueryWorkRequest {
    var r = Hermesh_Local_V1_QueryWorkRequest()
    r.timeZone = calendar.timeZone.identifier
    r.firstDay = dayKey(period.firstDay(now, calendar: calendar), calendar: calendar)
    r.days = UInt32(period.days)
    r.bucket = period.hourly ? .hour : .day
    r.filter = filter.group
    r.previous = true
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
/// the models it counts, when the agent counts groups of them apart ("" for the account's).
public struct LimitShown: Equatable, Identifiable, Sendable {
    public var minutes: Int
    public var left: Int
    public var timeLeft: Int
    public var resetsAt: Date
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
            let windows = said.filter { $0.resetsAt.date > now }.map { w in
                LimitShown(minutes: Int(w.minutes), left: max(0, 100 - Int(w.usedPercent)),
                           timeLeft: min(100, max(0, Int((w.resetsAt.date.timeIntervalSince(now) / (Double(w.minutes) * 60) * 100).rounded()))),
                           resetsAt: w.resetsAt.date, group: w.group)
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
