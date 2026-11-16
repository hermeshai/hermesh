import Foundation

// What the environment's console manages (docs/her-server.md §10), as Her's own pages on the iPhone show it: her
// skills (skills/*), web search (web_search/*), the browsers (browser/status and `[browser]` of config/read), where
// her data is (storage/usage), and the model sign-ins (chatgpt/*, auth/*, models/*). The console's pages read the
// same answers (her/apps/console): what each row says follows them.

// MARK: Skills

/// A skill as skills/list gives it (her's SkillEntry).
public struct HerSkill: Equatable, Sendable, Identifiable {
    public enum Group: String, Sendable { case user, extra, system }
    /// Where her installed it from (`.her-source.json`).
    public enum Source: Equatable, Sendable { case system, catalog, upload, local, extra }

    public var id: String { dir }
    public var name: String
    public var description: String
    /// Its SKILL.md.
    public var path: String
    /// Its folder on the Her Server: what skills/set_enabled and skills/remove take.
    public var dir: String
    public var group: Group
    public var on: Bool
    /// In `~/.her/skills`, so skills/remove takes it.
    public var removable: Bool
    /// False when only the owner may call it (`disable-model-invocation`).
    public var modelInvocable: Bool
    public var argumentHint: String?
    public var source: Source
    /// Why it did not load, for a broken one.
    public var problem: String?

    init?(_ j: JSON) {
        guard let name = j["name"].string, let path = j["path"].string, let scope = j["scope"].string else { return nil }
        self.name = name
        self.path = path
        dir = herSkillDir(path)
        description = j["description"].string ?? ""
        let state = j["state"].string ?? "active"
        on = state != "disabled"
        removable = j["removable"].bool ?? false
        modelInvocable = j["model_invocable"].bool ?? true
        argumentHint = j["argument_hint"].text
        problem = state == "invalid" ? j["detail"].text : nil
        switch scope {
        case "system": group = .system; source = .system
        case "extra": group = .extra; source = .extra
        default:
            group = .user
            switch j["source"]["kind"].string {
            case "catalog": source = .catalog
            case "upload": source = .upload
            default: source = .local
            }
        }
    }

    /// What typing it in the conversation looks like.
    public var command: String { "/skill:\(name)" }
}

/// A skill's folder from its SKILL.md.
public func herSkillDir(_ path: String) -> String {
    for suffix in ["/SKILL.md", "\\SKILL.md", "/skill.md"] where path.lowercased().hasSuffix(suffix.lowercased()) {
        return String(path.dropLast(suffix.count))
    }
    return path
}

/// skills/list as the pages show it: the skills a conversation here would find, by group, those turned off
/// included; and those that did not load. A project's own skills are not the environment's, and a copy another one
/// shadows is not listed.
public struct HerSkills: Equatable, Sendable {
    public var enabled: Bool
    public var skills: [HerSkill]
    public var broken: [HerSkill]

    public static let none = HerSkills(enabled: true, skills: [], broken: [])

    public init(enabled: Bool, skills: [HerSkill], broken: [HerSkill]) {
        self.enabled = enabled
        self.skills = skills
        self.broken = broken
    }

    public init(_ j: JSON) {
        enabled = j["enabled"].bool ?? true
        let all = (j["skills"].array ?? []).filter { $0["scope"].string != "project" }
        skills = all.filter { $0["state"].string != "invalid" && $0["state"].string != "shadowed" }.compactMap(HerSkill.init)
        broken = all.filter { $0["state"].string == "invalid" }.compactMap(HerSkill.init)
    }

    public func group(_ g: HerSkill.Group) -> [HerSkill] { skills.filter { $0.group == g } }
    public var onCount: Int { skills.count(where: \.on) }
}

/// A file of a skill's folder (skills/files).
public struct HerSkillFile: Equatable, Sendable, Identifiable {
    public var id: String { path }
    /// Relative to the skill's folder.
    public var path: String
    public var dir: Bool
    public var size: Int
}

