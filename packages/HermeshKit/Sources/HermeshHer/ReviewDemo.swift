import Foundation
import HermeshProto

/// The public demo's Her. Only preset data and in-memory protocol events; no core, model, files or network.
@MainActor public final class HerReviewDemo: HerLocal {
    public enum CodingState { case running, done, failed, stopped }
    public static var delegate: String { String(localized: "Delegate a sample task", bundle: .module) }
    public static var failure: String { String(localized: "Simulate a failure", bundle: .module) }
    public static var retry: String { String(localized: "Retry sample task", bundle: .module) }
    public static let codingID = "demo-her-coding"
    public private(set) var codingState: CodingState?
    public var codingChanged: (() -> Void)?
    public private(set) var session: HerSession
    private let initial: JSON
    private var events: [JSON] = []
    private var streams: [String: AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error>.Continuation] = [:]
    private var sent: Set<String> = []
    private var pending: String?
    private var fails = false
    private var stopped = false
    private var work: Task<Void, Never>?
    private var round = 0
    private var codingCall = ""
    private var agentID = ""
    private var agentSeq = 0

    public init() {
        initial = Self.initial
        session = HerSession(snapshot: initial)!
    }

    private static var bot: JSON { ["id": "her", "name": "Her", "default": true, "session_id": "her-review"] }
    private static var initial: JSON {
        let welcome = String(localized: "Explore Her with sample data. I can delegate a coding task, follow its checks and report back. Nothing is sent to a model or computer. Choose a sample below, or type a message to start the same simulated task.", bundle: .module)
        return ["session": ["session_id": "her-review", "model": "demo/sample", "permission_mode": "manual"], "last_seq": 0,
                "transcript": ["items": [
                    ["type": "message", "message_id": "welcome", "message": ["role": "assistant", "ts": .number(Date.now.timeIntervalSince1970 * 1000),
                        "content": [["type": "tool_call", "id": "welcome-call", "name": .string(sendToUser),
                                     "arguments": ["type": "question", "text": .string(welcome), "options": [["label": .string(delegate)], ["label": .string(failure)]]]]]]],
                    ["type": "tool_result", "call_id": "welcome-call", "result": [:]]]]]
    }

    public var keptBot: Data { Self.bot.data() }
    public var keptSnapshot: Data { initial.data() }

    nonisolated public func herOpen(_ req: Hermesh_Local_V1_HerOpenRequest) -> AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task { @MainActor in self.open(req, continuation) }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func open(_ req: Hermesh_Local_V1_HerOpenRequest, _ c: AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error>.Continuation) {
        guard !stopped, !Task.isCancelled, req.machineID == "demo-mac" else { c.finish(); return }
        let id = UUID().uuidString
        do {
            let responses: [Hermesh_Her_V1_Response] = try req.open.setup.map { r in
                let value = try request(r.method, (try? JSON.parse(r.params)) ?? [:])
                return .with { $0.result = value.data() }
            }
            streams[id] = c
            c.yield(.with { $0.opened = .with { $0.connectionID = id; $0.responses = responses } })
            if req.open.setup.contains(where: { $0.method == "bots/open" }) {
                for event in events { c.yield(notification(event)) }
            }
            c.onTermination = { [weak self] _ in Task { @MainActor in self?.streams[id] = nil } }
        } catch { c.finish(throwing: error) }
    }

    public func herCall(_ req: Hermesh_Local_V1_HerCallRequest) async throws -> Hermesh_Her_V1_CallResponse {
        guard !stopped, req.machineID == "demo-mac", req.call.connectionID.isEmpty || streams[req.call.connectionID] != nil else { throw HerRPCError.disconnected }
        let result = try request(req.call.request.method, (try? JSON.parse(req.call.request.params)) ?? [:])
        return .with { $0.response = .with { $0.result = result.data() } }
    }

