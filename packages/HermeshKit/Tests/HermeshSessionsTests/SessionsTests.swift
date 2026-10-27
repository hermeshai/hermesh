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

@Test func dayActivityAddsUpHoursAgentsAndPairs() {
    func hour(_ h: UInt32, _ by: String, _ agent: String, _ n: UInt32) -> Hermesh_Session_V1_ActivityHour {
        var x = Hermesh_Session_V1_ActivityHour()
        x.hour = h
        x.createdBy = by
        x.agent = agent
        x.sessions = n
        return x
    }
    func client(_ by: String, _ agent: String, _ n: UInt32, _ done: UInt32, turns: UInt32 = 0, tokens: UInt64 = 0) -> Hermesh_Session_V1_ActivityClient {
        var x = Hermesh_Session_V1_ActivityClient()
        x.createdBy = by
        x.agent = agent
        x.sessions = n
        x.done = done
        x.turns = turns
        x.tokens = tokens
        return x
    }
    let a = DayActivity(date: "2026-03-04",
                        hours: [hour(9, "c_bot", "claude", 2), hour(9, "c_cursor", "claude", 1), hour(9, "c_bot", "codex", 1), hour(14, "c_bot", "codex", 1), hour(30, "c_bot", "claude", 9)],
                        clients: [client("c_bot", "claude", 2, 1, turns: 5, tokens: 1000), client("c_bot", "codex", 1, 1, turns: 2, tokens: 300),
                                  client("c_cursor", "claude", 1, 0, turns: 1, tokens: 50), client("c_bot", "claude", 1, 0, turns: 1)])
    #expect(a.hours.count == 24 && a.hours[9] == 4 && a.hours[14] == 1 && a.hours.reduce(0, +) == 5)
    #expect(a.byAgent["claude"]?[9] == 3 && a.byAgent["codex"]?[9] == 1 && a.byAgent["codex"]?[14] == 1 && a.byAgent["claude"]?[14] == 0)
    #expect(a.sessions == 5 && a.done == 2 && a.turns == 9 && a.tokens == 1350)
    // Two machines' answers for one client and agent are one pair.
    #expect(a.pairs.count == 3 && a.pairs[0] == .init(createdBy: "c_bot", agent: "claude", sessions: 3, turns: 6, tokens: 1000))
    #expect(a.turns(agent: "claude") == 7 && a.sessions(agent: "claude") == 4 && a.turns(agent: "codex") == 2)
}

@Test func workFlowKeepsTheBusiestAndJoinsTheRest() {
    func pair(_ by: String, _ agent: String, _ turns: Int, sessions: Int = 1) -> DayActivity.Pair {
        .init(createdBy: by, agent: agent, sessions: sessions, turns: turns, tokens: 0)
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
