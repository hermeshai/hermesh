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
}

/// Where LocalService is served: hermeshd's Unix socket (the Mac), or the
/// loopback port of the owner role running in the iPhone app, with the
/// token it minted.
public enum LocalEndpoint: Sendable, Equatable {
    case unix(String)
    case loopback(port: Int, token: String)
}

/// A minimal Connect-protocol client (JSON codec) for LocalService. Unary
/// calls and server streams are enough. Unary calls give up after 10 s
/// without an answer, unless given a longer timeout.
public final class LocalAPI: @unchecked Sendable {
    public let endpoint: LocalEndpoint
    private let service = "hermesh.local.v1.LocalService"
    private let decodeOptions: JSONDecodingOptions = {
        var o = JSONDecodingOptions()
        o.ignoreUnknownFields = true
        return o
    }()

    public init(socketPath: String) { endpoint = .unix(socketPath) }
    public init(endpoint: LocalEndpoint) { self.endpoint = endpoint }

    // MARK: Unary

    public func call<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self, timeout: Int = 10) async throws -> Res {
        let body = try req.jsonUTF8Data()
        let path = "/\(service)/\(method)"
        let (status, headers, data) = try await Task.detached { [endpoint] in
            try HTTPOverUnix.roundTrip(endpoint: endpoint, path: path, contentType: "application/json", body: body, timeout: timeout)
        }.value
        if status != 200 {
            throw Self.parseError(data, status: status, headers: headers)
        }
        return try Res(jsonUTF8Data: data, options: decodeOptions)
    }

    // MARK: Server streaming

    public func stream<Req: SwiftProtobuf.Message, Res: SwiftProtobuf.Message>(_ method: String, _ req: Req, as _: Res.Type = Res.self) -> AsyncThrowingStream<Res, Error> {
        let path = "/\(service)/\(method)"
        let options = decodeOptions
        return AsyncThrowingStream { continuation in
            let conn = HTTPOverUnix()
            let task = Task.detached { [endpoint] in
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
                                    throw APIError(code: err["code"] as? String ?? "unknown", message: err["message"] as? String ?? "")
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
            continuation.onTermination = { _ in
                conn.close()
                task.cancel()
            }
        }
    }

    static func parseError(_ data: Data, status: Int, headers: [String: String]) -> APIError {
        if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            return APIError(code: obj["code"] as? String ?? "http_\(status)", message: obj["message"] as? String ?? "")
        }
        return APIError(code: "http_\(status)", message: String(data: data, encoding: .utf8) ?? "")
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
    private let lock = NSLock()

    static func roundTrip(endpoint: LocalEndpoint, path: String, contentType: String, body: Data, timeout: Int) throws -> (Int, [String: String], Data) {
        let c = HTTPOverUnix()
        defer { c.close() }
        let (status, headers) = try c.open(endpoint: endpoint, path: path, contentType: contentType, body: body, timeout: timeout)
        return (status, headers, try c.readAll(headers: headers))
    }

    func close() {
        lock.lock()
        defer { lock.unlock() }
        if fd >= 0 {
            Darwin.shutdown(fd, SHUT_RDWR)
            Darwin.close(fd)
            fd = -1
        }
    }

    func open(endpoint: LocalEndpoint, path: String, contentType: String, body: Data, timeout: Int? = nil) throws -> (Int, [String: String]) {
        let s: Int32
        var auth = ""
        switch endpoint {
        case .unix(let socketPath): s = try Self.connectUnix(socketPath)
        case .loopback(let port, let token):
            s = try Self.connectLoopback(port)
            auth = "Authorization: Bearer \(token)\r\n"
        }
        var on: Int32 = 1
        setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        if let timeout {
            var tv = timeval(tv_sec: timeout, tv_usec: 0)
            setsockopt(s, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        }
        lock.lock()
        fd = s
        lock.unlock()
        var req = "POST \(path) HTTP/1.1\r\nHost: hermesh\r\n\(auth)Content-Type: \(contentType)\r\nConnect-Protocol-Version: 1\r\nContent-Length: \(body.count)\r\n\r\n"
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
