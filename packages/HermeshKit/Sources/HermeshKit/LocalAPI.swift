import Darwin
import Foundation
import HermeshProto
import SwiftProtobuf

/// Errors returned by hermeshd (Connect protocol error codes).
public struct APIError: LocalizedError {
    public let code: String
    public let message: String
    public var errorDescription: String? { message.isEmpty ? code : message }

    public init(code: String, message: String) {
        self.code = code
        self.message = message
    }

    /// The text for something a machine's Hermesh doesn't have yet: its own version is older than
    /// this app's (docs/version-skew.md §6.3).
    public static let needsUpdateText = String(localized: "Hermesh on that computer is older and doesn’t have this yet. Update Hermesh there, then try again.", bundle: .module)

    /// An error as the core sent it. `unimplemented` only ever means a machine older than this app,
    /// so it says that, in place of the transport's words.
    static func received(code: String, message: String) -> APIError {
        APIError(code: code, message: code == "unimplemented" ? needsUpdateText : message)
    }
}

/// Where LocalService is served: hermeshd's Unix socket (the Mac), or the
/// loopback port of the owner role running in the iPhone app, with the
/// token it minted.
public enum LocalEndpoint: Sendable, Equatable {
    case unix(String)
    case loopback(port: Int, token: String)
}

/// In-process binary Protobuf FFI transport (used on iOS to call the embedded
/// core directly in memory with zero TCP loopback sockets, zero TN2277
/// listener reclaim races, and zero JSON/Base64 overhead).
public protocol LocalFFITransport: Sendable {
    func unary(method: String, requestData: Data, timeoutSeconds: Int) throws -> Data
    func openStream(method: String, requestData: Data) -> Int64
    func nextFrame(streamID: Int64) throws -> Data?
    func closeStream(streamID: Int64)
}

/// A minimal Connect-protocol client for LocalService. Uses in-process binary
/// Protobuf FFI when provided (iPhone), else HTTP/1.1 JSON over Unix socket
/// (Mac) or loopback. Unary calls give up after 10 s without an answer, unless
/// given a longer timeout.
///
/// Blocking transport calls run on a dedicated thread, never on Swift's
/// cooperative pool: a stream open for as long as a page shows, or a call that
/// waits for Face ID, would hold one of its few threads (one per core) and
/// could starve the work it waits for.
public final class LocalAPI: @unchecked Sendable {
    public let endpoint: LocalEndpoint
    private let ffi: (any LocalFFITransport)?
    private let service = "hermesh.local.v1.LocalService"
    private let sessionService = "hermesh.session.v1.SessionService"
    private let decodeOptions: JSONDecodingOptions = {
        var o = JSONDecodingOptions()
        o.ignoreUnknownFields = true
        return o
    }()

    public init(socketPath: String) {
        endpoint = .unix(socketPath)
        ffi = nil
    }
    public init(endpoint: LocalEndpoint, ffi: (any LocalFFITransport)? = nil) {
        self.endpoint = endpoint
        self.ffi = ffi
    }

    // MARK: Unary

