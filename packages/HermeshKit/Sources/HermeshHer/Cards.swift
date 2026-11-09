import Foundation

// Secure cards (docs/her-secure-cards.md §3, §4): what her made for the
// owner to answer, as send_to_user's result carries it, how each stands, and
// the answer that goes back (cards/answer). What the owner types goes into
// the answer and nowhere else.

public struct HerCard: Equatable, Sendable {
    public var id: String
    public var kind: String
    /// "her": nothing the owner said began the task that asks.
    public var askedBy: String
    public var expiresAt: Date?
    /// Console calls made when the owner agrees; a value goes where a string is "<value:FIELD_ID>".
    public var ops: JSON
    /// A `connect` card's: a connector added by its address, or signed in to again.
    public var connect: Connect?

    public struct Connect: Equatable, Sendable {
        public var name: String
        public var host: String
        /// Signing in again: no ops.
        public var reconnect: Bool
        /// A header's value for each, masked.
        public var fields: [Field]
    }

    public struct Field: Equatable, Sendable {
        public var id: String
        public var label: String
    }

    public init?(_ c: JSON) {
        guard let id = c["id"].string, let kind = c["kind"].string, c["expires_at"].string != nil, c["ops"].array != nil, c["body"].object != nil else { return nil }
        self.id = id
        self.kind = kind
        askedBy = c["asked_by"].string ?? "owner"
        expiresAt = c["expires_at"].string.flatMap(rfc3339)
        ops = c["ops"]
        let b = c["body"]
        if kind == "connect", let name = b["name"].string, let host = b["host"].string, let fields = b["fields"].array {
            connect = Connect(name: name, host: host, reconnect: b["reconnect"].bool ?? false,
                              fields: fields.compactMap { f in f["id"].string.map { Field(id: $0, label: f["label"].string ?? $0) } })
        }
    }

    /// The answer that approves: the ops with each "<value:ID>" filled with the owner's value for that field.
    public func approve(_ values: [String: String]) -> JSON {
        ["decision": "approve", "ops": herFilled(ops, values)]
    }

    public static let decline: JSON = ["decision": "decline"]
}

/// ops with each "<value:ID>" string filled with the owner's value for that field; the rest as it is.
public func herFilled(_ ops: JSON, _ values: [String: String]) -> JSON {
    switch ops {
    case .string(let s):
        if s.hasPrefix("<value:"), s.hasSuffix(">"), let v = values[String(s.dropFirst(7).dropLast())] { return .string(v) }
        return ops
    case .array(let a): return .array(a.map { herFilled($0, values) })
    case .object(let o): return .object(o.mapValues { herFilled($0, values) })
    default: return ops
    }
}

private let ended: Set<String> = ["done", "declined", "failed", "expired", "replaced"]

/// How a card stands now: as her last said it on this conversation (live), else as its history ended it, else as
/// her answered this device; open until then. One not ended by its expiry is expired.
public func herCardNow(_ card: HerCard, _ v: HerSession, answered: HerCardStatus?, now: Date) -> (status: HerCardStatus, live: Bool) {
    var s: (status: HerCardStatus, live: Bool)
    if let change = v.cards[card.id] {
        s = (change, true)
    } else {
        let resolved = v.entries.lazy.compactMap { e -> HerCardStatus? in
            if case .cardResolved(card.id, let outcome, let note) = e { return HerCardStatus(state: outcome, note: note) } else { return nil }
        }.first
        s = (resolved ?? answered ?? HerCardStatus(state: "open"), false)
    }
    if !ended.contains(s.status.state), let at = card.expiresAt, at <= now { s.status = HerCardStatus(state: "expired") }
    return s
}

/// What a card shows on this device: its form (with her's refusal after a retry, waiting while it connects), the
/// wait while the owner signs in here, or one line once it ended. `here`: this device answered it; one another
/// device answered shows it was handled there.
public enum HerCardShown: Equatable, Sendable {
    case form(note: String?, busy: Bool)
    case signIn
    /// "done", "declined", "failed", "expired", "replaced" or "elsewhere"; tools: how many the connector has.
    case ended(outcome: String, tools: Int?, note: String?)
}

public func herCardShown(_ s: (status: HerCardStatus, live: Bool), here: Bool) -> HerCardShown {
    switch s.status.state {
    case "open": .form(note: nil, busy: false)
    case "retry": .form(note: s.status.note, busy: false)
    case "working": here ? .form(note: nil, busy: true) : .ended(outcome: "elsewhere", tools: nil, note: nil)
    case "sign_in": here ? .signIn : .ended(outcome: "elsewhere", tools: nil, note: nil)
    case "done": .ended(outcome: "done", tools: herToolCount(s.status.note), note: s.status.note)
    // Ended while this device looked on, by another's answer.
    case "declined", "failed": s.live && !here ? .ended(outcome: "elsewhere", tools: nil, note: nil) : .ended(outcome: s.status.state, tools: nil, note: s.status.note)
    case "replaced": .ended(outcome: "replaced", tools: nil, note: nil)
    default: .ended(outcome: "expired", tools: nil, note: nil)
    }
}

/// The tools her listed once a connector connected, from its note ("Notion connected: 14 tools (…)").
public func herToolCount(_ note: String?) -> Int? {
    guard let note, let r = note.range(of: #"(\d+) tools?\b"#, options: .regularExpression) else { return nil }
    return Int(note[r].prefix { $0.isNumber })
}

/// A card's name for the connector: its first letter capital ("notion": Notion).
public func herConnectorName(_ name: String) -> String { name.prefix(1).uppercased() + name.dropFirst() }

func rfc3339(_ s: String) -> Date? {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: s) { return d }
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)
}
