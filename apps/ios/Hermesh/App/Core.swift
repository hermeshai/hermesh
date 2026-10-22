import Foundation
import HermeshCore
import HermeshKit

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
        return LocalAPI(endpoint: .loopback(port: port, token: token))
    }

    /// Keeps the relay connection up (the app is in front).
    static func resume() { Task.detached { HermeshCoreResume() } }

    /// Closes the relay connection (the app went to the background).
    static func pause() { Task.detached { HermeshCorePause() } }
}
