import Foundation

// An environment's connectors, as the connectors page manages them (docs/her-connectors-demo.html): her's MCP
// servers in the user's configuration, read with mcp/list and changed with config/write, as the console does
// (her/apps/console/src/customize). Her's conversation takes up every change from her next step.

/// One connector as mcp/list gives it (her's McpServerView): secrets never, only the names they are saved under.
public struct HerConnector: Equatable, Sendable, Identifiable {
    public enum Auth: String, Sendable {
        case none, credential, oauth
    }

    public enum State: String, Sendable {
        case ok, failed, unknown
        case needsLogin = "needs_login"
        case untrusted
    }

    public var id: String { name }
    public let name: String
    /// A curated connector her installs and signs in to itself (飞书, 钉钉…): its id.
    public let curated: String?
    /// In a file her writes for the owner: it can be changed and removed here.
    public let editable: Bool
    public var enabled: Bool
    /// The layer that turned it off, when one did.
    public let disabledBy: String?
    /// Started as a command on the machine: only the owner's signature adds or changes one.
    public let command: Bool
    /// The address without its query, or the command line.
    public let summary: String
    public let auth: Auth
    /// Signed in, for one that signs in: her's auth_state is logged_in.
    public let signedIn: Bool
    public let state: State
    public let tools: Int?
    public let error: String?
    public let checkedAt: Date?
    /// The service's logo, when her found one.
    public let icon: Data?

    public init?(_ j: JSON) {
        guard let name = j["name"].string else { return nil }
        self.name = name
        curated = j["connector"].string
        editable = j["editable"].bool ?? false
        enabled = j["enabled"].bool ?? true
        disabledBy = j["disabled_by"].string
        command = j["transport"].string == "stdio"
        summary = j["summary"].string ?? ""
        auth = Auth(rawValue: j["auth"].string ?? "") ?? .none
        signedIn = j["auth_state"].string == "logged_in"
        let status = j["status"]
        state = State(rawValue: status["state"].string ?? "") ?? .unknown
        tools = status["tools"].int
        error = status["error"].string
        checkedAt = status["checked_at"].string.flatMap { try? Date($0, strategy: .iso8601) }
        icon = j["icon"].string.flatMap(herDataURI)
    }
}

/// What a row says of a connector (the console's statusOf).
public enum HerConnectorLook: Equatable, Sendable {
    case off
    case checking
    case ok(tools: Int)
    case failed(String?)
    case needsLogin
    case unknown

    /// The owner has something to do: sign in, or see why it failed.
    public var needsOwner: Bool {
        switch self {
        case .failed, .needsLogin: true
        default: false
        }
    }
}

public func herConnectorLook(_ c: HerConnector, checking: Bool = false) -> HerConnectorLook {
    if c.curated != nil, !c.signedIn { return .needsLogin }
    if !c.enabled { return .off }
    if checking { return .checking }
    switch c.state {
    case .ok: return .ok(tools: c.tools ?? 0)
    case .failed: return .failed(c.error)
    case .needsLogin: return .needsLogin
    case .unknown, .untrusted: return .unknown
    }
}

/// A tool a connector offers (mcp/tools, mcp/check).
public struct HerConnectorTool: Equatable, Sendable, Identifiable {
    public var id: String { name }
    public let name: String
    public let description: String?

    init?(_ j: JSON) {
        guard let name = j["name"].string else { return nil }
        self.name = name
        description = j["title"].string ?? j["description"].string
    }
}

/// What mcp/probe found of an address before anything is written.
public struct HerConnectorProbe: Equatable, Sendable {
    public enum Auth: String, Sendable {
        case oauth, token, none
    }

    /// The way to sign in her suggests: what the server asked for.
    public let auth: Auth
    /// The server answered 401 to a request without credentials.
    public let requiresAuth: Bool
    /// Signing in needs an OAuth app of the owner's own: the server takes no client her can bring.
    public let ownClient: Bool
    public let tools: [HerConnectorTool]
    public let icon: Data?
    /// Why it could not be reached; nil when the probe passed.
    public let error: String?

    public init(_ j: JSON) {
        auth = Auth(rawValue: j["suggested"]["auth"].string ?? "") ?? .none
        requiresAuth = j["auth"]["required"].bool ?? false
        ownClient = j["suggested"]["client"].string == "own"
        tools = (j["tools"].array ?? []).compactMap(HerConnectorTool.init)
        icon = j["icon"].string.flatMap(herDataURI)
        error = j["error"].string
    }
}

/// How a new connector signs in.
public enum HerConnectorSignIn: Sendable, Equatable {
    case none
    /// A token sent as `Authorization: Bearer …`, kept in her's credential store on the machine.
    case token(String)
    /// The owner's account at the service: a sign-in in the browser before it is added.
    case oauth
}

/// The connectors page's calls to her: `call` is a request on the environment's connection.
public struct HerConnectorCalls: Sendable {
    public var call: @Sendable (_ method: String, _ params: JSON) async throws -> JSON

    public init(call: @escaping @Sendable (String, JSON) async throws -> JSON) {
        self.call = call
    }

    public func list() async throws -> [HerConnector] {
        (try await call("mcp/list", [:])["servers"].array ?? []).compactMap(HerConnector.init)
    }