public func herSkillFiles(_ j: JSON) -> [HerSkillFile] {
    (j["files"].array ?? []).compactMap { f in
        f["path"].string.map { HerSkillFile(path: $0, dir: f["dir"].bool ?? false, size: f["size"].int ?? 0) }
    }
}

// MARK: Web search

/// A search service (her's WebSearchSource).
public struct HerSearchSource: Equatable, Sendable, Identifiable {
    /// What it needs before it can be used.
    public enum Needs: String, Sendable { case key, nothing, account }
    /// Where its key comes from.
    public enum Key: String, Sendable { case saved, env, account }

    public var id: String
    public var name: String
    /// It has what it needs.
    public var configured: Bool
    /// Its switch: searches skip it when off.
    public var enabled: Bool
    public var needs: Needs
    public var key: Key?
    public var keyEnv: String?
    public var keyPage: URL?
    /// Its last search, when that failed: why, and when.
    public var failure: (kind: String, at: Date)?

    public static func == (a: Self, b: Self) -> Bool {
        a.id == b.id && a.name == b.name && a.configured == b.configured && a.enabled == b.enabled && a.needs == b.needs && a.key == b.key
            && a.keyEnv == b.keyEnv && a.keyPage == b.keyPage && a.failure?.kind == b.failure?.kind && a.failure?.at == b.failure?.at
    }

    init?(_ j: JSON) {
        guard let id = j["id"].string else { return nil }
        self.id = id
        name = j["name"].string ?? id
        configured = j["configured"].bool ?? false
        enabled = j["enabled"].bool ?? true
        needs = j["needs"].string.flatMap(Needs.init) ?? .nothing
        key = j["key"].string.flatMap(Key.init)
        keyEnv = j["key_env"].text
        keyPage = j["key_url"].string.flatMap(URL.init(string:))
        failure = j["failure"]["kind"].string.map { ($0, Date(timeIntervalSince1970: (j["failure"]["at_ms"].number ?? 0) / 1000)) }
    }

    /// Its row offers to add a key rather than its switch: one that needs a key and has none.
    public var wantsKey: Bool { needs == .key && !configured }
    /// Its row says to connect an account under Models first.
    public var wantsAccount: Bool { needs == .account && !configured }
}

/// web_search/status: whether the model may search, the service it uses now, and the services in order.
public struct HerSearch: Equatable, Sendable {
    public var enabled: Bool
    public var current: String?
    public var sources: [HerSearchSource]

    public init(_ j: JSON) {
        enabled = j["enabled"].bool ?? false
        current = j["current"].text
        sources = (j["sources"].array ?? []).compactMap(HerSearchSource.init)
    }

    public func source(_ id: String?) -> HerSearchSource? { sources.first { $0.id == id } }

    /// Every service turned off: the model can't search.
    public var noneLeft: Bool { !sources.contains(where: \.enabled) }

    /// The writes that turn the model's searching on or off.
    public static func enabledEdits(_ on: Bool) -> JSON { [["key_path": "web_search.enabled", "value": .bool(on)]] }

    /// The writes that turn one service on or off: `web_search.disabled` lists those off.
    public func switchEdits(_ id: String, on: Bool) -> JSON {
        let off = sources.filter { $0.id == id ? !on : !$0.enabled }.map { JSON.string($0.id) }
        return [["key_path": "web_search.disabled", "value": .array(off)]]
    }

    /// The writes that put the services in `order`.
    public static func orderEdits(_ order: [String]) -> JSON { [["key_path": "web_search.order", "value": .array(order.map(JSON.string))]] }

    /// The services moved as a list's `onMove` moves them.
    public func moved(from: IndexSet, to: Int) -> [String] {
        var ids = sources.map(\.id)
        let moving = from.sorted().map { ids[$0] }
        let before = from.filter { $0 < to }.count
        for i in from.sorted(by: >) { ids.remove(at: i) }
        ids.insert(contentsOf: moving, at: to - before)
        return ids
    }
}

