import Foundation
import HermeshProto

// A session's conversation as the app shows it, reduced from its unified
// SessionProjection, and how tool calls are summed up. A port of
// @hermesh/core's transcript.ts and steps.ts.

public typealias ToolCall = Hermesh_Session_V1_ToolCall
public typealias ToolResult = Hermesh_Session_V1_ToolResult
public typealias QuestionResolved = Hermesh_Session_V1_QuestionResolved
public typealias TurnCompleted = Hermesh_Session_V1_TurnCompleted
public typealias ErrorEvent = Hermesh_Session_V1_ErrorEvent

/// One tool call with its result, once there is one (resultSeq: its
/// event); running: the agent is still in it.
public struct ToolItem: Equatable {
    public let seq: UInt64
    public let call: ToolCall
    public let result: ToolResult?
    public var resultSeq: UInt64? = nil
    public let running: Bool

    public init(seq: UInt64, call: ToolCall, result: ToolResult?, resultSeq: UInt64? = nil, running: Bool) {
        self.seq = seq
        self.call = call
        self.result = result
        self.resultSeq = resultSeq
        self.running = running
    }
}

/// Whether a call's event holds only the first 16 KiB of its input arguments.
public func partialInput(_ c: ToolCall) -> Bool {
    (c.inputTruncated && !c.inputBlobSha256.isEmpty) || c.inputSize > UInt64(c.input.utf8.count)
}

/// Whether a result's event holds only part of its output: a preview, or its first 16 KiB.
public func partialOutput(_ r: ToolResult) -> Bool {
    r.preview || r.size > UInt64(r.summary.utf8.count)
}

/// One row of the conversation.
public enum TranscriptItem: Equatable {
    case turn(seq: UInt64, turn: Int, at: Date?, wakeReason: String = "")
    /// at: when it was sent. asker: who asked for its turn (a client id, or "owner"; "" when the machine didn't say).
    case user(seq: UInt64, text: String, at: Date?, asker: String = "", attachments: [Hermesh_Session_V1_AttachmentRef] = [])
    /// runID: the turn it belongs to, whose files its links open as that turn left them (docs/session-files.md §6.2).
    case agent(seq: UInt64, text: String, runID: String = "")
    case tool(ToolItem)
    case question(seq: UInt64, question: Question, resolution: QuestionResolved?)
    /// took: seconds since the turn started, when known.
    case turnEnd(seq: UInt64, end: TurnCompleted, took: Double?)
    case error(seq: UInt64, error: ErrorEvent)
    /// A message between the session and one of the owner's agents
    /// (docs/agent-messages.md): incoming, written to the session (queued: it
    /// waits for a turn of its own), or written by its coding agent.
    case message(seq: UInt64, incoming: Bool, name: String, text: String, queued: Bool)
    /// Where the session works, set up by its first turn: a worktree made, a branch switched to, or the folder kept.
    case workspace(WorkspaceRow)

    public var seq: UInt64 {
        switch self {
        case .turn(let s, _, _, _), .user(let s, _, _, _, _), .agent(let s, _, _), .question(let s, _, _), .turnEnd(let s, _, _), .error(let s, _),
             .message(let s, _, _, _, _): s
        case .tool(let t): t.seq
        case .workspace(let w): w.seq
        }
    }
}

/// A session's first turn setting its folder up (a WORKSPACE item), as its row shows it.
public struct WorkspaceRow: Equatable, Sendable {
    public enum State: Equatable, Sendable { case running, done, failed }
    public var seq: UInt64
    public var state: State
    public var step: Hermesh_Session_V1_WorkspaceStep
    /// Why it failed, as the machine said it.
    public var failure: String
    /// When the machine first said it, to show a step under 400 ms by its result alone.
    public var since: Date?

    public init(seq: UInt64, state: State, step: Hermesh_Session_V1_WorkspaceStep, failure: String = "", since: Date? = nil) {
        self.seq = seq
        self.state = state
        self.step = step
        self.failure = failure
        self.since = since
    }

