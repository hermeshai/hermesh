import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshSessions

// Her's conversations and the work she sent from them (docs/her-conversations.md §3.4, §3.5).

private func herConv(_ id: String, _ status: SessionStatus, updated: Double = -60, created: Double? = nil) -> Session {
    session(id, status, updated: updated, created: created, by: "owner", project: "scratch", agent: "her", machine: "mHer")
}

private func sentWork(_ id: String, parent: String, _ status: SessionStatus, updated: Double = -60, project: String = "app", agent: String = "claude") -> Session {
    var s = session(id, status, updated: updated, by: "cHer", project: project, agent: agent)
    s.lineage = .with {
        $0.parentSessionID = parent
        $0.relationship = "her"
    }
    return s
}

@Test func herConversationAndParentDetection() {
    var sub = session("sub", .idle)
    sub.lineage = .with {
        $0.parentSessionID = "x"
        $0.relationship = "subagent"
    }
    #expect(herConv("h", .idle).isHerConversation)
    #expect(!sub.isHerConversation)
    #expect(sentWork("t", parent: "h", .idle).herParentID == "h")
    #expect(sub.herParentID == nil)
}

@Test func nestedByStatusWorkGoesWithItsConversation() {
    let done = herConv("plan", .idle, updated: -3000)
    let waits = sentWork("bench", parent: "plan", .waitingInput, updated: -60)
    let runs = sentWork("e2e", parent: "plan", .running, updated: -30)
    let quiet = herConv("week", .idle, updated: -86400)
    let other = session("react", .running)
    let all = [done, waits, runs, quiet, other]
    let secs = sessionSections(all, view: SessionListLayout(), archived: false, now: now, calendar: utc)
    #expect(secs.map(\.key) == ["attention", "active", "done"])
    #expect(secs[0].sessions.map(\.sessionID) == ["plan"])
    #expect(secs[1].sessions.map(\.sessionID) == ["react"])
    #expect(secs[2].sessions.map(\.sessionID) == ["week"])
    #expect(childrenOf(all, view: SessionListLayout())["plan"]?.map(\.sessionID) == ["e2e", "bench"])
}

@Test func treeStatusAndTreeMark() {
    let h = herConv("h", .idle)
    #expect(treeStatus(h, children: [sentWork("a", parent: "h", .idle)]) == .done)
    #expect(treeStatus(h, children: [sentWork("a", parent: "h", .running)]) == .active)
    #expect(treeStatus(h, children: [sentWork("a", parent: "h", .running), sentWork("b", parent: "h", .waitingInput)]) == .attention)
    #expect(treeMark(h, children: [sentWork("b", parent: "h", .waitingInput)]) == .waiting)
    #expect(treeMark(h, children: [sentWork("b", parent: "h", .crashed)]) == .failed)
    #expect(treeMark(h, children: [sentWork("b", parent: "h", .running)]) == nil)
}

@Test func nestedByDateConversationMovesWithLatestWork() {
    let old = herConv("old", .idle, updated: -5 * 86400)
    let fresh = sentWork("fresh", parent: "old", .idle, updated: -60)
    let secs = sessionSections([old, fresh], view: SessionListLayout(group: .date), archived: false, now: now, calendar: utc)
    #expect(secs.map(\.key) == ["today"])
    #expect(secs[0].sessions.map(\.sessionID) == ["old"])
}

@Test func byProjectAndByAgentHerComesFirst() {
    let h = herConv("h", .idle)
    let w = sentWork("w", parent: "h", .idle, project: "web", agent: "codex")
    let a = session("a", .idle, project: "api", agent: "claude")

    let secs = sessionSections([h, w, a], view: SessionListLayout(group: .project), archived: false, now: now, calendar: utc)
    #expect(secs.map(\.key) == [herSection, "api"])
    #expect(secs[0].sessions.map(\.sessionID) == ["h"])

    let flat = sessionSections([h, w, a], view: SessionListLayout(group: .project, nest: false), archived: false, now: now, calendar: utc)
    #expect(flat.map(\.key) == [herSection, "api", "web"])
    #expect(childrenOf([h, w, a], view: SessionListLayout(nest: false)).isEmpty)

    #expect(sessionSections([h, w, a], view: SessionListLayout(group: .agent), archived: false, now: now, calendar: utc).map(\.key) == ["her", "claude"])
    #expect(sessionSections([h, w, a], view: SessionListLayout(group: .agent, nest: false), archived: false, now: now, calendar: utc).map(\.key) == ["her", "claude", "codex"])
}

