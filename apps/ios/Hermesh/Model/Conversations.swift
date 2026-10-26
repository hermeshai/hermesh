import Foundation
import HermeshProto
import HermeshSessions
import SwiftProtobuf

/// The last conversations opened, kept in memory and on the phone: going
/// back to one, or opening it again after a launch, shows it at once, and
/// its page then reads only the events after the last one kept
/// (docs/state-sync.md §6).
final class Conversations {
    /// How many are kept.
    private let keeps = 10
    private var held: [String: [SessionEvent]] = [:]
    /// Most recently opened last.
    private var order: [String] = []

    private var directory: URL { Core.dataDirectory.appendingPathComponent("conversations", isDirectory: true) }

    private func file(_ id: String) -> URL { directory.appendingPathComponent(id.replacingOccurrences(of: "/", with: "_") + ".pb") }

    /// What is kept of a session's conversation, oldest first.
    func events(_ id: String) -> [SessionEvent] {
        if let e = held[id] {
            touch(id)
            return e
        }
        guard let data = try? Data(contentsOf: file(id)), let r = try? Hermesh_Local_V1_ListSessionEventsResponse(serializedBytes: data) else { return [] }
        held[id] = r.events
        touch(id)
        return r.events
    }

    /// Keeps a conversation as its page last showed it.
    func keep(_ id: String, _ events: [SessionEvent]) {
        guard !events.isEmpty else { return }
        defer { touch(id) }
        if let kept = held[id], kept.last?.seq == events.last?.seq, kept.count == events.count { return } // kept already
        held[id] = events
        var r = Hermesh_Local_V1_ListSessionEventsResponse()
        r.events = events
        guard let data = try? r.serializedData() else { return }
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
        try? FileManager.default.removeItem(at: file(id))
    }

    /// Forgets them all: the phone left the owner.
    func forgetAll() {
        held = [:]
        order = []
        try? FileManager.default.removeItem(at: directory)
    }

    private func touch(_ id: String) {
        order.removeAll { $0 == id }
        order.append(id)
        while order.count > keeps { forget(order[0]) }
    }
}