    /// Its words, then the part in code: "New branch from main" `hermesh/s_…`, "Switching to branch" `feature/login`.
    public var title: (text: String, code: String) {
        let base = step.baseRef, branch = step.branch
        switch (step.action, state) {
        case (.fetch, .running): return (String(localized: "Fetching from the remote", bundle: .module), base)
        case (.fetch, _): return (String(localized: "Couldn’t fetch from the remote", bundle: .module), base)
        case (.create, .running): return (String(localized: "Making a worktree from", bundle: .module), base)
        case (.create, .done): return (String(localized: "New branch from \(base)", bundle: .module), branch)
        case (.create, _): return (String(localized: "Couldn’t make the worktree", bundle: .module), "")
        case (.switch, .running): return (String(localized: "Switching to branch", bundle: .module), branch)
        case (.switch, .done): return (String(localized: "Switched to branch", bundle: .module), branch)
        case (.switch, _): return (String(localized: "Couldn’t switch to branch", bundle: .module), branch)
        default: return (String(localized: "Working in place, on branch", bundle: .module), branch)
        }
    }

    /// Its details, opened: how, the branch, its base, the folder; or why it failed.
    public var details: [(label: String, value: String)] {
        if state == .failed {
            // Stopped while it was under way: no failure of its own.
            return [(String(localized: "Why", bundle: .module), failure.isEmpty ? String(localized: "Stopped before it was done", bundle: .module) : failure)]
        }
        var out: [(String, String)] = []
        let worktree = step.mode == .worktree
        out.append((String(localized: "Where", bundle: .module), worktree ? String(localized: "Separate worktree", bundle: .module) : String(localized: "In place", bundle: .module)))
        if !step.branch.isEmpty {
            let was = step.action == .switch && !step.previousBranch.isEmpty ? String(localized: "\(step.branch) (was \(step.previousBranch))", bundle: .module) : step.branch
            out.append((String(localized: "Branch", bundle: .module), was))
        }
        if worktree && !step.baseRef.isEmpty { out.append((String(localized: "From", bundle: .module), step.baseRef)) }
        if !step.path.isEmpty { out.append((String(localized: "Folder", bundle: .module), step.path)) }
        return out
    }
}

/// The run whose first turn could not set its folder up: its row says so, so the run's own error is not a row too (the
/// session's failure card still says what to do).
public func workspaceFailedRun(_ proj: Hermesh_Session_V1_SessionProjection?) -> String? {
    proj?.items.last { $0.kind == .workspace && $0.status == "failed" }?.runID
}

/// Each row's seq, unique and in order: an item's is its own ×4, the time line
/// before its run's first item 2 less, the end of its run's last 1 more.
private func itemSeq(_ it: Hermesh_Session_V1_TurnItem) -> UInt64 { it.seq * 4 }

