import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

private func item(_ seq: UInt64, _ run: String, _ kind: Hermesh_Session_V1_TurnItemKind, _ more: (inout Hermesh_Session_V1_TurnItem) -> Void = { _ in }) -> Hermesh_Session_V1_TurnItem {
    var it = Hermesh_Session_V1_TurnItem()
    it.itemID = "i\(seq)"
    it.runID = run
    it.seq = seq
    it.kind = kind
    it.status = "completed"
    more(&it)
    return it
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

private let projection: Hermesh_Session_V1_SessionProjection = .with {
    $0.session = .with { $0.sessionID = "s_1"; $0.status = .idle }
    $0.runs = [
        .with { $0.runID = "r1"; $0.ordinal = 1; $0.status = .completed; $0.startedAt = .init(date: Date(timeIntervalSince1970: 1_800_000_000)) },
        .with { $0.runID = "r2"; $0.ordinal = 2; $0.status = .interrupted },
    ]
    $0.items = [
        item(1, "r1", .userMessage) { $0.text = "hi" },
        item(2, "r1", .reasoning) { $0.text = "thinking" },
        item(3, "r1", .commandExecution) { $0.toolName = "Bash"; $0.command = "ls"; $0.output = "a\nb" },
        item(4, "r1", .commandExecution) { $0.command = "false"; $0.exitCode = 1; $0.status = "failed" },
        item(5, "r1", .toolCall) { $0.toolName = "Read"; $0.filePath = "README.md"; $0.status = "running" },
        item(6, "r1", .fileChange) { $0.filePath = "a.txt"; $0.diff = "+x" },
        item(7, "r1", .checkpoint) { $0.checkpointID = "c1" },
        item(8, "r1", .assistantMessage) { $0.text = "done" },
        item(9, "r2", .userMessage) { $0.text = "again" },
        item(10, "r2", .assistantMessage) { $0.text = "par" },
    ]
}

private func kind(_ it: TranscriptItem) -> String {
    switch it {
    case .turn: "turn"
    case .user: "user"
    case .agent: "agent"
    case .tool: "tool"
    case .question: "question"
    case .turnEnd: "turnEnd"
    case .error: "error"
    case .message: "message"
    case .workspace: "workspace"
    }
}

@Test func sessionRowsAndStepsFromProjection() {
    let rows = sessionTranscript(projection)
    #expect(rows.map(kind) == ["turn", "user", "tool", "tool", "tool", "tool", "agent", "turn", "user", "agent", "turnEnd"])
    #expect(Set(rows.map(\.seq)).count == rows.count)
    #expect(rows.map(\.seq) == rows.map(\.seq).sorted())
    if case .turn(_, let n, let at, let wake) = rows[0] {
        #expect(n == 1)
        #expect(at == Date(timeIntervalSince1970: 1_800_000_000))
        #expect(wake.isEmpty)
    } else { Issue.record("no time line first") }
    if case .turnEnd(_, let end, _) = rows.last! { #expect(end.failure.kind == .interrupted) } else { Issue.record("no end") }

    guard case .tools(_, let calls)? = groupSteps(rows).first(where: { if case .tools = $0 { true } else { false } }) else {
        Issue.record("no tools"); return
    }
    #expect(calls.map(\.call.kind) == ["shell", "shell", "read", "edit"])
    #expect(calls[0].call.summary == "ls")
    #expect(calls[0].result?.summary == "a\nb")
    #expect(calls[1].result?.isError == true)
    #expect(calls[2].call.summary == "README.md")
    #expect(calls[2].running == false)
    #expect(calls[3].call.summary == "a.txt")
    #expect(calls[3].result?.summary == "+x")
}

@Test func sessionFailureAndQuestion() {
    #expect(sessionFailure(.with { $0.status = .crashed; $0.error = "boom" })?.message == "boom")
    #expect(sessionFailure(.with { $0.status = .idle; $0.error = "rejected" })?.message == "rejected")
    #expect(sessionFailure(.with { $0.status = .running }) == nil)
    let q = sessionQuestion(.with {
        $0.requestID = "req1"
        $0.kind = .userInput
        $0.userInput.questions = [.with { $0.id = "q1"; $0.text = "Color?"; $0.options = [.with { $0.label = "Red" }]; $0.allowOther = true; $0.optional = false }]
    })
    #expect(q.questionID == "req1")
    #expect(q.items[0].text == "Color?")
    #expect(q.items[0].allowOther && !q.items[0].optional)
    #expect(q.items[0].options[0].label == "Red")

    // Settled USER_INPUT runtime request renders as a resolved question row.
    let settledProj: Hermesh_Session_V1_SessionProjection = .with {
        $0.runs = [.with { $0.runID = "r1"; $0.ordinal = 1; $0.status = .completed }]
        $0.requests = [.with {
            $0.requestID = "req1"
            $0.kind = .userInput
            $0.status = .resolved
            $0.resolvedBy = .with { $0.kind = .owner; $0.id = "owner" }
            $0.userInput.questions = [.with { $0.id = "q1"; $0.text = "Proceed?" }]
            $0.resolvedAnswers = ["q1": .with { $0.selectedOptions = ["yes"] }]
        }]
        $0.items = [item(1, "r1", .runtimeRequest) { $0.requestID = "req1" }]
    }
    let qRows = sessionTranscript(settledProj)
    guard case .question(_, _, let res?) = qRows[1] else {
        Issue.record("expected resolved question row in \(qRows)")
        return
    }
    #expect(resolutionTitle(res) == "You answered: yes")
}

@Test func proposedPlanFormattingAvoidsDuplicateHeadings() {
    let rows = sessionTranscript(.with {
        $0.items = [
            item(1, "r1", .proposedPlan) { $0.planTitle = "Unified Refactor"; $0.text = "1. Build\n2. Test" },
            item(2, "r1", .proposedPlan) { $0.planTitle = "Unified Refactor"; $0.text = "# Unified Refactor\n\n1. Build\n2. Test" },
        ]
    })
    let texts = rows.compactMap { row -> String? in
        if case .agent(_, let text, _) = row { return text }
        return nil
    }
    #expect(texts == [
        "### Unified Refactor\n\n1. Build\n2. Test",
        "# Unified Refactor\n\n1. Build\n2. Test",
    ])
}

