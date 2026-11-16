import Foundation

// The files Her sent, in a list of their own (docs/her-deliverables.md §3.1, §4.3): what the iPhone reads of
// deliveries/list. her answers the pages; here they are joined, grouped by when each was last sent, marked new, and
// turned into what to read and the quote a reply carries. her-client's deliveries.ts does the same, and
// both run the cases in packages/her-client/testdata/deliveries.json.

/// Where a file came from: a routine's report or a coding session's news Her passed on.
public struct HerDeliverySource: Equatable, Hashable, Sendable {
    public enum Kind: String, Sendable { case routine, session }
    public var kind: Kind
    public var id: String
    public var name: String
    /// The routine's run it came from, when known.
    public var run: String?

    init?(_ j: JSON) {
        guard let kind = j["kind"].string.flatMap(Kind.init), let id = j["id"].string else { return nil }
        self.kind = kind
        self.id = id
        name = j["name"].string ?? ""
        run = j["run"].string
    }
}

/// Which source's files the list shows (deliveries/list's `source`): what the owner asked for in the conversation, a
/// routine's by its id, or coding sessions' by their name.
public enum HerSourceFilter: Hashable, Sendable {
    case conversation
    case routine(id: String)
    case session(name: String)

    init?(_ j: JSON) {
        switch j["kind"].string {
        case "conversation": self = .conversation
        case "routine": guard let id = j["id"].string else { return nil }; self = .routine(id: id)
        case "session": guard let name = j["name"].string else { return nil }; self = .session(name: name)
        default: return nil
        }
    }

    /// As deliveries/list takes it.
    public var json: JSON {
        switch self {
        case .conversation: ["kind": "conversation"]
        case .routine(let id): ["kind": "routine", "id": .string(id)]
        case .session(let name): ["kind": "session", "name": .string(name)]
        }
    }
}

/// A source of Her's files, as deliveries/sources lists it: those whose latest send came from it.
public struct HerDeliverySourceCount: Equatable, Sendable, Identifiable {
    public var filter: HerSourceFilter
    /// A routine's name now, or a session's; nil for the conversation.
    public var name: String?
    public var files: Int
    public var media: Int
    public var latest: Int
    /// A deleted routine: the files it sent stay.
    public var gone: Bool
    public var id: HerSourceFilter { filter }

    init?(_ j: JSON) {
        guard let filter = HerSourceFilter(j["filter"]) else { return nil }
        self.filter = filter
        name = j["name"].string
        files = j["files"].int ?? 0
        media = j["media"].int ?? 0
        latest = j["latest"].int ?? 0
        gone = j["gone"].bool ?? false
    }

    /// How many of its files a tab shows: all (nil), only media, or only documents.
    public func count(media only: Bool?) -> Int {
        guard let only else { return files }
        return only ? media : files - media
    }
}

/// deliveries/sources: where Her's files came from, the latest first, and how many routines sent none.
public struct HerDeliverySources: Equatable, Sendable {
    public var sources: [HerDeliverySourceCount]
    public var silent: Int

    public init?(_ j: JSON) {
        guard let sources = j["sources"].array else { return nil }
        self.sources = sources.compactMap(HerDeliverySourceCount.init)
        silent = j["silent"].int ?? 0
    }

    /// The source a filter names, when it is listed.
    public func source(_ filter: HerSourceFilter) -> HerDeliverySourceCount? { sources.first { $0.filter == filter } }

    /// As the filter lists them: each kind apart, the latest first; a search keeps those with it in their name.
    public func groups(query: String) -> (conversation: HerDeliverySourceCount?, routines: [HerDeliverySourceCount], sessions: [HerDeliverySourceCount]) {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        let kept = sources.filter { q.isEmpty || ($0.name ?? "").lowercased().contains(q) }
        return (
            kept.first { $0.filter == .conversation },
            kept.filter { if case .routine = $0.filter { true } else { false } },
            kept.filter { if case .session = $0.filter { true } else { false } }
        )
    }
}

/// One send of a file.
public struct HerDelivery: Equatable, Sendable, Identifiable {
    public var seq: Int
    public var sessionID: String
    /// The send_to_user call that sent it: its message in the conversation.
    public var callID: String
    public var size: Int
    public var width: Int?
    public var height: Int?
    public var sha256: String?
    /// The copy her kept of it as it was sent; nil when none was kept.
    public var snapshot: String?
    /// Her's words with it.
    public var text: String?
    public var sentAt: Date
    public var source: HerDeliverySource?
    public var id: Int { seq }

    init?(_ j: JSON) {
        guard let seq = j["seq"].int, let call = j["call_id"].string else { return nil }
        self.seq = seq
        sessionID = j["session_id"].string ?? ""
        callID = call
        size = j["size"].int ?? 0
        width = j["width"].int
        height = j["height"].int
        sha256 = j["sha256"].text
        snapshot = j["snapshot"].text
        text = j["text"].text
        sentAt = j["sent_at"].string.flatMap(herDate) ?? .distantPast
        source = HerDeliverySource(j["source"])
    }
}

