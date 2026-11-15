import Foundation
import HermeshProto

// What this iPhone keeps of an environment's Her (docs/her-server.md §5.5,
// §7): Her and the end of her conversation as the iPhone last followed it
// (GetHerConversation), shown until her answers and, read-only, while the
// machine is offline; and the messages written meanwhile, kept here until
// the machine is back (HerSend, ListHerWaiting), shown after what was said.

/// Her and her conversation as this device keeps them.
public struct HerKept: Equatable, Sendable {
    public var bot: HerBot
    public var session: HerSession?
}

/// What hermeshd keeps, or nil for nothing.
public func herKept(_ r: Hermesh_Local_V1_GetHerConversationResponse) -> HerKept? {
    guard let j = try? JSON.parse(r.bot), let bot = HerBot(j) else { return nil }
    return HerKept(bot: bot, session: (try? JSON.parse(r.snapshot)).flatMap { HerSession(snapshot: $0) })
}

/// A message this app sent that HerSend has not answered for yet, or refused (docs/her-sending.md §5.2).
public struct HerOutgoing: Equatable, Sendable {
    /// Its id, the outbox's and her's.
    public var id: String
    public var text: String
    public var images: [HerImage]
    /// Its files' names.
    public var files: [String]
    /// When it was sent (ms).
    public var ts: Double
    /// Why it was not sent: nil while it is being sent.
    public var refused: String?

    public init(id: String, text: String, images: [HerImage] = [], files: [String] = [], ts: Double, refused: String? = nil) {
        self.id = id
        self.text = text
        self.images = images
        self.files = files
        self.ts = ts
        self.refused = refused
    }
}

/// What an assistant was sent that its conversation (`said`: its rows, its queue among them) doesn't show yet, as
/// rows after it: the messages kept in the outbox (`waiting`), oldest first, then those this app is sending, or
/// couldn't (`sending`). A message is shown once, in the conversation, the outbox or here, and keeps its id as its row's
/// key from one to the next. It is in the conversation when a message there has its id; one that her gave an id of her
/// own (an older her) when a message there has the same words, sent at or after it was written (less a minute, for
/// the clocks).
public func herOutbox(sending: [HerOutgoing] = [], waiting list: [Hermesh_Local_V1_HerWaiting], botID: String, said: [HerItem]) -> [HerItem] {
    let ids = Set(said.map(\.key))
    var left: [(text: String, ts: Double)] = said.compactMap {
        guard !$0.key.hasPrefix("hw_"), case .user(let text, _, _, _) = $0.kind else { return nil }
        return (text.trimmingCharacters(in: .whitespacesAndNewlines), $0.ts)
    }
    func shown(_ id: String, _ text: String, _ ts: Double) -> Bool {
        if ids.contains(id) { return true }
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let i = left.firstIndex(where: { $0.text == text && $0.ts >= ts - 60_000 }) else { return false }
        left.remove(at: i)
        return true
    }
    var out: [HerItem] = []
    let kept = list.filter { $0.botID == botID }.sorted { millis($0) < millis($1) }
    for w in kept where !shown(w.id, w.text, millis(w)) {
        out.append(HerItem(key: w.id, ts: millis(w), kind: .waiting(id: w.id, text: w.text, files: w.localPaths.map(baseName), error: w.error)))
    }
    let inOutbox = Set(kept.map(\.id))
    for m in sending.sorted(by: { $0.ts < $1.ts }) where !inOutbox.contains(m.id) && !shown(m.id, m.text, m.ts) {
        out.append(HerItem(key: m.id, ts: m.ts, kind: .sending(id: m.id, text: m.text, images: m.images, files: m.files, refused: m.refused)))
    }
    return out
}

private func millis(_ w: Hermesh_Local_V1_HerWaiting) -> Double {
    w.hasCreatedAt ? w.createdAt.date.timeIntervalSince1970 * 1000 : 0
}
