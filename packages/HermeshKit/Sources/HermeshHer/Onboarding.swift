import Foundation

// Her's onboarding while the environment shown has no model that works
// (docs/her-server.md §9.2, the Mac's onboarding.ts): Her speaks first, but
// no model can speak yet, so the app writes every message on Her's behalf
// and never sends any of it to her or keeps it in her's conversation. Here:
// what is said, the steps between, and the providers offered. The page
// words and draws it.

public struct HerOnboarding: Equatable, Sendable {
    /// A message of the onboarding: Her's, as the app writes it, or what the owner chose.
    public enum Said: Equatable, Sendable {
        case greeting, browser, signInCancelled, whichProvider
        case signedIn(account: String?)
        case signInFailed(String), keyFailed(String)
        case pasteKey(provider: String), keyUnverified(provider: String)
        case keyRejected(provider: String, message: String)
        case ownerChatGPT, ownerKey, ownerOtherWay
        case ownerProvider(String)

        public var byOwner: Bool {
            switch self {
            case .ownerChatGPT, .ownerKey, .ownerOtherWay, .ownerProvider: true
            default: false
            }
        }
    }

    /// Choosing a way; the ChatGPT sign-in under way, or being stopped; choosing a provider; its key asked for, or
    /// being checked; connected, until the conversation opens on a model; done.
    public enum Step: Equatable, Sendable { case choose, signingIn, stopping, provider, key, checking, connecting, done }

    public enum Connected: Equatable, Sendable {
        case chatGPT
        case key(provider: String)
    }

    /// Once done: the model Her talks with, and where the onboarding stays: in that conversation, after its row
    /// `after` (first for nil).
    public struct Done: Equatable, Sendable {
        public var model: String
        public var session: String?
        public var after: String?
    }

    public private(set) var said: [Said] = [.greeting]
    public private(set) var step = Step.choose
    /// When it began; once done, when that was.
    public private(set) var at: Date
    /// The provider whose key is asked for.
    public private(set) var provider: String?
    public private(set) var connected: Connected?
    public private(set) var done: Done?

    public init(at: Date) { self.at = at }

    // Each way on changes nothing when it does not apply now: an answer that comes after the onboarding moved on.

    public mutating func chooseChatGPT() {
        guard step == .choose else { return }
        step = .signingIn
        said += [.ownerChatGPT, .browser]
    }

    /// Another way, while the sign-in is under way: it is stopped, and Her answers once it has.
    public mutating func otherWay() {
        guard step == .signingIn else { return }
        step = .stopping
        said.append(.ownerOtherWay)
    }

    public mutating func signInEnded(_ r: HerSignInEnd) {
        guard step == .signingIn || step == .stopping else { return }
        switch r {
        case .connected(let account):
            step = .connecting
            connected = .chatGPT
            said.append(.signedIn(account: account))
        case .cancelled:
            step = .choose
            said.append(.signInCancelled)
        case .failed(let message):
            step = .choose
            said.append(.signInFailed(message))
        }
    }

    public mutating func chooseKey() {
        guard step == .choose else { return }
        step = .provider
        said += [.ownerKey, .whichProvider]
    }

    /// A provider picked: from the question, or another one while a key is asked for.
    public mutating func pickProvider(_ id: String) {
        guard step == .provider || step == .key else { return }
        step = .key
        provider = id
        said += [.ownerProvider(id), .pasteKey(provider: id)]
    }

    public mutating func checking() {
        if step == .key { step = .checking }
    }

    /// her saved the key; one it could not check with the provider is said to be so.
    public mutating func keySaved(verified: Bool) {
        guard step == .checking, let provider else { return }
        if !verified { said.append(.keyUnverified(provider: provider)) }
        step = .connecting
        connected = .key(provider: provider)
    }

