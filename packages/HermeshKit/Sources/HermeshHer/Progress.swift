import Foundation

// The work Her sent out, as the Progress page shows it (docs/her-team.md §15, her-client's progress.ts): for
// each thing the owner said to her, the sub-agents she started for that and the coding sessions she started on
// the mesh (the iPhone shows each as a post of its own, with what set it off at its foot); a sub-agent's post; and
// its record, everything it thought, said and called, in order. Worked out from the
// conversation the app already follows, sub-agents' own conversations included (their agent_events), so the
// page asks her for nothing more.

// MARK: The groups

/// A sub-agent Her started: the spawn_agent call, and the agent as it is now (once agent_started came).
public struct HerLaunch: Equatable, Sendable {
    public var callID: String
    /// Its handle: how Her addresses it.
    public var name: String
    /// What the user sees for it; empty for calls from before it had one.
    public var description: String
    public var task: String
    public var agent: HerAgent?

    public init(callID: String, name: String, description: String, task: String, agent: HerAgent?) {
        self.callID = callID
        self.name = name
        self.description = description
        self.task = task
        self.agent = agent
    }
}

public enum HerProgressTask: Equatable, Identifiable, Sendable {
    case agent(HerLaunch)
    /// A coding session Her started through the mesh; how it stands is the app's to say, from the sessions it holds.
    case session(key: String, HerStarted)

    /// The call that started it.
    public var id: String {
        switch self {
        case .agent(let l): l.callID
        case .session(let key, _): key
        }
    }
}

/// What set a group's work off. earlier: the calls that started these agents are on a page of the history not read yet.
public enum HerProgressParent: Equatable, Sendable {
    case user(text: String, ts: Double)
    case routine(name: String, ts: Double)
    case earlier
}

public struct HerProgressGroup: Equatable, Identifiable, Sendable {
    public var key: String
    public var parent: HerProgressParent
    /// What Her last told the owner since then.
    public var said: String?
    public var tasks: [HerProgressTask]
    public var id: String { key }
}

/// The groups, oldest first; only what sent work out is one.
public func herProgress(_ v: HerSession) -> [HerProgressGroup] {
    var results: [String: HerResult] = [:]
    for case .result(let r) in v.entries { results[r.callID] = r }
    let byCall = Dictionary(v.agents.map { ($0.callID, $0) }, uniquingKeysWith: { $1 })
    let lastReply = v.entries.lastIndex { if case .assistant = $0 { return true } else { return false } }

    var out: [HerProgressGroup] = []
    var current: HerProgressGroup?
    var launched: Set<String> = []
    func begin(_ g: HerProgressGroup) {
        if let c = current, !c.tasks.isEmpty { out.append(c) }
        current = g
    }
    // Work with nothing before it in what the conversation holds belongs to a message on an earlier page.
    func add(_ t: HerProgressTask) {
        if current == nil { current = HerProgressGroup(key: "earlier", parent: .earlier, tasks: []) }
        current?.tasks.append(t)
    }
    for (i, e) in v.entries.enumerated() {
        switch e {
        case .user(let id, let content, let ts, let mail):
            if let mail {
                // A routine's run is Her's to act on, as the owner's message is.
                if mail.kind == "routine" { begin(HerProgressGroup(key: id, parent: .routine(name: mail.fromName, ts: ts), tasks: [])) }
            } else if case .user(let text, _, _, _) = ownerKind(content, queued: false) {
                begin(HerProgressGroup(key: id, parent: .user(text: text, ts: ts), tasks: []))
            }
        case .assistant(_, let calls, _, _, _):
            for c in calls {
                let r = results[c.id]
                let target = herCallTarget(c.name, c.args).name
                if c.name == "spawn_agent" {
                    guard r?.isError != true else { continue }
                    launched.insert(c.id)
                    let agent = byCall[c.id]
                    let name = agent?.name ?? r?.details["name"].string ?? c.args["name"].string ?? "agent"
                    let said = c.args["description"].string?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                    add(.agent(HerLaunch(callID: c.id, name: name, description: agent.flatMap { $0.description.isEmpty ? nil : $0.description } ?? said,
                                         task: c.args["task"].string ?? "", agent: agent)))
                } else if target == sessionStart, let r, !r.isError, let started = HerStarted(result: r) {
                    add(.session(key: c.id, started))
                } else if c.name == sendToUser, current != nil {
                    let state: CallState = r.map { $0.isError ? .failed : .done } ?? (v.live != nil && i == lastReply ? .live : .cancelled)
                    switch delivered(c, state, r) {
                    case .her(let text), .question(let text, _, _): current?.said = text
                    case .attachment(_, let text?), .card(_, let text?): current?.said = text
                    default: break
                    }
                }
            }
        default:
            break
        }
    }
    if let c = current, !c.tasks.isEmpty { out.append(c) }
    // Agents whose spawn calls the conversation does not hold: started before its first item.
    let orphans = v.agents.filter { !launched.contains($0.callID) }
    if !orphans.isEmpty {
        var tasks = orphans.map { HerProgressTask.agent(HerLaunch(callID: $0.callID, name: $0.name, description: $0.description, task: "", agent: $0)) }
        if case .earlier? = out.first?.parent { tasks += out.removeFirst().tasks }
        out.insert(HerProgressGroup(key: "earlier", parent: .earlier, tasks: tasks), at: 0)
    }
    return out
}

