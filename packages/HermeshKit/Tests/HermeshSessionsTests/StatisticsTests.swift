import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshSessions

// The statistics (docs/overview-usage.md §6, §7), by the rules the Mac's
// packages/core usage.ts and limits.ts follow: the same cases, the same answers.

private let cal: Calendar = {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "Asia/Shanghai")!
    return c
}()

/// Tuesday 20 October 2026, 15:00 in Shanghai.
private let today = cal.date(from: DateComponents(year: 2026, month: 10, day: 20, hour: 15))!

@Test func thePageTheMenusAndTheWeeksAreThreeQuestionsOfOneRangeAndFilter() {
    let f = UsageFilter(agent: "claude", machineID: "m1", projectID: "app")
    let page = usagePageRequest(.today, filter: f, now: today, calendar: cal)
    #expect(page.timeZone == "Asia/Shanghai" && page.firstDay == "2026-10-20" && page.days == 1 && page.bucket == .hour && page.previous)
    #expect(page.filter.agent == "claude" && page.filter.machineID == "m1" && page.filter.projectID == "app")
    #expect(usagePageRequest(.week, filter: UsageFilter(), now: today, calendar: cal).firstDay == "2026-10-14")
    #expect(usagePageRequest(.month, filter: UsageFilter(), now: today, calendar: cal).days == 30)
    // The menus' choices are not filtered: each menu adds its lines up under the other filters.
    let menus = usageMenusRequest(.week, now: today, calendar: cal)
    #expect(menus.bucket == .none && menus.groupBy == [.client, .agent, .machine, .project] && !menus.hasFilter && menus.days == 7)
    let weeks = usageWeeksRequest(filter: f, now: today, calendar: cal)
    #expect(weeks.turnsOnly && weeks.filter == f.group && weeks.days == UInt32(17 * 7 + 3) && weeks.firstDay == "2026-06-21")
}

@Test func thePageAddsTheRangeUpAndSetsItAgainstTheSpanBefore() {
    let res = Hermesh_Local_V1_QueryWorkResponse.with {
        $0.sessions = [.with { $0.bucket = 0; $0.sessions = 2 }, .with { $0.bucket = 6; $0.sessions = 3 }, .with { $0.bucket = 6; $0.sessions = 1 }]
        $0.turns = [.with { $0.bucket = 0; $0.inputTokens = 900; $0.outputTokens = 100 }, .with { $0.bucket = 6; $0.inputTokens = 4000; $0.outputTokens = 500 }]
        $0.rangeSessions = [.with { $0.sessions = 4; $0.done = 3 }, .with { $0.sessions = 1 }]
        $0.rangeTurns = [.with { $0.turns = 9; $0.failedTurns = 2; $0.inputTokens = 4900; $0.outputTokens = 600; $0.busySeconds = 3700 }]
        $0.previousSessions = [.with { $0.sessions = 4 }]
        $0.previousTurns = [.with { $0.inputTokens = 10_000; $0.outputTokens = 1000 }]
    }
    let p = usagePage(res, period: .week, now: today, calendar: cal)
    #expect(p.buckets.count == 7 && p.buckets[0].sessions == 2 && p.buckets[0].tokens == 1000 && p.buckets[6].sessions == 4 && p.buckets[6].tokens == 4500)
    #expect(p.now == 6 && p.sessions == 5 && p.done == 3 && p.failedTurns == 2 && p.busySeconds == 3700 && p.tokens == 5500)
    #expect(p.sessionsDelta == 25 && p.tokensDelta == -50)
    // Today by hour: the hour now is in; nothing before the range, no change to say.
    let t = usagePage(.with { $0.sessions = [.with { $0.bucket = 15; $0.sessions = 1 }]; $0.rangeSessions = [.with { $0.sessions = 1 }] }, period: .today, now: today, calendar: cal)
    #expect(t.buckets.count == 24 && t.now == 15 && t.buckets[15].sessions == 1 && t.sessionsDelta == nil && t.tokensDelta == nil)
    #expect(usagePage(nil, period: .month, now: today, calendar: cal).now == 29)
}

