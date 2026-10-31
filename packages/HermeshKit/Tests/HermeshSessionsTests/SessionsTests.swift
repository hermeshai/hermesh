import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshSessions

let now = Date(timeIntervalSince1970: 1_792_652_400) // Thursday 2026-10-22, 18:20 in Shanghai
var utc: Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "Asia/Shanghai")!
    return c
}

func ts(_ offset: Double) -> Google_Protobuf_Timestamp { Google_Protobuf_Timestamp(date: now.addingTimeInterval(offset)) }

func session(_ id: String, _ status: SessionStatus, updated: Double = -60, created: Double? = nil, by: String = "c1", project: String = "app",
             agent: String = "claude", machine: String = "m1") -> Session {
    var s = Session()
    s.sessionID = id
    s.status = status
    s.updatedAt = ts(updated)
    s.createdAt = ts(created ?? updated)
    s.createdBy = by
    s.projectID = project
    s.agent = agent
    s.machineID = machine
    return s
}

func ask(_ s: inout Session) {
    var q = Question()
    q.questionID = "q1"
    s.progress.pendingQuestion = q
}

@Test func byStatusByDefault() {
    var waiting = session("w", .waitingInput, updated: -600)
    ask(&waiting)
    let all = [session("today", .idle, updated: -3600), session("old", .idle, updated: -30 * 86400), session("run", .running),
               session("crash", .crashed, updated: -120), waiting, session("arch", .archived, updated: -100)]
    let secs = sessionSections(all, view: SessionListLayout(), archived: false, now: now, calendar: utc)
    #expect(secs.map(\.key) == ["attention", "active", "done"])
    #expect(secs[0].sessions.map(\.sessionID) == ["crash", "w"]) // by last activity
    #expect(secs[0].counted && secs[1].counted && !secs[2].counted)
    let shown = sessionSections(all, view: SessionListLayout(), archived: true, now: now, calendar: utc)
    #expect(shown.last?.grouping == nil && shown.last?.sessions.map(\.sessionID) == ["arch"])
    let oldest = sessionSections(all, view: SessionListLayout(newestFirst: false), archived: false, now: now, calendar: utc)
    #expect(oldest.map(\.key) == ["attention", "active", "done"] && oldest[0].sessions.map(\.sessionID) == ["w", "crash"])
}

@Test func byDateTheDaysFollowTheSort() {
    let all = [session("today", .idle, updated: -3600), session("yday", .idle, updated: -86400), session("week", .idle, updated: -4 * 86400),
               session("old", .idle, updated: -30 * 86400)]
    #expect(sessionSections(all, view: SessionListLayout(group: .date), archived: false, now: now, calendar: utc).map(\.key) == ["today", "yesterday", "week", "earlier"])
    #expect(sessionSections(all, view: SessionListLayout(group: .date, newestFirst: false), archived: false, now: now, calendar: utc).map(\.key)
        == ["earlier", "week", "yesterday", "today"])
}

@Test func byAgentAndByMachine() {
    let all = [session("a", .idle, agent: "gemini", machine: "m2"), session("b", .idle, agent: "claude", machine: "m1"),
               session("c", .idle, agent: "codex", machine: "m3")]
    #expect(sessionSections(all, view: SessionListLayout(group: .agent), archived: false, now: now).map(\.key) == ["claude", "codex", "gemini"])
    #expect(sessionSections(all, view: SessionListLayout(group: .machine), archived: false, now: now, machines: ["m2", "m1"]).map(\.key) == ["m2", "m1", "m3"])
}

@Test func aFailedTurnNeedsTheOwnerAnInterruptedOneDoesNot() {
    var f = session("f", .idle)
    f.progress.lastFailure.kind = .contextOverflow
    #expect(f.isFailed && f.needsAttention && statusOf(f) == .attention)
    var i = session("i", .idle)
    i.progress.lastFailure.kind = .interrupted
    #expect(!i.isFailed && !i.needsAttention && statusOf(i) == .done && dayOf(i, now: now, calendar: utc) == .today)
}

