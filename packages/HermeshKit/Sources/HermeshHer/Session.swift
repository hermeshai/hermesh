import Foundation

// Her's conversation as the iPhone follows it (packages/her-client's
// session.ts, cut to what the Her entry shows): the snapshot bots/open
// returned, then each session/event after it. What it keeps is what the
// conversation draws (the owner's messages, what Her sent with send_to_user
// and how each call went), the task under way with its approvals, the
// sub-agents with their own conversations whole (what each thought, said
// and called: the Progress page's record, docs/her-team.md §15), and how
// secure cards stand. Words being written are never asked for.

/// A conversation: its snapshot, with every event after it applied.
public struct HerSession: Equatable, Sendable {
    public var sessionID: String
    /// The model it answers with ("provider/model").
    public var model: String
    /// How hard it thinks: "default" (the provider decides), "off", "low"… (her's ThinkingLevel).
    public var thinking: String
    /// "manual", "accept-edits" or "bypass-permissions".
    public var permissionMode: String
    public var lastSeq: Int
    public var entries: [HerEntry]
    /// Where earlier history begins (session/history's cursor), while there is more than the snapshot's last items.
    public var before: String?
    /// The task under way.
    public var live: HerLive?
    /// The last task failed, and none ran since.
    public var failure: HerFailure?
    /// Messages sent while a task runs, waiting for it.
    public var queued: [HerQueued]
    public var agents: [HerAgent]
    /// Secure cards whose state changed since the snapshot, by card id.
    public var cards: [String: HerCardStatus]
    /// Tool calls as tool_started described them, by call id.
    public var descriptions: [String: HerToolDescription]
    /// Tokens of the running task's finished turns.
    public var liveTokens: Int

    /// The conversation a snapshot (her's SessionSnapshot) describes.
    public init?(snapshot s: JSON) {
        guard let id = s["session"]["session_id"].string else { return nil }
        self.init(id: id, snapshot: s)
        model = s["session"]["model"].string ?? ""
        thinking = s["session"]["thinking"].string ?? "default"
        permissionMode = s["session"]["permission_mode"].string ?? ""
        agents = (s["agents"].array ?? []).compactMap { a in
            guard let agentID = a["agent_id"].string else { return nil }
            return HerAgent(id: agentID, name: a["name"].string ?? "", description: a["description"].string ?? "",
                            status: a["status"].string ?? "", view: HerSession(id: agentID, snapshot: a),
                            callID: a["call_id"].string ?? "", since: a["since"].number ?? 0, elapsedMs: a["elapsed_ms"].number,
                            summary: a["summary"].string, error: HerFailure(a["error"]))
        }
    }

    /// A conversation from a snapshot's transcript and task: a sub-agent's has no settings of its own.
    init(id: String, snapshot s: JSON) {
        sessionID = id
        model = ""
        thinking = "default"
        permissionMode = ""
        lastSeq = s["last_seq"].int ?? 0
        entries = (s["transcript"]["items"].array ?? []).map(HerEntry.init)
        before = s["transcript"]["before"].string
        cards = [:]
        agents = []
        descriptions = [:]
        liveTokens = 0
        queued = (s["queued"].array ?? []).compactMap(HerQueued.init)
        let task = s["active_task"]
        if let taskID = task["task_id"].string {
            let approvals = (task["approvals"].array ?? []).compactMap(HerApproval.init)
            let asked = Set(approvals.map(\.callID))
            let tools = (task["tools"].array ?? []).compactMap { t -> HerLiveTool? in
                guard let callID = t["call_id"].string else { return nil }
                return HerLiveTool(callID: callID, name: t["name"].string ?? "", category: t["category"].string ?? "other", title: t["title"].string ?? "",
                                   phase: asked.contains(callID) ? .awaitingApproval : t["running"].bool == true ? .running : .pending)
            }
            live = HerLive(taskID: taskID, tools: tools, approvals: approvals)
            for t in tools { descriptions[t.callID] = HerToolDescription(category: t.category, title: t.title) }
        } else {
            live = nil
        }
        if live == nil, case .taskEnd("failed", let error, _, _)? = entries.last { failure = error } else { failure = nil }
    }

    /// Earlier history of a sub-agent's own conversation (session/history with its cursor) put before what it has.
    public mutating func prepend(history page: JSON, agent id: String) {
        guard let i = agents.firstIndex(where: { $0.id == id }) else { return }
        agents[i].view.prepend(history: page)
    }

