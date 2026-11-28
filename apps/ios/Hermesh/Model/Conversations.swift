import Foundation
import HermeshProto
import HermeshSessions
import SwiftProtobuf

private actor ConversationDiskWriter {
    static let shared = ConversationDiskWriter()

    func read(_ url: URL) -> Hermesh_Session_V1_SessionProjection? {
        guard let data = try? Data(contentsOf: url),
              let p = try? Hermesh_Session_V1_SessionProjection(serializedBytes: data),
              p.hasSession else { return nil }
        return p
    }

    func write(_ proj: Hermesh_Session_V1_SessionProjection, dir: URL, url: URL) {
        guard let data = try? proj.serializedData() else { return }
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try? data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    func remove(_ url: URL) {
        try? FileManager.default.removeItem(at: url)
    }
}

/// The last session projections opened, kept in memory and on the phone: going
/// back to one, or opening it again after a launch, shows it at once while
/// GetSession / SubscribeSession brings it up to date.
final class Conversations {
    /// How many are kept.
    private let keeps = 10
    private let persistent: Bool
    private let directoryOverride: URL?

    init(persistent: Bool = true, directoryOverride: URL? = nil) {
        self.persistent = persistent
        self.directoryOverride = directoryOverride
    }
    private var held: [String: Hermesh_Session_V1_SessionProjection] = [:]
    /// Most recently opened last.
    private var order: [String] = []
    private var writeGen: [String: UInt64] = [:]

    private var directory: URL { directoryOverride ?? Core.dataDirectory.appendingPathComponent("conversations", isDirectory: true) }

    private func file(_ id: String) -> URL { directory.appendingPathComponent(id.replacingOccurrences(of: "/", with: "_") + ".pb") }

    /// Side-effect-free memory lookup for synchronous View initialization on the first frame.
    func peek(_ id: String) -> Hermesh_Session_V1_SessionProjection? {
        held[id]
    }

    /// Loads a kept projection from memory immediately, or off the main thread from disk.
    func load(_ id: String) async -> Hermesh_Session_V1_SessionProjection? {
        if let p = held[id] {
            touch(id)
            return p
        }
        guard persistent else { return nil }
        let url = file(id)
        let gen = writeGen[id, default: 0]
        guard let p = await ConversationDiskWriter.shared.read(url) else { return nil }
        if held[id] == nil, writeGen[id, default: 0] == gen {
            held[id] = p
            touch(id)
        }
        return held[id] ?? p
    }

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
        if let kept = held[id],
           kept.session.progress.lastEventSeq == proj.session.progress.lastEventSeq,
           kept.session.status == proj.session.status,
           kept.session.title == proj.session.title,
           kept.items.count == proj.items.count { return }
        held[id] = proj
        persist(id, proj)
    }

    /// Updates a kept projection's session metadata (e.g. archived or renamed) at once.
    func updateSession(_ s: Session) {
        guard var proj = held[s.sessionID] else { return }
        proj.session = s
        held[s.sessionID] = proj
        persist(s.sessionID, proj)
    }

    private func persist(_ id: String, _ proj: Hermesh_Session_V1_SessionProjection) {
        guard persistent else { return }
        let nextGen = writeGen[id, default: 0] &+ 1
        writeGen[id] = nextGen
        let dir = directory, url = file(id)
        Task(priority: .utility) { [weak self] in
            await ConversationDiskWriter.shared.write(proj, dir: dir, url: url)
            _ = self
        }
    }

    /// Forgets a session deleted.
    func forget(_ id: String) {
        writeGen[id] = writeGen[id, default: 0] &+ 1
        held[id] = nil
        order.removeAll { $0 == id }
        if persistent {
            let url = file(id)
            Task(priority: .utility) { await ConversationDiskWriter.shared.remove(url) }
        }
    }

    /// Forgets them all: the phone left the owner.
    func forgetAll() {
        writeGen.removeAll()
        held = [:]
        order = []
        if persistent {
            let dir = directory
            Task(priority: .utility) { await ConversationDiskWriter.shared.remove(dir) }
        }
    }

    func touch(_ id: String) {
        order.removeAll { $0 == id }
        order.append(id)
        while order.count > keeps { forget(order[0]) }
    }
}
