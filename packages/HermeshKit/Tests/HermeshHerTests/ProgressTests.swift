import Foundation
import Testing
@testable import HermeshHer

// The Progress page's groups, cards and records (docs/her-team.md §15): what the owner said, and under it the
// sub-agents Her started for it, each as it stands now, with its whole record. Coding sessions she started are not
// her progress.

private let usage: JSON = ["per_model": ["p/m": ["input": 100, "output": 20, "cache_read": 0, "cache_write": 0]]]

private func spawn(_ id: String, _ name: String, _ description: String, task: String = "Do it.") -> JSON {
    call(id, "spawn_agent", ["name": .string(name), "description": .string(description), "task": .string(task)])
}

private func inner(_ seq: Int, _ agent: String, _ n: Int, _ event: [String: JSON]) -> JSON {
    var e = event
    e["seq"] = .number(Double(n))
    if e["ts"] == nil { e["ts"] = .number(Double(1000 * seq)) }
    return ["seq": .number(Double(seq)), "ts": .number(Double(1000 * seq)), "type": "agent_event", "agent_id": .string(agent), "event": .object(e)]
}

@Test func eachThingTheOwnerSaidHeadsTheWorkHerSentOutForIt() {
    let v = snapshot([
        said("u1", "准备周报", ts: 1000),
        reply("a1", [call("c9", "send_to_user", ["text": "分两路去办。"]), spawn("c1", "data", "整理数据"), spawn("c2", "rivals", "", task: "**看竞品**的定价。\n整理成表。")], ts: 2000),
        result("c9"), result("c1", details: ["agent_id": "ag1", "name": "data"]), result("c2", details: ["agent_id": "ag2", "name": "rivals"]),
        ["type": "task_end", "task_id": "t1", "outcome": "completed", "ts": 2100],
        said("u2", "今天天气怎么样", ts: 3000),
        reply("a2", [call("c8", "send_to_user", ["text": "晴。"])], ts: 3100), result("c8"),
        said("u3", "把 relay 的测试修一下", ts: 4000),
        reply("a3", [call("c3", "call_tool", ["name": "session_start", "arguments": ["prompt": "fix it"]])], ts: 4100),
        result("c3", details: ["session": ["session_id": "S1", "title": "修测试", "agent": "claude", "machine": "MacBook"]]),
    ], agents: [
        ["agent_id": "ag1", "name": "data", "description": "整理数据", "call_id": "c1", "status": "running", "since": 2000, "transcript": ["items": []]],
    ])
    let groups = herProgress(v)
    #expect(groups.map(\.key) == ["u1"], "what sent nothing out, or a coding session only, is no group")
    #expect(groups[0].parent == .user(text: "准备周报", ts: 1000) && groups[0].said == "分两路去办。")
    guard case .agent(let first) = groups[0].tasks[0], case .agent(let second) = groups[0].tasks[1] else { Issue.record("no agents"); return }
    #expect(first.agent?.id == "ag1" && herAgentState(first) == .running && herAgentTitle(first) == "整理数据")
    // Its agent_started has not come: it is starting, and goes by its task's first line.
    #expect(second.agent == nil && herAgentState(second) == .starting && herAgentTitle(second) == "看竞品的定价。")
}