    private func request(_ method: String, _ r: JSON) throws -> JSON {
        switch method {
        case "bots/subscribe": return ["bots": [Self.bot]]
        case "models/list": return ["models": [["id": "demo/sample", "name": "Sample model (simulated)", "provider": "demo", "available": true, "shown": true]], "providers": []]
        case "bots/open": return ["session_id": "her-review", "snapshot": initial]
        case "bots/read", "deliveries/seen": return [:]
        case "session/op":
            let op = r["op"]
            switch op["type"].string {
            case "prompt":
                try send((op["input"].array ?? []).compactMap { $0["text"].string }.joined(separator: "\n"), id: UUID().uuidString)
            case "approval":
                guard let pending, op["call_id"].string == pending else { throw unavailable }
                let decision = op["decision"]["type"].string ?? "deny"
                guard ["approve", "deny", "abort"].contains(decision) else { throw unavailable }
                self.pending = nil
                emit(["type": "approval_resolved", "call_id": .string(pending), "decision": ["type": .string(decision)]])
                if decision == "approve" { begin() }
                else {
                    result(pending, name: "call_tool", error: true)
                    say(String(localized: "Permission denied. No sample task was started. You can try again; nothing was changed on a computer.", bundle: .module))
                    end()
                }
            case "interrupt":
                interrupt()
            case "agent_interrupt":
                guard op["agent"].string == agentID, codingState == .running else { throw unavailable }
                interrupt()
            default: throw unavailable
            }
            return [:]
        default: throw unavailable
        }
    }

    private var unavailable: HerRPCError {
        HerRPCError(code: -32000, message: String(localized: "This demo uses sample tasks only. No real model, account, file or service is connected.", bundle: .module), data: ["kind": "demo_only"])
    }

    public func send(_ text: String, id: String) throws {
        guard !stopped else { throw HerRPCError.disconnected }
        guard !sent.contains(id) else { return }
        guard session.live == nil else { throw unavailable }
        sent.insert(id)
        round += 1
        fails = text == Self.failure
        codingCall = "coding-\(round)"
        agentID = "reviewer-\(round)"
        agentSeq = 0
        emit(["type": "message_completed", "message_id": .string(id), "message": ["role": "user", "ts": now, "content": [["type": "text", "text": .string(text)]]]])
        emit(["type": "task_started", "task_id": .string("task-\(round)")])
        let args: JSON = ["name": .string(sessionStart), "arguments": ["machine": "demo-mac", "project": "hello-world", "agent": "codex", "prompt": "Update the sample welcome page", "title": "Her sample task"]]
        assistant(codingCall, name: "call_tool", args: args)
        emit(["type": "tool_started", "call_id": .string(codingCall), "name": "call_tool", "category": "execute", "title": "Start a sample coding task"])
        pending = codingCall
        emit(["type": "approval_requested", "call_id": .string(codingCall), "name": "call_tool", "args": args,
              "summary": .string(String(localized: "Start a sample coding task?\nCodex · Mac mini · hello-world\nSimulated only; no computer or agent will be contacted.", bundle: .module))])
    }

    private func begin() {
        emit(["type": "tool_running", "call_id": .string(codingCall)])
        result(codingCall, name: "call_tool", details: ["session": ["session_id": .string(Self.codingID), "title": "Her sample task", "agent": "codex", "machine": "Mac mini"]])
        assistant("spawn-\(round)", name: "spawn_agent", args: ["name": "reviewer", "description": "Check sample change", "task": "Inspect the sample diff and test results."])
        result("spawn-\(round)", name: "spawn_agent", details: ["agent_id": .string(agentID), "name": "reviewer"])
        emit(["type": "agent_started", "agent_id": .string(agentID), "call_id": .string("spawn-\(round)"), "name": "reviewer", "description": "Check sample change"])
        agent(["type": "task_started", "task_id": "check"])
        agent(["type": "message_completed", "message_id": "check-input", "message": ["role": "user", "ts": now, "content": [["type": "text", "text": "Inspect the sample diff and test results."]]]])
        agent(["type": "message_completed", "message_id": "check-step", "message": ["role": "assistant", "ts": now, "content": [["type": "text", "text": "Checking the preset sample change. No tools are running."]]]])
        say(String(localized: "The sample coding task is in progress. Open its card for output and changes, or Progress for the sample reviewer's record.", bundle: .module))
        codingState = .running
        codingChanged?()
        work?.cancel()
        work = Task { [weak self] in
            try? await Task.sleep(for: .seconds(8))
            guard !Task.isCancelled else { return }
            self?.complete()
        }
    }

