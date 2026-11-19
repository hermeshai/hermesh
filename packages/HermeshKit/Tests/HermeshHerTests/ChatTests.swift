import Foundation
import HermeshProto
import Testing
@testable import HermeshHer

// A conversation as her sends it: a snapshot (bots/open), then events.

let info: JSON = ["session_id": "s1", "model": "deepseek/deepseek-chat", "permission_mode": "manual", "cwd": "/w", "session_file": "/f", "thinking": "default", "context_window": 1000, "archived": false]

func said(_ id: String, _ text: String, ts: Double) -> JSON {
    ["type": "message", "message_id": .string(id), "message": ["role": "user", "content": [["type": "text", "text": .string(text)]], "ts": .number(ts)]]
}

func call(_ id: String, _ name: String, _ args: JSON) -> JSON { ["type": "tool_call", "id": .string(id), "name": .string(name), "arguments": args] }

func reply(_ id: String, _ calls: [JSON], ts: Double, duration: Double = 0) -> JSON {
    ["type": "message", "message_id": .string(id), "message": ["role": "assistant", "content": .array([["type": "text", "text": "thinking aloud"]] + calls), "ts": .number(ts), "duration_ms": .number(duration)]]
}

func result(_ callID: String, details: JSON = [:], error: Bool = false, text: String = "ok") -> JSON {
    ["type": "tool_result", "call_id": .string(callID), "result": ["call_id": .string(callID), "tool_name": "send_to_user", "is_error": .bool(error), "content": [["type": "text", "text": .string(text)]], "details": details, "ts": 0]]
}

func snapshotJSON(_ items: [JSON], task: JSON = nil, seq: Int = 10, queued: JSON = [], agents: JSON = []) -> JSON {
    ["session": info, "last_seq": .number(Double(seq)), "transcript": ["items": .array(items)], "active_task": task, "usage": ["per_model": [:]], "queued": queued, "agents": agents]
}

func snapshot(_ items: [JSON], task: JSON = nil, seq: Int = 10, queued: JSON = [], agents: JSON = []) -> HerSession {
    HerSession(snapshot: snapshotJSON(items, task: task, seq: seq, queued: queued, agents: agents))!
}

@Test func earlierHistoryGoesBeforeWhatTheSnapshotHas() {
    let json: JSON = ["session": info, "last_seq": 10, "transcript": ["items": [said("u3", "third", ts: 3000)], "before": "i2"], "active_task": nil,
                      "usage": ["per_model": [:]], "queued": [], "agents": []]
    var v = HerSession(snapshot: json)!
    #expect(v.before == "i2")
    v.prepend(history: ["items": [said("u2", "second", ts: 2000)], "before": "i1"])
    #expect(v.before == "i1")
    v.prepend(history: ["items": [said("u1", "first", ts: 1000)], "before": nil])
    #expect(v.before == nil, "the first page has nothing before it")
    #expect(herChat(v).map(\.key) == ["u1", "u2", "u3"])
    // A snapshot of a whole conversation has no cursor.
    #expect(snapshot([said("u1", "first", ts: 1000)]).before == nil)
}