    /// Earlier history (session/history's TranscriptPage) put before what it has, with the cursor to what came before
    /// that page (packages/her-client's withHistory).
    public mutating func prepend(history page: JSON) {
        entries = (page["items"].array ?? []).map(HerEntry.init) + entries
        before = page["before"].string
    }

    /// The conversation after an event (her's Event); one it has already (its seq at or below what it holds) changes nothing.
    public mutating func apply(_ e: JSON) {
        guard let seq = e["seq"].int, seq > lastSeq else { return }
        step(e)
        lastSeq = seq
    }

    private mutating func step(_ e: JSON) {
        let callID = e["call_id"].string ?? ""
        let ts = e["ts"].number ?? 0
        switch e["type"].string {
        case "card_changed":
            if let id = e["card_id"].string, let state = e["state"].string { cards[id] = HerCardStatus(state: state, note: e["note"].string) }
        case "session_started", "settings_changed":
            model = e["model"].string ?? model
            thinking = e["thinking"].string ?? thinking
            permissionMode = e["permission_mode"].string ?? permissionMode
        case "task_started":
            failure = nil
            liveTokens = 0
            live = HerLive(taskID: e["task_id"].string ?? "", tools: [], approvals: [])
        case "turn_completed":
            liveTokens += herTokens(usage: e["usage"])
        case "message_completed":
            entries.append(HerEntry(["type": "message", "message_id": e["message_id"], "message": e["message"], "mail": e["mail"]]))
        case "queue_changed":
            queued = (e["queued"].array ?? []).compactMap(HerQueued.init)
        case "session_rewound":
            let at = entries.firstIndex { if case .user(let id, _, _, _) = $0 { return id == e["message_id"].string } else { return false } }
            entries = at.map { Array(entries[..<$0]) } ?? []
            failure = nil
            let removed = Set((e["removed_agents"].array ?? []).compactMap(\.string))
            agents.removeAll { removed.contains($0.id) }
        case "tool_started":
            let tool = HerLiveTool(callID: callID, name: e["name"].string ?? "", category: e["category"].string ?? "other", title: e["title"].string ?? "", phase: .pending)
            live?.tools.removeAll { $0.callID == callID }
            live?.tools.append(tool)
            descriptions[callID] = HerToolDescription(category: tool.category, title: tool.title)
        case "approval_requested":
            if let a = HerApproval(e) {
                live?.approvals.removeAll { $0.callID == callID }
                live?.approvals.append(a)
                setPhase(callID, .awaitingApproval)
            }
        case "approval_resolved":
            live?.approvals.removeAll { $0.callID == callID }
            let refused = ["deny", "abort"].contains(e["decision"]["type"].string ?? "")
            setPhase(callID, refused ? .denied : .pending)
        case "tool_running":
            setPhase(callID, .running)
        case "tool_completed":
            entries.append(.result(HerResult(callID: callID, e["result"])))
            live?.tools.removeAll { $0.callID == callID }
        case "task_completed":
            end("completed", nil, ts, e["usage"])
        case "task_failed":
            let error = HerFailure(e["error"])
            end("failed", error, ts, e["usage"])
            failure = error
            queued = []
        case "task_interrupted":
            end("interrupted", nil, ts, e["usage"])
            queued = []
        case "agent_started":
            guard let id = e["agent_id"].string else { return }
            agents.removeAll { $0.id == id }
            agents.append(HerAgent(id: id, name: e["name"].string ?? "", description: e["description"].string ?? "", status: "running", view: HerSession(id: id, snapshot: .null),
                                   callID: callID, since: ts))
        case "agent_status":
            guard let i = agents.firstIndex(where: { $0.id == e["agent_id"].string }) else { return }
            agents[i].status = e["status"].string ?? agents[i].status
            agents[i].since = ts
            // A new round keeps what the last one reported until it reports itself.
            if agents[i].status != "running" {
                agents[i].summary = e["summary"].string
                agents[i].error = HerFailure(e["error"])
                agents[i].elapsedMs = e["elapsed_ms"].number
            }
        case "agent_event":
            if let i = agents.firstIndex(where: { $0.id == e["agent_id"].string }) { agents[i].view.apply(e["event"]) }
        case "session_closed":
            live = nil
            queued = []
        default:
            break // a newer her's event, or one the conversation does not show
        }
    }

    private mutating func setPhase(_ callID: String, _ phase: HerLiveTool.Phase) {
        guard let i = live?.tools.firstIndex(where: { $0.callID == callID }) else { return }
        live?.tools[i].phase = phase
    }

