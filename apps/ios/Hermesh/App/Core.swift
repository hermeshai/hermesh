import Foundation
import HermeshCore
import HermeshKit
import os

private let coreLog = Logger(subsystem: "ai.hermesh.app", category: "connectivity")

/// The owner role (Go, linked in as HermeshCore) running in this process.
/// It serves LocalService on a loopback port with a token minted per launch.
enum Core {
    struct Started: Decodable {
        var port: Int?
        var token: String?
        var error: String?
    }

    /// Where the owner keeps its data: Application Support/Hermesh, readable
    /// after the first unlock (a notification may wake the app while the
    /// phone is locked).
    static var dataDirectory: URL {
        if let d = ProcessInfo.processInfo.environment["HERMESH_DATA_DIR"], !d.isEmpty { return URL(fileURLWithPath: d) }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Hermesh")
    }

    /// Starts the core (once per process) and returns a client for it.
    static func start() throws -> LocalAPI {
        let dir = dataDirectory
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true,
                                                attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
        guard let raw = HermeshCoreStart(UnsafeMutablePointer(mutating: (dir.path as NSString).utf8String)) else {
            throw APIError(code: "unavailable", message: "The Hermesh core did not start")
        }
        defer { HermeshCoreFree(raw) }
        let started = try JSONDecoder().decode(Started.self, from: Data(String(cString: raw).utf8))
        guard let port = started.port, let token = started.token else {
            throw APIError(code: "unavailable", message: started.error ?? "The Hermesh core did not start")
        }
        return LocalAPI(endpoint: .loopback(port: port, token: token), ffi: CoreFFITransport())
    }

    /// Runs the calls below off the main thread (pausing waits for the core),
    /// one after another, in the order made: a quick trip to the background
    /// and back must not resume before it paused.
    private static let lifecycle = DispatchQueue(label: "hermesh.core.lifecycle")

    /// Serves LocalService again and keeps the relay connection up (the app
    /// is in front); then, once it answers calls again.
    static func resume(then: (@Sendable @MainActor () -> Void)? = nil) {
        let queued = Date()
        lifecycle.async {
            let start = Date()
            HermeshCoreResume()
            let queueMs = Int(start.timeIntervalSince(queued) * 1000)
            let resumeMs = Int(Date().timeIntervalSince(start) * 1000)
            coreLog.info("core.resume queue_ms=\(queueMs) resume_ms=\(resumeMs)")
            if let then { Task { @MainActor in then() } }
        }
    }

    /// Stops serving LocalService (iOS may take a suspended app's listening
    /// socket back) and following the machines, and keeps the relay
    /// connection to try again first (the app went to the background).
    static func pause() {
        lifecycle.async {
            let start = Date()
            HermeshCorePause()
            let pauseMs = Int(Date().timeIntervalSince(start) * 1000)
            coreLog.info("core.pause pause_ms=\(pauseMs)")
        }
    }

    /// Runs body on the main actor after queued lifecycle transitions settle.
    static func whenReady(_ body: @escaping @Sendable @MainActor () -> Void) {
        lifecycle.async { Task { @MainActor in body() } }
    }

    /// Connects again over the network now in use (Wi-Fi to cellular, say).
    static func networkChanged() { lifecycle.async { HermeshCoreNetworkChanged() } }
    /// The network changed in some way that left its interfaces as they
    /// were: the core connects again if the device's addresses differ.
    static func pathChanged() { lifecycle.async { HermeshCorePathChanged() } }
    /// Tells the core whether only cellular is active (no Wi-Fi/wired), so
    /// direct dialing skips unreachable RFC1918 LAN addresses.
    static func setCellularOnly(_ cellular: Bool) {
        lifecycle.async { HermeshCoreSetCellular(cellular ? 1 : 0) }
    }
}

/// Calls the embedded Go core directly in-process via C-ABI binary Protobuf FFI
/// (zero 127.0.0.1 loopback sockets, zero JSON/Base64 encoding, immune to iOS
/// TN2277 listening-socket reclaim).
private struct CoreFFITransport: LocalFFITransport {
    func unary(method: String, requestData: Data, timeoutSeconds: Int) throws -> Data {
        var outPtr: UnsafeMutableRawPointer?
        var outLen: Int32 = 0
        var errCode: UnsafeMutablePointer<CChar>?
        var errMsg: UnsafeMutablePointer<CChar>?

        let rc: Int32 = method.withCString { cMethod in
            requestData.withUnsafeBytes { raw in
                HermeshCoreUnary(
                    UnsafeMutablePointer(mutating: cMethod),
                    UnsafeMutableRawPointer(mutating: raw.baseAddress),
                    Int32(raw.count),
                    Int32(timeoutSeconds * 1000),
                    &outPtr,
                    &outLen,
                    &errCode,
                    &errMsg
                )
            }
        }
        defer {
            if let outPtr { HermeshCoreFreeBytes(outPtr) }
            if let errCode { HermeshCoreFree(errCode) }
            if let errMsg { HermeshCoreFree(errMsg) }
        }
        if rc != 0 {
            let code = errCode.map { String(cString: $0) } ?? "unknown"
            let msg = errMsg.map { String(cString: $0) } ?? ""
            throw APIError(code: code, message: code == "unimplemented" ? APIError.needsUpdateText : msg)
        }
        guard let outPtr, outLen > 0 else { return Data() }
        return Data(bytes: outPtr, count: Int(outLen))
    }

    func openStream(method: String, requestData: Data) -> Int64 {
        method.withCString { cMethod in
            requestData.withUnsafeBytes { raw in
                HermeshCoreStreamOpen(
                    UnsafeMutablePointer(mutating: cMethod),
                    UnsafeMutableRawPointer(mutating: raw.baseAddress),
                    Int32(raw.count)
                )
            }
        }
    }

    func nextFrame(streamID: Int64) throws -> Data? {
        var outPtr: UnsafeMutableRawPointer?
        var outLen: Int32 = 0
        var errCode: UnsafeMutablePointer<CChar>?
        var errMsg: UnsafeMutablePointer<CChar>?

        let rc = HermeshCoreStreamNext(streamID, &outPtr, &outLen, &errCode, &errMsg)
        defer {
            if let outPtr { HermeshCoreFreeBytes(outPtr) }
            if let errCode { HermeshCoreFree(errCode) }
            if let errMsg { HermeshCoreFree(errMsg) }
        }
        if rc < 0 {
            let code = errCode.map { String(cString: $0) } ?? "unknown"
            let msg = errMsg.map { String(cString: $0) } ?? ""
            throw APIError(code: code, message: code == "unimplemented" ? APIError.needsUpdateText : msg)
        }
        if rc == 0 {
            return nil
        }
        guard let outPtr, outLen > 0 else { return Data() }
        return Data(bytes: outPtr, count: Int(outLen))
    }

    func closeStream(streamID: Int64) {
        HermeshCoreStreamClose(streamID)
    }
}