@Test func snapshotShowsTheOwnerAndWhatHerSent() {
    let v = snapshot([
        said("u1", "list my tasks", ts: 1000),
        reply("a1", [call("c0", "bash", ["command": "ls"]), call("c1", "send_to_user", ["text": "Four are open."])], ts: 2000, duration: 500),
        result("c0"), result("c1"),
        reply("a2", [call("c2", "send_to_user", ["type": "attachment", "text": "Here it is", "files": [["path": "/b/report.pdf"]]])], ts: 3000),
        result("c2", details: ["files": [["path": "/b/report.pdf", "size": 2048, "image": false], ["path": "/b/chart.png", "size": 10, "image": true, "sha256": "3f9a", "snapshot": "/b/sent/3f9a/chart.png"]]]),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 3100],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "c1", "c2"], "words Her wrote for herself and other tools never show")
    #expect(items[0].kind == .user(text: "list my tasks", images: [], files: [], queued: false))
    #expect(items[1].kind == .her("Four are open.") && items[1].ts == 2500, "sent when the reply ended")
    guard case .attachment(let files, let text) = items[2].kind else { Issue.record("no attachment"); return }
    #expect(text == "Here it is" && files.map(\.name) == ["report.pdf", "chart.png"] && files[0].size == 2048 && files[1].image)
    // A file is read from the copy her kept of it as it was sent, when there is one (docs/her-deliverables.md §4.1).
    #expect(files.map(\.snapshot) == [nil, "/b/sent/3f9a/chart.png"] && files.map(\.source) == ["/b/report.pdf", "/b/sent/3f9a/chart.png"])
}

@Test func failedRefusedAndCancelledMessagesDoNotShow() {
    let v = snapshot([
        said("u1", "go", ts: 1),
        reply("a1", [call("c1", "send_to_user", ["text": "refused"]), call("c2", "send_to_user", ["text": "broke"]), call("c3", "send_to_user", ["text": "never ran"])], ts: 2),
        result("c1", error: true, text: "Denied by user"), result("c2", error: true, text: "boom"),
        reply("a2", [call("c4", "send_to_user", ["type": "attachment", "files": [["path": "/x"]]])], ts: 3),
    ])
    #expect(herChat(v).map(\.key) == ["u1"], "c3's task ended without it; c4's files never came through")
}

@Test func eventsAddToTheConversationOnce() {
    var v = snapshot([said("u1", "hi", ts: 1)], seq: 5)
    v.apply(["seq": 3, "ts": 2, "type": "task_started", "task_id": "old"])
    #expect(v.live == nil, "an event the snapshot holds already")
    v.apply(["seq": 6, "ts": 10, "type": "task_started", "task_id": "t2"])
    v.apply(["seq": 7, "ts": 11, "type": "message_completed", "message_id": "a1", "message": reply("a1", [call("c1", "send_to_user", ["text": "On it."])], ts: 11)["message"]])
    #expect(herChat(v).last?.kind == .her("On it."), "words show from the call's start")
    #expect(herBusy(v))
    v.apply(["seq": 8, "ts": 12, "type": "tool_completed", "call_id": "c1", "result": result("c1")["result"]])
    v.apply(["seq": 9, "ts": 13, "type": "task_completed", "task_id": "t2", "reason": "end_turn"])
    #expect(!herBusy(v) && v.lastSeq == 9)
    #expect(herChat(v).map(\.key) == ["u1", "c1"])
    #expect(v.thinking == "default", "the snapshot's level")
    v.apply(["seq": 10, "ts": 14, "type": "settings_changed", "model": "openai/gpt-5", "thinking": "low", "permission_mode": "bypass-permissions", "context_window": 1])
    #expect(v.model == "openai/gpt-5" && v.thinking == "low" && v.permissionMode == "bypass-permissions")
}

@Test func theLatestQuestionIsOpenUntilTheOwnerAnswers() {
    let asked = [
        said("u1", "paint it", ts: 1),
        reply("a1", [call("q1", "send_to_user", ["type": "question", "text": "Which colour?", "options": [["label": "Blue "], ["label": "Red"]]])], ts: 2),
        result("q1"),
    ]
    #expect(herChat(snapshot(asked)).last?.kind == .question(text: "Which colour?", options: ["Blue", "Red"], open: true))
    // Options alone say it is a question too.
    let untyped = snapshot([said("u1", "x", ts: 1), reply("a1", [call("q2", "send_to_user", ["text": "This or that?", "options": [["label": "This"]]])], ts: 2), result("q2")])
    #expect(herChat(untyped).last?.kind == .question(text: "This or that?", options: ["This"], open: true))
    let answered = herChat(snapshot(asked + [said("u2", "Blue", ts: 3)]))
    #expect(answered[1].kind == .question(text: "Which colour?", options: ["Blue", "Red"], open: false))
}