/// A file Her sent: her sends from one path, newest first, each a different content.
public struct HerDeliveredFile: Equatable, Sendable, Identifiable {
    public enum Kind: String, Sendable {
        case doc, sheet, slide, image, video, audio, code, other

        /// Pictures, video and audio: what the list shows apart from documents (deliveries/list's `media`).
        public var isMedia: Bool { self == .image || self == .video || self == .audio }
    }
    public var path: String
    public var name: String
    public var kind: Kind
    public var versions: [HerDelivery]
    public var id: String { path }
    public var latest: HerDelivery? { versions.first }

    init?(_ j: JSON) {
        guard let path = j["path"].string, let versions = j["versions"].array?.compactMap(HerDelivery.init), !versions.isEmpty else { return nil }
        self.path = path
        name = j["name"].string ?? baseName(path)
        kind = j["kind"].string.flatMap(Kind.init) ?? .other
        self.versions = versions
    }

    /// Sent since the owner last looked at the list.
    public func isNew(seen: Int) -> Bool { (latest?.seq ?? 0) > seen }

    /// It came from that source: its latest send did (for the page kept offline).
    public func from(_ filter: HerSourceFilter) -> Bool {
        let s = latest?.source
        switch filter {
        case .conversation: return s == nil
        case .routine(let id): return s?.kind == .routine && s?.id == id
        case .session(let name): return s?.kind == .session && s?.name == name
        }
    }

    /// What to read of a version: its copy, as it was sent, when there is one; else where the file is.
    public func file(_ v: HerDelivery) -> HerFile {
        HerFile(path: path, name: name, size: v.size, image: kind == .image, snapshot: v.snapshot)
    }

    /// The quote a reply to a version carries: its message, quoted in part as the file's name and Her's words.
    public func quote(_ v: HerDelivery) -> HerReply {
        HerReply(ref: v.callID, own: false, text: v.text.map { "\(name)\n\($0)" } ?? name, partial: true)
    }
}

/// A page of deliveries/list, or pages joined.
public struct HerDeliveries: Equatable, Sendable {
    /// Newest first, by their latest send.
    public var files: [HerDeliveredFile]
    /// The last send the owner saw: a later one is new.
    public var seen: Int
    /// The `before` of the next page, when there is one.
    public var next: Int?

    public init(files: [HerDeliveredFile] = [], seen: Int = 0, next: Int? = nil) {
        self.files = files
        self.seen = seen
        self.next = next
    }

    public init?(_ j: JSON) {
        guard let files = j["files"].array else { return nil }
        self.files = files.compactMap(HerDeliveredFile.init)
        seen = j["seen"].int ?? 0
        next = j["next"].int
    }

    /// Only media (true) or only documents (false), or all (nil): what a tab shows, whatever her answered (one older
    /// than the `media` filter answers all of them).
    public func only(media: Bool?) -> HerDeliveries {
        guard let media else { return self }
        return HerDeliveries(files: files.filter { $0.kind.isMedia == media }, seen: seen, next: next)
    }

    /// The next page after this, in order: a file on both (sent again between them) stays where it was first.
    public func joined(_ page: HerDeliveries) -> HerDeliveries {
        let have = Set(files.map(\.path))
        return HerDeliveries(files: files + page.files.filter { !have.contains($0.path) }, seen: max(seen, page.seen), next: page.next)
    }
}

/// When a file was last sent, as the list groups it.
public enum HerDeliveryDay: String, Sendable { case today, yesterday, week, earlier }

/// A group of the list: the day its files were last sent.
public struct HerDeliveryGroup: Equatable, Sendable, Identifiable {
    public var day: HerDeliveryDay
    public var files: [HerDeliveredFile]
    public var id: String { day.rawValue }
}

/// The day of `sent` from the day of `now`, in `zone`.
public func herDeliveryDay(_ sent: Date, now: Date, zone: TimeZone) -> HerDeliveryDay {
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = zone
    let ago = calendar.dateComponents([.day], from: calendar.startOfDay(for: sent), to: calendar.startOfDay(for: now)).day ?? 0
    return ago <= 0 ? .today : ago == 1 ? .yesterday : ago < 7 ? .week : .earlier
}

/// The files by the day each was last sent, each group where its first file is (the files come newest first).
public func herDeliveryGroups(_ files: [HerDeliveredFile], now: Date, zone: TimeZone) -> [HerDeliveryGroup] {
    var groups: [HerDeliveryGroup] = []
    for f in files {
        let day = herDeliveryDay(f.latest?.sentAt ?? .distantPast, now: now, zone: zone)
        if let i = groups.firstIndex(where: { $0.day == day }) {
            groups[i].files.append(f)
        } else {
            groups.append(HerDeliveryGroup(day: day, files: [f]))
        }
    }
    return groups
}

/// An RFC 3339 moment, with or without fractions of a second.
func herDate(_ text: String) -> Date? {
    let f = ISO8601DateFormatter()
    if let d = f.date(from: text) { return d }
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return f.date(from: text)
}