@Test func aMenusChoicesAreWhatWorkedUnderTheOtherFiltersAndAProjectIsItsEnvironmentsOwn() {
    func g(_ client: String, _ agent: String, _ machine: String, _ project: String) -> Hermesh_Local_V1_WorkGroup {
        .with { $0.client = client; $0.agent = agent; $0.machineID = machine; $0.projectID = project }
    }
    let groups = [(g("her", "claude", "mac", "app"), 3, 300), (g("bot", "codex", "mac", "app"), 1, 100), (g("her", "claude", "vps", "app"), 5, 500), (g("her", "codex", "vps", "blog"), 2, 200)]
    let res = Hermesh_Local_V1_QueryWorkResponse.with { r in
        r.rangeSessions = groups.map { x in .with { $0.group = x.0; $0.sessions = UInt32(x.1) } }
        r.rangeTurns = groups.map { x in .with { $0.group = x.0; $0.inputTokens = UInt64(x.2) } }
    }
    let all = usageChoices(res, filter: UsageFilter())
    #expect(all.agents.map { "\($0.id) \($0.sessions) \($0.tokens)" } == ["claude 8 800", "codex 3 300"])
    #expect(all.clients.map(\.id) == ["her", "bot"])
    #expect(all.machines.map { "\($0.id) \($0.sessions)" } == ["vps 7", "mac 4"])
    #expect(all.machines[0].projects.map { "\($0.id) \($0.sessions)" } == ["app 5", "blog 2"], "both environments have an app: two projects")
    #expect(all.machines[1].projects.map { "\($0.id) \($0.sessions)" } == ["app 4"])
    let app = usageChoices(res, filter: UsageFilter(machineID: "mac", projectID: "app"))
    #expect(app.agents.map { "\($0.id) \($0.sessions)" } == ["claude 3", "codex 1"] && app.machines.map(\.id) == ["vps", "mac"])
    // A choice that has no work under the others stays listed.
    let codex = usageChoices(res, filter: UsageFilter(agent: "codex", machineID: "vps", projectID: "app", client: "bot"))
    #expect(codex.machines.map { "\($0.id) \($0.sessions)" } == ["mac 1", "vps 0"] && codex.machines[1].projects.map(\.id) == ["app"])
    #expect(codex.agents.map { "\($0.id) \($0.sessions)" } == ["codex 0"])
}

private func agent(_ name: String, available: Bool = true, said: Date? = nil, plan: String = "", windows: [(Int, Int, Date)] = [], groups: [String] = []) -> Hermesh_Machine_V1_AgentStatus {
    .with { a in
        a.name = name
        a.available = available
        if let said {
            a.limits = .with { l in
                l.plan = plan
                l.observedAt = Google_Protobuf_Timestamp(date: said)
                l.windows = windows.enumerated().map { i, w in
                    .with { $0.minutes = UInt32(w.0); $0.usedPercent = UInt32(w.1); $0.resetsAt = Google_Protobuf_Timestamp(date: w.2); $0.group = i < groups.count ? groups[i] : "" }
                }
            }
        }
    }
}

@Test func oneLimitsCardAnAgentFromTheMachineThatSaidItLastTheLeastLeftFirst() {
    let min = { (n: Double) in today.addingTimeInterval(n * 60) }
    let cards = agentLimits([
        (machineID: "mac", agents: [agent("claude", said: min(-120), windows: [(10080, 10, min(5000)), (300, 30, min(100))]),
                                    agent("codex", said: min(-3), plan: "pro", windows: [(10080, 61, min(2520))]), agent("gemini")]),
        (machineID: "mini", agents: [agent("claude", said: min(-2), windows: [(300, 86, min(140)), (10080, 52, min(5000))]), agent("kimi", available: false)]),
    ], now: today)
    #expect(cards.map(\.agent) == ["claude", "codex", "gemini"])
    #expect(cards[0].machineID == "mini" && cards[0].observedAt == min(-2) && !cards[0].stale)
    #expect(cards[0].windows == [LimitShown(minutes: 300, left: 14, timeLeft: 47, resetsAt: min(140)), LimitShown(minutes: 10080, left: 48, timeLeft: 50, resetsAt: min(5000))])
    #expect(cards[1].plan == "pro" && cards[1].windows.map(\.left) == [39] && cards[1].windows[0].timeLeft == 25)
    #expect(cards[2].machineID == "" && cards[2].observedAt == nil && cards[2].windows.isEmpty && !cards[2].stale, "available and never said")
    #expect(lowLimits(cards).map(\.agent) == ["claude"])
    #expect(needs(team: teamState([], requests: 0), pairings: [], limits: ["claude"], expiring: ["g"], guide: false, herOff: false, notificationsOff: false,
                  secondDevice: false).map(\.id) == ["l:claude", "g:g"], "after what failed, before grants about to end")
}

