import Foundation
import HermeshProto
import Testing
@testable import HermeshSessions

private func makeAttachment(_ ch: String, bytes: UInt64 = 1024) -> Hermesh_Session_V1_AttachmentRef {
    .with {
        $0.blobSha256 = String(repeating: ch, count: 64)
        $0.name = "shot-\(ch).png"
        $0.mimeType = "image/png"
        $0.sizeBytes = bytes
    }
}

private func makeSuite() -> (UserDefaults, String) {
    let name = "ComposerDraftsTests.\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: name)!
    defaults.removePersistentDomain(forName: name)
    return (defaults, name)
}

@Test func composerDraftsIsolateTargetsAndPersistAcrossRestarts() {
    let (defaults, suite) = makeSuite()
    defer { defaults.removePersistentDomain(forName: suite) }

    let s1Key = ComposerDraftStore.key(sessionID: "s1")
    let s2Key = ComposerDraftStore.key(sessionID: "s2")
    let store1 = ComposerDraftStore(persistent: true, defaults: defaults)

    #expect(store1.draft(for: ComposerDraftStore.newSessionKey) == .empty)
    #expect(store1.draft(for: s1Key) == .empty)

    store1.setText("  new idea\nline 2 ", for: ComposerDraftStore.newSessionKey)
    store1.setText("reply in s1", for: s1Key)
    store1.addAttachment(makeAttachment("1", bytes: 4096), for: s1Key)
    store1.setText("reply in s2", for: s2Key)

    #expect(store1.draft(for: ComposerDraftStore.newSessionKey).text == "  new idea\nline 2 ")
    #expect(store1.draft(for: s1Key).text == "reply in s1")
    #expect(store1.draft(for: s1Key).attachments.count == 1)
    #expect(store1.draft(for: s2Key).text == "reply in s2")

    // Simulate app restart over the same UserDefaults.
    let store2 = ComposerDraftStore(persistent: true, defaults: defaults)
    #expect(store2.draft(for: ComposerDraftStore.newSessionKey).text == "  new idea\nline 2 ")
    #expect(store2.draft(for: s1Key).text == "reply in s1")
    #expect(store2.draft(for: s1Key).attachments.first?.sizeBytes == 4096)
    #expect(store2.draft(for: s2Key).text == "reply in s2")
}

@Test func composerDraftsIgnoreWhitespacePersistenceAndKeepAttachmentsOnly() {
    let (defaults, suite) = makeSuite()
    defer { defaults.removePersistentDomain(forName: suite) }

    let s1Key = ComposerDraftStore.key(sessionID: "s1")
    let store = ComposerDraftStore(persistent: true, defaults: defaults)

    store.setText("   ", for: s1Key)
    #expect(store.draft(for: s1Key).text == "   ")
    #expect(defaults.data(forKey: ComposerDraftStore.defaultStorageKey) == nil)
    #expect(ComposerDraftStore(persistent: true, defaults: defaults).draft(for: s1Key) == .empty)

    let pic = makeAttachment("a", bytes: 512)
    store.clear(s1Key)
    store.addAttachment(pic, for: s1Key)
    store.addAttachment(pic, for: s1Key)
    #expect(store.draft(for: s1Key).attachments.count == 1)

    let restarted = ComposerDraftStore(persistent: true, defaults: defaults)
    #expect(restarted.draft(for: s1Key).text == "")
    #expect(restarted.draft(for: s1Key).attachments == [pic])

    restarted.removeAttachment(sha256: pic.blobSha256, for: s1Key)
    #expect(restarted.draft(for: s1Key) == .empty)
    #expect(defaults.data(forKey: ComposerDraftStore.defaultStorageKey) == nil)
}

@Test func composerDraftsEvictLeastRecentlyUpdatedAndRestoreFailedSends() {
    let (defaults, suite) = makeSuite()
    defer { defaults.removePersistentDomain(forName: suite) }

    let store = ComposerDraftStore(persistent: true, defaults: defaults, maxEntries: 3)
    let k1 = ComposerDraftStore.key(sessionID: "s1")
    let k2 = ComposerDraftStore.key(sessionID: "s2")
    let k3 = ComposerDraftStore.key(sessionID: "s3")
    let k4 = ComposerDraftStore.key(sessionID: "s4")

    store.setText("one", for: k1)
    store.setText("two", for: k2)
    store.setText("three", for: k3)
    store.setText("one updated", for: k1)
    store.setText("four", for: k4)

    #expect(store.draft(for: k2) == .empty)
    #expect(store.draft(for: k1).text == "one updated")
    #expect(store.draft(for: k3).text == "three")
    #expect(store.draft(for: k4).text == "four")

    // Restore failed send into k2 (making [k1, k4, k2] with maxEntries=3) while k4 stays untouched, then merge with newer typing.
    store.restore(ComposerDraft(text: "failed msg", attachments: [makeAttachment("1")]), for: k2)
    #expect(store.draft(for: k2).text == "failed msg")
    #expect(store.draft(for: k4).text == "four")

    store.save(ComposerDraft(text: "typed later", attachments: [makeAttachment("1"), makeAttachment("2")]), for: k2)
    store.restore(ComposerDraft(text: "earlier failed", attachments: [makeAttachment("1")]), for: k2)
    #expect(store.draft(for: k2).text == "earlier failed\ntyped later")
    #expect(store.draft(for: k2).attachments.count == 2)

    store.move(from: k2, to: ComposerDraftStore.key(sessionID: "s-moved"))
    #expect(store.draft(for: k2) == .empty)
    #expect(store.draft(for: ComposerDraftStore.key(sessionID: "s-moved")).text == "earlier failed\ntyped later")

    store.forget(sessionID: "s-moved")
    #expect(store.draft(for: ComposerDraftStore.key(sessionID: "s-moved")) == .empty)

    store.forgetAll()
    #expect(store.draft(for: k4) == .empty)
    #expect(defaults.data(forKey: ComposerDraftStore.defaultStorageKey) == nil)
}
