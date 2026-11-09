import Foundation

// Signing in for an environment's Her from the iPhone (docs/her-server.md
// §5.4): to ChatGPT, or to a connector a secure card connects, as the Mac
// app does (apps/desktop/src/shared/herSignin.ts). This iPhone's core
// catches the browser's redirect on 127.0.0.1 (the console's device-side
// `signin` routes, internal/owner/her_console.go); her on the machine makes
// the authorization request and keeps the PKCE verifier; the browser opens
// it; what the redirect carried goes back to her, who exchanges it and keeps
// the tokens there. Nothing of it stays on the iPhone.

/// The catcher on this device's core: a redirect it catches once, and what it caught.
public struct HerCatcher: Sendable {
    /// Starts a catcher for a redirect to `path`: its id, and the redirect_uri to give her.
    public var start: @Sendable (_ path: String) async throws -> (id: String, redirectURI: String)
    /// What it caught: one request, which waits a while and says {"pending": true} when nothing came yet. It throws
    /// HerSignInRefusal when the catcher says no, any other error for one lost on the way (asked again).
    public var poll: @Sendable (_ id: String) async throws -> JSON

    public init(start: @escaping @Sendable (String) async throws -> (id: String, redirectURI: String), poll: @escaping @Sendable (String) async throws -> JSON) {
        self.start = start
        self.poll = poll
    }
}

/// The catcher refused (its JSON's `error`, or its text).
public struct HerSignInRefusal: LocalizedError, Sendable {
    public var message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

public struct HerSignInDeps: Sendable {
    public var catcher: HerCatcher
    /// A request to her on the environment, on a connection of its own: its result, or her's error thrown.
    public var call: @Sendable (_ method: String, _ params: JSON) async throws -> JSON
    /// Shows the authorization page.
    public var open: @Sendable (URL) async throws -> Void
    /// How long to wait before asking the catcher again after a request failed on the way.
    public var retry: Duration

    public init(catcher: HerCatcher, call: @escaping @Sendable (String, JSON) async throws -> JSON, open: @escaping @Sendable (URL) async throws -> Void, retry: Duration = .seconds(1)) {
        self.catcher = catcher
        self.call = call
        self.open = open
        self.retry = retry
    }
}

/// Signs in to ChatGPT for the environment; only an https page goes to the browser. Cancelling the task cancels
/// her's side too.
public func herSignInChatGPT(_ d: HerSignInDeps) async -> HerSignInEnd {
    func ended(_ s: JSON) -> HerSignInEnd {
        switch s["state"].string {
        case "connected": .connected(account: s["account"].text)
        case "cancelled": .cancelled
        default: .failed(s["message"].string ?? "")
        }
    }
    return await signIn(d, path: "/auth/callback", page: { $0.scheme == "https" },
        start: { redirect in
            let s = try await d.call("chatgpt/login", ["redirect_uri": .string(redirect)])
            guard let url = s["authorize_url"].string.flatMap(URL.init(string:)), let login = s["login_id"].string else { return .ended(.failed(s["message"].string ?? "")) }
            return .begun(url, login)
        },
        // ChatGPT names the client it registered in the redirect of a first sign-in.
        complete: { login, got in
            ended(try await d.call("chatgpt/complete", ["login_id": .string(login), "code": got["code"], "state": got["state"], "client_id": got["client_id"]]))
        },
        cancel: { login in _ = try? await d.call("chatgpt/cancel", ["login_id": .string(login)]) })
}

/// Signs in to the environment's connector `name`, in her's configuration already (a card's ops added it): her says
/// on the card how it went. The page is an https one, or http on loopback, as her lets a connector be addressed.
public func herSignInConnector(_ d: HerSignInDeps, name: String) async -> HerSignInEnd {
    await signIn(d, path: "/callback", page: herConnectorPage,
        start: { redirect in
            let r = try await d.call("mcp/login", ["name": .string(name), "redirect_uri": .string(redirect)])
            guard let url = r["authorize_url"].string.flatMap(URL.init(string:)), let login = r["login_id"].string else { return .ended(.failed(r["message"].string ?? "")) }
            return .begun(url, login)
        },
        complete: { login, got in
            _ = try await d.call("mcp/login_complete", ["login_id": .string(login), "code": got["code"], "state": got["state"], "iss": got["iss"]])
            return .connected(account: nil)
        },
        cancel: { _ in _ = try? await d.call("mcp/login_cancel", ["name": .string(name)]) })
}

/// Where a connector's sign-in may send the browser.
public func herConnectorPage(_ url: URL) -> Bool {
    url.scheme == "https" || (url.scheme == "http" && ["127.0.0.1", "localhost", "::1"].contains(url.host ?? ""))
}

private enum Started {
    case begun(URL, String)
    case ended(HerSignInEnd)
}

/// A sign-in that ends on this device: once her has the tokens, or with why not. The provider's refusal, a failure on
/// the way, or cancelling the task cancels her's side too.
private func signIn(_ d: HerSignInDeps, path: String, page: (URL) -> Bool, start: (String) async throws -> Started,
                    complete: (String, JSON) async throws -> HerSignInEnd, cancel: (String) async -> Void) async -> HerSignInEnd {
    var login: String?
    do {
        let catcher = try await d.catcher.start(path)
        let url: URL, id: String
        switch try await start(catcher.redirectURI) {
        case .ended(let e): return e
        case .begun(let u, let l): (url, id) = (u, l)
        }
        login = id
        guard page(url) else { throw HerSignInRefusal("not a page to open: \(url)") }
        try Task.checkCancellation()
        try await d.open(url)
        let got = try await caught(d, catcher.id)
        if let error = got["error"].string {
            await cancel(id)
            return .failed(got["error_description"].text ?? error)
        }
        return try await complete(id, got)
    } catch {
        if let login { await cancel(login) }
        if Task.isCancelled || error is CancellationError { return .cancelled }
        return .failed(error.localizedDescription)
    }
}

/// What the catcher caught, once it has; a request lost on the way is asked again.
private func caught(_ d: HerSignInDeps, _ id: String) async throws -> JSON {
    while true {
        try Task.checkCancellation()
        let body: JSON
        do {
            body = try await d.catcher.poll(id)
        } catch let e as HerSignInRefusal {
            throw e
        } catch {
            try Task.checkCancellation()
            try await Task.sleep(for: d.retry)
            continue
        }
        if body["pending"].bool != true { return body }
    }
}