// MARK: A sub-agent's card

/// How a sub-agent stands, as its card says it. waiting: it asks the owner to approve a call.
public enum HerAgentState: Equatable, Sendable { case starting, running, waiting, done, failed, stopped }

public func herAgentState(_ l: HerLaunch) -> HerAgentState {
    guard let a = l.agent else { return .starting }
    switch a.status {
    case "running": return a.view.live?.approvals.isEmpty == false ? .waiting : .running
    case "completed": return .done
    case "failed": return .failed
    default: return .stopped
    }
}

/// What a sub-agent is called: what Her called the work, else its task's first sentence, else its handle.
public func herAgentTitle(_ l: HerLaunch) -> String {
    if !l.description.isEmpty { return l.description }
    let first = plainLine(firstLine(l.task))
    return first.isEmpty ? l.name : first
}

/// What a sub-agent's post in the feed shows, from one pass over its record.
public struct HerAgentPost: Equatable, Sendable {
    /// A line of how it goes: what it asks, why it failed, the start of its report, else its latest words or thought.
    public var words: String?
    /// The call it is on while it works; nil while it thinks.
    public var now: HerToolStep?
    /// How many calls it made.
    public var calls: Int
    /// The files it wrote, in the order it first wrote them.
    public var files: [String]
}

public func herAgentPost(_ l: HerLaunch) -> HerAgentPost {
    var post = HerAgentPost(words: nil, now: nil, calls: 0, files: [])
    guard let a = l.agent else { return post }
    func line(_ s: String?) -> String? {
        guard let s else { return nil }
        let line = plainLine(firstLine(s))
        return line.isEmpty ? nil : line
    }
    var latest: String?, said: String?, last: HerStep.Kind?
    for s in herSteps(a.view) {
        switch s.kind {
        case .tool(let t):
            post.calls += 1
            if t.category == "edit", t.state == .done, !t.object.isEmpty, !post.files.contains(t.object) { post.files.append(t.object) }
        case .text(let t):
            latest = t
            said = t
        case .thinking(let t, redacted: false):
            latest = t
        default:
            break
        }
        last = s.kind
    }
    let state = herAgentState(l)
    switch state {
    case .waiting: post.words = line(a.view.live?.approvals.first?.summary)
    case .failed: post.words = line(a.error?.message ?? a.view.failure?.message)
    case .done: post.words = line(a.summary) ?? line(said)
    default: post.words = line(latest)
    }
    if state == .running || state == .waiting, case .tool(let t)? = last, t.state == .live || t.state == .awaiting { post.now = t }
    return post
}

