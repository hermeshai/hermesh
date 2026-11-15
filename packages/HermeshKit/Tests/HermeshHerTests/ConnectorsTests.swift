import Foundation
import Testing
@testable import HermeshHer

// The connectors page's model: her's McpServerView read, each row's look, and the calls the page makes.

func view(_ name: String, enabled: Bool = true, state: String = "ok", tools: Int? = 3, auth: String = "none", authState: JSON = nil,
          connector: JSON = nil, disabledBy: JSON = nil, transport: String = "http") -> JSON {
    ["name": .string(name), "layer": "user", "editable": true, "enabled": .bool(enabled), "disabled_by": disabledBy, "transport": .string(transport),
     "summary": "https://mcp.example.com/mcp", "auth": .string(auth), "auth_state": authState, "connector": connector,
     "status": ["state": .string(state), "tools": tools.map { .number(Double($0)) } ?? nil, "error": state == "failed" ? "timed out" : nil,
                "checked_at": "2026-11-15T09:12:00Z"]]
}

/// Records each call and answers from `answers` by method.
final class Recorder: @unchecked Sendable {
    private let lock = NSLock()
    private(set) var calls: [(String, JSON)] = []
    var answers: [String: JSON] = [:]

    var calls_: HerConnectorCalls {
        HerConnectorCalls { method, params in
            self.lock.withLock {
                self.calls.append((method, params))
                return self.answers[method] ?? [:]
            }
        }
    }

    var methods: [String] { lock.withLock { calls.map(\.0) } }
}

@Test func aConnectorIsReadAsHerListsIt() {
    let icon = "data:image/png;base64," + Data([1, 2, 3]).base64EncodedString()
    var j = view("github", tools: 26, auth: "credential")
    if case .object(var o) = j { o["icon"] = .string(icon); j = .object(o) }
    let c = HerConnector(j)!
    #expect(c.name == "github" && c.enabled && !c.command && c.auth == .credential && c.state == .ok && c.tools == 26)
    #expect(c.icon == Data([1, 2, 3]))
    #expect(c.checkedAt == (try? Date("2026-11-15T09:12:00Z", strategy: .iso8601)))
    #expect(HerConnector(view("pw", transport: "stdio"))!.command, "started as a command")
    #expect(HerConnector(["layer": "user"]) == nil, "no name, no connector")
}

@Test func eachRowSaysWhatTheConnectorIsUpTo() {
    #expect(herConnectorLook(HerConnector(view("a", tools: 26))!) == .ok(tools: 26))
    #expect(herConnectorLook(HerConnector(view("a", enabled: false))!) == .off)
    #expect(herConnectorLook(HerConnector(view("a"))!, checking: true) == .checking)
    #expect(herConnectorLook(HerConnector(view("a", state: "failed"))!) == .failed("timed out"))
    #expect(herConnectorLook(HerConnector(view("a", state: "needs_login", auth: "oauth", authState: "needs_login"))!) == .needsLogin)
    #expect(herConnectorLook(HerConnector(view("a", state: "unknown"))!) == .unknown)
    // A curated one not signed in needs the owner whatever else holds.
    #expect(herConnectorLook(HerConnector(view("feishu", enabled: false, connector: "feishu"))!) == .needsLogin)
    #expect(herConnectorLook(HerConnector(view("feishu", authState: "logged_in", connector: "feishu"))!) == .ok(tools: 3))
    #expect(HerConnectorLook.needsLogin.needsOwner && HerConnectorLook.failed(nil).needsOwner && !HerConnectorLook.off.needsOwner)
}

@Test func aSwitchWritesOnlyTheOwnersFile() async throws {
    let r = Recorder()
    let on = HerConnector(view("github"))!
    try await r.calls_.setEnabled(on, false)
    let off = HerConnector(view("github", enabled: false, disabledBy: "user"))!
    try await r.calls_.setEnabled(off, true)
    let offElsewhere = HerConnector(view("github", enabled: false, disabledBy: "user_project"))!
    try await r.calls_.setEnabled(offElsewhere, true)
    let edits = r.calls.map { $0.1["edits"][0] }
    #expect(r.calls.allSatisfy { $0.0 == "config/write" && $0.1["layer"] == "user" })
    #expect(edits.map { $0["key_path"].string } == Array(repeating: "mcp_servers.github.enabled", count: 3))
    #expect(edits.map { $0["value"] } == [false, nil, true], "off; on takes `false` out; on over another file says so")
}