    public func call<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self, timeout: Int = 10) async throws -> Res {
        if let ffi {
            let body = try req.serializedData()
            let data = try await withCheckedThrowingContinuation { (k: CheckedContinuation<Data, Error>) in
                Thread.detachNewThread {
                    k.resume(with: Result { try ffi.unary(method: method, requestData: body, timeoutSeconds: timeout) })
                }
            }
            return try Res(serializedBytes: data)
        }
        let body = try req.jsonUTF8Data()
        let path = method.hasPrefix("/") ? method : "/\(service)/\(method)"
        let endpoint = endpoint
        let (status, headers, data) = try await withCheckedThrowingContinuation { (k: CheckedContinuation<(Int, [String: String], Data), Error>) in
            Thread.detachNewThread {
                k.resume(with: Result { try HTTPOverUnix.roundTrip(endpoint: endpoint, path: path, contentType: "application/json", body: body, timeout: timeout) })
            }
        }
        if status != 200 {
            throw Self.parseError(data, status: status, headers: headers)
        }
        return try Res(jsonUTF8Data: data, options: decodeOptions)
    }

    public func callSession<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self, timeout: Int = 15) async throws -> Res {
        try await call("/\(sessionService)/\(method)", req, as: Res.self, timeout: timeout)
    }

    // MARK: Server streaming

    public func stream<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self) -> AsyncThrowingStream<Res, Error> {
        if let ffi {
            return AsyncThrowingStream { continuation in
                let state = FFIStreamCancelState()
                Thread.detachNewThread {
                    do {
                        let body = try req.serializedData()
                        let sid = ffi.openStream(method: method, requestData: body)
                        guard sid > 0 else {
                            throw APIError(code: "unavailable", message: "Hermesh core is not running")
                        }
                        if !state.attach(sid) {
                            ffi.closeStream(streamID: sid)
                            continuation.finish()
                            return
                        }
                        defer {
                            if state.markClosed() {
                                ffi.closeStream(streamID: sid)
                            }
                        }
                        while let frame = try ffi.nextFrame(streamID: sid) {
                            continuation.yield(try Res(serializedBytes: frame))
                        }
                        continuation.finish()
                    } catch {
                        continuation.finish(throwing: error)
                    }
                }
                continuation.onTermination = { _ in
                    if let sid = state.cancel() {
                        ffi.closeStream(streamID: sid)
                    }
                }
            }
        }
        let path = method.hasPrefix("/") ? method : "/\(service)/\(method)"
        let options = decodeOptions
        return AsyncThrowingStream { continuation in
            let conn = HTTPOverUnix()
            let endpoint = endpoint
            Thread.detachNewThread {
                defer { conn.release() }
                do {
                    var envelope = Data([0])
                    let json = try req.jsonUTF8Data()
                    var len = UInt32(json.count).bigEndian
                    envelope.append(Data(bytes: &len, count: 4))
                    envelope.append(json)
                    let (status, headers) = try conn.open(endpoint: endpoint, path: path, contentType: "application/connect+json", body: envelope)
                    if status != 200 {
                        let data = try conn.readAll(headers: headers)
                        throw Self.parseError(data, status: status, headers: headers)
                    }
                    var buffer = Data()
                    while let chunk = try conn.readBodyChunk(headers: headers) {
                        buffer.append(chunk)
                        while buffer.count >= 5 {
                            let flags = buffer[buffer.startIndex]
                            let n = buffer.subdata(in: buffer.startIndex + 1 ..< buffer.startIndex + 5).withUnsafeBytes { $0.load(as: UInt32.self).bigEndian }
                            guard buffer.count >= 5 + Int(n) else { break }
                            let payload = buffer.subdata(in: buffer.startIndex + 5 ..< buffer.startIndex + 5 + Int(n))
                            buffer.removeSubrange(buffer.startIndex ..< buffer.startIndex + 5 + Int(n))
                            if flags & 0x02 != 0 {
                                if let obj = try? JSONSerialization.jsonObject(with: payload) as? [String: Any],
                                   let err = obj["error"] as? [String: Any] {
                                    throw APIError.received(code: err["code"] as? String ?? "unknown", message: err["message"] as? String ?? "")
                                }
                                continuation.finish()
                                return
                            }
                            continuation.yield(try Res(jsonUTF8Data: payload, options: options))
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in conn.close() }
        }
    }

    public func streamSession<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self) -> AsyncThrowingStream<Res, Error> {
        stream("/\(sessionService)/\(method)", req, as: Res.self)
    }

    // MARK: SessionService Convenience Wrappers

    public func listProviders(machineID: String, projectID: String = "", refresh: Bool = false) async throws -> Hermesh_Session_V1_ListProvidersResponse {
        var r = Hermesh_Session_V1_ListProvidersRequest()
        r.machineID = machineID
        r.projectID = projectID
        r.refresh = refresh
        return try await callSession("ListProviders", r)
    }

    public func startSession(_ req: Hermesh_Session_V1_StartSessionRequest) async throws -> Hermesh_Session_V1_StartSessionResponse {
        try await callSession("StartSession", req, timeout: 30)
    }

    public func listSessions(_ req: Hermesh_Session_V1_ListSessionsRequest = .init()) async throws -> Hermesh_Session_V1_ListSessionsResponse {
        try await callSession("ListSessions", req)
    }

    public func getSession(sessionID: String, machineID: String = "") async throws -> Hermesh_Session_V1_GetSessionResponse {
        var r = Hermesh_Session_V1_GetSessionRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        return try await callSession("GetSession", r)
    }

    public func watchSessions(machineID: String = "", includeArchived: Bool = false) -> AsyncThrowingStream<Hermesh_Session_V1_WatchSessionsResponse, Error> {
        var r = Hermesh_Session_V1_WatchSessionsRequest()
        r.machineID = machineID
        r.includeArchived = includeArchived
        return streamSession("WatchSessions", r)
    }

    public func subscribeSession(sessionID: String, machineID: String = "", afterSeq: UInt64 = 0) -> AsyncThrowingStream<Hermesh_Session_V1_SessionEvent, Error> {
        var r = Hermesh_Session_V1_SubscribeSessionRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.afterSeq = afterSeq
        return streamSession("SubscribeSession", r)
    }

    public func dispatchMessage(_ req: Hermesh_Session_V1_DispatchMessageRequest) async throws -> Hermesh_Session_V1_DispatchMessageResponse {
        try await callSession("DispatchMessage", req, timeout: 20)
    }

    /// A file for a session's message, kept by hermeshd until the message goes (and sent on to its machine then).
    public func uploadBlob(machineID: String = "", name: String, mimeType: String, data: Data) async throws -> Hermesh_Session_V1_UploadBlobResponse {
        var r = Hermesh_Session_V1_UploadBlobRequest()
        r.machineID = machineID
        r.name = name
        r.mimeType = mimeType
        r.data = data
        return try await callSession("UploadBlob", r, timeout: 60)
    }

    public func interruptSession(sessionID: String, runID: String = "", force: Bool = false) async throws -> Hermesh_Session_V1_InterruptSessionResponse {
        var r = Hermesh_Session_V1_InterruptSessionRequest()
        r.sessionID = sessionID
        r.runID = runID
        r.force = force
        return try await callSession("InterruptSession", r)
    }

    public func stopBackgroundTask(sessionID: String, taskID: String = "", machineID: String = "") async throws -> Hermesh_Session_V1_StopBackgroundTaskResponse {
        var r = Hermesh_Session_V1_StopBackgroundTaskRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.taskID = taskID
        return try await callSession("StopBackgroundTask", r)
    }

    public func manageQueue(_ req: Hermesh_Session_V1_ManageQueueRequest) async throws -> Hermesh_Session_V1_ManageQueueResponse {
        try await callSession("ManageQueue", req)
    }

    public func updateSession(_ req: Hermesh_Session_V1_UpdateSessionRequest) async throws -> Hermesh_Session_V1_UpdateSessionResponse {
        try await callSession("UpdateSession", req)
    }

    public func deleteSession(sessionID: String, machineID: String = "") async throws -> Hermesh_Session_V1_DeleteSessionResponse {
        var r = Hermesh_Session_V1_DeleteSessionRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        return try await callSession("DeleteSession", r)
    }

    public func respondRuntimeRequest(_ req: Hermesh_Session_V1_RespondRuntimeRequestRequest) async throws -> Hermesh_Session_V1_RespondRuntimeRequestResponse {
        try await callSession("RespondRuntimeRequest", req)
    }

    public func listCheckpoints(sessionID: String) async throws -> Hermesh_Session_V1_ListCheckpointsResponse {
        var r = Hermesh_Session_V1_ListCheckpointsRequest()
        r.sessionID = sessionID
        return try await callSession("ListCheckpoints", r)
    }

    public func rollbackCheckpoint(sessionID: String, checkpointID: String, restoreFiles: Bool) async throws -> Hermesh_Session_V1_RollbackCheckpointResponse {
        var r = Hermesh_Session_V1_RollbackCheckpointRequest()
        r.sessionID = sessionID
        r.checkpointID = checkpointID
        r.restoreFiles = restoreFiles
        return try await callSession("RollbackCheckpoint", r)
    }

    public func getSessionDiff(sessionID: String, checkpointID: String = "", machineID: String = "") async throws -> Hermesh_Session_V1_GetDiffResponse {
        var r = Hermesh_Session_V1_GetDiffRequest()
        r.machineID = machineID
        r.sessionID = sessionID
        r.checkpointID = checkpointID
        return try await callSession("GetDiff", r)
    }

    // MARK: Plain HTTP on loopback

    /// A request to a port on this device's loopback, without a token: the
    /// Her console's sign-in routes (GetHerConsole), which wait a while
    /// before they answer. Cancelling the task ends it.
    public static func loopback(port: Int, method: String, path: String, body: Data = Data(), contentType: String = "application/json",
                                headers: [String: String] = [:], timeout: Int = 40) async throws -> (status: Int, body: Data) {
        let conn = HTTPOverUnix()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (k: CheckedContinuation<(status: Int, body: Data), Error>) in
                Thread.detachNewThread {
                    defer { conn.release() }
                    k.resume(with: Result {
                        let (status, answered) = try conn.open(endpoint: .loopback(port: port, token: ""), method: method, path: path, contentType: contentType,
                                                               headers: headers, body: body, timeout: timeout)
                        return (status, try conn.readAll(headers: answered))
                    })
                }
            }
        } onCancel: {
            conn.close()
        }
    }

    static func parseError(_ data: Data, status: Int, headers: [String: String]) -> APIError {
        if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            return APIError.received(code: obj["code"] as? String ?? "http_\(status)", message: obj["message"] as? String ?? "")
        }
        return APIError(code: "http_\(status)", message: String(data: data, encoding: .utf8) ?? "")
    }
}

