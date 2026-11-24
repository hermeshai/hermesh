import Foundation
import HermeshProto
import HermeshSessions
import SwiftProtobuf

/// The last session projections opened, kept in memory and on the phone: going
/// back to one, or opening it again after a launch, shows it at once while
/// GetSession / SubscribeSession brings it up to date.
final class Conversations {
    /// How many are kept.
    private let keeps = 10
    private let persistent: Bool

    init(persistent: Bool = true) { self.persistent = persistent }
    private var held: [String: Hermesh_Session_V1_SessionProjection] = [:]
    /// Most recently opened last.
    private var order: [String] = []

    private var directory: URL { Core.dataDirectory.appendingPathComponent("conversations", isDirectory: true) }

    private func file(_ id: String) -> URL { directory.appendingPathComponent(id.replacingOccurrences(of: "/", with: "_") + ".pb") }

    /// What is kept of a session's projection.
    func projection(_ id: String) -> Hermesh_Session_V1_SessionProjection? {
        if let p = held[id] {
            touch(id)
            return p
        }
        guard persistent, let data = try? Data(contentsOf: file(id)), let p = try? Hermesh_Session_V1_SessionProjection(serializedBytes: data), p.hasSession else { return nil }
        held[id] = p
        touch(id)
        return p
    }

    /// Keeps a session projection as its page last showed it.
    func keep(_ id: String, _ proj: Hermesh_Session_V1_SessionProjection) {
        guard !proj.items.isEmpty || proj.hasSession else { return }
        defer { touch(id) }
        if let kept = held[id], kept.session.progress.lastEventSeq == proj.session.progress.lastEventSeq, kept.items.count == proj.items.count { return }
        held[id] = proj
        guard persistent, let data = try? proj.serializedData() else { return }
        let dir = directory, url = file(id)
        Task.detached(priority: .utility) {
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try? data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        }
    }

    /// Forgets a session deleted.
    func forget(_ id: String) {
        held[id] = nil
        order.removeAll { $0 == id }
        if persistent { try? FileManager.default.removeItem(at: file(id)) }
    }

    /// Forgets them all: the phone left the owner.
    func forgetAll() {
        held = [:]
        order = []
        if persistent { try? FileManager.default.removeItem(at: directory) }
    }

    private func touch(_ id: String) {
        order.removeAll { $0 == id }
        order.append(id)
        while order.count > keeps { forget(order[0]) }
    }
}
