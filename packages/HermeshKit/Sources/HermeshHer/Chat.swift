import Foundation

// The conversation with Her as the Mac's Her entry draws it (her-client's
// botChat.ts): the owner's messages, and each message Her sent with
// send_to_user (words, files, a question, or a secure card), in the order
// they were sent, with what a sub-agent told the owner, which reads the
// same, and a failure where a task failed. What Her did and wrote for
// herself never shows. An approval is no part of it: it waits above the
// composer.

public let sendToUser = "send_to_user"

/// A row of the conversation.
public struct HerItem: Equatable, Identifiable, Sendable {
    public let key: String
    /// When it was sent (ms).
    public let ts: Double
    public var kind: Kind
    public var id: String { key }

    public enum Kind: Equatable, Sendable {
        /// The owner's: its words, its pictures, the files it names. queued: sent while Her was busy, not in the conversation yet.
        case user(text: String, images: [HerImage], files: [String], queued: Bool)
        /// Words from Her, in Markdown.
        case her(String)
        /// Files and pictures Her sent, with her words if she had any.
        case attachment(files: [HerFile], text: String?)
        /// A question, with answers to tap if they can be listed; open: they can still be tapped.
        case question(text: String, options: [String], open: Bool)
        /// A secure card her made for the owner to answer (as send_to_user's result carries it), with Her's words if any.
        case card(JSON, text: String?)
        /// A task that failed; last: none ran since.
        case failure(HerFailure, last: Bool)
        /// A message this device keeps until the machine is back (HerSend); error: why the last try failed.
        case waiting(id: String, text: String, files: [String], error: String)
        /// Her's onboarding while no model works there (HerOnboarding).
        case onboarding
    }

    public init(key: String, ts: Double, kind: Kind) {
        self.key = key
        self.ts = ts
        self.kind = kind
    }

    /// Drawn as the owner's: on the right.
    public var isOwner: Bool {
        switch kind {
        case .user, .waiting: true
        default: false
        }
    }
}

public struct HerImage: Equatable, Sendable {
    public var mime: String
    /// Base64.
    public var data: String
}

/// A file Her sent, as its call's result describes it.
public struct HerFile: Equatable, Sendable {
    /// On the machine, absolute.
    public var path: String
    public var name: String
    public var size: Int
    /// One that shows as a picture.
    public var image: Bool
}

/// How a tool call stands, from its result and the task under way.
enum CallState { case done, failed, denied, live, cancelled }

/// The conversation as rows, oldest first.
public func herChat(_ v: HerSession) -> [HerItem] {
    var results: [String: HerResult] = [:]
    for case .result(let r) in v.entries { results[r.callID] = r }
    let live = Dictionary((v.live?.tools ?? []).map { ($0.callID, $0) }, uniquingKeysWith: { $1 })
    // The last reply's calls may still run; earlier ones without a result were cancelled.
    let lastReply = v.entries.lastIndex { if case .assistant = $0 { return true } else { return false } }
    func state(_ id: String, open: Bool) -> CallState {
        if let r = results[id] { return r.isError ? (live[id]?.phase == .denied || deniedText(r.text) ? .denied : .failed) : .done }
        if let l = live[id] { return l.phase == .denied ? .denied : .live }
        return open ? .live : .cancelled
    }

    var out: [HerItem] = []
    for (i, e) in v.entries.enumerated() {
        switch e {
        case .user(let id, let content, let ts, let mail):
            if let mail {
                // What a sub-agent told the owner reads as a message from Her.
                if mail == "to_user" { out.append(HerItem(key: id, ts: ts, kind: .her(messageBody(texts(content))))) }
            } else {
                out.append(HerItem(key: id, ts: ts, kind: ownerKind(content, queued: false)))
            }
        case .assistant(_, let calls, let ts, let duration):
            // Sent when the reply that called send_to_user ended.
            for c in calls where c.name == sendToUser {
                if let kind = delivered(c, state(c.id, open: v.live != nil && i == lastReply), results[c.id]) {
                    out.append(HerItem(key: c.id, ts: ts + duration, kind: kind))
                }
            }
        case .taskEnd("failed", let error?, let ts):
            out.append(HerItem(key: "end:\(i)", ts: ts, kind: .failure(error, last: false)))
        default:
            break
        }
    }
    if v.live == nil, v.failure != nil, let i = out.lastIndex(where: { if case .failure = $0.kind { return true } else { return false } }),
       case .failure(let f, _) = out[i].kind {
        out[i].kind = .failure(f, last: true)
    }
    // One that joined the conversation keeps its id there.
    for q in v.queued where !out.contains(where: { $0.key == q.id }) { out.append(HerItem(key: q.id, ts: q.ts, kind: ownerKind(q.content, queued: true))) }
    // In the order things were sent; the same time keeps the history's order.
    var sorted = out.enumerated().sorted { $0.element.ts != $1.element.ts ? $0.element.ts < $1.element.ts : $0.offset < $1.offset }.map(\.element)
    // The latest question can be answered until the owner says anything.
    for i in sorted.indices.reversed() {
        if case .user = sorted[i].kind { break }
        if case .question(let text, let options, _) = sorted[i].kind {
            sorted[i].kind = .question(text: text, options: options, open: true)
            break
        }
    }
    return sorted
}

