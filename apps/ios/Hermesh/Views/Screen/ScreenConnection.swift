import Darwin
import Foundation
import HermeshKit
import HermeshProto
import SwiftProtobuf

/// The connection's state, as the core reports it (internal/owner/screen.go).
nonisolated struct ScreenLink: Decodable, Equatable, Sendable {
    /// "connecting", "connected", "reconnecting", "failed".
    var phase = "connecting"
    /// "direct", or "relay" when the packets go through the relay.
    var path: String?
    var rtt_ms: Int?
    /// Relayed though both ends look to be on one network, where a direct
    /// path should have worked.
    var nearby: Bool?
    /// Why the session ended, with phase "failed".
    var error: String?
}

/// One Computer Control session with a machine (docs/computer-control.md):
/// a connection to the core's /screen/<machine>, which becomes messages
/// both ways. Frames, the machine's reports and the connection's state come
/// in on a thread of its own and are handed to the closures there; input
/// goes out from any thread. Closing it ends the session.
nonisolated final class ScreenConnection: @unchecked Sendable {
    private enum Kind: UInt8 {
        case input = 0x69, move = 0x6D, keyframe = 0x6B // to the core
        case video = 0x76, state = 0x65, status = 0x70 // from the core
    }

    var onFrame: (@Sendable (_ key: Bool, _ annexB: Data) -> Void)?
    var onEvent: (@Sendable (Hermesh_Screen_V1_ScreenEvent) -> Void)?
    var onLink: (@Sendable (ScreenLink) -> Void)?

    private var fd: Int32 = -1
    private let lock = NSLock()
    private var closed = false

    /// Opens the session and reads until it ends; onLink gets "failed" last,
    /// unless close ended it.
    func start(port: Int, token: String, machineID: String, name: String, pointer: Bool) {
        Thread.detachNewThread { [self] in
            let reason = run(port: port, token: token, machineID: machineID, name: name, pointer: pointer)
            lock.lock()
            let wasClosed = closed
            closed = true
            if fd >= 0 { Darwin.close(fd); fd = -1 }
            lock.unlock()
            if !wasClosed { onLink?(ScreenLink(phase: "failed", error: reason)) }
        }
    }

    /// Ends the session from any thread.
    func close() {
        lock.lock()
        defer { lock.unlock() }
        closed = true
        if fd >= 0 { Darwin.shutdown(fd, SHUT_RDWR) }
    }

    func send(_ input: Hermesh_Screen_V1_Input) {
        guard let data = try? input.serializedData() else { return }
        if case .move = input.event { write(.move, data) } else { write(.input, data) }
    }

    /// The frames can't be shown: ask for one that needs none before it.
    func requestKeyframe() { write(.keyframe, Data()) }

    private func write(_ kind: Kind, _ payload: Data) {
        var msg = Data(capacity: payload.count + 5)
        var n = UInt32(payload.count + 1).bigEndian
        msg.append(Data(bytes: &n, count: 4))
        msg.append(kind.rawValue)
        msg.append(payload)
        lock.lock()
        defer { lock.unlock() }
        guard fd >= 0, !closed else { return }
        msg.withUnsafeBytes { raw in
            var off = 0
            while off < raw.count {
                let w = Darwin.write(fd, raw.baseAddress! + off, raw.count - off)
                if w <= 0 { return }
                off += w
            }
        }
    }

    /// Returns why the session ended, as far as this side knows.
    private func run(port: Int, token: String, machineID: String, name: String, pointer: Bool) -> String {
        let s = socket(AF_INET, SOCK_STREAM, 0)
        guard s >= 0 else { return "socket" }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(UInt16(port).bigEndian)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let ok = withUnsafePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(s, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        var on: Int32 = 1
        setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        setsockopt(s, IPPROTO_TCP, TCP_NODELAY, &on, socklen_t(MemoryLayout<Int32>.size))
        lock.lock()
        if closed || ok != 0 {
            lock.unlock()
            Darwin.close(s)
            return "The Hermesh core is not running"
        }
        fd = s
        lock.unlock()

        var query = URLComponents()
        query.queryItems = [URLQueryItem(name: "name", value: name), URLQueryItem(name: "pointer", value: pointer ? "1" : "0")]
        let path = "/screen/\(machineID)?\(query.percentEncodedQuery ?? "")"
        let request = "GET \(path) HTTP/1.1\r\nHost: hermesh\r\nAuthorization: Bearer \(token)\r\nConnection: Upgrade\r\nUpgrade: hermesh-screen\r\n\r\n"
        let sent = Array(request.utf8).withUnsafeBytes { Darwin.write(s, $0.baseAddress!, $0.count) }
        guard sent > 0 else { return "write" }

        var buffer = Data()
        var chunk = [UInt8](repeating: 0, count: 256 * 1024)
        func fill() -> Bool {
            let n = read(s, &chunk, chunk.count)
            if n <= 0 { return false }
            buffer.append(chunk, count: n)
            return true
        }
        // The answer's head: 101, then the messages.
        let end = Data("\r\n\r\n".utf8)
        while buffer.range(of: end) == nil {
            guard fill() else { return "The Hermesh core closed the connection" }
        }
        let head = buffer.range(of: end)!
        guard String(decoding: buffer[..<head.lowerBound], as: UTF8.self).hasPrefix("HTTP/1.1 101") else { return "The Hermesh core refused the session" }
        buffer.removeSubrange(..<head.upperBound)

        var reason = ""
        while true {
            while buffer.count >= 5 {
                let b = buffer.startIndex
                let n = Int(buffer[b]) << 24 | Int(buffer[b + 1]) << 16 | Int(buffer[b + 2]) << 8 | Int(buffer[b + 3])
                guard n >= 1, buffer.count >= 4 + n else { break }
                let kind = buffer[b + 4]
                let payload = buffer.subdata(in: b + 5 ..< b + 4 + n)
                buffer.removeSubrange(b ..< b + 4 + n)
                switch Kind(rawValue: kind) {
                case .video:
                    if payload.count > 9 { onFrame?(payload[payload.startIndex] == 1, payload.subdata(in: payload.startIndex + 9 ..< payload.endIndex)) }
                case .state:
                    if let ev = try? Hermesh_Screen_V1_ScreenEvent(serializedBytes: payload) { onEvent?(ev) }
                case .status:
                    if let link = try? JSONDecoder().decode(ScreenLink.self, from: payload) {
                        if link.phase == "failed" { reason = link.error ?? "" } else { onLink?(link) }
                    }
                default:
                    break
                }
            }
            guard fill() else { return reason }
        }
    }
}