    private mutating func end(_ outcome: String, _ error: HerFailure?, _ ts: Double, _ usage: JSON) {
        live = nil
        liveTokens = 0
        entries.append(.taskEnd(outcome: outcome, error: error, ts: ts, tokens: herTokens(totals: usage)))
    }
}

/// An item of the conversation's history (her's TranscriptItem), as far as the Her entry reads it.
public enum HerEntry: Equatable, Sendable {
    /// A message to the assistant: the owner's, or (with `mail`) one an agent of hers, or a routine's run, sent.
    case user(id: String, content: [HerUserContent], ts: Double, mail: HerMail?)
    /// A reply of the model's: its tool calls (send_to_user's are what the conversation shows), and all it holds
    /// in order, its words and thoughts too (a sub-agent's record shows them).
    case assistant(id: String, calls: [HerCall], ts: Double, duration: Double, blocks: [HerBlock])
    case result(HerResult)
    /// tokens: what the task used.
    case taskEnd(outcome: String, error: HerFailure?, ts: Double, tokens: Int)
    case cardResolved(id: String, outcome: String, note: String?)
    case other

    init(_ j: JSON) {
        switch j["type"].string {
        case "message":
            let m = j["message"], id = j["message_id"].string ?? "", ts = m["ts"].number ?? 0
            switch m["role"].string {
            case "user":
                self = .user(id: id, content: (m["content"].array ?? []).compactMap(HerUserContent.init), ts: ts, mail: HerMail(j["mail"]))
            case "assistant":
                let blocks = (m["content"].array ?? []).compactMap { c -> HerBlock? in
                    switch c["type"].string {
                    case "text": return .text(c["text"].string ?? "")
                    case "thinking": return .thinking(c["text"].string ?? "", redacted: c["redacted"].bool == true)
                    case "tool_call": return c["id"].string.map { .call(HerCall(id: $0, name: c["name"].string ?? "", args: c["arguments"])) }
                    default: return nil
                    }
                }
                let calls = blocks.compactMap { if case .call(let c) = $0 { return c } else { return nil } }
                self = .assistant(id: id, calls: calls, ts: ts, duration: m["duration_ms"].number ?? 0, blocks: blocks)
            default:
                self = .other
            }
        case "tool_result":
            self = .result(HerResult(callID: j["call_id"].string ?? "", j["result"]))
        case "task_end":
            self = .taskEnd(outcome: j["outcome"].string ?? "", error: HerFailure(j["error"]), ts: j["ts"].number ?? 0, tokens: herTokens(totals: j["usage"]))
        case "card_resolved":
            self = .cardResolved(id: j["card_id"].string ?? "", outcome: j["outcome"].string ?? "", note: j["note"].string)
        default:
            self = .other
        }
    }
}

/// Who sent a message that is mail (her's MailInfo): a sub-agent, or a routine's run.
public struct HerMail: Equatable, Sendable {
    /// "report", "message", "routine", "to_user" or "news".
    public var kind: String
    public var fromID: String
    public var fromName: String
    /// The routine whose run sent it.
    public var routineID: String?

    init?(_ j: JSON) {
        guard let kind = j["kind"].string else { return nil }
        self.kind = kind
        fromID = j["from"]["id"].string ?? ""
        fromName = j["from"]["name"].string ?? ""
        routineID = j["routine_id"].string
    }
}

/// A part of a reply, in the order the model wrote them.
public enum HerBlock: Equatable, Sendable {
    case text(String)
    case thinking(String, redacted: Bool)
    case call(HerCall)
}

/// A tool call as tool_started describes it.
public struct HerToolDescription: Equatable, Sendable {
    public var category: String
    public var title: String
}

/// Every token of a response (her's Usage): what it read, cached or not, and wrote.
func herTokens(usage u: JSON) -> Int {
    (u["input"].int ?? 0) + (u["output"].int ?? 0) + (u["cache_read"].int ?? 0) + (u["cache_write"].int ?? 0)
}

/// The tokens of her's UsageTotals, over its models.
func herTokens(totals t: JSON) -> Int {
    (t["per_model"].object ?? [:]).values.reduce(0) { $0 + herTokens(usage: $1) }
}

public enum HerUserContent: Equatable, Sendable {
    case text(String)
    /// Base64.
    case image(mime: String, data: String)

