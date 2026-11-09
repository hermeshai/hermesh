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

/// An assistant's waiting messages as rows, oldest first, but those its conversation shows already (`said`: its
/// rows): delivered while the list was asked for again. A delivered message has the same words, sent at or after it
/// was written (less a minute, for the clocks).
public func herWaiting(_ list: [Hermesh_Local_V1_HerWaiting], botID: String, said: [HerItem]) -> [HerItem] {
    var left: [(text: String, ts: Double)] = said.compactMap {
        if case .user(let text, _, _, _) = $0.kind { return (text.trimmingCharacters(in: .whitespacesAndNewlines), $0.ts) } else { return nil }
    }
    var out: [HerItem] = []
    for w in list.filter({ $0.botID == botID }).sorted(by: { millis($0) < millis($1) }) {
        let ts = millis(w), text = w.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if let i = left.firstIndex(where: { $0.text == text && $0.ts >= ts - 60_000 }) {
            left.remove(at: i)
            continue
        }
        out.append(HerItem(key: "waiting:\(w.id)", ts: ts, kind: .waiting(id: w.id, text: w.text, files: w.localPaths.map(baseName), error: w.error)))
    }
    return out
}

private func millis(_ w: Hermesh_Local_V1_HerWaiting) -> Double {
    w.hasCreatedAt ? w.createdAt.date.timeIntervalSince1970 * 1000 : 0
}