/// What a send_to_user call sent the owner. Words and questions show from the call's start (failed, refused and
/// cancelled ones did not reach them); files and cards once her let them through.
func delivered(_ c: HerCall, _ state: CallState, _ result: HerResult?) -> HerItem.Kind? {
    guard state == .done || state == .live else { return nil }
    let text = c.args["text"].text
    // Without a type, what a call carries says what it is.
    let some = { (key: String) in !(c.args[key].array ?? []).isEmpty }
    switch c.args["type"].string ?? (some("files") ? "attachment" : some("options") ? "question" : "text") {
    case "card":
        let card = state == .done ? result?.details["card"] ?? .null : .null
        return card.isNull ? nil : .card(card, text: text)
    case "attachment":
        let files = state == .done ? filesOf(result) : []
        return files.isEmpty ? nil : .attachment(files: files, text: text)
    case "question":
        let options = (c.args["options"].array ?? []).compactMap { $0["label"].string?.trimmingCharacters(in: .whitespaces) }
        return text.map { .question(text: $0, options: options, open: false) }
    default:
        return text.map { .her($0) }
    }
}

/// The files of a message of files, from its result's details.
private func filesOf(_ r: HerResult?) -> [HerFile] {
    (r?.details["files"].array ?? []).compactMap { f in
        f["path"].string.map { HerFile(path: $0, name: baseName($0), size: f["size"].int ?? 0, image: f["image"].bool == true) }
    }
}

/// The owner's message: its words, pictures and the files it names, either way they name them.
private func ownerKind(_ content: [HerUserContent], queued: Bool) -> HerItem.Kind {
    var words: [String] = [], files: [String] = [], images: [HerImage] = []
    for c in content {
        switch c {
        case .text(let t):
            if let paths = attachedPaths(t) { files += paths } else { words.append(t) }
        case .image(let mime, let data):
            images.append(HerImage(mime: mime, data: data))
        }
    }
    var text = words.joined(separator: "\n")
    if files.isEmpty { (text, files) = splitAttached(text) }
    return .user(text: text, images: images, files: files.map(baseName), queued: queued)
}

private func texts(_ content: [HerUserContent]) -> String {
    content.compactMap { if case .text(let t) = $0 { return t } else { return nil } }.joined(separator: "\n")
}