/// web_search/test: whether a search worked, with how many results in how long, or why not.
public struct HerSearchTest: Equatable, Sendable {
    public var ok: Bool
    public var count: Int
    public var seconds: Double
    public var error: String?

    public init(_ j: JSON) {
        ok = j["ok"].bool ?? false
        count = j["count"].int ?? 0
        seconds = (j["elapsed_ms"].number ?? 0) / 1000
        error = j["error"].text
    }
}

// MARK: Browser

/// How a site may be used: opened and acted on, only opened, asked about, or never.
public enum HerSiteLevel: String, Sendable, CaseIterable { case act, navigate, ask, deny }

/// What the Browser page shows (browser/status and `[browser]` of config/read): whether Her controls browsers,
/// where new pages open, the owner's own Chrome, the built-in browser, and the sites.
public struct HerBrowser: Equatable, Sendable {
    /// The owner's Chrome: not connected and not used; connected, not used yet; used; used but not connected now;
    /// connected but too old on one side.
    public enum Yours: String, Sendable { case off, ready, on, lost, outdated }

    /// `browser.enabled` as set, nil when not.
    public var control: Bool?
    /// `browser.default` is "yours".
    public var prefersYours: Bool
    /// `browser.extension`, nil when not set.
    public var extensionUsed: Bool?
    /// A desktop app shows embedded browsers there.
    public var embedded: Bool
    /// The built-in browser shows a window.
    public var window: Bool
    /// "macos" or "linux".
    public var os: String
    /// The browser the built-in browser starts, when there is one.
    public var program: String?
    /// The connected Chrome, as it named itself ("Google Chrome 141.0.7390.54").
    public var chrome: String?
    public var yours: Yours
    /// "All sites": ask, navigate or act.
    public var allSites: HerSiteLevel
    /// The sites set apart from "All sites", by origin.
    public var exceptions: [(origin: String, level: HerSiteLevel)]

    public static func == (a: Self, b: Self) -> Bool {
        a.control == b.control && a.prefersYours == b.prefersYours && a.extensionUsed == b.extensionUsed && a.embedded == b.embedded && a.window == b.window
            && a.os == b.os && a.program == b.program && a.chrome == b.chrome && a.yours == b.yours && a.allSites == b.allSites
            && a.exceptions.map(\.origin) == b.exceptions.map(\.origin) && a.exceptions.map(\.level) == b.exceptions.map(\.level)
    }

    /// From browser/status and config/read's `config`.
    public init(status: JSON, config: JSON) {
        let b = config["browser"]
        control = b["enabled"].bool
        prefersYours = b["default"].string == "yours"
        extensionUsed = b["extension"].bool ?? (b["backend"].string == "extension" ? true : nil)
        embedded = status["embedded"].bool ?? false
        window = status["window"].bool ?? false
        os = status["os"].string == "linux" ? "linux" : "macos"
        program = status["program"].text
        let ext = status["extension"]
        // "update_extension" or "update_her": connected, but one side must be updated before it can be used.
        let problem = ext["problem"].string != nil
        let connected = ext.object != nil && !problem
        chrome = connected ? ext["browser"].text : nil
        let used = extensionUsed == true
        yours = problem ? .outdated : connected ? (used ? .on : .ready) : used ? .lost : .off
        let sites = (b["sites"].object ?? [:]).compactMapValues { $0.string.flatMap(HerSiteLevel.init) }
        let all = sites["*"]
        allSites = all == .navigate || all == .act ? all! : .ask
        exceptions = sites.filter { $0.key != "*" }.sorted { $0.key < $1.key }.map { ($0.key, $0.value) }
    }

    /// Whether Her controls browsers: as set, else where a desktop app shows them or the owner's Chrome is used.
    public var controlling: Bool { control ?? (embedded || extensionUsed == true) }
    /// Where new pages open: the owner's Chrome only while Her uses it and it is connected.
    public var opensInYours: Bool { prefersYours && yours == .on }