@Test func pinnedAndArchivedAndSliceAndArchivableRules() {
    var h = herConv("h", .idle)
    h.pinned = true
    var w = sentWork("w", parent: "h", .running)
    w.pinned = true
    let secs = sessionSections([h, w], view: SessionListLayout(), archived: false, now: now, calendar: utc)
    #expect(secs.map(\.key) == ["pinned"])
    #expect(secs[0].sessions.map(\.sessionID) == ["h"])

    let arch = herConv("arch", .archived)
    let orphan = sentWork("orphan", parent: "arch", .idle)
    #expect(childrenOf([arch, orphan], view: SessionListLayout()).isEmpty)
    #expect(sessionSections([arch, orphan], view: SessionListLayout(), archived: false, now: now, calendar: utc).flatMap(\.sessions).map(\.sessionID) == ["orphan"])

    var kids = (0 ..< 8).map { i in sentWork("k\(i)", parent: "h", .idle, updated: Double(-60 * (i + 1))) }
    kids[7].status = .running
    let folded = sliceChildren(kids, expanded: false)
    #expect(folded.visible.count == childrenInitialLimit)
    #expect(folded.visible.map(\.sessionID).contains("k7"))
    #expect(folded.hiddenCount == 3)
    #expect(sliceChildren(kids, expanded: true).hiddenCount == 0)
    #expect(sliceChildren(Array(kids.prefix(5)), expanded: false).hiddenCount == 0)

    let idleHer = herConv("idleHer", .idle)
    #expect(isTreeArchivable(idleHer, children: [sentWork("a", parent: "idleHer", .idle)]))
    #expect(!isTreeArchivable(idleHer, children: [sentWork("a", parent: "idleHer", .running)]))
}

@Test func conversationWithRunningOrWaitingChildrenStaysActiveEverywhere() {
    let h = herConv("h", .idle, updated: -120)
    let runChild = sentWork("c_run", parent: "h", .running, updated: -30)
    let settledOther = session("done_1", .idle, updated: -10)

    let flat = sessionSections([h, runChild, settledOther], view: SessionListLayout(group: .status, nest: false), archived: false, now: now, calendar: utc)
    #expect(flat.map(\.key) == ["active", "done"])
    #expect(flat[0].sessions.map(\.sessionID) == ["h", "c_run"])
    #expect(flat[1].sessions.map(\.sessionID) == ["done_1"])

    let tRun = teamState([h, runChild, settledOther], requests: 0)
    #expect(tRun.running.map(\.sessionID).contains("h"))
    #expect(tRun.finished.map(\.sessionID) == ["done_1"])

    let waitChild = sentWork("c_wait", parent: "h", .waitingInput, updated: -20)
    let tWait = teamState([h, waitChild, settledOther], requests: 0)
    #expect(tWait.waiting.map(\.sessionID) == ["c_wait"])
    #expect(tWait.finished.map(\.sessionID) == ["done_1"])

    #expect(searchSessions([settledOther, h, runChild], query: "", filter: SessionFilter(), text: \.sessionID).map(\.sessionID) == ["c_run", "h", "done_1"])

    let many = (0 ..< 7).map { i in herConv("h_\(i)", .idle, updated: Double(-10 * (i + 1))) }
    let lateKids = [many[6].sessionID: [sentWork("c_late", parent: many[6].sessionID, .running)]]
    #expect(sliceSectionSessions(many, grouping: .project, limit: 5, selectedID: nil, trees: lateKids).visible.map(\.sessionID).contains(many[6].sessionID))
}