@Test func filesTheOwnerSentKeepWhereTheyWereUploaded() {
    let v = snapshot([
        ["type": "message", "message_id": "u1", "message": ["role": "user", "ts": 1, "content": [
            ["type": "text", "text": "look"], ["type": "image", "mime_type": "image/png", "data": "iVBO"], ["type": "text", "text": "Attached files:\n/b/uploads/a.txt\n/b/uploads/b.csv"],
        ]]],
        said("u2", "see these\n\nAttached files:\n- /b/uploads/c.pdf", ts: 2),
    ])
    let items = herChat(v)
    #expect(items[0].kind == .user(text: "look", images: [HerImage(mime: "image/png", data: "iVBO")], files: ["/b/uploads/a.txt", "/b/uploads/b.csv"], queued: false))
    #expect(items[1].kind == .user(text: "see these", images: [], files: ["/b/uploads/c.pdf"], queued: false), "as hermeshd delivers a message with files")
}

@Test func queuedMessagesComeLastAndFailuresSayWhichIsLast() {
    let v = snapshot([
        said("u1", "go", ts: 1),
        ["type": "task_end", "task_id": "t1", "outcome": "failed", "error": ["kind": "quota_exceeded", "message": "No credit", "retryable": false], "ts": 2],
        said("u2", "again", ts: 3),
        ["type": "task_end", "task_id": "t2", "outcome": "failed", "error": ["kind": "network", "message": "Offline", "retryable": true], "ts": 4],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "end:1", "u2", "end:3"])
    #expect(items[1].kind == .failure(HerFailure(["kind": "quota_exceeded", "message": "No credit"])!, last: false))
    #expect(items[3].kind == .failure(HerFailure(["kind": "network", "message": "Offline"])!, last: true))
    let running = snapshot([said("u1", "go", ts: 1)], task: ["task_id": "t1", "turn": 1], queued: [["id": "q1", "mode": "steer", "message": ["content": [["type": "text", "text": "and this"]], "ts": 5]]])
    #expect(herChat(running).last == HerItem(key: "q1", ts: 5, kind: .user(text: "and this", images: [], files: [], queued: true)))
}

@Test func whatASubAgentToldTheOwnerReadsAsHers() {
    let v = snapshot([
        said("u1", "check", ts: 1),
        ["type": "message", "message_id": "m1", "mail": ["id": "x", "kind": "to_user", "from": ["id": "ag1", "name": "sales"], "status": "running"],
         "message": ["role": "user", "ts": 2, "content": [["type": "text", "text": "<sent_to_user from=\"sales\">\nMarch is missing.\n</sent_to_user>"]]]],
        ["type": "message", "message_id": "m2", "mail": ["id": "y", "kind": "report", "from": ["id": "ag1", "name": "sales"], "status": "completed"],
         "message": ["role": "user", "ts": 3, "content": [["type": "text", "text": "<agent_report>done</agent_report>"]]]],
        // News of a coding session Her started (docs/her-mesh.md §6) is hers to read, as a report is.
        ["type": "message", "message_id": "m3", "mail": ["id": "z", "kind": "news", "from": ["id": "s_1", "name": "Fix the flaky relay test"], "status": "completed"],
         "message": ["role": "user", "ts": 4, "content": [["type": "text", "text": "<session_news id=\"msg_1\" kind=\"turn_done\">\n<result>\nok\n</result>\n</session_news>\nNews of a coding session you started: its turn ended."]]]],
    ])
    #expect(herChat(v).map(\.kind) == [.user(text: "check", images: [], files: [], queued: false), .her("March is missing.")])
}

