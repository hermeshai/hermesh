import Foundation
import HermeshProto
import SwiftProtobuf
import Testing
@testable import HermeshHer

// The kept copy and the waiting messages, cards, the onboarding, the
// environments, the call transport and the sign-ins.

@Test func theKeptCopyShowsUntilHerAnswersAndWaitingMessagesFollowIt() throws {
    var r = Hermesh_Local_V1_GetHerConversationResponse()
    #expect(herKept(r) == nil, "nothing kept")
    r.bot = Data(#"{"id":"b1","name":"Her","default":true,"unread":2,"session_id":"s1","avatar":{"kind":"mark"},"created_at":"2026-11-01T00:00:00Z"}"#.utf8)
    r.snapshot = snapshotJSON([said("u1", "hello", ts: 1000), said("u2", "deliver me", ts: 5000)]).data()
    let kept = try #require(herKept(r))
    #expect(kept.bot.isDefault && kept.bot.unread == 2 && kept.session?.sessionID == "s1")

    func waiting(_ id: String, _ text: String, at ms: Double, error: String = "") -> Hermesh_Local_V1_HerWaiting {
        .with { $0.id = id; $0.botID = "b1"; $0.text = text; $0.createdAt = .init(date: Date(timeIntervalSince1970: ms / 1000)); $0.localPaths = ["/var/a/photo.jpg"]; $0.error = error }
    }
    let list = [waiting("w2", "later", at: 9000, error: "offline"), waiting("w1", "deliver me", at: 4000), waiting("w3", "hello", at: 900_000)]
    let rows = herWaiting(list + [.with { $0.id = "other"; $0.botID = "b2"; $0.text = "x" }], botID: "b1", said: herChat(kept.session!))
    // "deliver me" was delivered meanwhile; "hello" was said long before the one waiting was written.
    #expect(rows.map(\.key) == ["waiting:w2", "waiting:w3"])
    #expect(rows[0].kind == .waiting(id: "w2", text: "later", files: ["photo.jpg"], error: "offline"))
}

@Test func aConnectCardFillsItsOpsAndEndsInOneLine() throws {
    let raw: JSON = ["id": "card1", "kind": "connect", "title": "Connect Notion", "asked_by": "owner", "expires_at": "2026-11-09T12:00:00.5Z",
                     "ops": [["method": "config/write", "params": ["mcp_servers": ["notion": ["url": "https://mcp.notion.com/mcp", "headers": ["Authorization": "<value:token>"]]]]]],
                     "body": ["name": "notion", "url": "https://mcp.notion.com/mcp", "host": "mcp.notion.com", "reconnect": false, "sign_in": "oauth", "fields": [["id": "token", "label": "Token", "secret": true]]]]
    let card = try #require(HerCard(raw))
    #expect(card.connect?.host == "mcp.notion.com" && card.connect?.fields.map(\.id) == ["token"] && herConnectorName("notion") == "Notion")
    #expect(card.approve(["token": "s3cret"])["ops"][0]["params"]["mcp_servers"]["notion"]["headers"]["Authorization"] == "s3cret")
    #expect(card.ops[0]["params"]["mcp_servers"]["notion"]["headers"]["Authorization"] == "<value:token>", "the card's own ops stay as they are")
    #expect(HerCard(["id": "x"]) == nil)

    let before = rfc3339("2026-11-09T11:00:00Z")!, after = rfc3339("2026-11-09T13:00:00Z")!
    var v = snapshot([])
    #expect(herCardShown(herCardNow(card, v, answered: nil, now: before), here: false) == .form(note: nil, busy: false))
    #expect(herCardShown(herCardNow(card, v, answered: nil, now: after), here: false) == .ended(outcome: "expired", tools: nil, note: nil))
    // Answered here: her waits for the sign-in this device runs.
    #expect(herCardShown(herCardNow(card, v, answered: HerCardStatus(state: "sign_in"), now: before), here: true) == .signIn)
    v.apply(["seq": 11, "ts": 1, "type": "card_changed", "card_id": "card1", "state": "retry", "note": "401 from the server"])
    #expect(herCardShown(herCardNow(card, v, answered: HerCardStatus(state: "sign_in"), now: before), here: true) == .form(note: "401 from the server", busy: false))
    v.apply(["seq": 12, "ts": 2, "type": "card_changed", "card_id": "card1", "state": "sign_in"])
    #expect(herCardShown(herCardNow(card, v, answered: nil, now: before), here: false) == .ended(outcome: "elsewhere", tools: nil, note: nil), "answered on another device")
    v.apply(["seq": 13, "ts": 3, "type": "card_changed", "card_id": "card1", "state": "done", "note": "Notion connected: 14 tools (search, …)"])
    #expect(herCardShown(herCardNow(card, v, answered: nil, now: after), here: true) == .ended(outcome: "done", tools: 14, note: "Notion connected: 14 tools (search, …)"))
    // As the history ended it, when the conversation is opened again.
    let reopened = snapshot([["type": "card_resolved", "card_id": "card1", "outcome": "declined", "note": ""]])
    #expect(herCardShown(herCardNow(card, reopened, answered: nil, now: before), here: false) == .ended(outcome: "declined", tools: nil, note: ""))

    // In the conversation: the card once her let it through, with Her's words.
    let sent = snapshot([said("u1", "notion", ts: 1), reply("a1", [call("k1", "send_to_user", ["type": "card", "text": "Connect it?"])], ts: 2), result("k1", details: ["card": raw])])
    #expect(herChat(sent).last?.kind == .card(raw, text: "Connect it?"))
}

@Test func theOnboardingConnectsAModelInTheConversation() {
    var f = HerOnboarding(at: Date(timeIntervalSince1970: 1))
    #expect(f.said == [.greeting] && f.controls == [0: .ways])
    f.chooseChatGPT()
    #expect(f.step == .signingIn && f.controls == [2: .browser])
    f.chooseKey()
    #expect(f.step == .signingIn, "an answer that no longer applies changes nothing")
    f.otherWay()
    f.signInEnded(.cancelled)
    #expect(f.step == .choose && f.said.last == .signInCancelled)
    f.chooseKey()
    f.pickProvider("deepseek")
    #expect(f.controls == [6: .providers, 8: .key])
    f.checking()
    f.keyFailed(HerRPCError(code: -32000, message: "Authentication Fails", data: ["kind": "key_rejected"]))
    #expect(f.step == .key && f.said.last == .keyRejected(provider: "deepseek", message: "Authentication Fails"))
    f.checking()
    f.keySaved(verified: false)
    #expect(f.step == .connecting && f.connected == .key(provider: "deepseek") && f.said.last == .keyUnverified(provider: "deepseek"))
    #expect(herOnboardingPlace(f, noModel: false, session: nil, last: "u9") == .after("u9"), "connected, until the conversation opens")
    f.finish(model: "deepseek/deepseek-chat", session: "s1", after: nil, at: Date(timeIntervalSince1970: 2))
    #expect(herOnboardingPlace(f, noModel: false, session: "s1", last: "u9") == .first)
    #expect(herOnboardingPlace(f, noModel: false, session: "s2", last: nil) == .hidden, "a cleared conversation starts without it")
    let items = [HerItem(key: "a", ts: 1, kind: .her("hi"))]
    #expect(herWithOnboarding(items, .after("a"), at: .now).map(\.key) == ["a", "onboarding"])
    #expect(herWithOnboarding(items, .after("gone"), at: .now).map(\.key) == ["a"])

    let providers = HerModels(["providers": [
        ["id": "zhipu", "name": "Zhipu", "auth": "api_key"], ["id": "openai", "name": "OpenAI", "auth": "api_key"],
        ["id": "deepseek", "name": "DeepSeek", "names": ["zh": "深度求索"], "auth": "api_key"], ["id": "chatgpt", "name": "ChatGPT", "auth": "connection"],
        ["id": "kimi-plan", "name": "Kimi Plan", "auth": "api_key", "plan": ["name": "x"]], ["id": "deepseek-anthropic", "name": "DeepSeek (Anthropic)", "auth": "api_key"],
    ]]).providers
    let choices = herKeyChoices(providers)
    #expect(choices.map(\.id) == ["deepseek", "openai", "zhipu"])
    #expect(choices[0].name(language: "zh-Hans") == "深度求索" && choices[0].name(language: "en") == "DeepSeek")
    #expect(herKeyPage("deepseek")?.host == "platform.deepseek.com" && herKeyPage("zhipu") == nil)
}

@Test func theOnboardingOffersATokenPlanAndAnotherWayAtEachStep() {
    var f = HerOnboarding(at: Date(timeIntervalSince1970: 1))
    f.choosePlan()
    #expect(f.step == .plan && f.said == [.greeting, .ownerPlan, .whichPlan] && f.controls == [2: .plans])
    f.pickProvider("tencent-token-plan")
    #expect(f.step == .key && f.controls == [2: .plans, 4: .key], "the plans stay under their question")
    // Another way, while a key is asked for: Her asks which at once, with the ways.
    f.otherWay()
    #expect(f.step == .choose && f.provider == nil && Array(f.said.suffix(2)) == [.ownerOtherWay, .whichWay] && f.controls == [6: .ways])
    f.chooseKey()
    f.otherWay()
    #expect(f.step == .choose && f.said.last == .whichWay, "from the providers too")
    f.choosePlan()
    f.pickProvider("zhipu-coding-plan")
    f.checking()
    f.otherWay()
    #expect(f.step == .checking, "not while a key is checked")
    f.keySaved(verified: false)
    #expect(f.connected == .key(provider: "zhipu-coding-plan") && f.said.last == .keyUnverified(provider: "zhipu-coding-plan"))

    let providers = HerModels(["providers": [
        ["id": "zhipu-coding-plan", "name": "Zhipu GLM Coding Plan", "auth": "api_key", "plan": ["key_url": "https://www.bigmodel.cn/glm-coding"]],
        ["id": "new-plan", "name": "Another Plan", "auth": "api_key", "plan": ["key_url": "https://example.invalid/keys"]],
        ["id": "deepseek", "name": "DeepSeek", "auth": "api_key"],
        ["id": "bailian-token-plan", "name": "Bailian Token Plan", "auth": "api_key", "plan": ["key_url": "https://bailian.console.aliyun.com/cn-beijing/subscription/overview"]],
        ["id": "aaa-plan", "name": "AAA Plan", "auth": "api_key", "plan": [:]],
    ]]).providers
    #expect(herPlanChoices(providers).map(\.id) == ["bailian-token-plan", "zhipu-coding-plan", "aaa-plan", "new-plan"], "the console's order, then by name")
    let zhipu = providers[0]
    #expect(zhipu.plan && herKeyPage(zhipu.id, plan: zhipu.keyPage)?.absoluteString == "https://www.bigmodel.cn/glm-coding", "a plan's own page")
    #expect(providers[4].keyPage == nil && !providers[2].plan)
}

@Test func environmentsAndWhereHerCanRun() {
    func machine(_ id: String, _ name: String, online: Bool = true, platform: String = "darwin/arm64", her: (Hermesh_Machine_V1_HerStatus) -> Hermesh_Machine_V1_HerStatus = { $0 }) -> HerMachine {
        .with { $0.machineID = id; $0.machineName = name; $0.online = online; $0.status.platform = platform; $0.her = her(.init()) }
    }
    let on = machine("m1", "studio", her: { var h = $0; h.enabled = true; h.running = true; h.unread = 2; return h })
    let away = machine("m2", "cloud", online: false, her: { var h = $0; h.enabled = true; h.unread = 1; return h })
    let machines = [away, on, machine("m3", "pc", platform: "windows/amd64"), machine("m4", "old", her: { var h = $0; h.unsupported = "no her"; return h }),
                    machine("m5", "asleep", online: false), machine("m6", "box", platform: "linux/amd64")]
    #expect(herEnvironments(machines).map(\.machineName) == ["cloud", "studio"])
    #expect(herUnread(machines) == 3)
    // Her's grant (docs/her-mesh.md §3.4): missing only while she is on and her machine keeps a key for her.
    let keyed = machine("m7", "mini", her: { var h = $0; h.enabled = true; h.clientPublicKey = Data([1, 2, 3]); return h })
    let held = machine("m8", "held", her: { var h = $0; h.enabled = true; h.clientPublicKey = Data([1, 2, 3]); h.mesh = true; return h })
    let off = machine("m9", "off", her: { var h = $0; h.clientPublicKey = Data([1, 2, 3]); return h })
    #expect(herLacksGrant(keyed) && !herLacksGrant(held) && !herLacksGrant(off) && !herLacksGrant(on))
    #expect(herPick(herEnvironments(machines), remembered: "m1")?.machineID == "m1" && herPick(herEnvironments(machines), remembered: "gone")?.machineID == "m2")
    #expect(herState(on) == .noModel && herState(away) == .offline(nil))
    #expect(herChoices(machines).map { "\($0.machine.machineName):\(String(describing: $0.why))" } ==
            ["box:nil", "asleep:Optional(HermeshHer.HerChoice.Why.offline)", "old:Optional(HermeshHer.HerChoice.Why.unsupported(\"no her\"))", "pc:Optional(HermeshHer.HerChoice.Why.windows)"])

    var bots = [HerBot(["id": "b1", "name": "Her", "default": true])!, HerBot(["id": "b2", "name": "Other"])!]
    bots = mergeBots(bots, ["bots": [["id": "b1", "name": "Her", "default": true, "unread": 3]], "deleted": ["b2"]])
    #expect(bots.map(\.id) == ["b1"] && bots[0].unread == 3)
    let models = HerModels(["models": [["id": "a/one", "name": "One", "provider": "a", "available": true, "shown": true], ["id": "b/two", "name": "Two", "provider": "b", "available": false, "shown": true]]])
    #expect(models.menu(current: "a/one").map(\.id) == ["a/one"] && models.menu(current: "b/two").map(\.id) == ["b/two", "a/one"])
    #expect(models.title("c/three") == "three")
}

