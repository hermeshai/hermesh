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

@Test func bucketsByStateThenByDay() {
    var waiting = session("w", .waitingInput)
    ask(&waiting)
    let all = [session("today", .idle, updated: -3600), session("yday", .idle, updated: -86400), session("week", .idle, updated: -4 * 86400),
               session("old", .idle, updated: -30 * 86400), session("run", .running), session("crash", .crashed, updated: -120), waiting,
               session("arch", .archived, updated: -100)]
    let secs = sessionSections(all, filter: SessionFilter(), now: now, calendar: utc)
    #expect(secs.map(\.bucket) == [.attention, .active, .today, .yesterday, .week, .earlier])
    #expect(secs[0].sessions.map(\.sessionID) == ["w", "crash"]) // a question comes before a failure
    #expect(sessionSections(all, filter: SessionFilter(archived: true), now: now, calendar: utc).last?.sessions.map(\.sessionID) == ["arch"])
}

@Test func aFailedTurnNeedsTheOwnerAnInterruptedOneDoesNot() {
    var f = session("f", .idle)
    f.progress.lastFailure.kind = .contextOverflow
    #expect(f.isFailed && f.needsAttention && bucketOf(f, now: now, calendar: utc) == .attention)
    var i = session("i", .idle)
    i.progress.lastFailure.kind = .interrupted
    #expect(!i.isFailed && !i.needsAttention && bucketOf(i, now: now, calendar: utc) == .today)
}

@Test func runningSortsNewestFirstAndTheFilterNarrows() {
    let secs = sessionSections([session("a", .running, created: -600), session("b", .running, created: -60)], filter: SessionFilter(), now: now, calendar: utc)
    #expect(secs[0].sessions.map(\.sessionID) == ["b", "a"])
    let all = [session("a", .idle, by: "c1", project: "app", agent: "claude", machine: "m1"), session("b", .idle, by: "c2", project: "api", agent: "codex", machine: "m2")]
    for f in [SessionFilter(grant: "c2"), SessionFilter(project: "api"), SessionFilter(agent: "codex"), SessionFilter(machine: "m2")] {
        #expect(sessionSections(all, filter: f, now: now, calendar: utc).flatMap(\.sessions).map(\.sessionID) == ["b"])
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
}

@Test func searchRanksWhatNeedsTheOwnerFirst() {
    var w = session("w", .waitingInput, updated: -900)
    ask(&w)
    let all = [session("i", .idle, updated: -10), session("r", .running, updated: -500), w, session("x", .archived)]
    #expect(searchSessions(all, query: "", filter: SessionFilter(), text: { $0.projectID }).map(\.sessionID) == ["w", "r", "i"])
    #expect(searchSessions(all, query: " APP ", filter: SessionFilter(), text: { $0.projectID }).count == 3)
    #expect(searchSessions(all, query: "nothing", filter: SessionFilter(), text: { $0.projectID }).isEmpty)
}

@Test func todayCountsDoneAndFailed() {
    var f = session("f", .idle)
    f.progress.lastFailure.kind = .network
    let t = todayStats([session("d", .idle), f, session("r", .running), session("old", .idle, updated: -40 * 86400)], now: now, calendar: utc)
    #expect(t == TodayStats(sessions: 3, done: 1, failed: 1))
}

@Test func turnsAndQuestions() {
    var s = session("s", .waitingInput)
    s.progress.turnCount = 2
    ask(&s)
    #expect(s.currentTurn == 3 && s.pendingQuestion?.questionID == "q1")
    s.status = .idle
    #expect(s.currentTurn == 2 && s.pendingQuestion == nil)
}
