import Foundation
import HermeshProto

// A v2 thread's conversation as a session's (Transcript.swift), so both draw
// with the same rows. A port of @hermesh/core's threadtranscript.ts.

/// Each row's seq, unique and in order: an item's is its own ×4, the time line
/// before its run's first item 2 less, the end of its run's last 1 more.
private func itemSeq(_ it: Hermesh_Thread_V2_TurnItem) -> UInt64 { it.seq * 4 }

/// The kind of step a tool is, by its name (as a session's machine tells it).
func threadToolKind(_ name: String) -> String {
    if name.hasPrefix("mcp__") { return "mcp" }
    switch name {
    case "Read", "NotebookRead", "read_file", "view": return "read"
    case "Grep", "Glob", "LS", "search": return "search"
    case "Edit", "MultiEdit", "Write", "NotebookEdit", "apply_patch": return "edit"
    case "Bash", "BashOutput", "KillShell", "shell": return "shell"
    case "WebFetch", "WebSearch": return "network"
    default: return "other"
    }
}

private func isRunActive(_ run: Hermesh_Thread_V2_Run?) -> Bool {
    guard let run else { return true }
    return run.status == .preparing || run.status == .running || run.status == .waitingRequest
}

private func toolItem(_ it: Hermesh_Thread_V2_TurnItem, run: Hermesh_Thread_V2_Run?) -> ToolItem {
    var call = ToolCall()
    call.callID = it.itemID
    var result = ToolResult()
    result.callID = it.itemID
    if it.kind == .fileChange {
        call.kind = "edit"
        call.name = "Edit"
        call.summary = it.filePath
        result.summary = it.diff
        return ToolItem(seq: itemSeq(it), call: call, result: result, running: false)
    }
    let shell = it.kind == .commandExecution
    call.name = it.toolName.isEmpty && shell ? "Bash" : it.toolName
    call.kind = shell ? "shell" : threadToolKind(call.name)
    call.summary = shell ? it.command : it.filePath
    let running = it.status == "running" && isRunActive(run)
    result.summary = it.output
    result.isError = it.status == "failed" || it.exitCode != 0
    result.truncated = it.outputTruncated
    return ToolItem(seq: itemSeq(it), call: call, result: running ? nil : result, running: running)
}

private func runFailure(_ message: String) -> Failure {
    var f = Failure()
    f.kind = .unspecified
    f.message = message
    return f
}

/// A thread's items as a session's conversation rows. Reasoning, to-do lists,
/// requests (the page asks those below), checkpoints and notices have no row of
/// a session's to be.
public func threadTranscript(_ proj: Hermesh_Thread_V2_ThreadProjection?) -> [TranscriptItem] {
    guard let proj else { return [] }
    let runs = Dictionary(proj.runs.map { ($0.runID, $0) }, uniquingKeysWith: { _, b in b })
    let items = proj.items
    var out: [TranscriptItem] = []
    for (i, it) in items.enumerated() {
        let run = runs[it.runID]
        let seq = itemSeq(it)
        if let run, i == 0 || items[i - 1].runID != it.runID {
            let at = dateOf(run.startedAt, set: run.hasStartedAt) ?? dateOf(run.createdAt, set: run.hasCreatedAt) ?? dateOf(it.createdAt, set: it.hasCreatedAt)
            out.append(.turn(seq: seq - 2, turn: Int(run.ordinal), at: at))
        }
        switch it.kind {
        case .userMessage: out.append(.user(seq: seq, text: it.text, at: dateOf(it.createdAt, set: it.hasCreatedAt)))
        case .assistantMessage: out.append(.agent(seq: seq, text: it.text))
        case .proposedPlan: out.append(.agent(seq: seq, text: it.planTitle.isEmpty ? it.text : "### \(it.planTitle)\n\n\(it.text)"))
        case .toolCall, .commandExecution, .fileChange: out.append(.tool(toolItem(it, run: run)))
        case .error:
            var e = ErrorEvent()
            e.message = it.text
            e.failure = runFailure(it.text)
            out.append(.error(seq: seq, error: e))
        default: break
        }
        // A run stopped on purpose ends with a line that says so, as a session's interrupted turn does.
        if run?.status == .interrupted, i == items.count - 1 || items[i + 1].runID != it.runID {
            var end = TurnCompleted()
            end.turn = run!.ordinal
            end.isError = true
            end.failure.kind = .interrupted
            out.append(.turnEnd(seq: seq + 1, end: end, took: nil))
        }
    }
    return out
}

/// Why the thread stopped, or why the machine rejected what was sent, until the next run starts (which clears
/// it): told once, at the end of the conversation.
public func threadFailure(_ th: Hermesh_Thread_V2_Thread?) -> Failure? {
    guard let th, !th.lastError.isEmpty else { return nil }
    return runFailure(th.lastError)
}

/// A thread's questions as a session's question, so the same panel asks them.
public func threadQuestion(_ req: Hermesh_Thread_V2_RuntimeRequest) -> Question {
    var q = Question()
    q.questionID = req.requestID
    if req.hasCreatedAt { q.askedAt = req.createdAt }
    q.items = req.userInput.questions.map { tq in
        var it = Hermesh_Session_V1_QuestionItem()
        it.id = tq.id
        it.header = tq.header
        it.text = tq.question
        it.options = tq.options.map { o in
            var opt = Hermesh_Session_V1_QuestionOption()
            opt.label = o.label
            opt.description_p = o.description_p
            return opt
        }
        it.multiSelect = tq.multiSelect
        it.allowOther = tq.allowCustom
        it.optional = !tq.required
        return it
    }
    return q
}