@Test func aSubAgentsCardAndRecordFollowItsOwnConversation() {
    var v = snapshot([said("u1", "go", ts: 1000), reply("a1", [spawn("c1", "data", "整理数据", task: "Sum the tables.")], ts: 2000)], task: ["task_id": "t1", "turn": 1], seq: 10)
    func launch() -> HerLaunch {
        guard case .agent(let l)? = herProgress(v).first?.tasks.first else { return HerLaunch(callID: "", name: "", description: "", task: "", agent: nil) }
        return l
    }
    #expect(herAgentPost(launch()) == HerAgentPost(words: nil, now: nil, calls: 0, files: []))

    v.apply(["seq": 11, "ts": 5000, "type": "agent_started", "agent_id": "ag1", "name": "data", "description": "整理数据", "call_id": "c1", "session_file": "/a"])
    v.apply(inner(12, "ag1", 1, ["type": "task_started", "task_id": "x"]))
    v.apply(inner(13, "ag1", 2, ["type": "message_completed", "message_id": "x1",
                                 "message": ["role": "user", "ts": 6000, "content": [["type": "text", "text": "You are a sub-agent.\n<task>\nSum the tables.\n</task>"]]]]))
    v.apply(inner(14, "ag1", 3, ["type": "message_completed", "message_id": "x2",
                                 "message": ["role": "assistant", "ts": 7000, "content": [["type": "thinking", "text": "先看表头。\n再算。"], call("k1", "read", ["path": "a.csv"])]]]))
    v.apply(inner(15, "ag1", 4, ["type": "tool_started", "call_id": "k1", "name": "read", "category": "read", "title": "read a.csv"]))
    v.apply(inner(16, "ag1", 5, ["type": "turn_completed", "task_id": "x", "turn": 1, "usage": ["input": 100, "output": 20, "cache_read": 0, "cache_write": 0]]))
    var a = launch().agent!
    #expect(a.callID == "c1" && a.since == 5000)
    #expect(herAgentState(launch()) == .running)
    // Its post: its latest thought, and the call it is on.
    let post = herAgentPost(launch())
    guard let now = post.now else { Issue.record("no call"); return }
    #expect(post.words == "先看表头。" && post.calls == 1)
    #expect(now.title == "read a.csv" && now.object == "a.csv" && now.state == .live && now.category == "read")
    #expect(herAgentTokens(a) == 120, "its running task's turns count already")
    #expect(herAgentElapsed(a, now: a.since + 4200) == 4200)
    #expect(herAgentBrief(launch()) == "Sum the tables.")

    // It asks: the post says what, and the record's call waits.
    v.apply(inner(17, "ag1", 6, ["type": "approval_requested", "call_id": "k1", "name": "read", "args": [:], "summary": "读取 a.csv\n在项目外"]))
    #expect(herAgentState(launch()) == .waiting && herAgentPost(launch()).words == "读取 a.csv" && herAgentPost(launch()).now?.state == .awaiting)
    #expect(herSteps(launch().agent!.view).last?.kind == .tool(HerToolStep(callID: "k1", name: "read", category: "read", title: "read a.csv", state: .awaiting, output: "")))

    v.apply(inner(18, "ag1", 7, ["type": "approval_resolved", "call_id": "k1", "decision": ["type": "approve"]]))
    v.apply(inner(19, "ag1", 8, ["type": "tool_completed", "call_id": "k1", "result": ["call_id": "k1", "tool_name": "read", "is_error": false, "content": [["type": "text", "text": "wau,dau"]]]]))
    v.apply(inner(20, "ag1", 9, ["type": "message_completed", "message_id": "x3",
                                 "message": ["role": "assistant", "ts": 9000, "content": [["type": "text", "text": "**周活** 多了 12%。\n表在附件里。"]]]]))
    v.apply(inner(21, "ag1", 10, ["type": "task_completed", "task_id": "x", "usage": usage, "ts": 40_000]))
    v.apply(["seq": 22, "ts": 41_000, "type": "agent_status", "agent_id": "ag1", "name": "data", "status": "completed", "summary": "**周活** 多了 12%。", "elapsed_ms": 30_000])
    a = launch().agent!
    #expect(herAgentState(launch()) == .done && herAgentPost(launch()) == HerAgentPost(words: "周活 多了 12%。", now: nil, calls: 1, files: []))
    #expect(herAgentElapsed(a, now: 0) == 30_000 && herAgentTokens(a) == 120 && herAgentCalls(a) == 1)
    // Its record: the task, its thought, its call with what came back, its words.
    #expect(herSteps(a.view).map(\.kind) == [
        .said("You are a sub-agent.\n<task>\nSum the tables.\n</task>"),
        .thinking("先看表头。\n再算。", redacted: false),
        .tool(HerToolStep(callID: "k1", name: "read", category: "read", title: "read a.csv", state: .done, output: "wau,dau")),
        .text("**周活** 多了 12%。\n表在附件里。"),
    ])

    // A round that failed says why, in the post and in the record.
    v.apply(inner(23, "ag1", 11, ["type": "task_started", "task_id": "y"]))
    v.apply(inner(24, "ag1", 12, ["type": "task_failed", "task_id": "y", "error": ["kind": "provider", "message": "模型没有回应"], "ts": 50_000]))
    v.apply(["seq": 25, "ts": 51_000, "type": "agent_status", "agent_id": "ag1", "name": "data", "status": "failed", "error": ["kind": "provider", "message": "模型没有回应"]])
    #expect(herAgentState(launch()) == .failed && herAgentPost(launch()).words == "模型没有回应")
    #expect(herSteps(launch().agent!.view).last?.kind == .ended(outcome: "failed", error: launch().agent!.error))
}