@Test func theRunUnderWaySaysWhetherItRunsAToolThinksOrReplies() {
    var p = projection
    p.session.activeRunID = "r2"
    #expect(sessionWork(p) == .working, "only r1 has items: r2 works")
    p.items.append(item(11, "r2", .reasoning) { $0.text = "weigh it"; $0.streaming = true })
    #expect(sessionWork(p) == .thinking)
    p.items[p.items.count - 1].streaming = false
    #expect(sessionWork(p) == .working, "done thinking, no reply yet")
    p.items.append(item(12, "r2", .assistantMessage) { $0.text = "On it"; $0.streaming = true })
    #expect(sessionWork(p) == .replying)
    p.items.append(item(13, "r2", .commandExecution) { $0.command = "go test ./..."; $0.status = "running" })
    #expect(sessionWork(p) == .tool, "a tool running comes first")
    p.items[p.items.count - 1].status = "completed"
    p.items[p.items.count - 2].streaming = false
    #expect(sessionWork(p) == .working)
    p.session.activeRunID = ""
    #expect(sessionWork(p) == .working, "no run under way")
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

@Test func transcriptShowsMessagesWithTheOwnersAgentsAndAskers() {
    let p: Hermesh_Session_V1_SessionProjection = .with {
        $0.runs = [
            .with { $0.runID = "r1"; $0.ordinal = 1; $0.status = .completed; $0.author = .with { $0.kind = .externalAgent; $0.id = "c_grok" } },
            .with { $0.runID = "r2"; $0.ordinal = 2; $0.status = .completed; $0.author = .with { $0.kind = .her; $0.id = "c_her" } },
        ]
        $0.items = [
            item(1, "r1", .userMessage) { $0.text = "do it" },
            item(2, "r1", .messageReceived) { $0.mailAddress = "c_helper"; $0.mailName = "helper"; $0.text = "check staging"; $0.mailDelivered = "queued" },
            item(3, "r1", .messageSent) { $0.mailAddress = "c_bot"; $0.text = "halfway" },
            item(4, "r2", .userMessage) { $0.text = "next" },
        ]
    }
    let rows = sessionTranscript(p)
    let msgs = rows.filter { if case .message = $0 { true } else { false } }
    #expect(msgs == [
        .message(seq: 8, incoming: true, name: "helper", text: "check staging", queued: true),
        .message(seq: 12, incoming: false, name: "c_bot", text: "halfway", queued: false),
    ])
    let askers = rows.compactMap { row -> String? in
        if case .user(_, _, _, let asker, _) = row { return asker } else { return nil }
    }
    #expect(askers == ["c_grok", "c_her"])

    let att = Hermesh_Session_V1_AttachmentRef.with {
        $0.blobSha256 = String(repeating: "a", count: 64)
        $0.name = "Photo.jpg"
        $0.mimeType = "image/jpeg"
        $0.sizeBytes = 1024
    }
    let withAttachments = sessionTranscript(.with {
        $0.runs = [.with { $0.runID = "r1"; $0.ordinal = 1; $0.status = .running }]
        $0.items = [item(1, "r1", .userMessage) { $0.text = ""; $0.attachments = [att] }]
    })
    guard case .user(_, let text, _, _, let gotAtts) = withAttachments.last else {
        Issue.record("expected user row with attachments")
        return
    }
    #expect(text == "" && gotAtts == [att])
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