private final class FFIStreamCancelState: @unchecked Sendable {
    private let lock = NSLock()
    private var sid: Int64 = 0
    private var closed = false

    func attach(_ id: Int64) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if closed { return false }
        sid = id
        return true
    }

    func cancel() -> Int64? {
        lock.lock()
        defer { lock.unlock() }
        if closed { return nil }
        closed = true
        return sid > 0 ? sid : nil
    }

    func markClosed() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if closed { return false }
        closed = true
        return true
    }
}

/// Blocking HTTP/1.1 over a Unix domain socket or to a loopback port (run
/// off the main thread).
final class HTTPOverUnix: @unchecked Sendable {
    private var fd: Int32 = -1
    private var pending = Data()
    private var remainingInChunk = 0
    private var contentRemaining: Int?
    private var finished = false
    private var closed = false
    private let lock = NSLock()

    static func roundTrip(endpoint: LocalEndpoint, path: String, contentType: String, body: Data, timeout: Int) throws -> (Int, [String: String], Data) {
        let c = HTTPOverUnix()
        defer { c.release() }
        let (status, headers) = try c.open(endpoint: endpoint, path: path, contentType: contentType, body: body, timeout: timeout)
        return (status, headers, try c.readAll(headers: headers))
    }

    /// Ends the exchange from any thread: a read blocked on the socket
    /// returns. The descriptor stays open until release, on the thread that
    /// reads, so it is never closed (and its number reused by another
    /// connection) under a read about to start.
    func close() {
        lock.lock()
        defer { lock.unlock() }
        closed = true
        if fd >= 0 { Darwin.shutdown(fd, SHUT_RDWR) }
    }