    init?(_ c: JSON) {
        switch c["type"].string {
        case "text": self = .text(c["text"].string ?? "")
        case "image": self = .image(mime: c["mime_type"].string ?? "", data: c["data"].string ?? "")
        default: return nil
        }
    }

    public var json: JSON {
        switch self {
        case .text(let t): ["type": "text", "text": .string(t)]
        case .image(let mime, let data): ["type": "image", "mime_type": .string(mime), "data": .string(data)]
        }
    }
}

public struct HerCall: Equatable, Sendable {
    public var id: String
    public var name: String
    public var args: JSON
}

/// A tool call's result (her's ToolResultMessage).
public struct HerResult: Equatable, Sendable {
    public var callID: String
    public var toolName: String
    public var isError: Bool
    /// What it said to the model, its texts together.
    public var text: String
    /// For user interfaces: a sent message's files, a card.
    public var details: JSON

    init(callID: String, _ r: JSON) {
        self.callID = callID
        toolName = r["tool_name"].string ?? ""
        isError = r["is_error"].bool ?? false
        text = (r["content"].array ?? []).compactMap { $0["type"].string == "text" ? $0["text"].string : nil }.joined(separator: "\n")
        details = r["details"]
    }
}

/// Why a task failed (her's ErrorInfo).
public struct HerFailure: Equatable, Sendable {
    public var kind: String
    public var message: String

    init?(_ j: JSON) {
        guard let message = j["message"].string else { return nil }
        self.message = message
        kind = j["kind"].string ?? "unknown"
    }
}

/// A message sent while a task runs, not in the conversation yet.
public struct HerQueued: Equatable, Sendable {
    public var id: String
    public var content: [HerUserContent]
    public var ts: Double

    init?(_ j: JSON) {
        guard let id = j["id"].string else { return nil }
        self.id = id
        content = (j["message"]["content"].array ?? []).compactMap(HerUserContent.init)
        ts = j["message"]["ts"].number ?? 0
    }
}

/// The task under way: its tool calls not completed, and the approvals they wait for, in the order asked.
public struct HerLive: Equatable, Sendable {
    public var taskID: String
    public var tools: [HerLiveTool]
    public var approvals: [HerApproval]
}

public struct HerLiveTool: Equatable, Sendable {
    public enum Phase: Equatable, Sendable { case pending, awaitingApproval, running, denied }

    public var callID: String
    public var name: String
    /// "read", "edit", "delete", "move", "search", "execute", "fetch" or "other".
    public var category: String
    public var title: String
    public var phase: Phase
}

/// A tool call waiting for the owner (her's PendingApproval).
public struct HerApproval: Equatable, Sendable {
    public var callID: String
    public var name: String
    public var args: JSON
    public var summary: String
    public var reason: String?
    /// The command prefix approving for the conversation allows from then on.
    public var rule: [String]?
    /// The mode approving for the conversation switches to.
    public var mode: String?
    /// Site and app permissions approving for the conversation may keep.
    public var grants: [HerGrant]

    init?(_ j: JSON) {
        guard let callID = j["call_id"].string else { return nil }
        self.callID = callID
        name = j["name"].string ?? ""
        args = j["args"]
        summary = j["summary"].string ?? ""
        reason = j["reason"].text
        rule = j["proposed_rule"]["prefix"].array?.compactMap(\.string)
        mode = j["proposed_mode"].string
        grants = (j["proposed_grants"].array ?? []).map { HerGrant(origin: $0["origin"].string ?? "", authority: $0["authority"].string ?? "") }
    }
}

public struct HerGrant: Equatable, Sendable {
    public var origin: String
    public var authority: String
}

/// A sub-agent of the conversation, with its own conversation as its agent_events tell it.
public struct HerAgent: Equatable, Sendable {
    public var id: String
    public var name: String
    public var description: String
    /// "running", "completed", "failed", "interrupted" or "closed".
    public var status: String
    public var view: HerSession
    /// The spawn_agent call that started it.
    public var callID = ""
    /// When the status was last set (ms): a running agent's round of work started then.
    public var since: Double = 0
    /// How long its last finished round of work took.
    public var elapsedMs: Double?
    /// The start of its last report.
    public var summary: String?
    public var error: HerFailure?
}

/// How a secure card stands, with her's words (docs/her-secure-cards.md §3.5).
public struct HerCardStatus: Equatable, Sendable {
    public var state: String
    public var note: String?

    public init(state: String, note: String? = nil) {
        self.state = state
        self.note = note
    }
}
