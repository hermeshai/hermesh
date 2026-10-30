import Foundation
import HermeshProto

// A session's conversation as the app shows it, made from its events, and
// how tool calls are summed up. A port of @hermesh/core's transcript.ts and
// steps.ts.

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
}

/// Whether a result's event holds only part of its output: a preview, or its first 16 KiB.
public func partialOutput(_ r: ToolResult) -> Bool {
    r.preview || r.size > UInt64(r.summary.utf8.count)
}

/// One row of the conversation.
public enum TranscriptItem: Equatable {
    case turn(seq: UInt64, turn: Int, at: Date?)
    /// at: when it was sent.
    case user(seq: UInt64, text: String, at: Date?)
    case agent(seq: UInt64, text: String)
    case tool(ToolItem)
    case question(seq: UInt64, question: Question, resolution: QuestionResolved?)
    /// took: seconds since the turn started, when known.
    case turnEnd(seq: UInt64, end: TurnCompleted, took: Double?)
    case error(seq: UInt64, error: ErrorEvent)
    /// A message between the session and one of the owner's agents
    /// (docs/agent-messages.md): incoming, written to the session (queued: it
    /// waits for a turn of its own), or written by its coding agent.
    case message(seq: UInt64, incoming: Bool, name: String, text: String, queued: Bool)

    public var seq: UInt64 {
        switch self {
        case .turn(let s, _, _), .user(let s, _, _), .agent(let s, _), .question(let s, _, _), .turnEnd(let s, _, _), .error(let s, _),
             .message(let s, _, _, _, _): s
        case .tool(let t): t.seq
        }
    }
}

/// Pairs tool calls with their results and questions with their answers;
/// status changes are left out. running: a turn is in progress, so its last
/// unanswered tool call runs.
public func transcript(_ events: [SessionEvent], running: Bool) -> [TranscriptItem] {
    var results: [String: ToolResult] = [:]
    var resultSeqs: [String: UInt64] = [:]
    var resolutions: [String: QuestionResolved] = [:]
    var lastCall: UInt64?
    for e in events {
        switch e.kind {
        case .toolResult(let r):
            results[r.callID] = r
            resultSeqs[r.callID] = e.seq
        case .questionResolved(let r): resolutions[r.questionID] = r
        case .toolCall: lastCall = e.seq
        default: break
        }
    }
    var items: [TranscriptItem] = []
    var turnStart: Date?
    for e in events {
        let at = dateOf(e.ts, set: e.hasTs)
        switch e.kind {
        case .turnStarted(let t):
            turnStart = at
            items.append(.turn(seq: e.seq, turn: Int(t.turn), at: at))
        case .userMessage(let m): items.append(.user(seq: e.seq, text: m.text, at: at))
        case .agentMessage(let m): items.append(.agent(seq: e.seq, text: m.text))
        case .toolCall(let c):
            let r = results[c.callID]
            items.append(.tool(ToolItem(seq: e.seq, call: c, result: r, resultSeq: resultSeqs[c.callID], running: r == nil && running && e.seq == lastCall && c.decision != "deny")))
        case .questionAsked(let q) where q.hasQuestion:
            items.append(.question(seq: e.seq, question: q.question, resolution: resolutions[q.question.questionID]))
        case .turnCompleted(let t):
            let took = at.flatMap { a in turnStart.map { a.timeIntervalSince($0) } }
            items.append(.turnEnd(seq: e.seq, end: t, took: took))
        case .error(let err): items.append(.error(seq: e.seq, error: err))
        case .messageReceived(let m):
            items.append(.message(seq: e.seq, incoming: true, name: m.fromName.isEmpty ? m.from : m.fromName, text: m.text, queued: m.delivered == "queued"))
        case .messageSent(let m):
            items.append(.message(seq: e.seq, incoming: false, name: m.toName.isEmpty ? m.to : m.toName, text: m.text, queued: false))
        default: break
        }
    }
    return items
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