    public static func controlEdits(_ on: Bool) -> JSON { [["key_path": "browser.enabled", "value": .bool(on)]] }
    public static func defaultEdits(yours: Bool) -> JSON { [["key_path": "browser.default", "value": .string(yours ? "yours" : "builtin")]] }
    /// "All sites" set: asking is no `"*"` at all.
    public static func allSitesEdits(_ level: HerSiteLevel) -> JSON {
        [["key_path": .string(siteKey("*")), "value": level == .ask ? .null : .string(level.rawValue)]]
    }
    /// A site set apart, or (nil) taken out.
    public static func siteEdits(_ origin: String, _ level: HerSiteLevel?) -> JSON {
        [["key_path": .string(siteKey(origin)), "value": level.map { .string($0.rawValue) } ?? .null]]
    }
    /// A site as `browser.sites` keys it: an origin is quoted, as it has dots and a colon.
    public static func siteKey(_ origin: String) -> String {
        let quoted = String(data: (try? JSONSerialization.data(withJSONObject: [origin], options: [.withoutEscapingSlashes])) ?? Data(), encoding: .utf8)?
            .dropFirst().dropLast() ?? Substring("\"\(origin)\"")
        return "browser.sites.\(quoted)"
    }
}

/// A site as the owner types it, as her keeps it: the origin of an http(s) address, a bare host being https. Nil
/// when it is none.
public func herSiteOrigin(_ text: String) -> String? {
    let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !t.isEmpty else { return nil }
    let full = t.range(of: "^[a-zA-Z][a-zA-Z0-9+.-]*://", options: .regularExpression) != nil ? t : "https://" + t
    guard let u = URLComponents(string: full), let scheme = u.scheme?.lowercased(), scheme == "https" || scheme == "http",
          let host = u.host?.lowercased(), !host.isEmpty, u.user == nil, u.password == nil else { return nil }
    let port = u.port.map { (scheme == "https" && $0 == 443) || (scheme == "http" && $0 == 80) ? "" : ":\($0)" } ?? ""
    return "\(scheme)://\(host)\(port)"
}

// MARK: Storage

/// Where her data is on the Her Server, and how much of each kind (storage/usage). Only to read.
public struct HerStorage: Equatable, Sendable {
    public struct Part: Equatable, Sendable, Identifiable {
        public var id: String { kind }
        /// files, sent, conversations, trash, memory, browser, cache or other.
        public var kind: String
        public var bytes: Int64
    }

    public var home: String
    /// HER_HOME put it somewhere other than `~/.her`.
    public var custom: Bool
    public var total: Int64
    public var parts: [Part]

    public init(_ j: JSON) {
        home = j["home"].string ?? ""
        custom = j["custom"].bool ?? false
        total = Int64(j["total"].number ?? 0)
        parts = (j["parts"].array ?? []).compactMap { p in p["kind"].string.map { Part(kind: $0, bytes: Int64(p["bytes"].number ?? 0)) } }
    }

    /// The parts by size, the largest first: a bar's segments and the list under it.
    public var bySize: [Part] { parts.sorted { $0.bytes > $1.bytes } }
}

// MARK: Models

/// The ChatGPT subscription's sign-in (chatgpt/status).
public struct HerChatGPT: Equatable, Sendable {
    /// idle, pending, connected, failed or cancelled: the sign-in.
    public var state: String
    public var account: String?
    /// The connection her keeps, when there is one: ready, needs_login…
    public var connection: String?
    /// The provider its models are under.
    public var provider: String?

    public init(_ j: JSON) {
        state = j["state"].string ?? "idle"
        account = j["account"].text
        connection = j["connection"]["state"].string
        provider = j["connection"]["provider_id"].string
    }

    public var ready: Bool { connection == "ready" }
    /// Signed in once, and the sign-in no longer works.
    public var expired: Bool { connection == "needs_login" }
    public var pending: Bool { state == "pending" }
}