@Test func anApprovalWaitsAboveTheComposerNotInTheConversation() {
    let ask: JSON = ["call_id": "b1", "name": "bash", "args": ["command": "brew upgrade"], "summary": "brew upgrade", "proposed_rule": ["prefix": ["brew", "upgrade"]]]
    var v = snapshot([said("u1", "upgrade", ts: 1)], task: ["task_id": "t1", "turn": 1, "approvals": [ask], "tools": [["call_id": "b1", "name": "bash", "category": "execute", "title": "brew upgrade", "running": false, "args": [:]]]])
    let pending = herPendingApproval(v)
    #expect(pending?.approval.callID == "b1" && pending?.approval.rule == ["brew", "upgrade"] && pending?.agent == nil && pending?.waiting == 1)
    #expect(herApprovalPreview(pending!.approval) == .command("brew upgrade"))
    #expect(v.live?.tools.first?.phase == .awaitingApproval)
    #expect(!herBusy(v), "waiting for an approval alone is not busy")
    #expect(herApprovalOp(pending!.approval, agent: nil, decision: ["type": "approve"]) == ["type": "approval", "call_id": "b1", "decision": ["type": "approve"]])
    v.apply(["seq": 11, "ts": 2, "type": "approval_resolved", "call_id": "b1", "decision": ["type": "deny"]])
    #expect(herPendingApproval(v) == nil && v.live?.tools.first?.phase == .denied && herBusy(v))

    // A sub-agent's, which its own conversation holds while Her may be idle.
    var w = snapshot([said("u1", "delegate", ts: 1)])
    w.apply(["seq": 11, "ts": 2, "type": "agent_started", "agent_id": "ag1", "name": "writer", "description": "写长文", "call_id": "sp1", "session_file": "/a"])
    w.apply(["seq": 12, "ts": 3, "type": "agent_event", "agent_id": "ag1", "event": ["seq": 1, "ts": 3, "type": "task_started", "task_id": "x"]])
    w.apply(["seq": 13, "ts": 4, "type": "agent_event", "agent_id": "ag1", "event": ["seq": 2, "ts": 4, "type": "approval_requested", "call_id": "e1", "name": "edit", "summary": "edit a.txt",
                                                                               "args": ["path": "a.txt", "edits": [["old_text": "one\n", "new_text": "two\nthree"]]]]])
    let theirs = herPendingApproval(w)
    #expect(theirs?.agent?.id == "ag1" && herBusy(w))
    #expect(herApprovalOp(theirs!.approval, agent: theirs!.agent, decision: ["type": "deny"])["type"] == "agent_approval")
    guard case .diff(let lines, 0) = herApprovalPreview(theirs!.approval) else { Issue.record("no diff"); return }
    #expect(lines.map { "\($0.sign)\($0.text)" } == ["-one", "+two", "+three"])
}