    /// her did not save the key: the provider refused it (her's error kind key_rejected, with why), or it failed
    /// otherwise. The key is asked for again.
    public mutating func keyFailed(_ e: Error) {
        guard step == .checking, let provider else { return }
        if let rpc = e as? HerRPCError, rpc.kind == "key_rejected" {
            said.append(.keyRejected(provider: provider, message: rpc.message))
        } else {
            said.append(.keyFailed(e.localizedDescription))
        }
        step = .key
    }

    /// The conversation opened on a model, however it was connected (the console's too).
    public mutating func finish(model: String, session: String?, after: String?, at: Date) {
        guard step != .done else { return }
        step = .done
        self.at = at
        done = Done(model: model, session: session, after: after)
    }

    /// What goes under one of Her's messages: the two ways, the sign-in's, the providers, or the key's field.
    public enum Control: Equatable, Sendable { case ways, browser, providers, key }

    /// Which of Her's messages get controls, by index: Her's last; while a key is asked for, its question keeps the providers.
    public var controls: [Int: Control] {
        let last = said.count - 1
        switch step {
        case .choose: return [last: .ways]
        case .signingIn: return [last: .browser]
        case .provider: return [last: .providers]
        case .key, .checking:
            var at = [last: Control.key]
            if let question = said.lastIndex(of: .whichProvider) { at[question] = .providers }
            return at
        default: return [:]
        }
    }
}

/// How a sign-in ended: her's words and the account, never a token.
public enum HerSignInEnd: Equatable, Sendable {
    case connected(account: String?)
    case failed(String)
    case cancelled
}

/// The providers people know best, first, in this order (Google's is `google`, or `gemini`).
private let known = ["deepseek", "openai", "anthropic", "google", "gemini", "kimi", "qwen", "glm"]

/// The providers offered for a key, at most five: those that take one and aren't token plans, the best known first,
/// then by name. The Anthropic-compatible DeepSeek endpoint is the same account as DeepSeek.
public func herKeyChoices(_ providers: [HerProvider]) -> [HerProvider] {
    let rank = { (p: HerProvider) in known.firstIndex(of: p.id) ?? known.count }
    return Array(providers.filter { $0.auth == "api_key" && !$0.plan && $0.id != "deepseek-anthropic" }
        .sorted { rank($0) != rank($1) ? rank($0) < rank($1) : $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        .prefix(5))
}

/// Where keys are made, for the providers the app knows.
public func herKeyPage(_ provider: String) -> URL? {
    [
        "deepseek": "https://platform.deepseek.com/api_keys",
        "openai": "https://platform.openai.com/api-keys",
        "anthropic": "https://console.anthropic.com/settings/keys",
        "google": "https://aistudio.google.com/apikey",
    ][provider].flatMap(URL.init(string:))
}

/// Where the onboarding goes in the conversation.
public enum HerOnboardingPlace: Equatable, Sendable {
    case hidden, first
    case after(String)
}

/// Under way, it is the last thing, while no model works there and while one connected is coming. Done, it stays
/// where it was, in its own conversation only (a cleared one starts without it).
public func herOnboardingPlace(_ f: HerOnboarding?, noModel: Bool, session: String?, last: String?) -> HerOnboardingPlace {
    guard let f else { return .hidden }
    if let done = f.done { return done.session == session ? done.after.map { .after($0) } ?? .first : .hidden }
    guard noModel || f.step == .connecting else { return .hidden }
    return last.map { .after($0) } ?? .first
}

/// The rows with the onboarding's in its place; without it when it does not show, or its place is gone.
public func herWithOnboarding(_ items: [HerItem], _ place: HerOnboardingPlace, at: Date) -> [HerItem] {
    let row = HerItem(key: "onboarding", ts: at.timeIntervalSince1970 * 1000, kind: .onboarding)
    switch place {
    case .hidden: return items
    case .first: return [row] + items
    case .after(let key):
        guard let i = items.firstIndex(where: { $0.key == key }) else { return items }
        var out = items
        out.insert(row, at: i + 1)
        return out
    }
}