@Test func interrupterIsWhoTheMachineRecorded() {
    var f = Failure()
    f.kind = .interrupted
    f.message = "interrupted by owner"
    #expect(interrupter(f) == "owner")
    f.message = "interrupted by c_abc"
    #expect(interrupter(f) == "c_abc")
    f.message = "agent revoked"
    #expect(interrupter(f) == nil)
    f.kind = .rateLimit
    f.message = "interrupted by owner"
    #expect(interrupter(f) == nil)
    #expect(interrupter(nil) == nil)
}

/// A first turn's WORKSPACE item is a row of its own, never in a group of tools; a failed one says why itself, so the
/// run's own error is not shown again.
@Test func workspaceStepIsARowOfItsOwn() {
    var p = Hermesh_Session_V1_SessionProjection()
    p.session.sessionID = "s"
    p.runs = [.with { $0.runID = "r1"; $0.ordinal = 1 }]
    let ws = Hermesh_Session_V1_WorkspaceStep.with {
        $0.mode = .worktree; $0.action = .create; $0.branch = "hermesh/s"; $0.baseRef = "main"; $0.path = "/wt/s"
    }
    p.items = [
        .with { $0.itemID = "u"; $0.runID = "r1"; $0.seq = 1; $0.kind = .userMessage; $0.text = "hi" },
        .with { $0.itemID = "w"; $0.runID = "r1"; $0.seq = 2; $0.kind = .workspace; $0.status = "running"; $0.workspace = ws },
        .with { $0.itemID = "t"; $0.runID = "r1"; $0.seq = 3; $0.kind = .commandExecution; $0.status = "completed"; $0.command = "ls" },
    ]
    var steps = groupSteps(sessionTranscript(p))
    guard case .item(.workspace(let row)) = steps[2] else { Issue.record("\(steps)"); return }
    #expect(row.state == .running && row.title.code == "main")
    #expect(steps.count == 4, "the tool after it is a group of its own")

    p.items[1].status = "completed"
    steps = groupSteps(sessionTranscript(p))
    guard case .item(.workspace(let done)) = steps[2] else { Issue.record("\(steps)"); return }
    #expect(done.state == .done && done.title.code == "hermesh/s")
    #expect(done.details.map(\.value) == ["Separate worktree", "hermesh/s", "main", "/wt/s"])

    // Failed: the row says why, and the run's error item is not a row too.
    p.items = [p.items[0], p.items[1], .with { $0.itemID = "e"; $0.runID = "r1"; $0.seq = 3; $0.kind = .error; $0.text = "no main" }]
    p.items[1].status = "failed"
    p.items[1].failure = .with { $0.message = "no main" }
    let rows = sessionTranscript(p)
    #expect(!rows.contains { if case .error = $0 { true } else { false } })
    guard case .workspace(let failed) = rows.last else { Issue.record("\(rows)"); return }
    #expect(failed.state == .failed && failed.details.map(\.value) == ["no main"])
    #expect(workspaceFailedRun(p) == "r1")

    // A switch says where the folder was.
    var sw = ws
    sw.mode = .inPlace; sw.action = .switch; sw.branch = "feat"; sw.previousBranch = "main"; sw.baseRef = ""
    let row2 = WorkspaceRow(seq: 1, state: .done, step: sw)
    #expect(row2.title.code == "feat" && row2.details.map(\.value) == ["In place", "feat (was main)", "/wt/s"])
}