@Test func theBusyBubbleNamesWhoIsAtWorkNeverWhat() {
    var v = snapshot([said("u1", "看看磁盘", ts: 1)], task: ["task_id": "t1", "turn": 1])
    #expect(herBusyBubble(v) == HerBusyBubble(her: true, agents: []), "she writes: Her alone")
    v.apply(["seq": 11, "ts": 2, "type": "tool_started", "call_id": "b1", "name": "bash", "category": "execute", "title": "df -h", "args": ["command": "df -h"]])
    v.apply(["seq": 12, "ts": 3, "type": "tool_running", "call_id": "b1"])
    #expect(herBusyBubble(v) == HerBusyBubble(her: true, agents: []), "on a call: still Her alone, not the call")
    v.apply(["seq": 15, "ts": 6, "type": "agent_started", "agent_id": "ag1", "name": "writer", "description": "写长文", "call_id": "s1", "session_file": "/a"])
    v.apply(["seq": 16, "ts": 7, "type": "task_completed", "task_id": "t1", "reason": "end_turn"])
    #expect(herBusyBubble(v) == HerBusyBubble(her: false, agents: [.init(id: "ag1", name: "写长文")]), "she answered: the sub-agent at work, by its name")
    v.apply(["seq": 17, "ts": 8, "type": "agent_started", "agent_id": "ag2", "name": "checker", "description": "", "call_id": "s2", "session_file": "/b"])
    #expect(herBusyBubble(v)?.agents.map(\.name) == ["checker", "写长文"], "the latest first; a nameless one by its handle")
    var asking = v
    asking.apply(["seq": 18, "ts": 9, "type": "agent_event", "agent_id": "ag2", "event": ["seq": 1, "ts": 9, "type": "task_started", "task_id": "x"]])
    asking.apply(["seq": 19, "ts": 10, "type": "agent_event", "agent_id": "ag2", "event": ["seq": 2, "ts": 10, "type": "approval_requested", "call_id": "e1", "name": "bash", "summary": "rm", "args": ["command": "rm -rf build"]]])
    #expect(herBusyBubble(asking)?.agents.map(\.id) == ["ag1"], "one waiting for the owner is not at work")
    v.apply(["seq": 19, "ts": 10, "type": "agent_status", "agent_id": "ag1", "status": "completed", "summary": "写完了"])
    v.apply(["seq": 20, "ts": 11, "type": "agent_status", "agent_id": "ag2", "status": "completed", "summary": "查完了"])
    #expect(herBusyBubble(v) == nil, "nothing at work, no bubble")
}

@Test func herStatusReportsTypingCompactingAndRetryingForHerTag() {
    var v = snapshot([said("u1", "hi", ts: 1)])
    #expect(herStatus(v, now: 100_000) == nil)
    v.apply(["seq": 11, "ts": 10_000, "type": "task_started", "task_id": "t1"])
    #expect(herStatus(v, now: 12_000) == .typing)
    v.apply(["seq": 12, "ts": 15_000, "type": "compaction_started", "reason": "auto"])
    #expect(herStatus(v, now: 16_000) == .compacting)
    v.apply(["seq": 13, "ts": 20_000, "type": "stream_retry", "attempt": 1, "delay_ms": 5000])
    #expect(herStatus(v, now: 25_000) == .compacting, "retry under 10s does not outrank compacting")
    #expect(herStatus(v, now: 31_000) == .retrying, "retry past 10s takes precedence")
    v.apply(["seq": 14, "ts": 32_000, "type": "compaction_completed", "tokens_before": 100, "tokens_after": 50])
    v.apply(["seq": 15, "ts": 33_000, "type": "turn_started", "turn": 2])
    #expect(herStatus(v, now: 35_000) == .typing, "turn_started clears retry")
    v.apply(["seq": 16, "ts": 36_000, "type": "approval_requested", "call_id": "c1", "name": "bash", "summary": "Run ls", "args": [:]])
    #expect(herStatus(v, now: 40_000) == nil, "awaiting approval yields back to Her's name")
}

@Test func aSessionOnTheOwnersComputersAsksInItsOwnWords() {
    let ask: JSON = ["call_id": "c1", "name": "session_start", "args": ["project": "homemini/hermesh"], "summary": "Start Codex in homemini/hermesh\n\nFix the flaky relay test.\n  Keep the diff small.\n",
                     "proposed_rule": ["prefix": ["mesh", "homemini/hermesh"]]]
    let v = snapshot([said("u1", "fix it", ts: 1)], task: ["task_id": "t1", "turn": 1, "approvals": [ask], "tools": []])
    let a = herPendingApproval(v)!.approval
    #expect(herMeshProject(a.rule) == "homemini/hermesh")
    #expect(herApprovalPreview(a) == .args(a.summary))
    let parts = herSummaryParts(a.summary)
    #expect(parts?.title == "Start Codex in homemini/hermesh" && parts?.body == "Fix the flaky relay test.\n  Keep the diff small.")
    // Any other rule, and a summary of one line, read as before.
    #expect(herMeshProject(["brew", "upgrade"]) == nil && herMeshProject(["mesh"]) == nil && herMeshProject(nil) == nil)
    #expect(herSummaryParts("brew upgrade") == nil && herSummaryParts("Start Codex in homemini/hermesh\n\n") == nil)
}