/// A refusal's result: her answers a denied call with an error result.
private func deniedText(_ text: String) -> Bool {
    text.range(of: #"^(denied|rejected) by (the )?user"#, options: [.regularExpression, .caseInsensitive]) != nil
}

/// A message's own words: the mail's text without its agent_message or sent_to_user wrapper.
func messageBody(_ text: String) -> String {
    let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
    for tag in ["agent_message", "sent_to_user"] where t.hasPrefix("<\(tag)") && t.hasSuffix("</\(tag)>") {
        guard let open = t.firstIndex(of: ">") else { break }
        let inner = t[t.index(after: open)..<t.index(t.endIndex, offsetBy: -(tag.count + 3))]
        return inner.trimmingCharacters(in: .whitespacesAndNewlines)
    }
    return t
}

/// A path's last part, a Mac's or a PC's.
public func baseName(_ path: String) -> String {
    path.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? path
}

// MARK: What a message names

/// The block the composer puts after a message's words to name the files uploaded for it (her-client's attachedBlock).
public func attachedBlock(_ paths: [String]) -> HerUserContent {
    .text((paths.contains { $0.lowercased().hasSuffix(".xlsx") || $0.lowercased().hasSuffix(".xlsm") } ? attachedWithWorkbooks : attached) + paths.joined(separator: "\n"))
}

private let attached = "Attached files:\n"
private let attachedWithWorkbooks = "Attached files. For XLSX and XLSM workbooks, use the deferred doc tools (doc_open, then doc_read) instead of unpacking them or parsing them with shell/Python:\n"

private func attachedPaths(_ text: String) -> [String]? {
    for prefix in [attachedWithWorkbooks, attached] where text.hasPrefix(prefix) {
        return text.dropFirst(prefix.count).split(separator: "\n").map(String.init)
    }
    return nil
}

/// A message hermeshd delivered with files (HerSend) names them after its words, "Attached files:" and a
/// line "- <path>" for each: its words, and the files' paths on the machine.
public func splitAttached(_ text: String) -> (text: String, files: [String]) {
    let mark = "Attached files:\n- "
    guard let r = text.range(of: mark), r.lowerBound == text.startIndex || text[..<r.lowerBound].hasSuffix("\n\n") else { return (text, []) }
    let words = String(text[..<r.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
    let files = text[r.lowerBound...].dropFirst("Attached files:".count).components(separatedBy: "\n- ").dropFirst().filter { !$0.isEmpty }
    return (words, Array(files))
}

// MARK: The task under way

/// What waits for the owner: Her's own question or, while she may be idle, a sub-agent's (her asks them one at a
/// time), and how many wait in all.
public func herPendingApproval(_ v: HerSession) -> (approval: HerApproval, agent: HerAgent?, waiting: Int)? {
    if let live = v.live, let a = live.approvals.first { return (a, nil, live.approvals.count) }
    for agent in v.agents where agent.status == "running" {
        if let live = agent.view.live, let a = live.approvals.first { return (a, agent, live.approvals.count) }
    }
    return nil
}

/// Her is at work: a task runs that waits for no approval, or sub-agents work.
public func herBusy(_ v: HerSession) -> Bool {
    (v.live.map { $0.approvals.isEmpty } ?? false) || v.agents.contains { $0.status == "running" }
}

/// The op that answers an approval: Her's own, or a sub-agent's.
public func herApprovalOp(_ a: HerApproval, agent: HerAgent?, decision: JSON) -> JSON {
    if let agent { return ["type": "agent_approval", "agent": .string(agent.id), "call_id": .string(a.callID), "decision": decision] }
    return ["type": "approval", "call_id": .string(a.callID), "decision": decision]
}

/// The tool a call runs: itself, or the one call_tool names, with that tool's arguments.
public func herCallTarget(_ name: String, _ args: JSON) -> (name: String, args: JSON) {
    if name == "call_tool", let target = args["name"].string { return (target, args["arguments"]) }
    return (name, args)
}

/// What a call does, when her has not said (tool_started): from her built-in tools' names.
public func herToolCategory(_ name: String) -> String {
    switch name {
    case "bash": "execute"
    case "read": "read"
    case "write", "edit": "edit"
    case "grep", "find", "ls", "web_search": "search"
    default: "other"
    }
}

/// What a call acted on, without its tool's name in front: "read src/main.rs" is "src/main.rs" (the verb says it reads).
public func herToolObject(name: String, title: String, category: String) -> String {
    if category == "execute" { return title }
    guard let space = title.firstIndex(of: " ") else { return title }
    let first = String(title[..<space])
    let words: Set = ["read", "write", "edit", "grep", "find", "ls", "fetch", "search", "open"]
    return first == name || words.contains(first) ? title[space...].trimmingCharacters(in: .whitespaces) : title
}

/// The project a `mesh <machine>/<project>` rule remembers (docs/her-mesh.md §5): the choice names it, not the raw rule.
public func herMeshProject(_ rule: [String]?) -> String? {
    guard let rule, rule.count == 2, rule[0] == "mesh" else { return nil }
    return rule[1]
}

/// A summary of several lines ("Start Codex in homemini/hermesh", then the prompt): its first line is the
/// question, the rest shows under it. Nil for a summary of one line.
public func herSummaryParts(_ summary: String) -> (title: String, body: String)? {
    guard let cut = summary.firstIndex(of: "\n") else { return nil }
    let title = summary[..<cut].trimmingCharacters(in: .whitespaces)
    let body = String(summary[cut...].drop { $0 == "\n" }).replacingOccurrences(of: "\\s+$", with: "", options: .regularExpression)
    return title.isEmpty || body.isEmpty ? nil : (title, body)
}

/// What an approval shows of the call it asks about: the command, the change as removed and added lines,
/// what it writes, or the arguments. Long ones are cut at `max` lines, with how many more there were.
public enum HerApprovalPreview: Equatable, Sendable {
    case command(String)
    case diff([(sign: Character, text: String)], more: Int)
    case content([String], more: Int)
    case args(String)

    public static func == (a: Self, b: Self) -> Bool {
        switch (a, b) {
        case (.command(let x), .command(let y)), (.args(let x), .args(let y)): x == y
        case (.diff(let x, let m), .diff(let y, let n)): m == n && x.map { "\($0.sign)\($0.text)" } == y.map { "\($0.sign)\($0.text)" }
        case (.content(let x, let m), .content(let y, let n)): x == y && m == n
        default: false
        }
    }
}

public func herApprovalPreview(_ a: HerApproval, max: Int = 12) -> HerApprovalPreview {
    let (name, args) = herCallTarget(a.name, a.args)
    // A text's lines; the newline that ends its last line starts no line of its own.
    func lines(_ s: String) -> [String] { (s.hasSuffix("\n") ? String(s.dropLast()) : s).components(separatedBy: "\n") }
    if name == "bash" { return .command(args["command"].string ?? a.summary) }
    if name == "edit", let edits = args["edits"].array {
        var all: [(sign: Character, text: String)] = []
        for e in edits {
            all += lines(e["old_text"].string ?? "").map { (sign: "-", text: $0) }
            all += lines(e["new_text"].string ?? "").map { (sign: "+", text: $0) }
        }
        return .diff(Array(all.prefix(max)), more: Swift.max(0, all.count - max))
    }
    if let text = (name == "write" ? args["content"] : name == "browser_eval" ? args["expression"] : .null).string {
        let all = lines(text)
        return .content(Array(all.prefix(max)), more: Swift.max(0, all.count - max))
    }
    return .args(a.summary.isEmpty ? String(decoding: args.data(), as: UTF8.self) : a.summary)
}
