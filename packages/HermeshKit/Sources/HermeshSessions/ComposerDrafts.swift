import Foundation
import HermeshProto
import SwiftProtobuf

/// Unsent composer text and uploaded attachment references for a session or New Session.
public struct ComposerDraft: Equatable, Sendable {
    public var text: String
    public var attachments: [Hermesh_Session_V1_AttachmentRef]

    public static let empty = ComposerDraft()

    public init(text: String = "", attachments: [Hermesh_Session_V1_AttachmentRef] = []) {
        self.text = text
        self.attachments = attachments
    }

    public var isEmpty: Bool {
        text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty
    }
}

/// Remembers unsent composer drafts per session (and one shared New Session draft) in memory
/// and in local `UserDefaults`, bounded by LRU eviction.
public final class ComposerDraftStore {
    public static let newSessionKey = "new"
    public static let defaultMaxEntries = 50
    public static let defaultStorageKey = "sessions.composerDrafts"

    public static func key(sessionID: String) -> String {
        sessionID.isEmpty ? newSessionKey : "session:\(sessionID)"
    }

    private struct StoredEntry: Codable {
        var key: String
        var text: String
        var attachments: [Data]
    }

    private let persistent: Bool
    private let defaults: UserDefaults
    private let storageKey: String
    private let maxEntries: Int

    private var persisted: [String: ComposerDraft] = [:]
    private var order: [String] = []
    private var live: [String: ComposerDraft] = [:]

    public init(
        persistent: Bool = true,
        defaults: UserDefaults = .standard,
        storageKey: String = ComposerDraftStore.defaultStorageKey,
        maxEntries: Int = ComposerDraftStore.defaultMaxEntries
    ) {
        self.persistent = persistent
        self.defaults = defaults
        self.storageKey = storageKey
        self.maxEntries = max(1, maxEntries)
        if persistent {
            loadFromDefaults()
        }
    }

    public func draft(for key: String) -> ComposerDraft {
        guard !key.isEmpty else { return .empty }
        if let active = live[key] { return active }
        return persisted[key] ?? .empty
    }

    public func save(_ next: ComposerDraft, for key: String) {
        guard !key.isEmpty else { return }
        let normalized = ComposerDraft(text: next.text, attachments: deduplicate(next.attachments))
        let prev = draft(for: key)
        if prev == normalized { return }

        if normalized.text.isEmpty && normalized.attachments.isEmpty {
            live.removeValue(forKey: key)
            if removePersisted(key) { flush() }
            return
        }

        if normalized.isEmpty {
            // Whitespace-only input while typing: keep in live memory without persisting blank junk.
            live[key] = normalized
            if removePersisted(key) { flush() }
            return
        }

        live.removeValue(forKey: key)
        persisted[key] = normalized
        order.removeAll { $0 == key }
        order.append(key)
        while order.count > maxEntries {
            let oldest = order.removeFirst()
            persisted.removeValue(forKey: oldest)
            live.removeValue(forKey: oldest)
        }
        flush()
    }

    public func setText(_ text: String, for key: String) {
        let cur = draft(for: key)
        save(ComposerDraft(text: text, attachments: cur.attachments), for: key)
    }

    public func addAttachment(_ ref: Hermesh_Session_V1_AttachmentRef, for key: String) {
        guard !ref.blobSha256.isEmpty else { return }
        let cur = draft(for: key)
        guard !cur.attachments.contains(where: { $0.blobSha256 == ref.blobSha256 }) else { return }
        save(ComposerDraft(text: cur.text, attachments: cur.attachments + [ref]), for: key)
    }

    public func removeAttachment(sha256: String, for key: String) {
        let cur = draft(for: key)
        guard cur.attachments.contains(where: { $0.blobSha256 == sha256 }) else { return }
        save(ComposerDraft(text: cur.text, attachments: cur.attachments.filter { $0.blobSha256 != sha256 }), for: key)
    }

    public func clear(_ key: String) {
        guard !key.isEmpty else { return }
        live.removeValue(forKey: key)
        if removePersisted(key) { flush() }
    }

    @discardableResult
    public func restore(_ failed: ComposerDraft, for key: String) -> ComposerDraft {
        guard !key.isEmpty, !failed.isEmpty else { return draft(for: key) }
        let cur = draft(for: key)
        let parts = [failed.text, cur.text].filter { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        let merged = ComposerDraft(
            text: parts.joined(separator: "\n"),
            attachments: deduplicate(failed.attachments + cur.attachments)
        )
        save(merged, for: key)
        return draft(for: key)
    }

    public func move(from fromKey: String, to toKey: String) {
        guard !fromKey.isEmpty, !toKey.isEmpty, fromKey != toKey else { return }
        let carried = draft(for: fromKey)
        clear(fromKey)
        guard !carried.isEmpty else { return }
        let existing = draft(for: toKey)
        let parts = [existing.text, carried.text].filter { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        save(ComposerDraft(
            text: parts.joined(separator: "\n"),
            attachments: deduplicate(existing.attachments + carried.attachments)
        ), for: toKey)
    }

    public func forget(sessionID: String) {
        guard !sessionID.isEmpty else { return }
        clear(Self.key(sessionID: sessionID))
    }

    public func forgetAll() {
        live.removeAll()
        persisted.removeAll()
        order.removeAll()
        if persistent {
            defaults.removeObject(forKey: storageKey)
        }
    }

    private func removePersisted(_ key: String) -> Bool {
        let existed = persisted.removeValue(forKey: key) != nil
        order.removeAll { $0 == key }
        return existed
    }

    private func deduplicate(_ list: [Hermesh_Session_V1_AttachmentRef]) -> [Hermesh_Session_V1_AttachmentRef] {
        var seen: Set<String> = []
        var out: [Hermesh_Session_V1_AttachmentRef] = []
        for a in list where !a.blobSha256.isEmpty && seen.insert(a.blobSha256).inserted {
            out.append(a)
        }
        return out
    }

    private func loadFromDefaults() {
        guard let raw = defaults.data(forKey: storageKey),
              let decoded = try? JSONDecoder().decode([StoredEntry].self, from: raw) else { return }
        for item in decoded where !item.key.isEmpty {
            let atts = item.attachments.compactMap { try? Hermesh_Session_V1_AttachmentRef(serializedBytes: $0) }
            let d = ComposerDraft(text: item.text, attachments: deduplicate(atts))
            guard !d.isEmpty else { continue }
            persisted[item.key] = d
            order.removeAll { $0 == item.key }
            order.append(item.key)
        }
        while order.count > maxEntries {
            let oldest = order.removeFirst()
            persisted.removeValue(forKey: oldest)
        }
    }

    private func flush() {
        guard persistent else { return }
        if order.isEmpty {
            defaults.removeObject(forKey: storageKey)
            return
        }
        let entries: [StoredEntry] = order.compactMap { k in
            guard let d = persisted[k], !d.isEmpty else { return nil }
            return StoredEntry(
                key: k,
                text: d.text,
                attachments: d.attachments.compactMap { try? $0.serializedData() }
            )
        }
        if let data = try? JSONEncoder().encode(entries) {
            defaults.set(data, forKey: storageKey)
        }
    }
}