/// A provider as the Models page lists it (her's ProviderSummary).
public struct HerProviderRow: Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var names: [String: String]
    /// api_key, connection, command or none.
    public var auth: String
    /// stored, env, config, command, connection, not_required or missing.
    public var credential: String
    /// The variable the key comes from, when `credential` is env.
    public var credentialSource: String?
    public var plan: Bool
    public var keyPage: URL?
    /// Its models in models/list, and those in the model menu; nil while it has no credentials.
    public var models: Int?
    public var shown: Int?
    /// A refresh of its list runs, or the last one failed.
    public var refreshing: Bool
    public var listingError: String?
    public var fetchedAt: Date?
    /// Where its list came from: provider, catalog, unsupported or config.
    public var listingSource: String?
    /// Its Token Plan's list can be asked for again.
    public var dynamicModels: Bool

    init?(_ p: JSON) {
        guard let id = p["id"].string else { return nil }
        self.id = id
        name = p["name"].string ?? id
        names = (p["names"].object ?? [:]).compactMapValues(\.string)
        auth = p["auth"].string ?? ""
        credential = p["credential"].string ?? "missing"
        credentialSource = p["credential_source"].text
        plan = !p["plan"].isNull && p["plan"].object != nil
        keyPage = p["plan"]["key_url"].string.flatMap(URL.init(string:))
        dynamicModels = p["plan"]["dynamic_models"].bool ?? false
        let l = p["listing"]
        models = l["models"].int
        shown = l["shown"].int
        refreshing = l["refreshing"].bool ?? false
        listingError = l["error"]["message"].text
        fetchedAt = l["fetched_at"].string.flatMap { try? Date($0, strategy: .iso8601) }
        listingSource = l["source"].string
    }

    public func name(language: String) -> String {
        names[language] ?? names[String(language.prefix { $0 != "-" })] ?? name
    }

    /// Its key is kept in her's credential store, so it can be removed or replaced here.
    public var removable: Bool { credential == "stored" }
}

/// The order the Token Plans are offered in; one not named here comes after them, by name.
private let herPlanOrder = ["bailian-token-plan", "tencent-token-plan", "volcengine-agent-plan", "volcengine-coding-plan", "zai-coding-plan", "zhipu-coding-plan"]

/// models/list's providers as the Models page groups them.
public struct HerProviders: Equatable, Sendable {
    public var all: [HerProviderRow]

    public init(_ j: JSON) { all = (j["providers"].array ?? []).compactMap(HerProviderRow.init) }

    private static func planRank(_ p: HerProviderRow) -> Int { herPlanOrder.firstIndex(of: p.id) ?? herPlanOrder.count }

    /// The Token Plans, in their order.
    public var plans: [HerProviderRow] {
        all.filter(\.plan).sorted { (Self.planRank($0), $0.name) < (Self.planRank($1), $1.name) }
    }
    /// The Token Plans with a key.
    public var addedPlans: [HerProviderRow] { plans.filter { $0.credential != "missing" } }
    /// The Token Plans without one, to connect.
    public var plansLeft: [HerProviderRow] { plans.filter { $0.credential == "missing" } }
    /// Providers with a key from somewhere; keyless ones need nothing.
    public var keyed: [HerProviderRow] { all.filter { !$0.plan && $0.auth == "api_key" && $0.credential != "missing" } }
    /// Providers that take a key and have none yet: what adding one offers.
    public var keyChoices: [HerProviderRow] {
        all.filter { !$0.plan && $0.auth == "api_key" && $0.credential == "missing" && $0.id != "deepseek-anthropic" }
    }
}

/// A model as a provider's list shows it, with whether it is in the model menu.
public func herProviderModels(_ j: JSON, provider: String) -> [HerModel] {
    HerModels(j).models.filter { $0.provider == provider }
}

// MARK: Calls

/// The calls the console's pages make here, through a function that calls her.
public struct HerAbilityCalls: Sendable {
    public var call: @Sendable (_ method: String, _ params: JSON) async throws -> JSON