@Test func removingTakesTheConnectorOutAndSignsACuratedOneOut() async throws {
    let r = Recorder()
    try await r.calls_.remove(HerConnector(view("my server"))!)
    #expect(r.methods == ["config/write"])
    #expect(r.calls[0].1["edits"][0] == ["key_path": "mcp_servers.\"my server\"", "value": nil, "merge": "replace"])
    let c = Recorder()
    try await c.calls_.remove(HerConnector(view("feishu", connector: "feishu"))!)
    #expect(c.methods == ["mcp/logout", "config/write"])
}

@Test func addingSavesATokenBeforeWritingAndChecksOnce() async throws {
    let r = Recorder()
    r.answers["mcp/check"] = ["server": view("docs"), "tools": [["name": "search"], ["name": "fetch", "description": "Reads a page"]]]
    let tools = try await r.calls_.add(name: "docs", url: "https://mcp.example.com/mcp", signIn: .token("t-1"))
    #expect(tools?.map(\.name) == ["search", "fetch"])
    #expect(r.methods == ["credentials/set", "config/write", "mcp/check"])
    #expect(r.calls[0].1 == ["name": "mcp.docs", "value": "t-1"])
    #expect(r.calls[1].1["edits"][0]["value"] == ["url": "https://mcp.example.com/mcp", "headers": ["Authorization": "Bearer ${credential:mcp.docs}"]])
    #expect(!r.calls[1].1.data().contains(Data("t-1".utf8)), "the token itself is never written")
}

@Test func anAccountIsSignedInToBeforeItIsWritten() async throws {
    let r = Recorder()
    let signedIn = Recorder()
    let tools = try await r.calls_.add(name: "linear", url: "https://mcp.linear.app/mcp", signIn: .oauth) { server in
        _ = try? await signedIn.calls_.call("sign-in", server)
        return .connected(account: nil)
    }
    #expect(tools == [])
    #expect(signedIn.calls[0].1 == ["url": "https://mcp.linear.app/mcp", "auth": "oauth", "name": "linear"], "the server, named, goes to mcp/login")
    #expect(r.methods == ["config/write", "mcp/check"])
    #expect(r.calls[0].1["edits"][0]["value"] == ["url": "https://mcp.linear.app/mcp", "auth": "oauth"])

    let cancelled = Recorder()
    let none = try await cancelled.calls_.add(name: "linear", url: "https://mcp.linear.app/mcp", signIn: .oauth) { _ in .cancelled }
    #expect(none == nil && cancelled.calls.isEmpty, "cancelled: nothing is written")
    let refused = Recorder()
    await #expect(throws: HerSignInRefusal.self) {
        try await refused.calls_.add(name: "linear", url: "https://mcp.linear.app/mcp", signIn: .oauth) { _ in .failed("access_denied") }
    }
    #expect(refused.calls.isEmpty)
}

@Test func aProbeSaysHowToSignIn() {
    let p = HerConnectorProbe(["kind": "remote", "steps": [], "auth": ["required": true], "suggested": ["auth": "oauth", "client": "own"],
                               "tools": [["name": "echo"]], "error": nil])
    #expect(p.auth == .oauth && p.requiresAuth && p.ownClient && p.tools.map(\.name) == ["echo"] && p.error == nil)
    #expect(HerConnectorProbe(["suggested": ["auth": "none"], "error": "connection refused"]).error == "connection refused")
}

@Test func aNameIsSuggestedFromTheAddress() {
    #expect(herSuggestedConnectorName("https://mcp.linear.app/mcp") == "linear")
    #expect(herSuggestedConnectorName("https://mcp.sentry.dev/mcp") == "sentry")
    #expect(herSuggestedConnectorName("https://api.githubcopilot.com/mcp/") == "githubcopilot")
    #expect(herSuggestedConnectorName("https://Docs.Example.com/mcp") == "example")
    #expect(herSuggestedConnectorName("http://127.0.0.1:8080/mcp") == "", "an address by number names nothing")
    #expect(herSuggestedConnectorName("ftp://files.example.com") == "")
    #expect(herSuggestedConnectorName("not an address") == "")
    #expect(herValidConnectorName("linear") && herValidConnectorName("my_server-2"))
    #expect(!herValidConnectorName("") && !herValidConnectorName("a b") && !herValidConnectorName(String(repeating: "a", count: 33)))
}