@Test func theFilterNarrows() {
    let all = [session("a", .idle, by: "c1", project: "app", agent: "claude", machine: "m1"), session("b", .idle, by: "c2", project: "api", agent: "codex", machine: "m2")]
    for f in [SessionFilter(grant: "c2"), SessionFilter(project: "api"), SessionFilter(agent: "codex"), SessionFilter(machine: "m2")] {
        #expect(all.filter(f.matches).map(\.sessionID) == ["b"])
        #expect(f.narrowed && f.any)
    }
    #expect(!SessionFilter().any && SessionFilter(archived: true).any && !SessionFilter(archived: true).narrowed)
}

@Test func mergeKeepsTheLaterState() {
    var old = session("a", .running)
    old.progress.lastEventSeq = 5
    var newer = old
    newer.progress.lastEventSeq = 7
    newer.status = .idle
    #expect(mergeSessions([old], [newer]).map(\.status) == [.idle])
    #expect(mergeSessions([newer], [old]).map(\.status) == [.idle])
    #expect(mergeSessions([old], [session("b", .idle)]).map(\.sessionID) == ["a", "b"])
    #expect(mergeSessions([old, session("b", .idle)], [], deleted: ["a"]).map(\.sessionID) == ["b"])
}

@Test func searchRanksWhatNeedsTheOwnerFirst() {
    var w = session("w", .waitingInput, updated: -900)
    ask(&w)
    let all = [session("i", .idle, updated: -10), session("r", .running, updated: -500), w, session("x", .archived)]
    #expect(searchSessions(all, query: "", filter: SessionFilter(), text: { $0.projectID }).map(\.sessionID) == ["w", "r", "i"])
    #expect(searchSessions(all, query: " APP ", filter: SessionFilter(), text: { $0.projectID }).count == 3)
    #expect(searchSessions(all, query: "nothing", filter: SessionFilter(), text: { $0.projectID }).isEmpty)
}

@Test func rangeWorkAddsUpBucketsAgentsAndPairs() {
    func group(_ client: String, _ agent: String) -> Hermesh_Local_V1_WorkGroup {
        var g = Hermesh_Local_V1_WorkGroup()
        g.client = client
        g.agent = agent
        return g
    }
    func sessions(_ b: UInt32, _ by: String, _ agent: String, _ n: UInt32, done: UInt32 = 0) -> Hermesh_Local_V1_SessionCount {
        var x = Hermesh_Local_V1_SessionCount()
        x.bucket = b
        x.group = group(by, agent)
        x.sessions = n
        x.done = done
        return x
    }
    func turns(_ by: String, _ agent: String, _ n: UInt32, tokens: UInt64) -> Hermesh_Local_V1_TurnCount {
        var x = Hermesh_Local_V1_TurnCount()
        x.group = group(by, agent)
        x.turns = n
        x.inputTokens = tokens
        return x
    }
    var r = Hermesh_Local_V1_QueryWorkResponse()
    r.sessions = [sessions(9, "c_bot", "claude", 2), sessions(9, "c_cursor", "claude", 1), sessions(9, "c_bot", "codex", 1), sessions(14, "c_bot", "codex", 1)]
    r.rangeSessions = [sessions(0, "c_bot", "claude", 2, done: 1), sessions(0, "c_bot", "codex", 1, done: 1), sessions(0, "c_cursor", "claude", 1)]
    // Grok asked for a turn in a session bot started: its own pair, turns and no sessions.
    r.rangeTurns = [turns("c_bot", "claude", 5, tokens: 1000), turns("c_bot", "codex", 2, tokens: 300), turns("c_cursor", "claude", 1, tokens: 50), turns("c_grok", "claude", 1, tokens: 7)]
    let a = RangeWork(period: .today, firstDay: "2026-03-04", r)
    #expect(a.buckets.count == 24 && a.buckets[9] == 4 && a.buckets[14] == 1 && a.buckets.reduce(0, +) == 5)
    #expect(a.byAgent["claude"]?[9] == 3 && a.byAgent["codex"]?[9] == 1 && a.byAgent["codex"]?[14] == 1 && a.byAgent["claude"]?[14] == 0)
    #expect(a.sessions == 4 && a.done == 2 && a.turns == 9 && a.tokens == 1357)
    #expect(a.pairs.count == 4 && a.pairs[0] == .init(client: "c_bot", agent: "claude", sessions: 2, turns: 5, tokens: 1000))
    #expect(a.pairs[3] == .init(client: "c_grok", agent: "claude", sessions: 0, turns: 1, tokens: 7))
    #expect(a.turns(agent: "claude") == 7 && a.sessions(agent: "claude") == 3 && a.turns(agent: "codex") == 2)
    // Seven days: a bucket a day.
    #expect(RangeWork(period: .week, firstDay: "2026-03-04", Hermesh_Local_V1_QueryWorkResponse()).buckets.count == 7)
}