    public init(call: @escaping @Sendable (_ method: String, _ params: JSON) async throws -> JSON) { self.call = call }

    public func skills() async throws -> HerSkills { HerSkills(try await call("skills/list", [:])) }
    public func setSkill(_ s: HerSkill, on: Bool) async throws { _ = try await call("skills/set_enabled", ["path": .string(s.dir), "enabled": .bool(on)]) }
    public func removeSkill(_ s: HerSkill) async throws { _ = try await call("skills/remove", ["path": .string(s.dir)]) }
    public func skillFiles(_ s: HerSkill) async throws -> [HerSkillFile] { herSkillFiles(try await call("skills/files", ["path": .string(s.dir)])) }
    /// A file of a skill as text, or nil when it is not text.
    public func skillFile(_ s: HerSkill, _ file: String) async throws -> String? {
        let j = try await call("skills/read", ["path": .string(s.dir), "file": .string(file)])
        return j["binary"].bool == true ? nil : j["text"].string
    }
    /// Checks an uploaded skill (`dryRun`) or installs it; what it found, where it goes, and a skill of the same name there.
    public func installSkill(from path: String, dryRun: Bool, replace: Bool = false) async throws -> (skill: HerSkill?, target: String, conflict: String?) {
        let j = try await call("skills/install", ["from": .string(path), "dry_run": .bool(dryRun), "replace": .bool(replace)])
        return (HerSkill(j["skill"]), j["target"].string ?? "", j["conflict"].text)
    }

    public func search() async throws -> HerSearch { HerSearch(try await call("web_search/status", [:])) }
    public func testSearch(_ id: String) async throws -> HerSearchTest { HerSearchTest(try await call("web_search/test", ["source": .string(id)])) }
    /// Saves a key for a search service; whether her took it, and its test.
    public func setSearchKey(_ id: String, key: String) async throws -> (saved: Bool, test: HerSearchTest) {
        let j = try await call("web_search/set_key", ["source": .string(id), "key": .string(key.trimmingCharacters(in: .whitespacesAndNewlines))])
        return (j["saved"].bool ?? false, HerSearchTest(j["test"]))
    }
    public func removeSearchKey(_ id: String) async throws { _ = try await call("credentials/delete", ["name": .string("web-search.\(id)")]) }

    public func browser() async throws -> HerBrowser {
        async let status = call("browser/status", [:])
        async let config = call("config/read", [:])
        return HerBrowser(status: try await status, config: try await config["config"])
    }

    public func storage() async throws -> HerStorage { HerStorage(try await call("storage/usage", [:])) }

    /// Writes to her's user file.
    public func write(_ edits: JSON) async throws { _ = try await call("config/write", ["layer": "user", "edits": edits]) }

    public func chatGPT() async throws -> HerChatGPT { HerChatGPT(try await call("chatgpt/status", [:])) }
    /// Forgets the ChatGPT account: its connection goes.
    public func forgetChatGPT() async throws -> HerChatGPT {
        _ = try await call("connections/remove", ["connection_id": "chatgpt-api"])
        return try await chatGPT()
    }
    public func providers() async throws -> (HerProviders, HerModels) {
        let j = try await call("models/list", [:])
        return (HerProviders(j), HerModels(j))
    }
    /// Saves a provider's key; whether her could check it with the provider.
    public func setKey(_ provider: String, key: String) async throws -> Bool {
        let j = try await call("auth/set", ["provider": .string(provider), "api_key": .string(key.trimmingCharacters(in: .whitespacesAndNewlines))])
        return j["verified"].bool ?? false
    }
    public func removeKey(_ provider: String) async throws { _ = try await call("auth/delete", ["provider": .string(provider)]) }
    public func refreshModels(_ provider: String) async throws { _ = try await call("models/refresh", ["provider": .string(provider)]) }
    public func showModel(_ id: String, shown: Bool) async throws { _ = try await call("models/show", ["model": .string(id), "shown": .bool(shown)]) }
}