// MARK: The call transport

/// A machine's LocalService for a connection: what opening it says, and how calls are answered.
final class FakeLocal: HerLocal, @unchecked Sendable {
    let lock = NSLock()
    var calls: [Hermesh_Local_V1_HerCallRequest] = []
    var opened: [Hermesh_Local_V1_HerOpenRequest] = []
    let events: AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error>
    let send: AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error>.Continuation
    var answer: (Hermesh_Local_V1_HerCallRequest) -> Hermesh_Her_V1_Response = { _ in .with { $0.result = Data(#"{"ok":true}"#.utf8) } }

    init() { (events, send) = AsyncThrowingStream.makeStream() }

    func herOpen(_ req: Hermesh_Local_V1_HerOpenRequest) -> AsyncThrowingStream<Hermesh_Her_V1_OpenEvent, Error> {
        lock.withLock { opened.append(req) }
        return events
    }

    func herCall(_ req: Hermesh_Local_V1_HerCallRequest) async throws -> Hermesh_Her_V1_CallResponse {
        lock.withLock { calls.append(req) }
        return .with { $0.response = answer(req) }
    }
}

@Test func aCallWaitsForTheConnectionAndInitializeIsAnsweredHere() async throws {
    let local = FakeLocal()
    let c = HerConnection(local: local, machineID: "m1")
    let stream = c.open(setup: [("bots/subscribe", [:]), ("bots/open", ["bot_id": "b1"])])
    #expect(try await c.call("initialize")["protocol_version"] == 2)
    let early = Task { try await c.call("bots/read", ["bot_id": "b1"]) }
    var it = stream.makeAsyncIterator()
    local.send.yield(.with { $0.opened = .with {
        $0.connectionID = "conn-7"
        $0.responses = [.with { $0.result = Data(#"{"bots":[]}"#.utf8) }, .with { $0.error = .with { $0.code = -32000; $0.message = "no model"; $0.data = Data(#"{"kind":"no_model"}"#.utf8) } }]
    } })
    guard case .opened(let responses)? = try await it.next() else { Issue.record("not opened"); return }
    #expect(try responses[0].get() == ["bots": []])
    #expect(throws: HerRPCError.self) { try responses[1].get() }
    if case .failure(let e) = responses[1] { #expect(e.kind == "no_model") }
    #expect(try await early.value == ["ok": true], "a call made before the connection opened went once it did")
    local.send.yield(.with { $0.notification = .with { $0.method = "bots/changed"; $0.params = Data(#"{"bots":[{"id":"b1"}]}"#.utf8) } })
    guard case .notification("bots/changed", let params)? = try await it.next() else { Issue.record("no notification"); return }
    #expect(params["bots"][0]["id"] == "b1")
    #expect(local.opened[0].machineID == "m1" && local.opened[0].open.setup.map(\.method) == ["bots/subscribe", "bots/open"])
    #expect(try JSON.parse(local.opened[0].open.setup[1].params) == ["bot_id": "b1"])
    #expect(local.calls.map(\.call.connectionID) == ["conn-7"] && local.calls[0].call.request.method == "bots/read")
    local.answer = { _ in .with { $0.error = .with { $0.code = -32602; $0.message = "bad" } } }
    await #expect(throws: HerRPCError.self) { try await c.call("session/op", [:]) }
    local.send.finish()
    #expect(try await it.next() == nil)
    await #expect(throws: HerRPCError.disconnected) { try await c.call("bots/read", [:]) }
}

// MARK: Sign-ins

/// her and the catcher for a sign-in: what was called, and how the catcher answers each poll.
final class FakeSignIn: @unchecked Sendable {
    let lock = NSLock()
    var called: [(String, JSON)] = []
    var opened: [URL] = []
    var polls: [() throws -> JSON]
    var answers: [String: JSON]

    init(polls: [() throws -> JSON], answers: [String: JSON]) {
        self.polls = polls
        self.answers = answers
    }

    var deps: HerSignInDeps {
        HerSignInDeps(
            catcher: HerCatcher(start: { path in ("c1", "http://127.0.0.1:5555" + path) }, poll: { _ in
                let next = self.lock.withLock { self.polls.isEmpty ? { ["pending": true] } : self.polls.removeFirst() }
                return try next()
            }),
            call: { method, params in self.lock.withLock { self.called.append((method, params)); return self.answers[method] ?? [:] } },
            open: { url in self.lock.withLock { self.opened.append(url) } },
            retry: .milliseconds(1))
    }
}

@Test func chatGPTSignsInThroughTheCatcherOnThisDevice() async {
    struct Lost: Error {}
    let f = FakeSignIn(polls: [{ ["pending": true] }, { throw Lost() }, { ["code": "c0de", "state": "st4te", "client_id": "app_1"] }],
                       answers: ["chatgpt/login": ["state": "pending", "authorize_url": "https://auth.openai.com/authorize?x=1", "login_id": "L1"],
                                 "chatgpt/complete": ["state": "connected", "account": "me@example.com"]])
    #expect(await herSignInChatGPT(f.deps) == .connected(account: "me@example.com"))
    #expect(f.opened == [URL(string: "https://auth.openai.com/authorize?x=1")!])
    #expect(f.called.map(\.0) == ["chatgpt/login", "chatgpt/complete"])
    #expect(f.called[0].1 == ["redirect_uri": "http://127.0.0.1:5555/auth/callback"])
    #expect(f.called[1].1 == ["login_id": "L1", "code": "c0de", "state": "st4te", "client_id": "app_1"])

    // The provider says no: her's side is cancelled, and why is said.
    let refused = FakeSignIn(polls: [{ ["error": "access_denied", "error_description": "The person said no"] }],
                             answers: ["chatgpt/login": ["state": "pending", "authorize_url": "https://auth.openai.com/a", "login_id": "L2"]])
    #expect(await herSignInChatGPT(refused.deps) == .failed("The person said no"))
    #expect(refused.called.map(\.0) == ["chatgpt/login", "chatgpt/cancel"])

    // A page that is not https never opens.
    let plain = FakeSignIn(polls: [], answers: ["chatgpt/login": ["state": "pending", "authorize_url": "http://evil.example/a", "login_id": "L3"]])
    guard case .failed = await herSignInChatGPT(plain.deps) else { Issue.record("opened a plain page"); return }
    #expect(plain.opened.isEmpty && plain.called.map(\.0) == ["chatgpt/login", "chatgpt/cancel"])
}

@Test func aConnectorSignInCanBeStopped() async {
    let f = FakeSignIn(polls: [], answers: ["mcp/login": ["authorize_url": "http://127.0.0.1:9000/authorize", "login_id": "M1", "client": "dynamic"]])
    let signIn = Task { await herSignInConnector(f.deps, name: "tracker") }
    while f.lock.withLock({ f.opened.isEmpty }) { try? await Task.sleep(for: .milliseconds(5)) }
    signIn.cancel()
    #expect(await signIn.value == .cancelled)
    #expect(f.called.map(\.0) == ["mcp/login", "mcp/login_cancel"] && f.called[1].1 == ["name": "tracker"])
    #expect(f.called[0].1 == ["name": "tracker", "redirect_uri": "http://127.0.0.1:5555/callback"])

    let done = FakeSignIn(polls: [{ ["code": "k", "state": "s", "iss": "https://issuer"] }], answers: ["mcp/login": ["authorize_url": "https://mcp.example/auth", "login_id": "M2"]])
    #expect(await herSignInConnector(done.deps, name: "tracker") == .connected(account: nil))
    #expect(done.called.last?.0 == "mcp/login_complete" && done.called.last?.1 == ["login_id": "M2", "code": "k", "state": "s", "iss": "https://issuer"])
}
