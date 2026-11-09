import Foundation
import HermeshProto

// her's connections as hermeshd forwards them (proto/hermesh/her/v1/her.proto,
// docs/her-server.md §6): a connection is a stream (HerOpen) that opens it,
// says its id with what each setup request returned, then carries her's
// notifications; each request is a call of its own on that connection
// (HerCall), answered once. As her-client's openCallTransport does, a call
// waits for the connection's id, and `initialize` is answered here: hermeshd
// initialized the connection itself and does not pass her's answer on.

/// The part of LocalService a connection uses: the app's core, or a test's.
public protocol HerLocal: Sendable {
    func herOpen(_ req: Hermesh_Local_V1_HerOpenRequest) -> AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error>
    func herCall(_ req: Hermesh_Local_V1_HerCallRequest) async throws -> Hermesh_Her_V1_CallResponse
}

/// A JSON-RPC error of her's, with the kind every her error carries.
public struct HerRPCError: LocalizedError, Equatable, Sendable {
    public var code: Int
    public var message: String
    public var kind: String
    public var data: JSON

    public init(code: Int, message: String, data: JSON = .null) {
        self.code = code
        self.message = message
        self.data = data
        kind = data["kind"].string ?? "internal_error"
    }

    public var errorDescription: String? { message }

    /// The connection ended before the answer came.
    public static let disconnected = HerRPCError(code: -32099, message: "disconnected from her", data: ["kind": "disconnected"])
}

/// What a connection's stream carries: its opening, with what each setup request returned, then her's notifications.
public enum HerConnectionEvent: Sendable {
    case opened([Result<JSON, HerRPCError>])
    case notification(method: String, params: JSON)
}

/// One connection to her on a machine. Open it once, then call on it while its stream lasts; a new
/// connection is opened for a stream that ended, and what the old one opened is asked for again.
public final class HerConnection: @unchecked Sendable {
    private let local: HerLocal
    private let machineID: String
    private let lock = NSLock()
    private var id: String?
    private var ended = false
    private var waiting: [CheckedContinuation<String?, Never>] = []

    public init(local: HerLocal, machineID: String) {
        self.local = local
        self.machineID = machineID
    }

    /// Opens the connection, sending `setup` on it first: the stream ends when the connection does, and
    /// ending the stream (cancelling the task that reads it) ends the connection.
    public func open(setup: [(method: String, params: JSON)]) -> AsyncThrowingStream<HerConnectionEvent, Error> {
        var req = Hermesh_Local_V1_HerOpenRequest()
        req.machineID = machineID
        req.open.setup = setup.map { r in .with { $0.method = r.method; $0.params = r.params.data() } }
        let events = local.herOpen(req)
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    for try await e in events {
                        switch e.kind {
                        case .opened(let o):
                            continuation.yield(.opened(o.responses.map(Self.result)))
                            self.opened(o.connectionID)
                        case .notification(let n):
                            continuation.yield(.notification(method: n.method, params: (try? JSON.parse(n.params)) ?? .null))
                        case nil:
                            break
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
                self.end()
            }
            continuation.onTermination = { _ in
                task.cancel()
                self.end()
            }
        }
    }

    /// her's answer to a request on this connection, or her's error thrown. A call made before the
    /// connection opened waits for it; once it ended, calls fail as disconnected.
    public func call(_ method: String, _ params: JSON = [:]) async throws -> JSON {
        if method == "initialize" { return Self.initialized }
        guard let id = await connectionID() else { throw HerRPCError.disconnected }
        var req = Hermesh_Local_V1_HerCallRequest()
        req.machineID = machineID
        req.call.connectionID = id
        req.call.request.method = method
        req.call.request.params = params.data()
        let res = try await local.herCall(req)
        return try Self.result(res.response).get()
    }

    /// her's answer to a request on a connection of its own (a sign-in's, which outlives the page's).
    public static func call(_ local: HerLocal, machineID: String, _ method: String, _ params: JSON = [:]) async throws -> JSON {
        var req = Hermesh_Local_V1_HerCallRequest()
        req.machineID = machineID
        req.call.request.method = method
        req.call.request.params = params.data()
        return try result(try await local.herCall(req).response).get()
    }

    /// Whether the connection has ended.
    public var isEnded: Bool { lock.withLock { ended } }

    private func connectionID() async -> String? {
        await withCheckedContinuation { k in
            lock.lock()
            if let id { lock.unlock(); k.resume(returning: id); return }
            if ended { lock.unlock(); k.resume(returning: nil); return }
            waiting.append(k)
            lock.unlock()
        }
    }

    private func opened(_ connectionID: String) {
        let waiters: [CheckedContinuation<String?, Never>] = lock.withLock {
            guard !ended else { return [] }
            id = connectionID
            defer { waiting = [] }
            return waiting
        }
        waiters.forEach { $0.resume(returning: connectionID) }
    }

    private func end() {
        let waiters: [CheckedContinuation<String?, Never>] = lock.withLock {
            ended = true
            id = nil
            defer { waiting = [] }
            return waiting
        }
        waiters.forEach { $0.resume(returning: nil) }
    }

    /// A request's answer as her gave it: its result, or her's error.
    static func result(_ r: Hermesh_Her_V1_Response) -> Result<JSON, HerRPCError> {
        if r.hasError {
            return .failure(HerRPCError(code: Int(r.error.code), message: r.error.message, data: (try? JSON.parse(r.error.data)) ?? .null))
        }
        return .success(r.result.isEmpty ? [:] : (try? JSON.parse(r.result)) ?? [:])
    }

    /// What `initialize` answers: her's own (her-server's native.rs), as her-client has it.
    static let initialized: JSON = [
        "protocol_version": 2,
        "server_info": ["name": "her"],
        "capabilities": ["bots": true, "routines": true, "auth": true, "model_connections": true, "input_queue": true, "session_metadata": true],
    ]
}