@Test func theConversationGoesBackWhenRewound() {
    var v = snapshot([said("u1", "one", ts: 1), said("u2", "two", ts: 2)])
    v.apply(["seq": 11, "ts": 3, "type": "session_rewound", "message_id": "u2"])
    #expect(herChat(v).map(\.key) == ["u1"])
}

@Test func anApprovalSaysWhatItActsOn() {
    #expect(herToolObject(name: "read", title: "read src/main.rs", category: "read") == "src/main.rs")
    #expect(herToolObject(name: "bash", title: "brew upgrade", category: "execute") == "brew upgrade")
    #expect(herToolObject(name: "mcp__notion__search", title: "Search Notion", category: "other") == "Search Notion")
    #expect(herToolCategory("bash") == "execute" && herToolCategory("mcp__x") == "other")
    #expect(herCallTarget("call_tool", ["name": "browser_navigate", "arguments": ["url": "https://a.example"]]).name == "browser_navigate")
}

@Test func aSessionHerStartedIsARowWhereSheStartedIt() {
    let doc = #"{"ok":true,"exit_code":0,"error":null,"session":{"session_id":"s_ab_12","title":"Fix the relay test","machine":"Mac mini","agent":"claude"}}"#
    let v = snapshot([
        said("u1", "fix the relay test", ts: 1000),
        reply("a1", [call("c1", "send_to_user", ["text": "Starting Claude Code."]), call("c2", "call_tool", ["name": "session_start", "arguments": ["project": "mini/hermesh"]])], ts: 2000, duration: 500),
        result("c1"), result("c2", text: doc),
        reply("a2", [call("c3", "session_start", ["project": "mini/hermesh"]), call("c4", "session_start", [:]), call("c5", "session_start", [:]), call("c7", "session_start", [:])], ts: 3000),
        result("c3", text: doc.replacingOccurrences(of: "s_ab_12", with: "s_cd_34")),
        result("c4", error: true, text: #"{"ok":false,"exit_code":3,"error":{"kind":"denied"}}"#),
        result("c5", text: "denied by the user"),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 3100],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "c1", "session:s_ab_12", "session:s_cd_34"], "one row a session started: none for a call that failed, was refused or never ran")
    #expect(items[2].kind == .session(HerStarted(sessionID: "s_ab_12", title: "Fix the relay test", agent: "claude", machine: "Mac mini")), "inside call_tool too")
    #expect(items[2].ts == 2500 && !items[2].isOwner)
    guard case .session(let second) = items[3].kind else { Issue.record("no session row"); return }
    #expect(second.sessionID == "s_cd_34")
}

/// More asked of a session days later (docs/her-team.md §4.4): its one card comes down to where she asked, and the
/// earlier one goes. Named as the result names it, or as it was named before; a call that failed moves nothing, and
/// one that names no session it can be named as makes no card.
@Test func aSessionsCardFollowsWhatSheAsksOfIt() {
    let doc = #"{"ok":true,"session":{"session_id":"s_ab_12","title":"Fix the relay test","machine":"Mac mini","agent":"claude"}}"#
    let v = snapshot([
        said("u1", "fix the relay test", ts: 1000),
        reply("a1", [call("c1", "session_start", ["project": "mini/hermesh"])], ts: 2000),
        result("c1", text: doc),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 2100],
        said("u2", "also the flaky one", ts: 900_000_000),
        reply("a2", [call("c2", "send_to_user", ["text": "Asking it."]), call("c3", "call_tool", ["name": "session_send", "arguments": ["session_id": "s_ab_12", "message": "and the flaky one"]]),
                     call("c4", "session_send", ["session_id": "s_ab_12", "message": "again"]), call("c5", "session_answer", ["session_id": "s_zz_99", "text": "yes"])], ts: 900_001_000, duration: 300),
        result("c2"), result("c3", text: #"{"ok":true,"message":{"id":"m1","delivered":"queued"}}"#), result("c4", error: true, text: #"{"ok":false}"#), result("c5", text: #"{"ok":true}"#),
        ["type": "task_end", "task_id": "t2", "outcome": "completed", "ts": 900_001_400],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "u2", "c2", "session:s_ab_12"], "one card, under what she said when she asked")
    #expect(items[3].ts == 900_001_300)
    #expect(items[3].kind == .session(HerStarted(sessionID: "s_ab_12", title: "Fix the relay test", agent: "claude", machine: "Mac mini")), "named as it was")
}

