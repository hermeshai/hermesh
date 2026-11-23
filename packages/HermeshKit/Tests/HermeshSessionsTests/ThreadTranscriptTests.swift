import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

// A thread drawn as a session's conversation (ThreadTranscript.swift), as @hermesh/core's threadtranscript.test.ts.

private func item(_ seq: UInt64, _ run: String, _ kind: Hermesh_Thread_V2_TurnItemKind, _ more: (inout Hermesh_Thread_V2_TurnItem) -> Void = { _ in }) -> Hermesh_Thread_V2_TurnItem {
    var it = Hermesh_Thread_V2_TurnItem()
    it.itemID = "i\(seq)"
    it.runID = run
    it.seq = seq
    it.kind = kind
    it.status = "completed"
    more(&it)
    return it
}

private let projection: Hermesh_Thread_V2_ThreadProjection = .with {
    $0.thread = .with { $0.threadID = "th_1"; $0.status = .idle }
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
    }
}

@Test func threadRowsAreASessionsRows() {
    let rows = threadTranscript(projection)
    #expect(rows.map(kind) == ["turn", "user", "tool", "tool", "tool", "tool", "agent", "turn", "user", "agent", "turnEnd"])
    #expect(Set(rows.map(\.seq)).count == rows.count)
    #expect(rows.map(\.seq) == rows.map(\.seq).sorted())
    if case .turn(_, let n, let at) = rows[0] {
        #expect(n == 1)
        #expect(at == Date(timeIntervalSince1970: 1_800_000_000))
    } else { Issue.record("no time line first") }
    if case .turnEnd(_, let end, _) = rows.last! { #expect(end.failure.kind == .interrupted) } else { Issue.record("no end") }
}

@Test func threadToolsAreASessionsSteps() {
    guard case .tools(_, let calls)? = groupSteps(threadTranscript(projection)).first(where: { if case .tools = $0 { true } else { false } }) else {
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

@Test func threadFailureAndQuestion() {
    #expect(threadFailure(.with { $0.status = .failed; $0.lastError = "boom" })?.message == "boom")
    #expect(threadFailure(.with { $0.status = .idle; $0.lastError = "rejected" })?.message == "rejected")
    #expect(threadFailure(.with { $0.status = .running }) == nil)
    let q = threadQuestion(.with {
        $0.requestID = "req1"
        $0.kind = .userInput
        $0.userInput.questions = [.with { $0.id = "q1"; $0.question = "Color?"; $0.options = [.with { $0.label = "Red" }]; $0.allowCustom = true; $0.required = true }]
    })
    #expect(q.questionID == "req1")
    #expect(q.items[0].text == "Color?")
    #expect(q.items[0].allowOther && !q.items[0].optional)
    #expect(q.items[0].options[0].label == "Red")
}