@Test func aSubAgentsPostNamesTheFilesItWroteOnceEach() {
    let items: [JSON] = [
        said("x0", "You are a sub-agent.\n<task>\nWrite it.\n</task>", ts: 1000),
        reply("x1", [call("k1", "write", ["path": "out/周报.md"]), call("k2", "edit", ["path": "out/周报.md"]), call("k3", "edit", ["path": "b.txt"]), call("k4", "read", ["path": "c.txt"])], ts: 2000),
        result("k1"), result("k2"), result("k3", error: true), result("k4"),
        ["type": "message", "message_id": "x2", "message": ["role": "assistant", "ts": 3000, "content": [["type": "text", "text": "写好了。"]]]],
    ]
    let v = snapshot([said("u1", "go", ts: 500), reply("a1", [spawn("c1", "w", "写周报")], ts: 600)], agents: [
        ["agent_id": "ag1", "name": "w", "description": "写周报", "call_id": "c1", "status": "completed", "since": 4000, "transcript": ["items": .array(items)]],
    ])
    guard case .agent(let l)? = herProgress(v).first?.tasks.first else { Issue.record("no agent"); return }
    let post = herAgentPost(l)
    // What failed wrote nothing; a read is no file of its.
    #expect(post.files == ["out/周报.md"] && post.calls == 4 && post.now == nil && post.words == "写好了。")
}

@Test func agentsOfEarlierPagesAreAGroupOfTheirOwnAndTheirHistoryIsReadByTheirCursor() {
    let old: JSON = ["agent_id": "ag0", "name": "old", "description": "旧的活", "call_id": "c0", "status": "completed", "since": 5000,
                     "transcript": ["items": [said("m2", "second", ts: 2000), ["type": "task_end", "task_id": "t", "outcome": "completed", "ts": 9000, "usage": usage]], "before": "ag0/i4"]]
    var v = HerSession(snapshot: ["session": info, "last_seq": 10, "transcript": ["items": [said("u1", "go", ts: 1000), reply("a1", [spawn("c1", "data", "整理数据")], ts: 2000)], "before": "i7"],
                                  "active_task": nil, "usage": ["per_model": [:]], "queued": [], "agents": [old]])!
    let groups = herProgress(v)
    #expect(groups.map(\.key) == ["earlier", "u1"] && groups[0].parent == .earlier && groups[0].tasks.map(\.id) == ["c0"])
    let agent = v.agents[0]
    #expect(herAgentHistoryCursor(agent) == "ag0/i4")
    // From its first message to its last task's end, when her did not time it; its tokens from its ended tasks.
    #expect(herAgentElapsed(agent, now: 0) == 7000 && herAgentTokens(agent) == 120)
    v.prepend(history: ["items": [said("m1", "first", ts: 1000)], "before": nil], agent: "ag0")
    #expect(v.agents[0].view.entries.count == 3 && herAgentHistoryCursor(v.agents[0]) == nil)
    #expect(herAgentElapsed(v.agents[0], now: 0) == 8000)
    // An older her's cursor is the main conversation's kind: not read with.
    var plain = agent
    plain.view.before = "i4"
    #expect(herAgentHistoryCursor(plain) == nil)
}

@Test func aRoutinesRunHeadsWhatHerStartsWhenItsReportWakesHer() {
    let v = snapshot([
        said("u1", "hi", ts: 1000),
        ["type": "message", "message_id": "r1", "mail": ["id": "m1", "kind": "routine", "status": "completed", "routine_id": "rt1", "from": ["id": "run1", "name": "晨报"]],
         "message": ["role": "user", "ts": 9000, "content": [["type": "text", "text": "<routine_report>该发晨报了</routine_report>"]]]],
        reply("a1", [spawn("c1", "news", "收集新闻")], ts: 9100),
    ])
    let groups = herProgress(v)
    #expect(groups.count == 1 && groups[0].parent == .routine(name: "晨报", ts: 9000))
}