/// The tokens an agent used: its ended tasks', and its running task's turns so far.
public func herAgentTokens(_ a: HerAgent) -> Int {
    a.view.entries.reduce(a.view.live == nil ? 0 : a.view.liveTokens) { n, e in
        if case .taskEnd(_, _, _, let tokens) = e { return n + tokens }
        return n
    }
}

/// How long an agent has worked, in ms: a running one, since its round began; an ended one, its last round, as her
/// timed it, else from its first message to its last task's end (an agent of an earlier run of her).
public func herAgentElapsed(_ a: HerAgent, now: Double) -> Double {
    if a.status == "running" { return max(0, now - a.since) }
    if let ms = a.elapsedMs { return ms }
    var first: Double?, end: Double?
    for e in a.view.entries {
        switch e {
        case .user(_, _, let ts, _): if first == nil { first = ts }
        case .taskEnd(_, _, let ts, _): end = ts
        default: break
        }
    }
    guard let first, let end else { return 0 }
    return max(0, end - first)
}

/// How many calls an agent made: its record's length, as the page counts it.
public func herAgentCalls(_ a: HerAgent) -> Int {
    herSteps(a.view).reduce(0) { n, s in if case .tool = s.kind { return n + 1 } else { return n } }
}

/// What Her asked an agent to do: its spawn_agent call's task, else the task in its first message.
public func herAgentBrief(_ l: HerLaunch) -> String {
    if !l.task.isEmpty { return herTaskBrief(l.task) }
    guard let a = l.agent else { return "" }
    for case .user(_, let content, _, _) in a.view.entries { return herTaskBrief(texts(content)) }
    return ""
}

/// The task in a sub-agent's first message (the rest is how sub-agents work).
func herTaskBrief(_ text: String) -> String {
    let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let open = t.range(of: "<task>", options: .backwards), let close = t.range(of: "</task>", options: .backwards), open.upperBound <= close.lowerBound, t.hasSuffix("</task>") else { return t }
    return t[open.upperBound..<close.lowerBound].trimmingCharacters(in: .whitespacesAndNewlines)
}

/// The cursor to a sub-agent's earlier history, when her can read on from it: one that names the agent
/// ("<agent id>/i<n>"). An older her gives the agent's page a cursor of the main conversation's kind, which
/// session/history would answer with the main conversation's items.
public func herAgentHistoryCursor(_ a: HerAgent) -> String? {
    a.view.before.flatMap { $0.contains("/") ? $0 : nil }
}

// MARK: A sub-agent's record

/// How a tool call stands. awaiting: it waits for the owner's approval.
public enum HerToolState: Equatable, Sendable { case live, awaiting, done, failed, denied, cancelled }

/// A call of the record: what it is, how it went, and what it gave back.
public struct HerToolStep: Equatable, Sendable {
    public var callID: String
    public var name: String
    /// "read", "edit", "delete", "move", "search", "execute", "fetch" or "other".
    public var category: String
    /// One line: the command, or "read <path>".
    public var title: String
    public var state: HerToolState
    /// Its result's text.
    public var output: String

    /// What it acted on, without its tool's name in front.
    public var object: String { herToolObject(name: name, title: title, category: category) }
}

/// A step of a conversation's record, in the order things happened.
public struct HerStep: Equatable, Identifiable, Sendable {
    public let key: String
    public var kind: Kind
    public var id: String { key }

    public enum Kind: Equatable, Sendable {
        /// A message to it: its task first, then whatever Her told it later.
        case said(String)
        case thinking(String, redacted: Bool)
        /// Its own words.
        case text(String)
        case tool(HerToolStep)
        /// A task of its that failed or was interrupted.
        case ended(outcome: String, error: HerFailure?)
    }
}