/// News of a session wakes her (docs/her-mesh.md §6.2): what she then tells the owner has the session's card under
/// it, named from the news when nothing else named it (her history holds the last page only). News she tells nothing
/// of moves nothing, and the next task's words have no card under them.
@Test func aSessionsCardFollowsWhatSheSaysOfItsNews() {
    func news(_ id: String, _ session: String, ts: Double) -> JSON {
        ["type": "message", "message_id": .string(id), "mail": ["id": .string(id), "kind": "news", "from": ["id": .string(session), "name": "Fix"], "status": "completed"],
         "message": ["role": "user", "ts": .number(ts), "content": [["type": "text", "text": .string(#"<session_news id="\#(id)" kind="turn_done" session="\#(session)" title="Fix \"the\" test" agent="codex" project="macbook/hermesh" sent="x">"# + "\n<result>\nok\n</result>\n</session_news>\nNews of a coding session you started: its turn ended.")]]]]
    }
    let v = snapshot([
        news("n1", "s_ab_12", ts: 1000),
        reply("a1", [call("c1", "send_to_user", ["text": "Done."])], ts: 2000, duration: 100),
        result("c1"),
        reply("a2", [call("c2", "send_to_user", ["text": "Two files."])], ts: 3000, duration: 100),
        result("c2"),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 3200],
        news("n2", "s_cd_34", ts: 4000),
        ["type": "task_end", "task_id": "t2", "outcome": "completed", "ts": 4100],
        said("u1", "thanks", ts: 5000),
        reply("a3", [call("c3", "send_to_user", ["text": "You're welcome."])], ts: 6000),
        result("c3"),
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["c1", "c2", "session:s_ab_12", "u1", "c3"], "under the last she said of it")
    #expect(items[2].kind == .session(HerStarted(sessionID: "s_ab_12", title: #"Fix "the" test"#, agent: "codex", machine: "macbook")))
    #expect(HerStarted(news: "<agent_report>done</agent_report>") == nil && HerStarted(news: #"<session_news id="n" session="">"#) == nil)
}

/// The session a start made, from its result's details (docs/her-team.md §6.2): they win over the text, which a long
/// result has cut; a Her Server on an older hermeshd sends none, and the text still names the session.
@Test func aSessionCardComesFromTheResultsDetails() {
    let details: JSON = ["session": ["session_id": "s_ef_56", "title": "Fix the relay test", "agent": "codex", "machine": "Mac mini", "machine_id": "m_ef"]]
    let v = snapshot([
        reply("a1", [call("c1", "session_start", ["project": "mini/hermesh"]), call("c2", "session_start", ["project": "mini/hermesh"])], ts: 2000),
        result("c1", details: details, text: #"{"ok":true,"session":{"session_id":"s_ef_56","tit"#),
        result("c2", details: ["session": ["title": "no id"]], text: "not a document"),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 2100],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["session:s_ef_56"], "details without a session id make no card")
    #expect(items.first?.kind == .session(HerStarted(sessionID: "s_ef_56", title: "Fix the relay test", agent: "codex", machine: "Mac mini")))
}

/// The team's work Her counted is a card where she counted it (docs/her-team.md §6.3): from its result's details; a
/// call that failed, or a result without counts, makes none.
@Test func theTeamsWorkHerCountedIsACard() {
    let work: JSON = ["work": ["days": 1, "sessions": 5, "done": 3, "turns": 9, "failed_turns": 1, "input_tokens": 1000, "output_tokens": 200,
                               "buckets": [0, 2, 3], "by_agent": [["agent": "claude", "sessions": 4, "turns": 8], ["agent": "codex", "sessions": 1, "turns": 1]],
                               "by_client": [["client": "c_her", "name": "", "you": true, "sessions": 3, "turns": 6], ["client": "owner", "sessions": 2, "turns": 3]]]]
    let v = snapshot([
        reply("a1", [call("c1", "call_tool", ["name": "work_summary", "arguments": [:]]), call("c2", "work_summary", [:]), call("c3", "work_summary", [:])], ts: 2000, duration: 100),
        result("c1", details: work),
        result("c2", error: true, text: #"{"ok":false}"#),
        result("c3", details: ["work": ["by_agent": []]]),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 2200],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["c1"])
    #expect(items.first?.kind == .work(HerWork(days: 1, sessions: 5, done: 3, turns: 9, failedTurns: 1, tokens: 1200, buckets: [0, 2, 3],
                                               agents: [.init(id: "claude", sessions: 4, turns: 8), .init(id: "codex", sessions: 1, turns: 1)],
                                               senders: [.init(id: "c_her", sessions: 3, turns: 6), .init(id: "owner", sessions: 2, turns: 3)])))
    #expect(items.first?.ts == 2100)
}

/// The cases the core writes its blocks from (internal/owner TestHerReplyBlock), so that what it writes is what is read.
@Test func whatAMessageRepliesToIsReadBackFromItsBlock() throws {
    struct Shared: Decodable {
        struct Case: Decodable {
            struct Reply: Decodable { var ref: String; var own: Bool; var text: String; var partial: Bool }
            var reply: Reply
            var block: String
        }
        var cases: [Case]
        var not: [String]
    }
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("internal/owner/testdata/her_reply.json")
    let shared = try JSONDecoder().decode(Shared.self, from: Data(contentsOf: url))
    #expect(shared.cases.count >= 4 && shared.not.count >= 8)
    for c in shared.cases {
        #expect(herReply(c.block) == HerReply(ref: c.reply.ref, own: c.reply.own, text: c.reply.text, partial: c.reply.partial), "\(c.block)")
    }
    for text in shared.not { #expect(herReply(text) == nil, "\(text)") }

    // It shows as a quote, not as words, sent or still queued; words that only look like one stay words.
    let block = shared.cases[0].block, reply = try #require(herReply(block))
    let v = snapshot([
        ["type": "message", "message_id": "u1", "message": ["role": "user", "ts": 1, "content": [
            ["type": "text", "text": "is it far?"], ["type": "text", "text": .string(block)], ["type": "text", "text": "Attached files:\n/b/uploads/a.txt"],
        ]]],
        said("u2", shared.not[0], ts: 2),
    ], task: ["task_id": "t1", "turn": 1], queued: [["id": "q1", "mode": "steer", "message": ["content": [["type": "text", "text": "by car"], ["type": "text", "text": .string(block)]], "ts": 5]]])
    let items = herChat(v)
    #expect(items[0] == HerItem(key: "u1", ts: 1, kind: .user(text: "is it far?", images: [], files: ["/b/uploads/a.txt"], queued: false), reply: reply))
    #expect(items[1] == HerItem(key: "u2", ts: 2, kind: .user(text: shared.not[0], images: [], files: [], queued: false)))
    #expect(items[2] == HerItem(key: "q1", ts: 5, kind: .user(text: "by car", images: [], files: [], queued: true), reply: reply))
}