@Test func anAgentThatCountsGroupsOfModelsApartKeepsThemInItsOrder() {
    let min = { (n: Double) in today.addingTimeInterval(n * 60) }
    let cards = agentLimits([(machineID: "mac", agents: [agent("antigravity", said: min(-1),
                                                               windows: [(300, 3, min(120)), (10080, 17, min(9000)), (10080, 88, min(1080)), (300, 0, min(300))],
                                                               groups: ["Gemini Models", "Gemini Models", "Claude and GPT models", "Claude and GPT models"])])], now: today)
    #expect(cards[0].windows.map { "\($0.group) \($0.minutes) \($0.left)" } ==
            ["Gemini Models 300 97", "Gemini Models 10080 83", "Claude and GPT models 300 100", "Claude and GPT models 10080 12"])
    #expect(cards[0].least?.id == "Claude and GPT models/10080" && lowLimits(cards).map(\.agent) == ["antigravity"])
}

@Test func aWindowNotStartedIsShownWholeItsTimeToo() {
    // As agy says it of a group of models not yet used: no time it resets (1970), all of it left.
    let min = { (n: Double) in today.addingTimeInterval(n * 60) }
    var a = agent("antigravity", said: min(-1), windows: [(10080, 0, Date(timeIntervalSince1970: 0)), (300, 0, Date(timeIntervalSince1970: 0)), (300, 3, min(-10))],
                  groups: ["Claude and GPT models", "Claude and GPT models", "Gemini Models"])
    a.limits.windows.append(.with { $0.minutes = 60; $0.group = "Other" }) // no reset time at all
    let card = agentLimits([(machineID: "mac", agents: [a])], now: today)[0]
    #expect(card.windows == [LimitShown(minutes: 300, left: 100, timeLeft: 100, resetsAt: nil, group: "Claude and GPT models"),
                             LimitShown(minutes: 10080, left: 100, timeLeft: 100, resetsAt: nil, group: "Claude and GPT models"),
                             LimitShown(minutes: 60, left: 100, timeLeft: 100, resetsAt: nil, group: "Other")])
    #expect(!card.stale && lowLimits([card]).isEmpty)
}

@Test func aWindowThatHasResetSinceIsNotShown() {
    let min = { (n: Double) in today.addingTimeInterval(n * 60) }
    let cards = agentLimits([(machineID: "mac", agents: [agent("codex", said: min(-400), windows: [(300, 95, min(-100)), (10080, 70, min(3000))]),
                                                         agent("claude", said: min(-600), windows: [(300, 99, min(-300))])])], now: today)
    #expect(cards.first { $0.agent == "codex" }?.windows.map(\.minutes) == [10080])
    let claude = cards.first { $0.agent == "claude" }
    #expect(claude?.windows.isEmpty == true && claude?.stale == true)
    #expect(lowLimits(cards).isEmpty)
}

// The page's bars in parts (docs/overview-usage.md §6.9): the Mac's cases, and the phone's four parts.

private let agentOrder = ["claude", "codex", "gemini", "antigravity", "cursor", "copilot", "opencode", "qwen"]
private let machineOrder = ["mac", "vps", "mini"]

/// An answer whose rows are each their own sessions: by bucket and over the range.
private func grouped(_ rows: [(agent: String, machine: String, project: String, bucket: Int, sessions: Int, tokens: Int)]) -> Hermesh_Local_V1_QueryWorkResponse {
    .with { r in
        for x in rows {
            let g = Hermesh_Local_V1_WorkGroup.with { $0.agent = x.agent; $0.machineID = x.machine; $0.projectID = x.project }
            r.sessions.append(.with { $0.bucket = UInt32(x.bucket); $0.group = g; $0.sessions = UInt32(x.sessions) })
            r.turns.append(.with { $0.bucket = UInt32(x.bucket); $0.group = g; $0.inputTokens = UInt64(x.tokens) })
            r.rangeSessions.append(.with { $0.group = g; $0.sessions = UInt32(x.sessions) })
            r.rangeTurns.append(.with { $0.group = g; $0.inputTokens = UInt64(x.tokens) })
        }
    }
}

private func parts(_ res: Hermesh_Local_V1_QueryWorkResponse?, _ group: UsageGroup, _ filter: UsageFilter = UsageFilter(), period: WorkPeriod = .week, drawn: Int = 4) -> UsageParts? {
    usageParts(res, period: period, group: group, filter: filter, agents: agentOrder, machines: machineOrder, drawn: drawn)
}

@Test func thePageIsAskedGroupedAsItsBarsAre() {
    let f = UsageFilter(agent: "claude")
    #expect(usagePageRequest(.week, filter: f, group: .agent, now: today, calendar: cal).groupBy == [.agent])
    #expect(usagePageRequest(.week, filter: f, group: .place, now: today, calendar: cal).groupBy == [.machine, .project])
    let total = usagePageRequest(.week, filter: f, group: .total, now: today, calendar: cal)
    #expect(total.groupBy.isEmpty && total.previous && total.filter.agent == "claude")
}