@Test func activeTodoStepsAndFocusTrackTurnChainAndAutoCollapse() {
    var p = Hermesh_Session_V1_SessionProjection()
    p.session = .with { $0.sessionID = "s_todo"; $0.status = .running; $0.activeRunID = "r1" }
    p.runs = [
        .with { $0.runID = "r1"; $0.ordinal = 1; $0.status = .running; $0.author = .with { $0.kind = .owner; $0.id = "dev" } },
    ]
    p.items = [
        item(1, "r1", .userMessage) { $0.text = "optimize halo" },
        item(2, "r1", .todoList) {
            $0.itemID = "r1:todo:todo"
            $0.text = "Inspecting motion constraints"
            $0.todoSteps = [
                .with { $0.text = "Check motion tests"; $0.status = "in_progress" },
                .with { $0.text = "Rewrite CALayer sprite"; $0.status = "pending" },
            ]
        },
    ]

    #expect(activeTodoSteps(p) == [
        TodoStepView(text: "Check motion tests", status: .inProgress),
        TodoStepView(text: "Rewrite CALayer sprite", status: .pending),
    ])
    #expect(sessionFocusText(p) == "Inspecting motion constraints")

    p.items.append(item(3, "r1", .reasoning) { $0.text = "thinking..."; $0.streaming = true })
    #expect(sessionFocusText(p) == nil)
    p.items[2].streaming = false

    p.items[1].text = ""
    p.items.append(item(4, "r1", .commandExecution) { $0.command = "pnpm bench"; $0.text = "Run GPU benchmark"; $0.status = "running" })
    #expect(sessionFocusText(p) == "Run GPU benchmark")
    p.items[3].status = "completed"
    #expect(sessionFocusText(p) == "Check motion tests")

    p.session.status = .idle
    p.session.activeRunID = ""
    p.session.activeBackgroundCount = 1
    #expect(activeTodoSteps(p).count == 2)

    p.runs.append(.with { $0.runID = "r2"; $0.ordinal = 2; $0.status = .running; $0.author = .with { $0.kind = .system }; $0.wakeReason = "Run GPU benchmark" })
    p.session.status = .running
    p.session.activeRunID = "r2"
    #expect(activeTodoSteps(p).count == 2)

    p.runs.append(.with { $0.runID = "r3"; $0.ordinal = 3; $0.status = .running; $0.author = .with { $0.kind = .owner } })
    p.session.activeRunID = "r3"
    #expect(activeTodoSteps(p).isEmpty)

    p.items.append(item(5, "r3", .todoList) {
        $0.itemID = "r3:todo:todo"
        $0.todoSteps = [
            .with { $0.text = "Step A"; $0.status = "done" },
            .with { $0.text = "Step B"; $0.status = "skipped" },
        ]
    })
    #expect(activeTodoSteps(p).isEmpty)
}
