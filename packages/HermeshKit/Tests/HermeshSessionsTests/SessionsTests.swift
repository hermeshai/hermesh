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
    #expect(!SessionListLayout().metadata, "titles alone until the owner shows the metadata")
    let shown = sessionSections(all, view: SessionListLayout(), archived: true, now: now, calendar: utc)
    #expect(shown.last?.grouping == nil && shown.last?.sessions.map(\.sessionID) == ["arch"])
    let oldest = sessionSections(all, view: SessionListLayout(newestFirst: false), archived: false, now: now, calendar: utc)
    #expect(oldest.map(\.key) == ["attention", "active", "done"] && oldest[0].sessions.map(\.sessionID) == ["w", "crash"])
}

@Test func runningSessionsStayOrderedByCreationAndRespectManualAndPinnedOrder() throws {
    let rOld = session("rOld", .running, updated: -1, created: -600)
    var rMid = session("rMid", .running, updated: -50, created: -300)
    let rNew = session("rNew", .running, updated: -55, created: -60)
    let dOld = session("dOld", .idle, updated: -10, created: -5000)
    var dEarly = session("dEarly", .idle, updated: -400, created: -200)

    let secs = sessionSections([rOld, rMid, rNew, dOld, dEarly], view: SessionListLayout(), archived: false, now: now, calendar: utc)
    #expect(secs.map { "\($0.key):\($0.sessions.map(\.sessionID).joined(separator: ","))" } == ["active:rNew,rMid,rOld", "done:dOld,dEarly"])

    let dragged = reorderIDs(["rNew", "rMid", "rOld"], movedID: "rOld", slot: 0)
    #expect(dragged == ["rOld", "rNew", "rMid"])
    let brandNew = session("rBrandNew", .running, updated: -5, created: -5)
    let ancient = session("rAncient", .running, updated: -2, created: -86400)
    let withDrag = sessionSections([rOld, rMid, rNew, brandNew, ancient], view: SessionListLayout(), archived: false, now: now, activeOrder: dragged, calendar: utc)
    #expect(withDrag[0].sessions.map(\.sessionID) == ["rBrandNew", "rOld", "rNew", "rMid", "rAncient"])

    dEarly.pinned = true
    rMid.pinned = true
    let withPin = sessionSections([rOld, rMid, rNew, dOld, dEarly], view: SessionListLayout(), archived: false, now: now, pinnedOrder: ["dEarly", "rMid"], calendar: utc)
    #expect(withPin.map { "\($0.key):\($0.sessions.map(\.sessionID).joined(separator: ","))" } == ["pinned:dEarly,rMid", "active:rNew,rOld", "done:dOld"])
    #expect(withPin[0].id == "pinned:pinned")
    #expect(sessionSections([dEarly], view: SessionListLayout(), archived: false, now: now, alwaysActive: true, calendar: utc).map(\.key) == ["pinned", "active"])

    // Legacy saved JSON without `sort` decodes cleanly to `.default`.
    let legacy = try JSONDecoder().decode(SessionListLayout.self, from: Data(#"{"group":"project","newestFirst":false,"metadata":true}"#.utf8))
    #expect(legacy == SessionListLayout(group: .project, sort: .default, newestFirst: false, metadata: true))
}

@Test func theMenuKeepsTheRunningSectionWhileNothingRuns() {
    var waiting = session("w", .waitingInput)
    ask(&waiting)
    let done = session("d", .idle)
    func keep(_ list: [Session], _ view: SessionListLayout = SessionListLayout()) -> [String] {
        sessionSections(list, view: view, archived: true, now: now, alwaysActive: true, calendar: utc).map { "\($0.key):\($0.sessions.map(\.sessionID).joined(separator: ","))" }
    }
    #expect(keep([done]) == ["active:", "done:d"])
    #expect(keep([waiting, done]) == ["attention:w", "active:", "done:d"])
    #expect(keep([]).isEmpty, "no session at all: no section")
    #expect(keep([session("a", .archived)]) == ["archived:a"])
    #expect(keep([done], SessionListLayout(group: .agent)) == ["claude:d"])
    #expect(sessionSections([done], view: SessionListLayout(), archived: false, now: now, calendar: utc).map(\.key) == ["done"])
}

@Test func theRailListsTheAgentsAtWork() {
    var w = session("w", .waitingInput, agent: "codex")
    ask(&w)
    let team = teamState([session("r1", .running, agent: "claude"), w, session("r2", .running, agent: "codex"), session("d", .idle, agent: "gemini")], requests: 0)
    #expect(liveByAgent(team).map { "\($0.agent):\($0.sessions.map(\.sessionID).joined(separator: ","))" } == ["codex:w,r2", "claude:r1"])
}

@Test func byDateTheDaysFollowTheSort() {
    let all = [session("today", .idle, updated: -3600), session("yday", .idle, updated: -86400), session("week", .idle, updated: -4 * 86400),
               session("old", .idle, updated: -30 * 86400)]
    #expect(sessionSections(all, view: SessionListLayout(group: .date), archived: false, now: now, calendar: utc).map(\.key) == ["today", "yesterday", "week", "earlier"])
    #expect(sessionSections(all, view: SessionListLayout(group: .date, newestFirst: false), archived: false, now: now, calendar: utc).map(\.key)
        == ["earlier", "week", "yesterday", "today"])
}

@Test func byProjectByAgentAndByMachine() {
    let all = [session("a", .idle, updated: -10, project: "web", agent: "gemini", machine: "m2"), session("b", .idle, updated: -20, project: "api", agent: "claude", machine: "m1"),
               session("c", .idle, updated: -30, project: "web", agent: "codex", machine: "m3")]
    let projects = sessionSections(all, view: SessionListLayout(group: .project), archived: false, now: now)
    #expect(projects.map(\.key) == ["api", "web"] && projects[1].sessions.map(\.sessionID) == ["a", "c"])
    #expect(sessionSections(all, view: SessionListLayout(group: .agent), archived: false, now: now).map(\.key) == ["claude", "codex", "gemini"])
    #expect(sessionSections(all, view: SessionListLayout(group: .machine), archived: false, now: now, machines: ["m2", "m1"]).map(\.key) == ["m2", "m1", "m3"])
}

@Test func entitySectionsFoldPastFiveWhileStatusDateAndPinnedStayVisible() {
    let ten = (1 ... 10).map { i in session("s\(i)", .idle, updated: Double(-i * 10)) }
    // Status, date and archived never fold.
    for g: SessionGrouping? in [.status, .date, nil] {
        let s = sliceSectionSessions(ten, grouping: g)
        #expect(s.visible.count == 10 && s.hiddenCount == 0)
    }
    // Six rows do not fold for a single orphan row; seven fold to five + two.
    let six = Array(ten.prefix(6))
    #expect(sliceSectionSessions(six, grouping: .project).visible.count == 6)
    #expect(sliceSectionSessions(six, grouping: .project).hiddenCount == 0)
    let seven = Array(ten.prefix(7))
    let folded7 = sliceSectionSessions(seven, grouping: .project)
    #expect(folded7.visible.map(\.sessionID) == ["s1", "s2", "s3", "s4", "s5"] && folded7.hiddenCount == 2)

    // Entity groupings (project, agent, machine) all fold to 5, and +10 reveals all 10.
    for g: SessionGrouping in [.project, .agent, .machine] {
        let f = sliceSectionSessions(ten, grouping: g)
        #expect(f.visible.count == sectionInitialLimit && f.hiddenCount == 5)
        let expanded = sliceSectionSessions(ten, grouping: g, limit: sectionInitialLimit + sectionExpandStep)
        #expect(expanded.visible.count == 10 && expanded.hiddenCount == 0)
    }

    // Selected, running, waiting and failed sessions in the tail stay visible in place.
    var mixed = ten
    mixed[6].status = .running
    ask(&mixed[8])
    mixed[8].status = .waitingInput
    let pinned = sliceSectionSessions(mixed, grouping: .project, selectedID: "s10")
    #expect(pinned.visible.map(\.sessionID) == ["s1", "s2", "s3", "s4", "s5", "s7", "s9", "s10"])
    #expect(pinned.hiddenCount == 2)

    // If pinning leaves only 1 hidden idle row, the orphan guard reveals all rows.
    let sevenWithRunningTail = Array(mixed.prefix(7))
    let noOrphan = sliceSectionSessions(sevenWithRunningTail, grouping: .project)
    #expect(noOrphan.visible.count == 7 && noOrphan.hiddenCount == 0)
}

@Test func aFailedTurnNeedsTheOwnerAnInterruptedOneDoesNot() {
    var f = session("f", .idle)
    f.progress.lastFailure.kind = .contextOverflow
    #expect(f.isFailed && f.needsAttention && statusOf(f) == .attention)
    for active: SessionStatus in [.queued, .preparing, .running] {
        f.status = active
        #expect(!f.isFailed && !f.needsAttention && statusOf(f) == .active)
    }
    let resumedTeam = teamState([f], requests: 0)
    #expect(resumedTeam.failed.isEmpty && resumedTeam.running.map(\.sessionID) == ["f"] && resumedTeam.tag == .working(1))
    #expect(liveByAgent(resumedTeam).map { "\($0.agent):\($0.sessions.map(\.sessionID).joined(separator: ","))" } == ["claude:f"])
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

@Test func aMachineBeingReadAgainKeepsTheStateItWasShownWith() {
    func machine(_ id: String, agents: [String]? = nil, syncing: Bool = false, online: Bool = true) -> Hermesh_Local_V1_Machine {
        var m = Hermesh_Local_V1_Machine()
        m.machineID = id
        m.online = online
        m.syncing = syncing
        if let agents {
            m.status.agents = agents.map { var a = Hermesh_Machine_V1_AgentStatus(); a.name = $0; a.available = true; return a }
        }
        return m
    }
    // Shown before the app left the front (or kept on the phone): m1 with Claude
    // Code, m2 with Codex, m3 with Gemini CLI.
    let known = [machine("m1", agents: ["claude"]), machine("m2", agents: ["codex"]), machine("m3", agents: ["gemini"])]
    // The watch's first message: m1 being read again, m2 read and now with
    // Claude Code too, m3 offline; m4 new, being read.
    let out = holdingState([machine("m1", syncing: true), machine("m2", agents: ["codex", "claude"]), machine("m3", online: false),
                            machine("m4", syncing: true)], known: known)
    #expect(out.map { $0.status.agents.map(\.name) } == [["claude"], ["codex", "claude"], [], []])
    #expect(out[0].syncing && !out[3].hasStatus)
    // Without the relay, whether m3 is online is not known: keep what was last shown.
    let offlineOut = holdingState([machine("m3", online: false)], known: known, relayConnected: false)
    #expect(offlineOut.map { $0.status.agents.map(\.name) } == [["gemini"]])
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

@Test func aMissingBaseRefExplainsHowToRetryTheOriginalTask() {
    var failure = Failure()
    failure.kind = .baseRefUnavailable
    failure.message = "Project movieclaw: base ref ios-v0.12.2 unavailable."
    #expect(failureHeadline(failure.kind, agent: "Codex") == "The worktree’s base ref was not found")
    #expect(failureAdvice(failure) == failure.message + " Check the base ref name, and that the branch or tag is on the project’s remote (the machine fetches it from there). Then send another message in this task to retry the original request.")
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

@Test func theTeamsStateSaysWhatTheNameTagShows() {
    var asked = session("w", .waitingInput, updated: -600, agent: "codex")
    ask(&asked)
    var failed = session("f", .idle, updated: -300)
    failed.progress.lastFailure.kind = .auth
    let run = session("r", .running, created: -50, agent: "claude"), run2 = session("r2", .queued, created: -20, agent: "claude")
    let done = session("d", .idle, updated: -10), archived = session("a", .archived)

    #expect(teamState([done, archived], requests: 0).tag == .quiet)
    #expect(teamState([done, run, run2], requests: 0).tag == .working(2))
    #expect(teamState([run, failed], requests: 0).tag == .failed(1), "a failure shows before what runs")
    #expect(teamState([run, failed, asked], requests: 0).tag == .waiting(agent: "codex"), "one question names its agent")
    #expect(teamState([asked], requests: 1).tag == .needYou(2))
    #expect(teamState([], requests: 1).tag == .needYou(1), "a request to connect alone has no agent to name")

    let t = teamState([done, archived, run, run2, failed, asked], requests: 0)
    #expect(t.waiting.map(\.sessionID) == ["w"] && t.failed.map(\.sessionID) == ["f"])
    #expect(t.running.map(\.sessionID) == ["r2", "r"], "the newest first")
    #expect(t.finished.map(\.sessionID) == ["d"], "archived sessions are no part of it")
    #expect(t.runningAgents == ["claude"])
}

@Test func aQuestionWithOneChoiceIsAnsweredWhereItIsListed() {
    func question(_ change: (inout Hermesh_Session_V1_QuestionItem) -> Void = { _ in }, items: Int = 1) -> Question {
        var item = Hermesh_Session_V1_QuestionItem()
        item.id = "i"
        item.options = ["pnpm", "npm"].map { l in var o = Hermesh_Session_V1_QuestionOption(); o.label = l; return o }
        change(&item)
        var q = Question()
        q.items = Array(repeating: item, count: items)
        return q
    }
    #expect(quickChoice(question())?.options.map(\.label) == ["pnpm", "npm"])
    #expect(quickChoice(question { $0.multiSelect = true }) == nil)
    #expect(quickChoice(question { $0.secret = true }) == nil)
    #expect(quickChoice(question { $0.options = [] }) == nil)
    #expect(quickChoice(question(items: 2)) == nil)
    #expect(quickChoice(question(items: 0)) == nil)
    #expect(quickChoice(question { $0.optional = true })?.id == "i", "a single optional choice still works")
    #expect(quickChoice(question({ $0.optional = true }, items: 2)) == nil, "several optional choices need the full form")

    var withNotes = question()
    let note = Hermesh_Session_V1_QuestionItem.with {
        $0.id = "note"
        $0.text = "Anything else?"
        $0.optional = true
    }
    withNotes.items.append(note)
    #expect(quickChoice(withNotes)?.options.map(\.label) == ["pnpm", "npm"], "optional notes can be left blank")
    withNotes.items.reverse()
    #expect(quickChoice(withNotes)?.options.map(\.label) == ["pnpm", "npm"], "the choice need not be first")
    #expect(quickChoice(withNotes)?.id == "i", "send the answer to the choice, not the first item")
    withNotes.items[0].optional = false
    #expect(quickChoice(withNotes) == nil, "another required answer needs the full form")
}

@Test func questionStepsAndRecommendedLabels() {
    #expect(parseOptionLabel("(Recommended) 一次性完整落地") == ParsedOptionLabel(recommended: true, text: "一次性完整落地"))
    #expect(parseOptionLabel("（推荐）零配置自动选择") == ParsedOptionLabel(recommended: true, text: "零配置自动选择"))
    #expect(parseOptionLabel("分两阶段落地") == ParsedOptionLabel(recommended: false, text: "分两阶段落地"))

    let items: [Hermesh_Session_V1_QuestionItem] = [
        .with { $0.id = "q1"; $0.text = "第一项" },
        .with { $0.id = "q2"; $0.text = "第二项" },
        .with { $0.id = "q3"; $0.text = "第三项"; $0.optional = true },
    ]
    var picked: [String: Set<String>] = [:]
    var text: [String: String] = [:]

    #expect(initialQuestionStep(items, picked: picked, text: text) == 0)
    applyQuestionChoice(items[0], label: "(Recommended) A", picked: &picked, text: &text)
    #expect(nextAutoQuestionStep(items, picked: picked, text: text, from: 0) == 1)
    #expect(!isQuestionReady(items, picked: picked, text: text))

    applyQuestionChoice(items[1], label: "B", picked: &picked, text: &text)
    #expect(nextAutoQuestionStep(items, picked: picked, text: text, from: 1) == nil, "trailing optional item does not hijack auto-advance")
    #expect(nextManualQuestionStep(items, picked: picked, text: text, from: 1) == 2)
    #expect(isQuestionReady(items, picked: picked, text: text))

    applyQuestionCustomText(items[0], value: "自定义方案", picked: &picked, text: &text)
    #expect(picked["q1"]?.isEmpty == true)
    #expect(text["q1"] == "自定义方案")
}