/// A conversation's record (her-client's conversation(), cut to what a sub-agent's has): the messages to it, its
/// thoughts, words and calls in the order it made them, and where a task failed or was stopped.
public func herSteps(_ v: HerSession) -> [HerStep] {
    var results: [String: HerResult] = [:]
    for case .result(let r) in v.entries { results[r.callID] = r }
    let live = Dictionary((v.live?.tools ?? []).map { ($0.callID, $0) }, uniquingKeysWith: { $1 })
    // The last reply's calls may still run; earlier ones without a result were cancelled.
    let lastReply = v.entries.lastIndex { if case .assistant = $0 { return true } else { return false } }
    var seen: Set<String> = []
    func tool(_ c: HerCall, open: Bool) -> HerToolStep {
        let target = herCallTarget(c.name, c.args)
        let d = v.descriptions[c.id] ?? herDescribe(target.name, target.args)
        let r = results[c.id]
        let l = live[c.id]
        let state: HerToolState =
            if let r { r.isError ? (l?.phase == .denied || deniedText(r.text) ? .denied : .failed) : .done }
            else if let l { l.phase == .denied ? .denied : l.phase == .awaitingApproval ? .awaiting : .live }
            else { open ? .live : .cancelled }
        return HerToolStep(callID: c.id, name: target.name, category: d.category, title: d.title, state: state, output: r?.text ?? "")
    }

    var out: [HerStep] = []
    for (i, e) in v.entries.enumerated() {
        switch e {
        case .user(let id, let content, _, _):
            out.append(HerStep(key: id, kind: .said(messageBody(texts(content)))))
        case .assistant(let id, _, _, _, let blocks):
            for (j, b) in blocks.enumerated() {
                let key = "\(id):\(j)"
                switch b {
                case .thinking(let text, let redacted):
                    if redacted || !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { out.append(HerStep(key: key, kind: .thinking(text, redacted: redacted))) }
                case .text(let text):
                    if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { out.append(HerStep(key: key, kind: .text(text))) }
                case .call(let c):
                    seen.insert(c.id)
                    out.append(HerStep(key: c.id, kind: .tool(tool(c, open: v.live != nil && i == lastReply))))
                }
            }
        case .result(let r):
            // A result whose call is on an earlier page of the history.
            if !seen.contains(r.callID) { out.append(HerStep(key: r.callID, kind: .tool(tool(HerCall(id: r.callID, name: r.toolName, args: .null), open: false)))) }
        case .taskEnd(let outcome, let error, _, _):
            if outcome != "completed" { out.append(HerStep(key: "end:\(i)", kind: .ended(outcome: outcome, error: error))) }
        default:
            break
        }
    }
    return out
}

/// A tool call described as tool_started would, from her built-in tools' arguments (her-client's describe).
func herDescribe(_ name: String, _ args: JSON) -> HerToolDescription {
    func str(_ key: String) -> String { args[key].string ?? "" }
    func line(_ words: String...) -> String { words.filter { !$0.isEmpty }.joined(separator: " ") }
    switch name {
    case "bash":
        let command = str("command")
        let first = command.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
        return HerToolDescription(category: "execute", title: first.isEmpty ? "bash" : first == command ? first : first + " …")
    case "read": return HerToolDescription(category: "read", title: line("read", str("path")))
    case "write", "edit": return HerToolDescription(category: "edit", title: line(name, str("path")))
    case "grep": return HerToolDescription(category: "search", title: line("grep", str("pattern")))
    case "find", "ls": return HerToolDescription(category: "search", title: line(name, str("path").isEmpty ? str("pattern") : str("path")))
    case "web_search": return HerToolDescription(category: "search", title: str("query").isEmpty ? name : str("query"))
    default: return HerToolDescription(category: "other", title: name)
    }
}

// MARK: Words

/// A text's first line that says something.
func firstLine(_ text: String) -> String {
    text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
}

/// A Markdown line as plain words: heading, quote and list marks, emphasis and code marks go.
func plainLine(_ line: String) -> String {
    var s = line.replacingOccurrences(of: #"^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)"#, with: "", options: .regularExpression)
    s = s.replacingOccurrences(of: #"!?\[([^\]]*)\]\([^)]*\)"#, with: "$1", options: .regularExpression)
    s = s.replacingOccurrences(of: #"(\*\*|__|~~|`)"#, with: "", options: .regularExpression)
    return s.trimmingCharacters(in: .whitespaces)
}
