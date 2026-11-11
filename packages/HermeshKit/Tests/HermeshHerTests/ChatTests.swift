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
        result("c2", details: ["files": [["path": "/b/report.pdf", "size": 2048, "image": false], ["path": "/b/chart.png", "size": 10, "image": true]]]),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 3100],
    ])
    let items = herChat(v)
    #expect(items.map(\.key) == ["u1", "c1", "c2"], "words Her wrote for herself and other tools never show")
    #expect(items[0].kind == .user(text: "list my tasks", images: [], files: [], queued: false))
    #expect(items[1].kind == .her("Four are open.") && items[1].ts == 2500, "sent when the reply ended")
    guard case .attachment(let files, let text) = items[2].kind else { Issue.record("no attachment"); return }
    #expect(text == "Here it is" && files.map(\.name) == ["report.pdf", "chart.png"] && files[0].size == 2048 && files[1].image)
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
    v.apply(["seq": 10, "ts": 14, "type": "settings_changed", "model": "openai/gpt-5", "thinking": "default", "permission_mode": "bypass-permissions", "context_window": 1])
    #expect(v.model == "openai/gpt-5" && v.permissionMode == "bypass-permissions")
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

@Test func filesTheOwnerSentShowByName() {
    let v = snapshot([
        ["type": "message", "message_id": "u1", "message": ["role": "user", "ts": 1, "content": [
            ["type": "text", "text": "look"], ["type": "image", "mime_type": "image/png", "data": "iVBO"], ["type": "text", "text": "Attached files:\n/b/uploads/a.txt\n/b/uploads/b.csv"],
        ]]],
        said("u2", "see these\n\nAttached files:\n- /b/uploads/c.pdf", ts: 2),
    ])
    let items = herChat(v)
    #expect(items[0].kind == .user(text: "look", images: [HerImage(mime: "image/png", data: "iVBO")], files: ["a.txt", "b.csv"], queued: false))
    #expect(items[1].kind == .user(text: "see these", images: [], files: ["c.pdf"], queued: false), "as hermeshd delivers a message with files")
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