@Test func groupedByAgentEachAgentsWorkByDayAddsUpToThePages() {
    let res = grouped([("codex", "", "", 0, 1, 100), ("gemini", "", "", 6, 3, 50), ("codex", "", "", 6, 1, 400)])
    let p = parts(res, .agent)!
    #expect(p.by == .agent)
    #expect(p.all.map { "\($0.id) \($0.sessions) \($0.tokens) \($0.color)" } == ["gemini 3 50 2", "codex 2 500 1"], "the busiest first, each in its own colour")
    #expect(p.drawn == p.all && p.folded == 0)
    #expect(p.all[1].buckets[0] == .init(sessions: 1, tokens: 100) && p.all[1].buckets[6] == .init(sessions: 1, tokens: 400))
    let page = usagePage(res, period: .week, now: today, calendar: cal)
    for i in page.buckets.indices { #expect(p.all.reduce(0) { $0 + $1.buckets[i].sessions } == page.buckets[i].sessions, "a bucket's parts are the bucket") }
    #expect(parts(res, .total) == nil)
    #expect(parts(nil, .agent) == UsageParts(by: .agent))
    #expect(parts(grouped([("claude", "", "", 15, 1, 9)]), .agent, period: .today)!.all[0].buckets[15].sessions == 1, "today by hour")
}

@Test func groupedByPlaceTheEnvironmentsOrOneEnvironmentsProjects() {
    let res = grouped([("", "vps", "app", 6, 2, 20), ("", "mac", "app", 6, 1, 10), ("", "mac", "blog", 5, 3, 30)])
    let machines = parts(res, .place)!
    #expect(machines.by == .machine && machines.all.map { "\($0.id) \($0.sessions) \($0.color)" } == ["mac 4 0", "vps 2 1"])
    // The filter on mac answers only mac's rows; its projects are the parts.
    let mac = grouped([("", "mac", "app", 6, 1, 10), ("", "mac", "blog", 5, 3, 30)])
    let projects = parts(mac, .place, UsageFilter(machineID: "mac"))!
    #expect(projects.by == .project && projects.machineID == "mac")
    #expect(projects.all.map { "\($0.id) \($0.sessions) \($0.color)" } == ["blog 3 1", "app 1 0"], "projects coloured by name")
    let only = parts(mac, .place)!
    #expect(only.by == .project && only.machineID == "mac", "only one environment worked: its projects, unasked")
}

@Test func aColourFollowsItsPartAndPastWhatCanBeDrawnTheLeastBusyAreOther() {
    let both = parts(grouped([("claude", "", "", 0, 1, 1), ("codex", "", "", 0, 5, 1)]), .agent)!
    let alone = parts(grouped([("codex", "", "", 0, 5, 1)]), .agent)!
    #expect(both.all.first { $0.id == "codex" }!.color == alone.all[0].color, "codex's colour is the same with and without claude")
    // An agent past the colours, or not on the list, among those drawn: each takes its place among them.
    let late = parts(grouped([("qwen", "", "", 0, 1, 1), ("codex", "", "", 0, 2, 1), ("hermit", "", "", 0, 3, 1)]), .agent)!
    #expect(late.all.map { "\($0.id) \($0.color)" } == ["hermit 2", "codex 0", "qwen 1"])
    // Eight agents on the phone: the three busiest in colour, the rest as Other; every one still listed.
    let eight = parts(grouped(agentOrder.enumerated().map { ($0.element, "", "", 0, 10 - $0.offset, 1) }), .agent)!
    #expect(eight.drawn.map(\.id) == ["claude", "codex", "gemini", UsagePart.other] && eight.drawn.map(\.color) == [0, 1, 2, -1])
    #expect(eight.drawn[3].sessions == 7 + 6 + 5 + 4 + 3 && eight.drawn[3].tokens == 5 && eight.drawn[3].buckets[0].sessions == 25)
    #expect(eight.all.count == 8 && eight.folded == 5 && eight.all[3...].allSatisfy { $0.color == -1 }, "those among Other have no colour of their own")
    // Four are drawn each in its colour; the Mac draws six.
    #expect(parts(grouped(agentOrder.prefix(4).map { ($0, "", "", 0, 1, 1) }), .agent)!.drawn.map { "\($0.id) \($0.color)" } == ["antigravity 3", "claude 0", "codex 1", "gemini 2"])
    #expect(parts(grouped(agentOrder.enumerated().map { ($0.element, "", "", 0, 10 - $0.offset, 1) }), .agent, drawn: 6)!.drawn.map(\.id) == ["claude", "codex", "gemini", "antigravity", "cursor", UsagePart.other])
}