    /// Deterministic completion is also used by the state tests, without a clock or external service.
    func complete() {
        guard !stopped, codingState == .running, session.live != nil else { return }
        let summary = fails ? String(localized: "The sample check failed. This is an intentional simulated error; no live task ran.", bundle: .module)
                            : String(localized: "Sample task complete. The preset change and two simulated tests passed. No code ran, no PR was created and nothing was pushed or merged.", bundle: .module)
        agent(["type": "message_completed", "message_id": "check-result", "message": ["role": "assistant", "ts": now, "content": [["type": "text", "text": .string(summary)]]]])
        agent(["type": fails ? "task_failed" : "task_completed", "error": fails ? ["message": .string(summary)] : nil])
        emit(["type": "agent_status", "agent_id": .string(agentID), "status": fails ? "failed" : "completed", "summary": .string(summary), "error": fails ? ["message": .string(summary)] : nil])
        codingState = fails ? .failed : .done
        codingChanged?()
        say(summary, choices: [Self.retry])
        emit(["type": fails ? "task_failed" : "task_completed", "error": fails ? ["message": .string(summary)] : nil])
    }

    public func interrupt() {
        guard session.live != nil else { return }
        work?.cancel()
        pending = nil
        if codingState == .running {
            codingState = .stopped
            emit(["type": "agent_status", "agent_id": .string(agentID), "status": "interrupted"])
            codingChanged?()
        }
        say(String(localized: "The simulated task was stopped. Try again when you are ready.", bundle: .module), choices: [Self.retry])
        emit(["type": "task_interrupted"])
    }

    public func stop() {
        stopped = true
        work?.cancel()
        codingChanged = nil
        let closing = streams.values
        streams.removeAll()
        for c in closing { c.finish() }
    }

    private var now: JSON { .number(Date.now.timeIntervalSince1970 * 1000) }
    private func end() { emit(["type": "task_completed"]) }
    private func say(_ text: String, choices: [String] = []) {
        let args: JSON = choices.isEmpty ? ["text": .string(text)] : ["type": "question", "text": .string(text), "options": .array(choices.map { ["label": .string($0)] })]
        let id = "reply-\(events.count)"
        assistant(id, name: sendToUser, args: args)
        result(id, name: sendToUser)
    }
    private func assistant(_ id: String, name: String, args: JSON) {
        emit(["type": "message_completed", "message_id": .string("message-" + id), "message": ["role": "assistant", "ts": now, "content": [["type": "tool_call", "id": .string(id), "name": .string(name), "arguments": args]]]])
    }
    private func result(_ id: String, name: String, details: JSON = [:], error: Bool = false) {
        emit(["type": "tool_completed", "call_id": .string(id), "result": ["tool_name": .string(name), "is_error": .bool(error), "details": details]])
    }
    private func agent(_ event: JSON) {
        var e = event.object ?? [:]
        agentSeq += 1
        e["seq"] = .number(Double(agentSeq)); e["ts"] = now
        emit(["type": "agent_event", "agent_id": .string(agentID), "event": .object(e)])
    }
    private func emit(_ event: JSON) {
        var e = event.object ?? [:]
        e["seq"] = .number(Double(events.count + 1)); e["ts"] = now
        let value = JSON.object(e)
        events.append(value)
        session.apply(value)
        for c in streams.values { c.yield(notification(value)) }
    }
    private func notification(_ event: JSON) -> Hermesh_Her_V1_OpenEvent {
        .with { $0.notification = .with { $0.method = "session/event"; $0.params = JSON.object(["session_id": "her-review", "event": event]).data() } }
    }
}