@Test func workPeriodsAsk() {
    let now = utc.date(from: DateComponents(year: 2026, month: 3, day: 10, hour: 15))!
    let today = WorkPeriod.today.request(now, calendar: utc), week = WorkPeriod.week.request(now, calendar: utc)
    #expect(today.firstDay == "2026-03-10" && today.days == 1 && today.bucket == .hour && today.timeZone == utc.timeZone.identifier)
    #expect(week.firstDay == "2026-03-04" && week.days == 7 && week.bucket == .day && week.groupBy == [.client, .agent])
    #expect(WorkPeriod.month.request(now, calendar: utc).firstDay == "2026-02-09")
}

@Test func workFlowKeepsTheBusiestAndJoinsTheRest() {
    func pair(_ by: String, _ agent: String, _ turns: Int, sessions: Int = 1) -> RangeWork.Pair {
        .init(client: by, agent: agent, sessions: sessions, turns: turns, tokens: 0)
    }
    let f = WorkFlow([pair("claude", "cc", 30), pair("claude", "cx", 6), pair("grok", "cc", 16), pair("grok", "cx", 12), pair("old", "", 9)])
    #expect(f.clients.map(\.id) == ["claude", "grok"] && f.clients.map(\.turns) == [36, 28])
    #expect(f.agents.map(\.id) == ["cc", "cx"] && f.agents.map(\.turns) == [46, 18])
    #expect(f.bands.count == 4 && f.bands.reduce(0) { $0 + $1.turns } == 64, "a machine that doesn't say the agent is left out")

    // Four clients: the busiest two, then the other two together; a client
    // whose session hasn't ended a turn yet still counts, after those that did.
    let g = WorkFlow([pair("a", "cc", 9), pair("b", "cc", 5), pair("c", "cc", 1), pair("d", "cx", 0, sessions: 2), pair("a", "cx", 1)])
    #expect(g.clients.map(\.id) == ["a", "b", ""] && g.clients.map(\.turns) == [10, 5, 1] && g.clients.last?.count == 2)
    #expect(g.bands.contains(.init(client: "", agent: "cc", turns: 1)) && g.bands.contains(.init(client: "", agent: "cx", turns: 0)))
    #expect(WorkFlow([]).clients.isEmpty && WorkFlow([]).bands.isEmpty)

    // A band joined from several pairs adds up their tokens too (what tapping it says).
    let h = WorkFlow([.init(client: "a", agent: "cc", sessions: 1, turns: 2, tokens: 300), .init(client: "b", agent: "cc", sessions: 1, turns: 1, tokens: 200),
                      .init(client: "c", agent: "cc", sessions: 1, turns: 1, tokens: 50), .init(client: "d", agent: "cc", sessions: 1, turns: 1, tokens: 25)])
    #expect(h.bands.first { $0.client == "" }.map { ($0.turns, $0.tokens) } ?? (0, 0) == (2, 75))
}

