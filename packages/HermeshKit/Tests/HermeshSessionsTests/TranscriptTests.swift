import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

func event(_ seq: UInt64, _ kind: SessionEvent.OneOf_Kind, at: Double = 0) -> SessionEvent {
    var e = SessionEvent()
    e.seq = seq
    e.kind = kind
    e.ts = ts(at)
    return e
}

func call(_ kind: String, _ summary: String, id: String? = nil, name: String = "", decision: String = "") -> ToolCall {
    var c = ToolCall()
    c.callID = id ?? summary
    c.kind = kind
    c.summary = summary
    c.name = name
    c.decision = decision
    return c
}

@Test func transcriptPairsCallsWithResultsAndQuestionsWithAnswers() {
    var started = Hermesh_Session_V1_TurnStarted()
    started.turn = 1
    var user = Hermesh_Session_V1_UserMessage()
    user.text = "do it"
    var result = ToolResult()
    result.callID = "a"
    var asked = Hermesh_Session_V1_QuestionAsked()
    asked.question.questionID = "q"
    var resolved = QuestionResolved()
    resolved.questionID = "q"
    resolved.resolution = .answered
    resolved.by = "owner"
    var answer = Hermesh_Session_V1_Answer()
    answer.selected = ["yes"]
    resolved.answers = [answer]
    var done = TurnCompleted()
    done.turn = 1
    let events = [event(1, .turnStarted(started), at: -100), event(2, .userMessage(user)), event(3, .toolCall(call("read", "a.ts", id: "a"))),
                  event(4, .toolResult(result)), event(5, .questionAsked(asked)), event(6, .questionResolved(resolved)),
                  event(7, .toolCall(call("shell", "go test", id: "b"))), event(8, .turnCompleted(done), at: -40)]
    let items = transcript(events, running: false)
    #expect(items.map(\.seq) == [1, 2, 3, 5, 7, 8])
    guard case .tool(let t) = items[2], case .question(_, _, let r?) = items[3], case .tool(let running) = items[4], case .turnEnd(_, _, let took?) = items[5] else {
        Issue.record("unexpected items \(items)")
        return
    }
    #expect(t.result != nil && !t.running && t.resultSeq == 4)
    #expect(resolutionTitle(r) == "You answered: yes")
    #expect(!running.running) // the turn ended
    #expect(took == 60)
    guard case .tool(let live) = transcript(Array(events.prefix(7)), running: true)[4] else { return }
    #expect(live.running)
}

@Test func consecutiveToolCallsAreOneStep() {
    let items: [TranscriptItem] = [.user(seq: 1, text: "do", at: nil), .tool(ToolItem(seq: 2, call: call("read", "a.ts"), result: nil, running: false)),
                                   .tool(ToolItem(seq: 3, call: call("read", "b.ts"), result: nil, running: false)),
                                   .tool(ToolItem(seq: 4, call: call("shell", "go test"), result: nil, running: false)),
                                   .agent(seq: 5, text: "ok"), .tool(ToolItem(seq: 6, call: call("edit", "a.ts"), result: nil, running: false))]
    let steps = groupSteps(items)
    #expect(steps.count == 4)
    guard case .tools(_, let calls) = steps[1] else { return }
    #expect(stepSummary(calls) == "Read 2 files · Ran 1 command")
    #expect(stepSummary([ToolItem(seq: 1, call: call("x", "y", decision: "deny"), result: nil, running: false),
                         ToolItem(seq: 2, call: call("weird", "z"), result: nil, running: false)]) == "Denied 1 action · Used 1 tool")
}

@Test func aCallInAFewWords() {
    #expect(toolVerb(call("edit", "x"), running: false) == "Edited")
    #expect(toolVerb(call("shell", "x"), running: true) == "Running")
    #expect(toolVerb(call("shell", "x", decision: "deny"), running: false) == "Denied")
    #expect(toolObject(call("edit", "Write hello.txt", name: "edit")) == "hello.txt")
    #expect(toolObject(call("shell", "Bash go test ./...", name: "Bash")) == "go test ./...")
    #expect(toolObject(call("shell", "echo ran > ran.txt")) == "echo ran > ran.txt")
    #expect(toolObject(call("other", "", name: "Task")) == "Task")
}

@Test func timesAndDurations() {
    #expect(clockText(252) == "4:12" && clockText(3723) == "1:02:03")
    #expect(tokensText(412_345) == "412K" && tokensText(1_800_000) == "1.8M" && tokensText(12) == "12")
    #expect(durationText(42) == "42s" && durationText(185) == "3m 05s" && durationText(4320) == "1h 12m")
    #expect(agoText(now.addingTimeInterval(-30), now: now, calendar: utc) == "just now")
    #expect(agoText(now.addingTimeInterval(-720), now: now, calendar: utc) == "12 min ago")
    #expect(minutesLeft(until: now.addingTimeInterval(61), now: now) == "2 min left")
    #expect(turnTimeLine(now.addingTimeInterval(-300), previous: now.addingTimeInterval(-600), now: now, calendar: utc) == nil)
    #expect(turnTimeLine(now.addingTimeInterval(-300), previous: nil, now: now, calendar: utc)?.hasPrefix("Today ") == true)
}

@Test func whatASessionIsDoing() {
    var s = session("abc123", .running)
    s.progress.currentTool = "Edit:/work/abc123/store_test.go"
    s.progress.phase = "editing"
    #expect(phaseText(s) == "Editing store_test.go")
    s.progress.currentTool = "Bash:sh -lc 'go test ./...'"
    s.progress.phase = "running_shell"
    #expect(phaseText(s) == "Running go test ./...")
    s.progress.currentTool = ""
    #expect(phaseText(s) == "Thinking")
    s.status = .waitingInput
    #expect(phaseText(s) == "Waiting for you")
}

@Test func failuresSayWhatToDo() {
    var f = Failure()
    f.kind = .auth
    #expect(failureTitle(.auth) == "Not signed in" && failureAdvice(f).contains("CLI"))
    f.kind = .unknown
    f.hint = "check the logs"
    #expect(failureAdvice(f) == "check the logs")
}

@Test func transcriptShowsMessagesWithTheOwnersAgents() {
    var received = Hermesh_Session_V1_MessageReceived()
    received.messageID = "msg_1"
    received.from = "c_helper"
    received.fromName = "helper"
    received.text = "check staging"
    received.delivered = "queued"
    var sent = Hermesh_Session_V1_MessageSent()
    sent.messageID = "msg_2"
    sent.to = "c_bot"
    sent.text = "halfway"
    let items = transcript([event(1, .messageReceived(received)), event(2, .messageSent(sent))], running: false)
    #expect(items == [
        .message(seq: 1, incoming: true, name: "helper", text: "check staging", queued: true),
        .message(seq: 2, incoming: false, name: "c_bot", text: "halfway", queued: false),
    ])
}

@Test func partialOutputIsAPreviewOrLongerThanTheSummary() {
    var r = ToolResult()
    r.summary = "ok"
    r.size = 2
    #expect(!partialOutput(r))
    r.size = 70_000
    #expect(partialOutput(r))
    r.size = 2
    r.preview = true
    #expect(partialOutput(r))
}