    /// The tools the connector offered when it last connected, and when that was.
    public func tools(_ name: String) async throws -> (tools: [HerConnectorTool], checkedAt: Date?) {
        let r = try await call("mcp/tools", ["name": .string(name)])
        return ((r["tools"].array ?? []).compactMap(HerConnectorTool.init), r["checked_at"].string.flatMap { try? Date($0, strategy: .iso8601) })
    }

    /// Connects once and lists the tools; the connector as it is after.
    public func check(_ name: String) async throws -> (connector: HerConnector?, tools: [HerConnectorTool]) {
        let r = try await call("mcp/check", ["name": .string(name)])
        return (HerConnector(r["server"]), (r["tools"].array ?? []).compactMap(HerConnectorTool.init))
    }

    /// On or off in the owner's file: off writes `enabled = false`; on takes that out, or says `true` over another
    /// file that turned it off.
    public func setEnabled(_ c: HerConnector, _ on: Bool) async throws {
        let value: JSON = on ? (c.disabledBy == "user" ? nil : true) : false
        try await write("mcp_servers.\(herKeySegment(c.name)).enabled", value)
    }

    /// Takes the connector out of the owner's file; her forgets what was saved for it. A curated one is signed out
    /// first.
    public func remove(_ c: HerConnector) async throws {
        if c.curated != nil { _ = try await call("mcp/logout", ["name": .string(c.name)]) }
        try await write("mcp_servers.\(herKeySegment(c.name))", nil)
    }

    public func logout(_ name: String) async throws {
        _ = try await call("mcp/logout", ["name": .string(name)])
    }

    /// Checks an address as it would be added, writing nothing.
    public func probe(name: String, url: String) async throws -> HerConnectorProbe {
        let id = UUID().uuidString
        return HerConnectorProbe(try await call("mcp/probe", ["probe_id": .string(id), "server": ["name": .string(name), "url": .string(url)]]))
    }

    /// The remote connector as it is written: a token as a reference to where it is saved.
    public static func server(url: String, signIn: HerConnectorSignIn, name: String) -> JSON {
        switch signIn {
        case .none: ["url": .string(url)]
        case .token: ["url": .string(url), "headers": ["Authorization": .string("Bearer ${credential:\(herCredentialName(name))}")]]
        case .oauth: ["url": .string(url), "auth": "oauth"]
        }
    }

    /// Adds a remote connector: a token is saved first; an account is signed in to first with `signIn`, which runs
    /// the browser (herSignInConnector with the server); then it is written and checked once. The tools it offers,
    /// nil when the sign-in did not end with one.
    public func add(name: String, url: String, signIn: HerConnectorSignIn,
                    signingIn: @Sendable (_ server: JSON) async -> HerSignInEnd = { _ in .failed("") }) async throws -> [HerConnectorTool]? {
        let server = Self.server(url: url, signIn: signIn, name: name)
        switch signIn {
        case .none: break
        case .token(let value): _ = try await call("credentials/set", ["name": .string(herCredentialName(name)), "value": .string(value)])
        case .oauth:
            var login = server
            if case .object(var o) = login { o["name"] = .string(name); login = .object(o) }
            switch await signingIn(login) {
            case .connected: break
            case .cancelled: return nil
            case .failed(let why): throw HerSignInRefusal(why)
            }
        }
        try await write("mcp_servers.\(herKeySegment(name))", server)
        return try await check(name).tools
    }

    private func write(_ key: String, _ value: JSON) async throws {
        _ = try await call("config/write", ["layer": "user", "edits": [["key_path": .string(key), "value": value, "merge": "replace"]]])
    }
}

/// Where a connector's token is saved in her's credential store (the console's `mcp.<name>`).
public func herCredentialName(_ name: String) -> String { "mcp.\(name)" }

/// A name her accepts for a connector.
public func herValidConnectorName(_ name: String) -> Bool {
    (1...32).contains(name.count) && name.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-") }
}

/// A name for the connector at `url` (the console's suggestName): the host's own name, without `mcp`, `api`, `www` or
/// `server`; empty for an address by number, or one that is not http(s).
public func herSuggestedConnectorName(_ url: String) -> String {
    guard let u = URL(string: url.trimmingCharacters(in: .whitespacesAndNewlines)), ["http", "https"].contains(u.scheme ?? ""),
          let host = u.host(percentEncoded: false), !host.isEmpty,
          host.contains(where: \.isLetter), !host.contains(":") else { return "" }
    let parts = host.split(separator: ".").map(String.init).filter { !["mcp", "api", "www", "server"].contains($0) }
    let n = parts.count > 1 ? parts[parts.count - 2] : (parts.first ?? host)
    var out = ""
    for c in n.lowercased() {
        if c.isASCII && (c.isLetter || c.isNumber || c == "_" || c == "-") { out.append(c) } else if out.last != "-" { out.append("-") }
    }
    return String(out.trimmingCharacters(in: CharacterSet(charactersIn: "-")).prefix(32))
}

/// A connector's name as one segment of a key path.
func herKeySegment(_ name: String) -> String {
    name.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-") } ? name : "\"\(name)\""
}

/// The bytes of a `data:` URI in base64.
func herDataURI(_ s: String) -> Data? {
    guard s.hasPrefix("data:"), let comma = s.firstIndex(of: ","), s[..<comma].hasSuffix(";base64") else { return nil }
    return Data(base64Encoded: String(s[s.index(after: comma)...]))
}