/// The kind of step a tool is, by its name.
func sessionToolKind(_ name: String) -> String {
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

private func isRunActive(_ run: Hermesh_Session_V1_Run?) -> Bool {
    guard let run else { return true }
    return run.status == .preparing || run.status == .running || run.status == .waitingRequest
}

private func actorID(_ actor: Hermesh_Session_V1_Actor?) -> String {
    guard let actor, actor.kind != .unspecified else { return "" }
    return actor.kind == .owner ? "owner" : actor.id
}

private func toolItem(_ it: Hermesh_Session_V1_TurnItem, run: Hermesh_Session_V1_Run?, activeBgItems: Set<String>) -> ToolItem {
    var call = ToolCall()
    call.callID = it.itemID
    if it.status == "denied" { call.decision = "deny" }
    call.input = it.input
    call.inputTruncated = it.inputTruncated
    call.inputBlobSha256 = it.inputBlobSha256
    call.inputSize = it.inputSize
    var result = ToolResult()
    result.callID = it.itemID
    if it.kind == .fileChange {
        call.kind = "edit"
        call.name = "Edit"
        call.summary = it.filePath.isEmpty ? it.text : it.filePath
        result.summary = it.diff
        return ToolItem(seq: itemSeq(it), call: call, result: result, running: false)
    }
    let shell = it.kind == .commandExecution
    call.name = it.toolName.isEmpty && shell ? "Bash" : it.toolName
    call.kind = shell ? "shell" : sessionToolKind(call.name)
    call.summary = shell ? it.command : it.filePath
    let running = it.status == "running" && (isRunActive(run) || activeBgItems.contains(it.itemID)) && call.decision != "deny"
    result.summary = it.output
    result.isError = it.status == "failed" || it.exitCode != 0
    result.truncated = it.outputTruncated
    result.size = it.outputSize
    result.blobSha256 = it.outputBlobSha256
    return ToolItem(seq: itemSeq(it), call: call, result: running ? nil : result, running: running)
}

private func formatProposedPlan(title: String, text: String) -> String {
    if title.isEmpty || text.drop(while: { $0.isWhitespace || $0.isNewline }).hasPrefix("#") {
        return text
    }
    return "### \(title)\n\n\(text)"
}

private func runFailure(_ message: String) -> Failure {
    var f = Failure()
    f.kind = .unspecified
    f.message = message
    return f
}

private func settledQuestionResolution(_ req: Hermesh_Session_V1_RuntimeRequest) -> QuestionResolved {
    var r = QuestionResolved()
    r.questionID = req.requestID
    r.by = actorID(req.hasResolvedBy ? req.resolvedBy : nil)
    if req.questionResolution != .unspecified {
        r.resolution = req.questionResolution
    } else {
        switch req.status {
        case .expired: r.resolution = .timedOut
        case .dismissed: r.resolution = .cancelled
        default: r.resolution = req.resolvedDecision == .decline ? .declined : .answered
        }
    }
    if r.resolution == .answered {
        let orderedIDs = req.userInput.questions.map(\.id)
        for qid in orderedIDs {
            if let ans = req.resolvedAnswers[qid] {
                var a = Hermesh_Session_V1_Answer()
                a.itemID = qid
                a.selected = ans.selectedOptions
                a.text = ans.customText
                r.answers.append(a)
            }
        }
    }
    return r
}

/// A session's items reduced into conversation rows.
public func sessionTranscript(_ proj: Hermesh_Session_V1_SessionProjection?) -> [TranscriptItem] {
    guard let proj else { return [] }
    let runs = Dictionary(proj.runs.map { ($0.runID, $0) }, uniquingKeysWith: { _, b in b })
    let requests = Dictionary(proj.requests.map { ($0.requestID, $0) }, uniquingKeysWith: { _, b in b })
    let activeBgItems = activeBackgroundItemIDs(of: proj)
    let items = proj.items
    let setupFailed = workspaceFailedRun(proj)
    var out: [TranscriptItem] = []
    for (i, it) in items.enumerated() {
        let run = runs[it.runID]
        let seq = itemSeq(it)
        if let run, i == 0 || items[i - 1].runID != it.runID {
            let at = dateOf(run.startedAt, set: run.hasStartedAt) ?? dateOf(run.createdAt, set: run.hasCreatedAt) ?? dateOf(it.createdAt, set: it.hasCreatedAt)
            let wakeReason = run.author.kind == .system ? run.wakeReason : ""
            out.append(.turn(seq: seq - 2, turn: Int(run.ordinal), at: at, wakeReason: wakeReason))
        }
        switch it.kind {
        case .userMessage:
            let asker = actorID(it.hasActor ? it.actor : (run?.hasAuthor == true ? run?.author : nil))
            out.append(.user(seq: seq, text: it.text, at: dateOf(it.createdAt, set: it.hasCreatedAt), asker: asker, attachments: it.attachments))
        case .assistantMessage:
            out.append(.agent(seq: seq, text: it.text, runID: it.runID))
        case .proposedPlan:
            out.append(.agent(seq: seq, text: formatProposedPlan(title: it.planTitle, text: it.text), runID: it.runID))
        case .toolCall:
            if isMetaProgressToolItem(it) { break }
            out.append(.tool(toolItem(it, run: run, activeBgItems: activeBgItems)))
        case .commandExecution, .fileChange:
            out.append(.tool(toolItem(it, run: run, activeBgItems: activeBgItems)))
        case .runtimeRequest:
            if let req = requests[it.requestID], req.kind == .userInput, req.status != .pending {
                out.append(.question(seq: seq, question: sessionQuestion(req), resolution: settledQuestionResolution(req)))
            }
        case .messageReceived:
            out.append(.message(seq: seq, incoming: true, name: it.mailName.isEmpty ? it.mailAddress : it.mailName, text: it.text, queued: it.mailDelivered == "queued"))
        case .messageSent:
            out.append(.message(seq: seq, incoming: false, name: it.mailName.isEmpty ? it.mailAddress : it.mailName, text: it.text, queued: false))
        case .workspace:
            let state: WorkspaceRow.State = it.status == "completed" ? .done : it.status == "running" ? .running : .failed
            out.append(.workspace(WorkspaceRow(seq: seq, state: state, step: it.workspace,
                                               failure: it.hasFailure ? it.failure.message : it.text,
                                               since: dateOf(it.createdAt, set: it.hasCreatedAt))))
        case .error where it.runID == setupFailed:
            break
        case .error:
            var e = ErrorEvent()
            e.message = it.text
            e.failure = it.hasFailure ? it.failure : runFailure(it.text)
            out.append(.error(seq: seq, error: e))
        default:
            break
        }
        // A run stopped on purpose ends with a line that says so.
        if run?.status == .interrupted, i == items.count - 1 || items[i + 1].runID != it.runID {
            var end = TurnCompleted()
            end.turn = run!.ordinal
            end.isError = true
            if run!.hasFailure {
                end.failure = run!.failure
            } else {
                end.failure.kind = .interrupted
                end.failure.message = run!.errorMessage
            }
            let start = dateOf(run!.startedAt, set: run!.hasStartedAt)
            let done = dateOf(run!.completedAt, set: run!.hasCompletedAt)
            let took = done.flatMap { d in start.map { d.timeIntervalSince($0) } }
            out.append(.turnEnd(seq: seq + 1, end: end, took: took))
        }
    }
    return out
}

/// What the session's active run does now: runs a tool, thinks, writes its reply, or anything else (working).
public enum SessionWork: Equatable, Sendable { case tool, thinking, replying, working }

public func sessionWork(_ proj: Hermesh_Session_V1_SessionProjection?) -> SessionWork {
    guard let proj, !proj.session.activeRunID.isEmpty else { return .working }
    let now = proj.items.filter { $0.runID == proj.session.activeRunID }
    if now.contains(where: { $0.status == "running" && [.toolCall, .commandExecution, .fileChange].contains($0.kind) && !isMetaProgressToolItem($0) }) { return .tool }
    if now.contains(where: { $0.kind == .reasoning && $0.streaming }) { return .thinking }
    if now.contains(where: { $0.kind == .assistantMessage && $0.streaming }) { return .replying }
    return .working
}

/// The `seq` values of replies the agent is still streaming, so their rows sweep in as they grow.
public func sessionLiveReplies(_ proj: Hermesh_Session_V1_SessionProjection?) -> Set<UInt64> {
    guard let proj else { return [] }
    return Set(proj.items.filter { $0.kind == .assistantMessage && $0.streaming }.map(itemSeq))
}

public enum TodoStepStatus: String, Equatable, Sendable {
    case pending
    case inProgress = "in_progress"
    case completed
    case skipped
    case failed
}

public struct TodoStepView: Equatable, Sendable {
    public let text: String
    public let status: TodoStepStatus

    public init(text: String, status: TodoStepStatus) {
        self.text = text
        self.status = status
    }
}

public func normalizeTodoStepStatus(_ raw: String) -> TodoStepStatus {
    switch raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() {
    case "completed", "complete", "done": .completed
    case "in_progress", "inprogress", "active", "running": .inProgress
    case "skipped", "cancelled", "canceled": .skipped
    case "failed", "error": .failed
    default: .pending
    }
}

private func currentTurnChainRunIDs(_ proj: Hermesh_Session_V1_SessionProjection) -> Set<String> {
    var out = Set<String>()
    let activeID = proj.session.activeRunID
    var idx: Int? = activeID.isEmpty ? nil : proj.runs.firstIndex(where: { $0.runID == activeID })
    if idx == nil {
        idx = proj.runs.lastIndex(where: { $0.status != .queued && $0.status != .rolledBack })
    }
    guard var i = idx else {
        if !activeID.isEmpty { out.insert(activeID) }
        return out
    }
    while i >= 0 {
        let r = proj.runs[i]
        out.insert(r.runID)
        if r.author.kind != .system { break }
        i -= 1
    }
    return out
}

private func isSessionChecklistLive(_ proj: Hermesh_Session_V1_SessionProjection) -> Bool {
    let s = proj.session
    switch s.status {
    case .preparing, .running, .waitingInput:
        return true
    case .idle:
        return s.activeBackgroundCount > 0 || activeBackgroundTasks(of: proj).contains(where: \.awaited)
    default:
        return false
    }
}

private func normalizeMetaProgressToolName(_ rawName: String) -> String {
    var norm = rawName.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    for pfx in ["run ", "call ", "tool: ", "tool "] where norm.hasPrefix(pfx) {
        norm = String(norm.dropFirst(pfx.count)).trimmingCharacters(in: .whitespacesAndNewlines)
        break
    }
    if let r = norm.range(of: "__", options: .backwards) {
        norm = String(norm[r.upperBound...])
    } else if let i = norm.lastIndex(of: "/") {
        norm = String(norm[norm.index(after: i)...])
    } else if let i = norm.lastIndex(of: ".") {
        norm = String(norm[norm.index(after: i)...])
    }
    for sfx in ["update_focus", "update_steps", "todowrite", "todo_write", "update_plan"] {
        if norm == sfx || (norm.hasPrefix("mcp_") && norm.hasSuffix("_\(sfx)")) { return sfx }
    }
    if ["todoread", "taskcreate", "taskupdate", "tasklist", "taskget"].contains(norm) { return norm }
    return ""
}

private func isMetaProgressToolItem(_ it: Hermesh_Session_V1_TurnItem) -> Bool {
    it.kind == .toolCall && it.command.isEmpty && !normalizeMetaProgressToolName(it.toolName).isEmpty
}

private func reduceChainProgress(_ proj: Hermesh_Session_V1_SessionProjection, chain: Set<String>) -> (steps: [TodoStepView], focus: String) {
    var steps: [TodoStepView] = []
    var focus = ""
    for it in proj.items where chain.contains(it.runID) {
        if it.kind == .todoList {
            if !it.todoSteps.isEmpty {
                steps = it.todoSteps.compactMap { step in
                    let text = step.text.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !text.isEmpty else { return nil }
                    return TodoStepView(text: text, status: normalizeTodoStepStatus(step.status))
                }
            }
            focus = it.text.trimmingCharacters(in: .whitespacesAndNewlines)
            continue
        }
        guard it.kind == .toolCall, it.command.isEmpty else { continue }
        let meta = normalizeMetaProgressToolName(it.toolName)
        guard !meta.isEmpty,
              let data = it.input.data(using: .utf8),
              let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else { continue }
        if meta == "update_focus" {
            if let f = (obj["focus"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines), !f.isEmpty {
                focus = f
            }
            continue
        }
        let rawList = (obj["steps"] as? [[String: Any]])
            ?? (obj["todos"] as? [[String: Any]])
            ?? (obj["plan"] as? [[String: Any]])
            ?? (obj["items"] as? [[String: Any]])
        guard let rawList else { continue }
        var nextFocus = ((obj["focus"] as? String) ?? (obj["explanation"] as? String) ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        var parsed: [TodoStepView] = []
        for rec in rawList {
            let candidates = [rec["title"], rec["content"], rec["step"], rec["text"]].compactMap { $0 as? String }
            guard let label = candidates.map({ $0.trimmingCharacters(in: .whitespacesAndNewlines) }).first(where: { !$0.isEmpty }) else {
                continue
            }
            let st = normalizeTodoStepStatus((rec["status"] as? String) ?? "")
            parsed.append(TodoStepView(text: label, status: st))
            if nextFocus.isEmpty, st == .inProgress,
               let af = (rec["activeForm"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines),
               !af.isEmpty {
                nextFocus = af
            }
        }
        steps = parsed
        focus = nextFocus
    }
    return (steps, focus)
}

/// Steps for the floating live checklist above the composer while the session is working.
/// Automatically returns `[]` once the turn settles or all steps are completed/skipped.
public func activeTodoSteps(_ proj: Hermesh_Session_V1_SessionProjection?) -> [TodoStepView] {
    guard let proj, isSessionChecklistLive(proj) else { return [] }
    let chain = currentTurnChainRunIDs(proj)
    guard !chain.isEmpty else { return [] }
    let (steps, _) = reduceChainProgress(proj, chain: chain)
    if steps.isEmpty || steps.allSatisfy({ $0.status == .completed || $0.status == .skipped }) {
        return []
    }
    return steps
}

/// Native live focus phrase for the bottom working line when the model is not actively streaming
/// thoughts or reply prose; `nil` falls back to the localized generic work label.
public func sessionFocusText(_ proj: Hermesh_Session_V1_SessionProjection?) -> String? {
    guard let proj,
          proj.session.status == .running,
          !proj.session.activeRunID.isEmpty else {
        return nil
    }
    let work = sessionWork(proj)
    if work == .thinking || work == .replying { return nil }

    let chain = currentTurnChainRunIDs(proj)
    let (_, focus) = reduceChainProgress(proj, chain: chain)
    if !focus.isEmpty { return focus }

    let activeRunID = proj.session.activeRunID
    if let runningTool = proj.items.last(where: {
        $0.runID == activeRunID &&
        $0.status == "running" &&
        ($0.kind == .commandExecution || ($0.kind == .toolCall && !isMetaProgressToolItem($0))) &&
        !$0.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }) {
        return runningTool.text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    return activeTodoSteps(proj).first(where: { $0.status == .inProgress })?.text
}

/// Why the session stopped, or why the machine rejected what was sent, until the next run starts.
public func sessionFailure(_ s: Session?) -> Failure? {
    guard let s else { return nil }
    if s.progress.hasLastFailure && s.progress.lastFailure.kind != .interrupted {
        return s.progress.lastFailure
    }
    if !s.error.isEmpty {
        return runFailure(s.error)
    }
    return nil
}

/// A runtime `USER_INPUT` request projected as `Question` for `QuestionPanel`.
public func sessionQuestion(_ req: Hermesh_Session_V1_RuntimeRequest) -> Question {
    var q = Question()
    q.questionID = req.requestID
    if req.hasCreatedAt { q.askedAt = req.createdAt }
    if req.hasExpiresAt { q.expiresAt = req.expiresAt }
    q.items = req.userInput.questions
    return q
}

/// How long a turn took: "42s", "3m 05s", "1h 12m".
public func durationText(_ seconds: Double) -> String {
    let s = max(0, Int(seconds.rounded()))
    if s < 60 { return String(localized: "\(s)s", bundle: .module) }
    let m = s / 60
    let seconds = String(format: "%02d", s % 60)
    if m < 60 { return String(localized: "\(m)m \(seconds)s", bundle: .module) }
    return String(localized: "\(m / 60)h \(m % 60)m", bundle: .module)
}

/// The turn was stopped on purpose (by the owner or the agent), not by a failure.
public func isInterruption(_ f: Failure?) -> Bool { f?.kind == .interrupted }

/// Who stopped an interrupted turn, as the machine recorded it: "owner" or an agent's client id.
public func interrupter(_ f: Failure?) -> String? {
    let prefix = "interrupted by "
    guard let f, isInterruption(f), f.message.hasPrefix(prefix) else { return nil }
    return String(f.message.dropFirst(prefix.count))
}

/// How a question was settled. name: who answered, by the id the machine recorded (the owner is "You").
public func resolutionTitle(_ r: QuestionResolved, name: (String) -> String = { $0 }) -> String {
    let you = r.by == "owner"
    switch r.resolution {
    case .answered:
        let answers = r.answers.map { a in (a.selected + (a.text.isEmpty ? [] : [a.text])).joined(separator: ", ") }
            .joined(separator: String(localized: "; ", bundle: .module, comment: "Separates the answers to a question's items"))
        return you ? String(localized: "You answered: \(answers)", bundle: .module) : String(localized: "\(name(r.by)) answered: \(answers)", bundle: .module)
    case .declined:
        return you ? String(localized: "You chose not to answer", bundle: .module) : String(localized: "\(name(r.by)) chose not to answer", bundle: .module)
    case .timedOut: return String(localized: "No one answered; the agent went on by itself", bundle: .module)
    default: return String(localized: "The question was withdrawn", bundle: .module)
    }
}

// MARK: Steps

/// A conversation row, with consecutive tool calls gathered into one group.
public enum Step: Identifiable, Equatable {
    case item(TranscriptItem)
    case tools(seq: UInt64, calls: [ToolItem])

    public var id: UInt64 {
        switch self {
        case .item(let i): i.seq
        case .tools(let seq, _): seq
        }
    }
}

public func groupSteps(_ items: [TranscriptItem]) -> [Step] {
    var out: [Step] = []
    for it in items {
        if case .tool(let t) = it {
            if case .tools(let seq, var calls)? = out.last {
                calls.append(t)
                out[out.count - 1] = .tools(seq: seq, calls: calls)
            } else {
                out.append(.tools(seq: t.seq, calls: [t]))
            }
        } else {
            out.append(.item(it))
        }
    }
    return out
}

private enum VerbKind: String {
    case read, search, edit, shell, network, mcp, other, deny
}

private func verbKind(_ c: ToolCall) -> VerbKind {
    if c.decision == "deny" { return .deny }
    return VerbKind(rawValue: c.kind).flatMap { $0 == .other || $0 == .deny ? nil : $0 } ?? .other
}

/// The verb for a call: "Edited", "Editing"; a denied call is "Denied".
public func toolVerb(_ c: ToolCall, running: Bool) -> String {
    switch verbKind(c) {
    case .read: running ? String(localized: "Reading", bundle: .module) : String(localized: "Read", bundle: .module)
    case .search: running ? String(localized: "Searching", bundle: .module) : String(localized: "Searched", bundle: .module)
    case .edit: running ? String(localized: "Editing", bundle: .module) : String(localized: "Edited", bundle: .module)
    case .shell: running ? String(localized: "verb.running", defaultValue: "Running", bundle: .module, comment: "A command being run (the status “Running” is another word in some languages)") : String(localized: "Ran", bundle: .module)
    case .network: running ? String(localized: "Fetching", bundle: .module) : String(localized: "Fetched", bundle: .module)
    case .mcp: running ? String(localized: "Calling", bundle: .module) : String(localized: "Called", bundle: .module)
    case .other: running ? String(localized: "Using", bundle: .module) : String(localized: "Used", bundle: .module)
    case .deny: String(localized: "Denied", bundle: .module)
    }
}

private let toolWords: Set<String> = ["read", "write", "edit", "multiedit", "bash", "grep", "glob", "search", "fetch", "webfetch", "websearch", "ls", "list", "view", "create"]

/// What a call acted on, without the agent's own tool name in front.
public func toolObject(_ c: ToolCall) -> String {
    let s = c.summary.trimmingCharacters(in: .whitespacesAndNewlines)
    if s.isEmpty { return c.name }
    if let m = s.firstMatch(of: #/^([A-Za-z][\w-]*)\s+([\s\S]+)$/#) {
        let w = String(m.output.1).lowercased()
        if w == c.name.lowercased() || toolWords.contains(w) { return String(m.output.2) }
    }
    return s
}

private func countText(_ k: VerbKind, _ n: Int) -> String {
    switch k {
    case .read: n == 1 ? String(localized: "Read 1 file", bundle: .module) : String(localized: "Read \(n) files", bundle: .module)
    case .search: n == 1 ? String(localized: "Searched once", bundle: .module) : String(localized: "Searched \(n) times", bundle: .module)
    case .edit: n == 1 ? String(localized: "Edited 1 file", bundle: .module) : String(localized: "Edited \(n) files", bundle: .module)
    case .shell: n == 1 ? String(localized: "Ran 1 command", bundle: .module) : String(localized: "Ran \(n) commands", bundle: .module)
    case .network: n == 1 ? String(localized: "Fetched 1 page", bundle: .module) : String(localized: "Fetched \(n) pages", bundle: .module)
    case .mcp: n == 1 ? String(localized: "Called 1 tool", bundle: .module) : String(localized: "Called \(n) tools", bundle: .module)
    case .other: n == 1 ? String(localized: "Used 1 tool", bundle: .module) : String(localized: "Used \(n) tools", bundle: .module)
    case .deny: n == 1 ? String(localized: "Denied 1 action", bundle: .module) : String(localized: "Denied \(n) actions", bundle: .module)
    }
}

/// A group's line: "Read 3 files · Ran 2 commands", in the order the kinds first appear.
public func stepSummary(_ calls: [ToolItem]) -> String {
    var order: [VerbKind] = []
    var counts: [VerbKind: Int] = [:]
    for c in calls {
        let k = verbKind(c.call)
        if counts[k] == nil { order.append(k) }
        counts[k, default: 0] += 1
    }
    return order.map { countText($0, counts[$0]!) }.joined(separator: String(localized: " · ", bundle: .module, comment: "Separates the parts of a step's summary"))
}

/// A turn's time line, shown only for the first turn and after a pause of half an hour.
public func turnTimeLine(_ at: Date, previous: Date?, now: Date, calendar: Calendar = .current) -> String? {
    if let previous, at.timeIntervalSince(previous) < 30 * 60 { return nil }
    let day = calendar.isDate(at, inSameDayAs: now) ? String(localized: "Today", bundle: .module)
        : isYesterday(at, now: now, calendar: calendar) ? String(localized: "Yesterday", bundle: .module)
        : withinWeek(at, now: now, calendar: calendar) ? weekdayName(at) : monthDay(at)
    return "\(day) \(hourMinute(at, calendar: calendar))"
}