@Test func turnsAndQuestions() {
    var s = session("s", .waitingInput)
    s.progress.turnCount = 2
    ask(&s)
    #expect(s.currentTurn == 3 && s.pendingQuestion?.questionID == "q1")
    s.status = .idle
    #expect(s.currentTurn == 2 && s.pendingQuestion == nil)
}

@Test func stateUnknownOnAnUnreachableMachine() {
    var asking = session("q", .waitingInput, machine: "m2")
    ask(&asking)
    let all = [session("r", .running, machine: "m2"), asking, session("a", .archived, machine: "m2"), session("here", .running, machine: "m1")]
    #expect(withKnownState(all, unreachable: []).map(\.status) == all.map(\.status))
    let shown = withKnownState(all, unreachable: ["m2"])
    #expect(shown.map(\.status) == [.unspecified, .unspecified, .archived, .running])
    #expect(!shown[0].isActive && !shown[1].needsAttention && !shown[1].isInterruptible && !shown[0].isArchivable)
    #expect(statusTitle(shown[0].status) == "State unknown")
}

@Test func aResetForgetsWhatWasDeletedMeanwhile() {
    // Held: what the app showed before it left (or was closed); an older
    // page of m1's; m2's, a machine being read again; m3's, offline.
    let held = [session("kept", .idle, updated: -60), session("gone", .idle, updated: -120), session("older", .idle, updated: -9000),
                session("m2s", .idle, updated: -90, machine: "m2"), session("m3s", .idle, updated: -30, machine: "m3")]
    var later = session("kept", .running, updated: -5)
    later.progress.lastEventSeq = 9
    var cursor = SessionCursor()
    cursor.updatedAt = ts(-3600)
    cursor.sessionID = "x"
    let out = resetSessions(held, [later, session("new", .idle, updated: -10)], next: cursor, readNow: ["m1"])
    #expect(Set(out.map(\.sessionID)) == ["kept", "new", "older", "m2s", "m3s"]) // m1 is read now, and "gone" is within its first page
    #expect(out.first { $0.sessionID == "kept" }?.status == .running)
    // Without a cursor the first page is all m1 has.
    #expect(!resetSessions(held, [], next: nil, readNow: ["m1"]).contains { $0.sessionID == "older" })
    // A machine not read now keeps all it had.
    #expect(resetSessions(held, [], next: nil, readNow: []).count == held.count)
}

@Test func aFailureIsToldOfTheCodingAgentEveryKindInItsOwnWords() {
    #expect(failureHeadline(.startFailed, agent: "Codex") == "Codex couldn’t start")
    let generic = failureHeadline(.unspecified, agent: "Codex")
    let kinds = FailureKind.allCases.filter { ![.unspecified, .unknown, .interrupted].contains($0) }
    #expect(kinds.filter { failureHeadline($0, agent: "Codex") == generic }.isEmpty)
    // Every one has its Chinese.
    let zh = Bundle.module.path(forResource: "zh-Hans", ofType: "lproj").flatMap(Bundle.init(path:))
    for k in kinds + [.unspecified] {
        let en = failureHeadline(k, agent: "%@")
        #expect(zh?.localizedString(forKey: en, value: nil, table: nil) != en, "no Chinese for \(en)")
    }
}

@Test func modelsReadAsTheirAgentNamesThem() {
    var astra = Hermesh_Machine_V1_AgentModel()
    astra.id = "gpt-6-astra"
    astra.name = "GPT-6 Astra"
    #expect(modelTitle("gpt-6-astra", [astra]) == "GPT-6 Astra")
    #expect(modelTitle("claude-opus-5-5") == "Opus 5.5")
    #expect(modelTitle("claude-haiku-5-5") == "Haiku 5.5")
    #expect(modelTitle("claude-fable-5-1[1m]") == "Fable 5.1 (1M)")
    #expect(modelTitle("claude-sonnet-4-20250514") == "Sonnet 4")
    #expect(modelTitle("claude-3-5-sonnet-20241022") == "claude-3-5-sonnet-20241022")
    #expect(modelTitle("gemini-3-pro") == "gemini-3-pro")
}
