import Foundation
import HermeshProto

// Her's environments as the iPhone shows them (docs/her-server.md §9): the
// machines Her is on for, each as its Machine.her last said, the machines
// the first run offers, and what an environment's connection follows: its
// assistants (bots/subscribe, bots/changed) and models (models/list). Her is
// the environment's default assistant; others her may hold are left alone.

public typealias HerMachine = Hermesh_Local_V1_Machine

/// An assistant as bots/subscribe lists it (her's BotSummary).
public struct HerBot: Equatable, Sendable {
    public var id: String
    public var name: String
    /// The assistant every environment has: Her.
    public var isDefault: Bool
    /// Messages and approvals since the owner last looked (bots/read).
    public var unread: Int
    public var muted: Bool
    /// Its conversation, once there is one: a cleared conversation is a new one.
    public var sessionID: String?

    public init?(_ j: JSON) {
        guard let id = j["id"].string else { return nil }
        self.id = id
        name = j["name"].string ?? ""
        isDefault = j["default"].bool ?? false
        unread = j["unread"].int ?? 0
        muted = j["muted"].bool ?? false
        sessionID = j["session_id"].string
    }
}

/// The list with bots/changed applied: each bot replaces its earlier row or joins; deleted ones leave.
public func mergeBots(_ list: [HerBot], _ changed: JSON) -> [HerBot] {
    let deleted = Set((changed["deleted"].array ?? []).compactMap(\.string))
    let new = (changed["bots"].array ?? []).compactMap(HerBot.init)
    let ids = Set(new.map(\.id))
    return list.filter { !deleted.contains($0.id) && !ids.contains($0.id) } + new.filter { !deleted.contains($0.id) }
}

/// A model of the environment's (models/list).
public struct HerModel: Equatable, Sendable {
    /// provider/model-id
    public var id: String
    public var name: String
    public var provider: String
    /// Its provider's credentials work.
    public var available: Bool
    /// In the model menu.
    public var shown: Bool
}

/// A provider of models, with how it is signed in to.
public struct HerProvider: Equatable, Sendable {
    public var id: String
    public var name: String
    public var names: [String: String]
    /// "api_key", "connection", "command" or "none".
    public var auth: String
    /// A Token Plan: a model plan the owner subscribes to, with a key of its own.
    public var plan: Bool
    /// A Token Plan's page for its key.
    public var keyPage: URL?

    /// Its name in the app's language (zh-Hans takes `zh`), else its own.
    public func name(language: String) -> String {
        names[language] ?? names[String(language.prefix { $0 != "-" })] ?? name
    }
}

/// What models/list says: the models and the providers.
public struct HerModels: Equatable, Sendable {
    public var models: [HerModel]
    public var providers: [HerProvider]

    public static let none = HerModels(models: [], providers: [])

    public init(models: [HerModel], providers: [HerProvider]) {
        self.models = models
        self.providers = providers
    }

    public init(_ j: JSON) {
        models = (j["models"].array ?? []).compactMap { m in
            m["id"].string.map { HerModel(id: $0, name: m["name"].string ?? $0, provider: m["provider"].string ?? "", available: m["available"].bool ?? false, shown: m["shown"].bool ?? false) }
        }
        providers = (j["providers"].array ?? []).compactMap { p in
            p["id"].string.map {
                HerProvider(id: $0, name: p["name"].string ?? $0, names: (p["names"].object ?? [:]).compactMapValues(\.string),
                            auth: p["auth"].string ?? "", plan: !p["plan"].isNull, keyPage: p["plan"]["key_url"].string.flatMap(URL.init(string:)))
            }
        }
    }

    /// The models a menu offers: those the environment can use, and first the current one when it is not.
    public func menu(current: String?) -> [HerModel] {
        let list = models.filter { $0.available && $0.shown }
        guard let current, !list.contains(where: { $0.id == current }) else { return list }
        return [models.first { $0.id == current } ?? HerModel(id: current, name: herModelName(current), provider: "", available: false, shown: false)] + list
    }

    /// A model as people read it: its name, else its id without the provider.
    public func title(_ ref: String) -> String { models.first { $0.id == ref }?.name ?? herModelName(ref) }

    public func provider(_ id: String) -> HerProvider? { providers.first { $0.id == id } }
}

/// A model's id without its provider ("deepseek/deepseek-chat": deepseek-chat).
public func herModelName(_ ref: String) -> String {
    ref.split(separator: "/", maxSplits: 1).last.map(String.init) ?? ref
}

// MARK: Environments

/// Reached now: a machine online (the iPhone is never one).
public func herReachable(_ m: HerMachine) -> Bool { m.local || m.online }

/// The environments: the machines Her is on for, by name.
public func herEnvironments(_ machines: [HerMachine]) -> [HerMachine] {
    machines.filter(\.her.enabled).sorted { $0.machineName.localizedStandardCompare($1.machineName) == .orderedAscending }
}

/// Her on without her grant (docs/her-mesh.md §3.4): her machine keeps a key for her (its software knows the
/// mesh), and the owner has not let her use the computers yet, or revoked it.
public func herLacksGrant(_ m: HerMachine) -> Bool { m.her.enabled && !m.her.mesh && !m.her.clientPublicKey.isEmpty }

/// The environment to show: the one remembered while it is one, else the first.
public func herPick(_ envs: [HerMachine], remembered: String?) -> HerMachine? {
    envs.first { $0.machineID == remembered } ?? envs.first
}

/// Unread messages and approvals over the environments (her counts them).
public func herUnread(_ machines: [HerMachine]) -> Int {
    machines.reduce(0) { $0 + ($1.her.enabled ? Int($1.her.unread) : 0) }
}

/// What an environment's Her is doing.
public enum HerEnvState: Equatable, Sendable {
    /// Offline, with when it was last seen.
    case offline(Date?)
    case starting
    case failed(String)
    /// Running, with no model connection that works.
    case noModel
    case ready
}

public func herState(_ m: HerMachine) -> HerEnvState {
    if !herReachable(m) { return .offline(m.hasLastSeen ? m.lastSeen.date : nil) }
    if !m.her.error.isEmpty { return .failed(m.her.error) }
    if !m.her.running { return .starting }
    return m.her.modelReady ? .ready : .noModel
}

/// A machine the first run offers, and why it can't be chosen when it can't.
public struct HerChoice: Equatable, Sendable {
    public enum Why: Equatable, Sendable {
        case windows, offline
        case unsupported(String)
    }

    public var machine: HerMachine
    public var why: Why?
}

/// Where Her can run (docs/her-server.md §9.2): the owner's machines Her is
/// not on for yet, those that can be chosen first. Windows machines show,
/// not to be chosen yet; a machine offline can't be asked now.
public func herChoices(_ machines: [HerMachine]) -> [HerChoice] {
    machines.filter { !$0.her.enabled }.map { m -> HerChoice in
        let unsupported = m.her.unsupported
        if unsupported == "windows" || m.status.platform.hasPrefix("windows") { return HerChoice(machine: m, why: .windows) }
        if !unsupported.isEmpty { return HerChoice(machine: m, why: .unsupported(unsupported)) }
        return HerChoice(machine: m, why: herReachable(m) ? nil : .offline)
    }
    .sorted { a, b in
        (a.why == nil) != (b.why == nil) ? a.why == nil : a.machine.machineName.localizedStandardCompare(b.machine.machineName) == .orderedAscending
    }
}