    /// Closes the descriptor, once the thread that reads is done with it.
    func release() {
        lock.lock()
        defer { lock.unlock() }
        closed = true
        if fd >= 0 {
            Darwin.close(fd)
            fd = -1
        }
    }

    func open(endpoint: LocalEndpoint, method: String = "POST", path: String, contentType: String, headers: [String: String] = [:], body: Data,
              timeout: Int? = nil) throws -> (Int, [String: String]) {
        let s: Int32
        var auth = ""
        switch endpoint {
        case .unix(let socketPath): s = try Self.connectUnix(socketPath)
        case .loopback(let port, let token):
            s = try Self.connectLoopback(port)
            if !token.isEmpty { auth = "Authorization: Bearer \(token)\r\n" }
        }
        var on: Int32 = 1
        setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        if let timeout {
            var tv = timeval(tv_sec: timeout, tv_usec: 0)
            setsockopt(s, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        }
        lock.lock()
        if closed { // ended before it was open
            lock.unlock()
            Darwin.close(s)
            throw APIError(code: "canceled", message: "")
        }
        fd = s
        lock.unlock()
        let more = headers.sorted { $0.key < $1.key }.map { "\($0.key): \($0.value)\r\n" }.joined()
        var req = "\(method) \(path) HTTP/1.1\r\nHost: hermesh\r\n\(auth)\(more)Content-Type: \(contentType)\r\nConnect-Protocol-Version: 1\r\nContent-Length: \(body.count)\r\n\r\n"
        return try exchange(req: &req, contentType: contentType, body: body)
    }

    private static func connectUnix(_ socketPath: String) throws -> Int32 {
        let s = socket(AF_UNIX, SOCK_STREAM, 0)
        guard s >= 0 else { throw APIError(code: "unavailable", message: "socket() failed") }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(socketPath.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { throw APIError(code: "unavailable", message: "socket path too long") }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        let ok = withUnsafePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard ok == 0 else {
            Darwin.close(s)
            throw APIError(code: "unavailable", message: "Hermesh background service is not running")
        }
        return s
    }

    private static func connectLoopback(_ port: Int) throws -> Int32 {
        let s = socket(AF_INET, SOCK_STREAM, 0)
        guard s >= 0 else { throw APIError(code: "unavailable", message: "socket() failed") }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(UInt16(port).bigEndian)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let ok = withUnsafePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(s, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard ok == 0 else {
            Darwin.close(s)
            throw APIError(code: "unavailable", message: "Hermesh core is not running")
        }
        var on: Int32 = 1
        setsockopt(s, IPPROTO_TCP, TCP_NODELAY, &on, socklen_t(MemoryLayout<Int32>.size))
        return s
    }

    private func exchange(req: inout String, contentType: String, body: Data) throws -> (Int, [String: String]) {
        if contentType.contains("connect+") { req = req.replacingOccurrences(of: "\r\n\r\n", with: "\r\nConnect-Accept-Encoding: identity\r\n\r\n") }
        try writeAll(Data(req.utf8) + body)
        // Status line + headers.
        var head = Data()
        while head.range(of: Data("\r\n\r\n".utf8)) == nil {
            guard let b = try readSome() else { throw APIError(code: "unavailable", message: "connection closed") }
            head.append(b)
        }
        let r = head.range(of: Data("\r\n\r\n".utf8))!
        pending = head.subdata(in: r.upperBound ..< head.endIndex)
        let text = String(decoding: head.subdata(in: head.startIndex ..< r.lowerBound), as: UTF8.self)
        var lines = text.components(separatedBy: "\r\n")
        let statusLine = lines.removeFirst().split(separator: " ")
        let status = statusLine.count > 1 ? Int(statusLine[1]) ?? 0 : 0
        var headers: [String: String] = [:]
        for l in lines {
            if let i = l.firstIndex(of: ":") {
                headers[l[..<i].lowercased()] = l[l.index(after: i)...].trimmingCharacters(in: .whitespaces)
            }
        }
        if let cl = headers["content-length"], let n = Int(cl) { contentRemaining = n }
        return (status, headers)
    }

    func readAll(headers: [String: String]) throws -> Data {
        var out = Data()
        while let c = try readBodyChunk(headers: headers) { out.append(c) }
        return out
    }

    /// Returns the next piece of the response body, or nil at the end.
    func readBodyChunk(headers: [String: String]) throws -> Data? {
        if finished { return nil }
        if headers["transfer-encoding"]?.lowercased() == "chunked" {
            if remainingInChunk == 0 {
                let line = try readLine()
                let size = Int(line.split(separator: ";").first.map(String.init) ?? "0", radix: 16) ?? 0
                if size == 0 {
                    finished = true
                    return nil
                }
                remainingInChunk = size
            }
            let data = try readUpTo(remainingInChunk)
            remainingInChunk -= data.count
            if remainingInChunk == 0 { _ = try readLine() }
            return data
        }
        if var n = contentRemaining {
            if n == 0 { finished = true; return nil }
            let data = try readUpTo(n)
            n -= data.count
            contentRemaining = n
            return data
        }
        if let b = try takePendingOrRead() { return b }
        finished = true
        return nil
    }

    private func takePendingOrRead() throws -> Data? {
        if !pending.isEmpty {
            defer { pending = Data() }
            return pending
        }
        return try readSome()
    }

    private func readUpTo(_ n: Int) throws -> Data {
        if pending.isEmpty, let b = try readSome() { pending = b }
        guard !pending.isEmpty else { throw APIError(code: "unavailable", message: "connection closed") }
        let take = min(n, pending.count)
        let out = pending.prefix(take)
        pending = pending.dropFirst(take)
        return Data(out)
    }

    private func readLine() throws -> String {
        while pending.range(of: Data("\r\n".utf8)) == nil {
            guard let b = try readSome() else { throw APIError(code: "unavailable", message: "connection closed") }
            pending.append(b)
        }
        let r = pending.range(of: Data("\r\n".utf8))!
        let line = String(decoding: pending.subdata(in: pending.startIndex ..< r.lowerBound), as: UTF8.self)
        pending = pending.subdata(in: r.upperBound ..< pending.endIndex)
        return line
    }

    private func readSome() throws -> Data? {
        lock.lock()
        let s = fd
        lock.unlock()
        guard s >= 0 else { return nil }
        var buf = [UInt8](repeating: 0, count: 64 * 1024)
        let n = read(s, &buf, buf.count)
        if n < 0 { throw APIError(code: "unavailable", message: String(cString: strerror(errno))) }
        if n == 0 { return nil }
        return Data(buf[0 ..< n])
    }

    private func writeAll(_ data: Data) throws {
        try data.withUnsafeBytes { raw in
            var off = 0
            while off < raw.count {
                let n = write(fd, raw.baseAddress! + off, raw.count - off)
                if n <= 0 { throw APIError(code: "unavailable", message: "write failed") }
                off += n
            }
        }
    }
}
